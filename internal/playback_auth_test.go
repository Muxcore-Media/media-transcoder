package internal

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type testCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pem  []byte
}

var serialN int64

func newTestCA(t *testing.T) testCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serialN++
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(serialN),
		Subject:               pkix.Name{CommonName: "test mesh CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	return testCA{cert: cert, key: key, pem: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}
}

// issue returns PEM cert and key for cn, usable as TLS server and client.
func (ca testCA) issue(t *testing.T, cn string) (certPEM, keyPEM []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serialN++
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(serialN),
		Subject:      pkix.Name{CommonName: cn},
		DNSNames:     []string{cn, "localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	kb, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb})
}

func clearInsecureEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{"MUXCORE_INSECURE_DISABLE_TLS", "MUXCORE_DEV_TLS_SKIP", "MUXCORE_GRPC_INSECURE"} {
		t.Setenv(k, "")
	}
}

// meshEnv writes a module identity signed by ca and exports MUXCORE_TLS_*.
func meshEnv(t *testing.T, ca testCA) {
	t.Helper()
	clearInsecureEnv(t)
	dir := t.TempDir()
	c, k := ca.issue(t, "media-transcoder")
	write := func(name string, b []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, b, 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	t.Setenv("MUXCORE_TLS_CERT", write("tls.crt", c))
	t.Setenv("MUXCORE_TLS_KEY", write("tls.key", k))
	t.Setenv("MUXCORE_TLS_CA", write("ca.crt", ca.pem))
	t.Setenv(envHTTPAddr, "127.0.0.1:0")
}

func startPlayback(t *testing.T) *Module {
	t.Helper()
	m := newTestModule(t)
	if err := m.startPlaybackHTTP(context.Background()); err != nil {
		t.Fatal(err)
	}
	return m
}

func mtlsClient(t *testing.T, serverCA testCA, cert, key []byte) *http.Client {
	t.Helper()
	pool := x509.NewCertPool()
	pool.AddCert(serverCA.cert)
	cfg := &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12, ServerName: "media-transcoder"}
	if cert != nil {
		pair, err := tls.X509KeyPair(cert, key)
		if err != nil {
			t.Fatal(err)
		}
		cfg.Certificates = []tls.Certificate{pair}
	}
	return &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{TLSClientConfig: cfg}}
}

func getStatus(t *testing.T, c *http.Client, u string, hdr ...string) (int, error) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	resp, err := c.Do(req)
	if err != nil {
		return 0, err
	}
	_ = resp.Body.Close()
	return resp.StatusCode, nil
}

func TestPlaybackHTTPMeshTLSCallerPolicy(t *testing.T) {
	ca := newTestCA(t)
	meshEnv(t, ca)
	m := startPlayback(t)
	base := "https://" + m.PlaybackHTTPAddr()
	api := base + "/api/playback/hardware"

	bffCert, bffKey := ca.issue(t, "media-ui")
	evilCert, evilKey := ca.issue(t, "media-movies")
	rogue := newTestCA(t)
	rogueCert, rogueKey := rogue.issue(t, "media-ui")

	noCert := mtlsClient(t, ca, nil, nil)
	if code, err := getStatus(t, noCert, base+"/healthz"); err != nil || code != http.StatusOK {
		t.Fatalf("healthz without cert: %d %v", code, err)
	}
	for _, path := range []string{"/api/playback/hardware", "/stream/transcode?src=/etc/passwd", "/stream/hls?src=/etc/passwd",
		"/stream/hls/0123456789abcdef0123456789abcdef/index.m3u8", "/stream/trickplay?src=/etc/passwd&duration=10"} {
		if code, err := getStatus(t, noCert, base+path); err != nil || code != http.StatusUnauthorized {
			t.Errorf("%s without cert: %d %v, want 401", path, code, err)
		}
	}
	// A bearer header is no substitute for the mesh identity.
	if code, _ := getStatus(t, noCert, api, "Authorization", "Bearer anything"); code != http.StatusUnauthorized {
		t.Errorf("bearer without cert: %d", code)
	}
	if code, err := getStatus(t, mtlsClient(t, ca, evilCert, evilKey), api); err != nil || code != http.StatusForbidden {
		t.Errorf("wrong CN: %d %v, want 403", code, err)
	}
	// Right CN, wrong CA: refused at the handshake (or 401), never served.
	if code, err := getStatus(t, mtlsClient(t, ca, rogueCert, rogueKey), api); err == nil && code == http.StatusOK {
		t.Error("certificate from a foreign CA accepted")
	}
	bff := mtlsClient(t, ca, bffCert, bffKey)
	if code, err := getStatus(t, bff, api); err != nil || code != http.StatusOK {
		t.Fatalf("media-ui: %d %v", code, err)
	}
	// Authenticated callers are still bound by the source policy.
	if code, _ := getStatus(t, bff, base+"/stream/transcode?src=/etc/passwd"); code != http.StatusForbidden {
		t.Errorf("authenticated unconfined src: %d, want 403", code)
	}
	// Plaintext HTTP is not served on the TLS port.
	if code, err := getStatus(t, &http.Client{Timeout: 5 * time.Second}, "http://"+m.PlaybackHTTPAddr()+"/api/playback/hardware"); err == nil && code == http.StatusOK {
		t.Error("plaintext request served")
	}
}

