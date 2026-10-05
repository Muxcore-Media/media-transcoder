package internal

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	transcodev1 "github.com/Muxcore-Media/media-transcoder/proto/transcodev1"
)

const hlsPlaylistName = "index.m3u8"

var hlsSegmentName = regexp.MustCompile(`^seg_\d+\.ts$`)

type hlsSession struct {
	started chan struct{}
	cancel  context.CancelFunc
	err     error
	key     string
	dir     string
	done    bool
}

type playbackQuery struct {
	input         string
	profileID     string
	encoderMode   hwBackend
	startSeconds  float64
	audioIndex    int
	subtitleIndex int
	maxHeight     int
}

func (m *Module) parsePlaybackQuery(r *http.Request) (playbackQuery, error) {
	q := playbackQuery{
		profileID:     "h264_fast",
		audioIndex:    -1,
		subtitleIndex: -1,
	}
	input, err := m.resolveStreamInput(r.URL.Query().Get("src"))
	if err != nil {
		return q, err
	}
	q.input = input
	if profileID := strings.TrimSpace(r.URL.Query().Get("profile")); profileID != "" {
		q.profileID = profileID
	}
	q.encoderMode = parseStreamEncoderMode(r.URL.Query().Get("gpu"))
	if raw := strings.TrimSpace(r.URL.Query().Get("start")); raw != "" {
		if v, parseErr := strconv.ParseFloat(raw, 64); parseErr == nil && v > 0 {
			q.startSeconds = v
		}
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("audio_index")); raw != "" {
		if v, parseErr := strconv.Atoi(raw); parseErr == nil && v >= 0 {
			q.audioIndex = v
		}
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("subtitle_index")); raw != "" {
		if v, parseErr := strconv.Atoi(raw); parseErr == nil && v >= 0 {
			q.subtitleIndex = v
		}
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("max_height")); raw != "" {
		if h, parseErr := strconv.Atoi(raw); parseErr == nil && h > 0 {
			q.maxHeight = h
		}
	}
	return q, nil
}

func applyPlaybackMaxHeight(profile *transcodev1.TranscodeProfile, maxHeight int) *transcodev1.TranscodeProfile {
	if profile == nil || maxHeight <= 0 {
		return profile
	}
	return &transcodev1.TranscodeProfile{
		Id:         profile.GetId(),
		Name:       profile.GetName(),
		VideoCodec: profile.GetVideoCodec(),
		AudioCodec: profile.GetAudioCodec(),
		Preset:     profile.GetPreset(),
		Crf:        profile.GetCrf(),
		MaxWidth:   0,
		MaxHeight:  int32(maxHeight), //nolint:gosec // query param capped by operator-controlled player UI
		UseGpu:     profile.GetUseGpu(),
		Container:  profile.GetContainer(),
	}
}

func hlsStartBucket(startSeconds float64) int {
	if startSeconds <= 0 {
		return 0
	}
	return int(startSeconds / 4) // 4s HLS segments
}

func hlsSessionKey(src, profileID string, mode hwBackend, maxHeight, audioIndex, subtitleIndex, startBucket int) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%s\x00%d\x00%d\x00%d\x00%d", src, profileID, mode, maxHeight, audioIndex, subtitleIndex, startBucket)))
	return hex.EncodeToString(sum[:16])
}

