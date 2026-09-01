package internal

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestHandleTrickplayRejectsMissingDuration(t *testing.T) {
	m := newTestModule(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /stream/trickplay", m.handleTrickplaySprite)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/stream/trickplay?src=http://127.0.0.1:9430/stream/m1")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status=%d", resp.StatusCode)
	}
}

func TestHandleTrickplayRejectsExternalURL(t *testing.T) {
	m := newTestModule(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /stream/trickplay", m.handleTrickplaySprite)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/stream/trickplay?src=https://evil.example/x.mkv&duration=120")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status=%d", resp.StatusCode)
	}
}

func TestHandleTrickplayRequiresAuthWhenTokenSet(t *testing.T) {
	t.Setenv("TRANSCODER_HTTP_TOKEN", "secret")
	m := newTestModule(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /stream/trickplay", m.handleTrickplaySprite)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/stream/trickplay?src=http://127.0.0.1:9430/x&duration=10")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status=%d", resp.StatusCode)
	}
}

func TestComputeTrickplayGrid(t *testing.T) {
	sprite := computeTrickplayGrid(3600, 10)
	if sprite.Count < 1 || sprite.Cols < 1 || sprite.Rows < 1 {
		t.Fatalf("sprite=%+v", sprite)
	}
}

func TestTrickplayCacheKeyStable(t *testing.T) {
	a := trickplayCacheKey("/media/a.mkv", 10)
	b := trickplayCacheKey("/media/a.mkv", 10)
	if a != b {
		t.Fatalf("keys differ: %q %q", a, b)
	}
}

func TestResolveStreamInputLibraryPrefix(t *testing.T) {
	m := newTestModule(t)
	dir := t.TempDir()
	t.Setenv("TRANSCODER_LIBRARY_PATHS", dir)
	src := filepath.Join(dir, "movie.mkv")
	if err := os.WriteFile(src, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := m.resolveStreamInput(t.Context(), src)
	if err != nil {
		t.Fatal(err)
	}
	if got != filepath.Clean(src) {
		t.Fatalf("got=%q", got)
	}
}

func TestResolveStreamInputRejectsOutsideLibrary(t *testing.T) {
	m := newTestModule(t)
	dir := t.TempDir()
	t.Setenv("TRANSCODER_LIBRARY_PATHS", dir)
	other := filepath.Join(t.TempDir(), "other.mkv")
	if err := os.WriteFile(other, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := m.resolveStreamInput(t.Context(), other); err == nil {
		t.Fatal("expected error for path outside library")
	}
}

func TestDefaultPlaybackHTTPAddr(t *testing.T) {
	if defaultPlaybackHTTPAddr() != "127.0.0.1:9526" {
		t.Fatalf("addr=%q", defaultPlaybackHTTPAddr())
	}
}

func TestIsLoopbackHost(t *testing.T) {
	for _, host := range []string{"127.0.0.1", "127.0.0.1:9430", "localhost", "[::1]:8080"} {
		if !isLoopbackHost(host) {
			t.Fatalf("expected loopback for %q", host)
		}
	}
	if isLoopbackHost("evil.example") {
		t.Fatal("expected non-loopback")
	}
}