func TestPlaybackHTTPAllowedCallersEnv(t *testing.T) {
	ca := newTestCA(t)
	meshEnv(t, ca)
	t.Setenv(envHTTPAllowedCallers, "media-ui-beta, playback-guard")
	m := startPlayback(t)
	api := "https://" + m.PlaybackHTTPAddr() + "/api/playback/hardware"
	c, k := ca.issue(t, "media-ui")
	if code, _ := getStatus(t, mtlsClient(t, ca, c, k), api); code != http.StatusForbidden {
		t.Errorf("media-ui not in allow-list: %d", code)
	}
	c, k = ca.issue(t, "playback-guard")
	if code, err := getStatus(t, mtlsClient(t, ca, c, k), api); err != nil || code != http.StatusOK {
		t.Errorf("playback-guard: %d %v", code, err)
	}
}

func TestPlaybackHTTPMeshRequiresIdentity(t *testing.T) {
	clearInsecureEnv(t)
	t.Setenv("MUXCORE_TLS_CERT", "")
	t.Setenv("MUXCORE_TLS_KEY", "")
	t.Setenv("MUXCORE_TLS_CA", "")
	if _, err := playbackHTTPSecurity(); err == nil {
		t.Fatal("mesh mode without identity must fail closed")
	}
	ca := newTestCA(t)
	meshEnv(t, ca)
	t.Setenv("MUXCORE_TLS_CA", "")
	if _, err := playbackHTTPSecurity(); err == nil {
		t.Fatal("mesh mode without a CA cannot verify callers and must fail closed")
	}
}

func TestPlaybackHTTPDevBinding(t *testing.T) {
	t.Setenv("MUXCORE_INSECURE_DISABLE_TLS", "true")
	t.Setenv(envHTTPToken, "")
	t.Setenv(envHTTPAddr, "")
	sec, err := playbackHTTPSecurity()
	if err != nil || sec.addr != "127.0.0.1:9526" || sec.tls != nil {
		t.Fatalf("dev default: %+v %v", sec, err)
	}
	for _, addr := range []string{":9526", "0.0.0.0:9526", "[::]:9526", "media-transcoder:9526", "192.168.1.5:9526"} {
		t.Setenv(envHTTPAddr, addr)
		if _, err := playbackHTTPSecurity(); err == nil {
			t.Errorf("non-loopback %q without token accepted", addr)
		}
	}
	for _, addr := range []string{"127.0.0.1:0", "localhost:9526", "[::1]:9526"} {
		t.Setenv(envHTTPAddr, addr)
		if _, err := playbackHTTPSecurity(); err != nil {
			t.Errorf("loopback %q: %v", addr, err)
		}
	}
}

func TestPlaybackHTTPDevToken(t *testing.T) {
	t.Setenv("MUXCORE_INSECURE_DISABLE_TLS", "true")
	t.Setenv(envHTTPAddr, "0.0.0.0:0")
	t.Setenv(envHTTPToken, "s3cret")
	m := startPlayback(t)
	_, port, _ := net.SplitHostPort(m.PlaybackHTTPAddr())
	base := "http://127.0.0.1:" + port
	c := &http.Client{Timeout: 5 * time.Second}
	if code, _ := getStatus(t, c, base+"/healthz"); code != http.StatusOK {
		t.Errorf("healthz: %d", code)
	}
	for _, path := range []string{"/api/playback/hardware", "/stream/transcode?src=/etc/passwd", "/stream/trickplay?src=/etc/passwd&duration=1"} {
		if code, _ := getStatus(t, c, base+path); code != http.StatusUnauthorized {
			t.Errorf("%s no token: %d", path, code)
		}
	}
	if code, _ := getStatus(t, c, base+"/api/playback/hardware", "Authorization", "Bearer wrong"); code != http.StatusUnauthorized {
		t.Errorf("wrong token: %d", code)
	}
	if code, _ := getStatus(t, c, base+"/api/playback/hardware", "Authorization", "Bearer s3cret"); code != http.StatusOK {
		t.Errorf("right token: %d", code)
	}
}

func TestLifecycleMeshTLS(t *testing.T) {
	ca := newTestCA(t)
	meshEnv(t, ca)
	m := NewModule(Config{DBPath: filepath.Join(t.TempDir(), "l.db"), GRPCAddr: "127.0.0.1:0"})
	ctx := context.Background()
	if err := m.Init(ctx); err != nil {
		t.Fatal(err)
	}
	if err := m.Start(ctx); err != nil {
		t.Fatalf("Start with mesh identity: %v", err)
	}
	if m.PlaybackHTTPAddr() == "" {
		t.Fatal("playback HTTP not bound")
	}
	if err := m.Stop(ctx); err != nil {
		t.Fatal(err)
	}

	// Without an identity and without the dev flag, Start fails closed.
	t.Setenv("MUXCORE_TLS_CERT", "")
	m2 := NewModule(Config{DBPath: filepath.Join(t.TempDir(), "l2.db"), GRPCAddr: "127.0.0.1:0"})
	if err := m2.Init(ctx); err != nil {
		t.Fatal(err)
	}
	if err := m2.Start(ctx); err == nil {
		t.Fatal("Start without mesh identity succeeded")
	}
	_ = m2.Stop(ctx)
}