func validHLSKey(key string) bool {
	if len(key) < 16 || len(key) > 64 {
		return false
	}
	for _, c := range key {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func validHLSFile(name string) bool {
	return name == hlsPlaylistName || hlsSegmentName.MatchString(name)
}

func (m *Module) hlsCacheDir() string {
	if v := strings.TrimSpace(os.Getenv("TRANSCODER_HLS_CACHE")); v != "" {
		return v
	}
	return filepath.Join(filepath.Dir(m.dbPath), "hls")
}

func playlistPath(dir string) string {
	return filepath.Join(dir, hlsPlaylistName)
}

func playlistReady(dir string) bool {
	st, err := os.Stat(playlistPath(dir))
	return err == nil && st.Size() > 0
}

func playlistHasEndlist(dir string) bool {
	raw, err := os.ReadFile(playlistPath(dir)) //nolint:gosec // playlist path is under the HLS cache keyed by hex digest
	if err != nil {
		return false
	}
	return strings.Contains(string(raw), "#EXT-X-ENDLIST")
}

func (m *Module) handleHLSRedirect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	q, err := m.parsePlaybackQuery(r)
	if err != nil {
		http.Error(w, err.Error(), streamInputStatus(err))
		return
	}
	m.mu.RLock()
	profile := applyPlaybackMaxHeight(m.loadProfile(r.Context(), q.profileID), q.maxHeight)
	m.mu.RUnlock()
	if profile == nil {
		http.Error(w, "profile not found", http.StatusBadRequest)
		return
	}
	sess, err := m.ensureHLSSession(r.Context(), profile, q)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	http.Redirect(w, r, "/stream/hls/"+sess.key+"/"+hlsPlaylistName, http.StatusFound)
}

func (m *Module) handleHLSAsset(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	file := r.PathValue("file")
	if !validHLSKey(key) || !validHLSFile(file) {
		http.Error(w, "invalid hls path", http.StatusBadRequest)
		return
	}
	dir := filepath.Join(m.hlsCacheDir(), key)
	path := filepath.Join(dir, file)
	if filepath.Dir(path) != dir {
		http.Error(w, "invalid hls path", http.StatusBadRequest)
		return
	}
	if file == hlsPlaylistName {
		if err := waitForPlaylist(r.Context(), dir, 30*time.Second); err != nil {
			http.Error(w, "hls playlist not ready", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		w.Header().Set("Cache-Control", "no-store")
		http.ServeFile(w, r, path) //nolint:gosec // path is built from a hex-validated key and an allow-listed file name (validHLSKey/validHLSFile)
		return
	}
	if err := waitForFile(r.Context(), path, 20*time.Second); err != nil {
		http.Error(w, "segment not ready", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "video/mp2t")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	http.ServeFile(w, r, path) //nolint:gosec // path is built from a hex-validated key and an allow-listed file name (validHLSKey/validHLSFile)
}

func waitForFile(ctx context.Context, path string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		if st, err := os.Stat(path); err == nil && st.Size() > 0 { //nolint:gosec // callers pass paths under the HLS cache built from validated key/file names
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timeout")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func waitForPlaylist(ctx context.Context, dir string, timeout time.Duration) error {
	return waitForFile(ctx, playlistPath(dir), timeout)
}

func (m *Module) ensureHLSSession(ctx context.Context, profile *transcodev1.TranscodeProfile, q playbackQuery) (*hlsSession, error) {
	key := hlsSessionKey(q.input, q.profileID, q.encoderMode, q.maxHeight, q.audioIndex, q.subtitleIndex, hlsStartBucket(q.startSeconds))
	dir := filepath.Join(m.hlsCacheDir(), key)

	m.hlsMu.Lock()
	if m.hlsSessions == nil {
		m.hlsSessions = make(map[string]*hlsSession)
	}
	if sess, ok := m.hlsSessions[key]; ok {
		m.hlsMu.Unlock()
		<-sess.started
		if sess.err != nil {
			return nil, sess.err
		}
		return sess, nil
	}
	if playlistReady(dir) {
		sess := &hlsSession{key: key, dir: dir, started: make(chan struct{}), done: playlistHasEndlist(dir)}
		close(sess.started)
		m.hlsSessions[key] = sess
		m.hlsMu.Unlock()
		return sess, nil
	}
	sess := &hlsSession{key: key, dir: dir, started: make(chan struct{})}
	m.hlsSessions[key] = sess
	m.hlsMu.Unlock()

	if err := m.startHLSFFmpeg(ctx, sess, profile, q); err != nil {
		sess.err = err
		close(sess.started)
		m.hlsMu.Lock()
		delete(m.hlsSessions, key)
		m.hlsMu.Unlock()
		return nil, err
	}
	close(sess.started)
	return sess, nil
}

func (m *Module) startHLSFFmpeg(ctx context.Context, sess *hlsSession, profile *transcodev1.TranscodeProfile, q playbackQuery) error {
	if err := os.MkdirAll(sess.dir, 0o700); err != nil {
		return fmt.Errorf("hls cache: %w", err)
	}
	m.ensurePlaybackSlots()
	select {
	case <-m.playbackSlots:
	case <-time.After(30 * time.Second):
		return fmt.Errorf("playback capacity busy")
	}

	// The ffmpeg process outlives the request that started it, so detach from
	// the request's cancellation while keeping its values.
	procCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	sess.cancel = cancel
	args := m.buildHLSStreamArgs(profile, q.input, q.encoderMode, q.startSeconds, q.audioIndex, q.subtitleIndex, playlistPath(sess.dir), filepath.Join(sess.dir, "seg_%05d.ts"))
	cmd := exec.CommandContext(procCtx, m.getFFmpegBin(), args...) //nolint:gosec // ffmpeg paths come from operator-controlled media library
	stderr, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		m.playbackSlots <- struct{}{}
		return fmt.Errorf("hls stderr: %w", err)
	}
	if err := cmd.Start(); err != nil {
		cancel()
		m.playbackSlots <- struct{}{}
		return fmt.Errorf("ffmpeg start failed: %w", err)
	}
	picked := m.pickStreamEncoder(profile, q.encoderMode)
	go drainPlaybackStderr(stderr, q.profileID+"-hls", picked)
	go func() {
		waitErr := cmd.Wait()
		cancel()
		m.playbackSlots <- struct{}{}
		m.hlsMu.Lock()
		sess.done = true
		if waitErr != nil && !playlistHasEndlist(sess.dir) {
			slog.Warn("hls ffmpeg exited", "key", sess.key, "error", waitErr)
		}
		m.hlsMu.Unlock()
	}()
	return nil
}

func (m *Module) stopHLSSessions() {
	m.hlsMu.Lock()
	defer m.hlsMu.Unlock()
	for _, sess := range m.hlsSessions {
		if sess.cancel != nil {
			sess.cancel()
		}
	}
	m.hlsSessions = make(map[string]*hlsSession)
}
