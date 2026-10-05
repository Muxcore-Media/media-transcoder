package internal

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	transcodev1 "github.com/Muxcore-Media/media-transcoder/proto/transcodev1"
)

func (m *Module) ensurePlaybackSlots() {
	m.playbackMu.Lock()
	defer m.playbackMu.Unlock()
	if m.playbackSlots != nil {
		return
	}
	maxPlayback := 4
	if v := strings.TrimSpace(os.Getenv("TRANSCODER_MAX_PLAYBACK")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			maxPlayback = n
		}
	}
	slots := make(chan struct{}, maxPlayback)
	for i := 0; i < maxPlayback; i++ {
		slots <- struct{}{}
	}
	m.playbackSlots = slots
}

// playbackMux routes the playback HTTP API (without authentication).
func (m *Module) playbackMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	mux.HandleFunc("GET /stream/transcode", m.handlePlaybackStream)
	mux.HandleFunc("GET /stream/hls", m.handleHLSRedirect)
	mux.HandleFunc("GET /stream/hls/{key}/{file}", m.handleHLSAsset)
	mux.HandleFunc("GET /api/playback/hardware", m.handlePlaybackHardware)
	mux.HandleFunc("GET /stream/trickplay", m.handleTrickplaySprite)
	return mux
}

// startPlaybackHTTP binds the playback HTTP API with caller authentication
// (see playbackHTTPSecurity). A bind or security misconfiguration fails Start.
func (m *Module) startPlaybackHTTP(ctx context.Context) error {
	sec, err := playbackHTTPSecurity()
	if err != nil {
		return err
	}
	m.ensurePlaybackSlots()

	lc := net.ListenConfig{}
	lis, err := lc.Listen(ctx, "tcp", sec.addr)
	if err != nil {
		return fmt.Errorf("listen playback HTTP %s: %w", sec.addr, err)
	}
	srv := &http.Server{
		Handler:           sec.middleware(m.playbackMux()),
		ReadHeaderTimeout: 10 * time.Second,
		TLSConfig:         sec.tls,
	}
	m.playbackMu.Lock()
	m.httpAddr = lis.Addr().String()
	m.playbackMu.Unlock()
	m.httpSrv = srv
	go func() {
		slog.Info("media-transcoder playback HTTP started", "addr", lis.Addr().String(), "mode", sec.mode())
		var serveErr error
		if sec.tls != nil {
			serveErr = srv.ServeTLS(lis, "", "")
		} else {
			serveErr = srv.Serve(lis)
		}
		if serveErr != nil && serveErr != http.ErrServerClosed {
			slog.Error("media-transcoder playback HTTP error", "error", serveErr)
		}
	}()
	return nil
}

// PlaybackHTTPAddr returns the bound playback HTTP address after Start.
func (m *Module) PlaybackHTTPAddr() string {
	m.playbackMu.Lock()
	defer m.playbackMu.Unlock()
	return m.httpAddr
}

func (m *Module) handlePlaybackHardware(w http.ResponseWriter, r *http.Request) {
	resp, err := m.DetectHardware(r.Context(), &transcodev1.DetectHardwareRequest{})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"devices": resp.GetDevices(),
		"software": map[string]any{
			"available": true,
			"encoders":  []string{"libx264", "libx265", "libaom-av1"},
		},
	})
}

func (m *Module) handlePlaybackStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	q, err := m.parsePlaybackQuery(r)
	if err != nil {
		http.Error(w, err.Error(), streamInputStatus(err))
		return
	}

	m.ensurePlaybackSlots()

	m.mu.RLock()
	profile := applyPlaybackMaxHeight(m.loadProfile(r.Context(), q.profileID), q.maxHeight)
	m.mu.RUnlock()
	if profile == nil {
		http.Error(w, "profile not found", http.StatusBadRequest)
		return
	}
	profileID := q.profileID
	encoderMode := q.encoderMode
	startSeconds := q.startSeconds
	audioStreamIndex := q.audioIndex
	subtitleStreamIndex := q.subtitleIndex
	input := q.input

	select {
	case <-m.playbackSlots:
	case <-r.Context().Done():
		return
	case <-time.After(30 * time.Second):
		http.Error(w, "playback capacity busy", http.StatusServiceUnavailable)
		return
	}
	defer func() { m.playbackSlots <- struct{}{} }()

	picked := m.pickStreamEncoder(profile, encoderMode)
	args := m.buildPlaybackStreamArgs(profile, input, encoderMode, startSeconds, audioStreamIndex, subtitleStreamIndex)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	cmd := exec.CommandContext(ctx, m.getFFmpegBin(), args...) //nolint:gosec // ffmpeg paths come from operator-controlled media library
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		http.Error(w, "stream pipe error", http.StatusInternalServerError)
		return
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		http.Error(w, "stream pipe error", http.StatusInternalServerError)
		return
	}

	if err := cmd.Start(); err != nil {
		http.Error(w, "ffmpeg start failed", http.StatusInternalServerError)
		return
	}

	go drainPlaybackStderr(stderr, profileID, picked)

	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Transcode-Profile", profileID)
	w.Header().Set("X-Transcode-Encoder", string(picked))
	w.Header().Set("X-Transcode-Start-Seconds", strconv.FormatFloat(startSeconds, 'f', 3, 64))
	w.WriteHeader(http.StatusOK)

	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}

	if _, err := io.Copy(w, stdout); err != nil && ctx.Err() == nil {
		slog.Debug("playback stream copy ended", "profile", profileID, "error", err)
	}
	cancel()
	_ = cmd.Wait()
}

func drainPlaybackStderr(r io.Reader, profileID string, mode streamEncoderMode) {
	buf := make([]byte, 4096)
	n, _ := r.Read(buf)
	if n == 0 {
		return
	}
	slog.Warn("playback ffmpeg stderr", "profile", profileID, "gpu", mode, "detail", strings.TrimSpace(string(buf[:n])))
}
