package internal

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

func defaultPlaybackHTTPAddr() string {
	return "127.0.0.1:9526"
}

func playbackHTTPToken() string {
	return strings.TrimSpace(os.Getenv("TRANSCODER_HTTP_TOKEN"))
}

func (m *Module) checkPlaybackAuth(r *http.Request) bool {
	token := playbackHTTPToken()
	if token == "" {
		return true
	}
	if q := strings.TrimSpace(r.URL.Query().Get("token")); q == token {
		return true
	}
	if h := strings.TrimSpace(r.Header.Get("Authorization")); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimPrefix(h, "Bearer ") == token
	}
	return false
}

func (m *Module) requirePlaybackAuth(w http.ResponseWriter, r *http.Request) bool {
	if m.checkPlaybackAuth(r) {
		return true
	}
	http.Error(w, "unauthorized", http.StatusUnauthorized)
	return false
}

func isLoopbackHost(host string) bool {
	host = strings.TrimSpace(host)
	if host == "" {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func extraLibraryPrefixes() []string {
	raw := strings.TrimSpace(os.Getenv("TRANSCODER_LIBRARY_PATHS"))
	if raw == "" {
		return nil
	}
	var out []string
	for _, p := range strings.Split(raw, ",") {
		p = filepathClean(strings.TrimSpace(p))
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func (m *Module) libraryPrefixes(ctx context.Context) ([]string, error) {
	seen := map[string]struct{}{}
	var prefixes []string
	add := func(p string) {
		p = filepathClean(p)
		if p == "" {
			return
		}
		if _, ok := seen[p]; ok {
			return
		}
		seen[p] = struct{}{}
		prefixes = append(prefixes, p)
	}
	for _, p := range extraLibraryPrefixes() {
		add(p)
	}
	m.mu.RLock()
	setups, err := m.loadAllSetups(ctx)
	m.mu.RUnlock()
	if err != nil {
		return nil, err
	}
	for _, setup := range setups {
		for _, p := range setup.GetLibraryPaths() {
			add(p)
		}
	}
	return prefixes, nil
}

func pathUnderPrefixes(cleanPath string, prefixes []string) bool {
	for _, prefix := range prefixes {
		if cleanPath == prefix || strings.HasPrefix(cleanPath, prefix+string(os.PathSeparator)) {
			return true
		}
	}
	return false
}

func (m *Module) resolveStreamInput(ctx context.Context, src string) (string, error) {
	src = strings.TrimSpace(src)
	if src == "" {
		return "", fmt.Errorf("src required")
	}
	if strings.HasPrefix(src, "http://") || strings.HasPrefix(src, "https://") {
		u, err := url.Parse(src)
		if err != nil {
			return "", fmt.Errorf("invalid src url: %w", err)
		}
		if u.Host == "" {
			return "", fmt.Errorf("invalid src url host")
		}
		if !isLoopbackHost(u.Host) {
			return "", fmt.Errorf("src url must target loopback")
		}
		return src, nil
	}
	if !filepath.IsAbs(src) {
		return "", fmt.Errorf("src must be an absolute path or loopback http(s) url")
	}
	clean := filepathClean(src)
	st, err := os.Stat(clean) //nolint:gosec // path validated as absolute before stat
	if err != nil {
		return "", fmt.Errorf("src not accessible: %w", err)
	}
	if st.IsDir() {
		return "", fmt.Errorf("src must be a file")
	}
	prefixes, err := m.libraryPrefixes(ctx)
	if err != nil {
		return "", fmt.Errorf("library prefixes: %w", err)
	}
	if len(prefixes) == 0 {
		return "", fmt.Errorf("src file path not allowed: no library prefixes configured")
	}
	if !pathUnderPrefixes(clean, prefixes) {
		return "", fmt.Errorf("src file path outside configured library prefixes")
	}
	return clean, nil
}
