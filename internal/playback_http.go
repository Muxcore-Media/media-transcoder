package internal

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
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

func (m *Module) startPlaybackHTTP() {
	addr := strings.TrimSpace(os.Getenv("TRANSCODER_HTTP_ADDR"))
	if addr == "" {
		addr = ":9526"
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
	m.playbackMu.Lock()
	m.playbackSlots = slots
	m.httpAddr = addr
	m.playbackMu.Unlock()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	mux.HandleFunc("GET /stream/transcode", m.handlePlaybackStream)
	mux.HandleFunc("GET /api/playback/hardware", m.handlePlaybackHardware)
	mux.HandleFunc("GET /stream/trickplay", m.handleTrickplaySprite)

	m.httpSrv = &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		slog.Info("media-transcoder playback HTTP started", "addr", addr, "max_playback", maxPlayback)
		if err := m.httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("media-transcoder playback HTTP error", "error", err)
		}
	}()
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

	input, err := resolveStreamInput(r.URL.Query().Get("src"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	profileID := strings.TrimSpace(r.URL.Query().Get("profile"))
	if profileID == "" {
		profileID = "h264_fast"
	}
	encoderMode := parseStreamEncoderMode(r.URL.Query().Get("gpu"))
	startSeconds := 0.0
	if raw := strings.TrimSpace(r.URL.Query().Get("start")); raw != "" {
		if v, parseErr := strconv.ParseFloat(raw, 64); parseErr == nil && v > 0 {
			startSeconds = v
		}
	}
	audioStreamIndex := -1
	if raw := strings.TrimSpace(r.URL.Query().Get("audio_index")); raw != "" {
		if v, parseErr := strconv.Atoi(raw); parseErr == nil && v >= 0 {
			audioStreamIndex = v
		}
	}

	m.ensurePlaybackSlots()

	m.mu.RLock()
	profile := m.loadProfile(r.Context(), profileID)
	m.mu.RUnlock()
	if profile == nil {
		http.Error(w, "profile not found", http.StatusBadRequest)
		return
	}
	// Manual quality selection (player "Quality" menu): cap output height
	// without needing a dedicated profile per resolution.
	if raw := strings.TrimSpace(r.URL.Query().Get("max_height")); raw != "" {
		if h, hErr := strconv.Atoi(raw); hErr == nil && h > 0 {
			profile = &transcodev1.TranscodeProfile{
				Id:         profile.GetId(),
				Name:       profile.GetName(),
				VideoCodec: profile.GetVideoCodec(),
				AudioCodec: profile.GetAudioCodec(),
				Preset:     profile.GetPreset(),
				Crf:        profile.GetCrf(),
				MaxWidth:   0,
				MaxHeight:  int32(h), //nolint:gosec // query param capped by operator-controlled player UI
				UseGpu:     profile.GetUseGpu(),
				Container:  profile.GetContainer(),
			}
		}
	}

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
	args := m.buildPlaybackStreamArgs(profile, input, encoderMode, startSeconds, audioStreamIndex)
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
