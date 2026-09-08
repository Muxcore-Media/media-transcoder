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

func TestHandleHLSRejectsBadInput(t *testing.T) {
	m := newTestModule(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /stream/hls", m.handleHLSRedirect)
	mux.HandleFunc("GET /stream/hls/{key}/{file}", m.handleHLSAsset)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/stream/hls?src=relative.mkv")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status=%d", resp.StatusCode)
	}

	resp2, err := http.Get(srv.URL + "/stream/hls/not-hex/index.m3u8")
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusBadRequest {
		t.Fatalf("asset status=%d", resp2.StatusCode)
	}
}

func TestHandleHLSIntegration(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not on PATH")
	}

	dir := t.TempDir()
	t.Setenv("TRANSCODER_HLS_CACHE", filepath.Join(dir, "hls"))
	inPath := filepath.Join(dir, "in.mkv")
	cmd := exec.Command(ffmpeg, "-y", "-f", "lavfi", "-i", "testsrc=size=320x240:rate=24",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=6",
		"-shortest", "-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "aac", inPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("ffmpeg fixture: %v\n%s", err, out)
	}

	m := newTestModule(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /stream/hls", m.handleHLSRedirect)
	mux.HandleFunc("GET /stream/hls/{key}/{file}", m.handleHLSAsset)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client := &http.Client{
		Timeout: 45 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Get(srv.URL + "/stream/hls?src=" + inPath + "&gpu=software")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%d body=%s", resp.StatusCode, body)
	}
	loc := resp.Header.Get("Location")
	if !strings.HasPrefix(loc, "/stream/hls/") || !strings.HasSuffix(loc, "/index.m3u8") {
		t.Fatalf("location=%q", loc)
	}

	follow := &http.Client{Timeout: 45 * time.Second}
	playlist, err := follow.Get(srv.URL + loc)
	if err != nil {
		t.Fatal(err)
	}
	defer playlist.Body.Close()
	if playlist.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(playlist.Body)
		t.Fatalf("playlist status=%d body=%s", playlist.StatusCode, body)
	}
	if ct := playlist.Header.Get("Content-Type"); !strings.Contains(ct, "mpegurl") {
		t.Fatalf("content-type=%q", ct)
	}
	body, _ := io.ReadAll(playlist.Body)
	if !bytes.Contains(body, []byte("#EXTM3U")) || !bytes.Contains(body, []byte("seg_")) {
		t.Fatalf("playlist=%s", body)
	}

	segName := ""
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "seg_") && strings.HasSuffix(line, ".ts") {
			segName = line
			break
		}
	}
	if segName == "" {
		t.Fatalf("no segment in playlist: %s", body)
	}
	base := strings.TrimSuffix(loc, "index.m3u8")
	seg, err := follow.Get(srv.URL + base + segName)
	if err != nil {
		t.Fatal(err)
	}
	defer seg.Body.Close()
	if seg.StatusCode != http.StatusOK {
		t.Fatalf("segment status=%d", seg.StatusCode)
	}
	chunk := make([]byte, 188)
	n, _ := io.ReadFull(seg.Body, chunk)
	if n < 188 || chunk[0] != 0x47 {
		t.Fatalf("expected MPEG-TS sync byte, n=%d", n)
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
