package internal

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHandlePlaybackStreamRejectsBadInput(t *testing.T) {
	m := newTestModule(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /stream/transcode", m.handlePlaybackStream)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/stream/transcode?src=relative.mkv")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status=%d", resp.StatusCode)
	}
}

func TestHandlePlaybackStreamSoftwareIntegration(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not on PATH")
	}

	dir := t.TempDir()
	inPath := filepath.Join(dir, "in.mkv")
	outProbe := filepath.Join(dir, "probe.txt")
	cmd := exec.Command(ffmpeg, "-y", "-f", "lavfi", "-i", "testsrc=size=320x240:rate=24",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=2",
		"-shortest", "-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "aac", inPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("ffmpeg fixture: %v\n%s", err, out)
	}

	m := newTestModule(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /stream/transcode", m.handlePlaybackStream)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/stream/transcode?src="+inPath+"&gpu=software", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%d body=%s", resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "video/mp4" {
		t.Fatalf("content-type=%q", ct)
	}
	if enc := resp.Header.Get("X-Transcode-Encoder"); enc != string(streamEncoderSoftware) {
		t.Fatalf("encoder=%q want software", enc)
	}

	head := make([]byte, 32*1024)
	n, _ := io.ReadFull(resp.Body, head)
	if n < 8 {
		t.Fatalf("short read n=%d", n)
	}
	if !bytes.Contains(head[:n], []byte("ftyp")) {
		t.Fatalf("expected fragmented mp4 ftyp in first %d bytes", n)
	}

	cancel()
	_ = os.WriteFile(outProbe, []byte("ok"), 0o600)
}

func TestHandlePlaybackStreamMaxHeightOverride(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not on PATH")
	}
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe not on PATH")
	}

	dir := t.TempDir()
	inPath := filepath.Join(dir, "in.mkv")
	cmd := exec.Command(ffmpeg, "-y", "-f", "lavfi", "-i", "testsrc=size=640x480:rate=24",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=2",
		"-shortest", "-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "aac", inPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("ffmpeg fixture: %v\n%s", err, out)
	}

	m := newTestModule(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /stream/transcode", m.handlePlaybackStream)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/stream/transcode?src="+inPath+"&gpu=software&max_height=240", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%d body=%s", resp.StatusCode, body)
	}

	outPath := filepath.Join(dir, "out.mp4")
	outFile, err := os.Create(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(outFile, resp.Body); err != nil {
		outFile.Close()
		t.Fatal(err)
	}
	outFile.Close()
	cancel()

	probe := exec.Command(ffprobe, "-v", "error", "-select_streams", "v:0",
		"-show_entries", "stream=height", "-of", "default=nw=1:nk=1", outPath)
	out, err := probe.Output()
	if err != nil {
		t.Fatalf("ffprobe: %v", err)
	}
	height := strings.TrimSpace(string(out))
	if height != "240" {
		t.Fatalf("expected height=240, got %q", height)
	}
}

func TestHandlePlaybackHardwareJSON(t *testing.T) {
	m := newTestModule(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/playback/hardware", m.handlePlaybackHardware)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/playback/hardware")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `"software"`) {
		t.Fatalf("body=%s", body)
	}
}
