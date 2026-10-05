package internal

import (
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"

	"github.com/Muxcore-Media/core/sdk/go/module/meshtls"
)

// Playback HTTP caller authentication.
//
// Mesh (default, household): the API is served over TLS with the module's
// mesh certificate (MUXCORE_TLS_CERT/KEY) and every request except
// GET /healthz needs a client certificate that verifies against the mesh CA
// (MUXCORE_TLS_CA) and whose CN is in TRANSCODER_HTTP_ALLOWED_CALLERS
// (comma separated, default "media-ui", the BFF's mesh identity).
//
// Dev (MUXCORE_INSECURE_DISABLE_TLS and friends): plaintext. The default bind
// is 127.0.0.1:9526; a non-loopback TRANSCODER_HTTP_ADDR requires
// TRANSCODER_HTTP_TOKEN, and when a token is set every request except
// GET /healthz needs "Authorization: Bearer <token>" (NFR-SEC-011).
const (
	envHTTPAddr           = "TRANSCODER_HTTP_ADDR"
	envHTTPToken          = "TRANSCODER_HTTP_TOKEN" //nolint:gosec // environment variable name, not a credential
	envHTTPAllowedCallers = "TRANSCODER_HTTP_ALLOWED_CALLERS"
	defaultHTTPCallers    = "media-ui"
	defaultDevHTTPAddr    = "127.0.0.1:9526"
	defaultMeshHTTPAddr   = ":9526"
)

type playbackSecurity struct {
	tls     *tls.Config // nil = plaintext (dev)
	callers map[string]bool
	addr    string
	token   string
}

func (s playbackSecurity) mode() string {
	switch {
	case s.tls != nil:
		return "mtls"
	case s.token != "":
		return "bearer"
	default:
		return "loopback"
	}
}

// isLoopbackAddr reports whether a listen address binds only to loopback.
// Empty or wildcard hosts and names other than "localhost" are non-loopback.
func isLoopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(strings.TrimSpace(addr))
	if err != nil {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// playbackHTTPSecurity derives the playback listener's security from the
// environment; it fails closed on any misconfiguration.
func playbackHTTPSecurity() (playbackSecurity, error) {
	addr := strings.TrimSpace(os.Getenv(envHTTPAddr))
	token := strings.TrimSpace(os.Getenv(envHTTPToken))
	if meshtls.Insecure() {
		if addr == "" {
			addr = defaultDevHTTPAddr
		}
		if !isLoopbackAddr(addr) && token == "" {
			return playbackSecurity{}, fmt.Errorf("refusing to serve playback HTTP on non-loopback address %q without %s "+
				"(set a bearer token or bind to 127.0.0.1)", addr, envHTTPToken)
		}
		return playbackSecurity{addr: addr, token: token}, nil
	}

	if addr == "" {
		addr = defaultMeshHTTPAddr
	}
	certFile := strings.TrimSpace(os.Getenv(meshtls.EnvTLSCert))
	keyFile := strings.TrimSpace(os.Getenv(meshtls.EnvTLSKey))
	caFile := strings.TrimSpace(os.Getenv(meshtls.EnvTLSCA))
	if certFile == "" || keyFile == "" || caFile == "" {
		return playbackSecurity{}, fmt.Errorf("playback HTTP needs the mesh identity: set %s, %s and %s "+
			"(or the dev insecure flag)", meshtls.EnvTLSCert, meshtls.EnvTLSKey, meshtls.EnvTLSCA)
	}
	pair, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return playbackSecurity{}, fmt.Errorf("playback HTTP TLS key pair: %w", err)
	}
	caPEM, err := os.ReadFile(caFile) //nolint:gosec // operator-configured mesh CA path
	if err != nil {
		return playbackSecurity{}, fmt.Errorf("playback HTTP mesh CA: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return playbackSecurity{}, fmt.Errorf("playback HTTP mesh CA: no certificates in %s", caFile)
	}
	callerList := os.Getenv(envHTTPAllowedCallers)
	if strings.TrimSpace(callerList) == "" {
		callerList = defaultHTTPCallers
	}
	callers := map[string]bool{}
	for _, c := range parseCSV(callerList) {
		callers[c] = true
	}
	return playbackSecurity{
		addr: addr,
		tls: &tls.Config{
			MinVersion:   tls.VersionTLS12,
			Certificates: []tls.Certificate{pair},
			ClientCAs:    pool,
			// Verified when presented; the middleware turns a missing
			// certificate into 401 so /healthz stays probeable.
			ClientAuth: tls.VerifyClientCertIfGiven,
		},
		callers: callers,
	}, nil
}

func tokenMatches(want, got string) bool {
	a := sha256.Sum256([]byte(want))
	b := sha256.Sum256([]byte(got))
	return subtle.ConstantTimeCompare(a[:], b[:]) == 1
}

func bearerToken(h string) string {
	const p = "bearer "
	if len(h) > len(p) && strings.EqualFold(h[:len(p)], p) {
		return strings.TrimSpace(h[len(p):])
	}
	return ""
}

func isHealthProbe(r *http.Request) bool {
	return r.URL.Path == "/healthz" && (r.Method == http.MethodGet || r.Method == http.MethodHead)
}

// middleware enforces the caller policy in front of next.
func (s playbackSecurity) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isHealthProbe(r) {
			next.ServeHTTP(w, r)
			return
		}
		if s.tls != nil {
			if r.TLS == nil || len(r.TLS.VerifiedChains) == 0 || len(r.TLS.PeerCertificates) == 0 {
				http.Error(w, "client certificate required", http.StatusUnauthorized)
				return
			}
			cn := strings.TrimSpace(r.TLS.PeerCertificates[0].Subject.CommonName)
			if !s.callers[cn] {
				http.Error(w, "caller not allowed", http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		if s.token != "" {
			got := bearerToken(r.Header.Get("Authorization"))
			if got == "" || !tokenMatches(s.token, got) {
				w.Header().Set("WWW-Authenticate", `Bearer realm="media-transcoder"`)
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
