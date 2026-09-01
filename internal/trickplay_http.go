package internal

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// trickplaySprite describes one generated scrubbing-preview sheet: a grid of
// small thumbnails sampled at a fixed interval across the whole runtime,
// analogous to Jellyfin/Plex trickplay (BIF) sprites.
type trickplaySprite struct {
	Interval float64
	Cols     int
	Rows     int
	Count    int
}

var (
	trickplayMu    sync.Mutex
	trickplayInFly = map[string]*sync.Mutex{}
)

func (m *Module) trickplayDir() string {
	if v := strings.TrimSpace(os.Getenv("TRANSCODER_TRICKPLAY_DIR")); v != "" {
		return v
	}
	return filepath.Join(filepath.Dir(m.dbPath), "trickplay")
}

func trickplayCacheKey(src string, interval float64) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%.2f", src, interval)))
	return hex.EncodeToString(sum[:])[:32]
}

// lockFor serializes concurrent sprite generation for the same cache key so a
// burst of hover events doesn't spawn duplicate ffmpeg processes.
func lockFor(key string) *sync.Mutex {
	trickplayMu.Lock()
	defer trickplayMu.Unlock()
	l, ok := trickplayInFly[key]
	if !ok {
		l = &sync.Mutex{}
		trickplayInFly[key] = l
	}
	return l
}

// handleTrickplaySprite serves (generating + caching on first request) a
// scrubbing-preview sprite sheet for a media source: a tiled grid of small
// JPEG thumbnails sampled at a fixed interval, so the player can show a
// hover-preview while dragging the seek bar without loading the full video.
// SPA: GET /stream/trickplay?src=…&duration=<seconds>[&interval=<seconds>]
func (m *Module) handleTrickplaySprite(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !m.requirePlaybackAuth(w, r) {
		return
	}
	input, err := m.resolveStreamInput(r.Context(), r.URL.Query().Get("src"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	duration, err := strconv.ParseFloat(strings.TrimSpace(r.URL.Query().Get("duration")), 64)
	if err != nil || duration <= 0 {
		http.Error(w, "duration (seconds, >0) required", http.StatusBadRequest)
		return
	}
	interval := 10.0
	if raw := strings.TrimSpace(r.URL.Query().Get("interval")); raw != "" {
		if v, pErr := strconv.ParseFloat(raw, 64); pErr == nil && v >= 1 {
			interval = v
		}
	}

	key := trickplayCacheKey(input, interval)
	dir := m.trickplayDir()
	if err := os.MkdirAll(dir, 0o750); err != nil {
		http.Error(w, "trickplay cache dir: "+err.Error(), http.StatusInternalServerError)
		return
	}
	spritePath := filepath.Join(dir, key+".jpg")

	sprite := computeTrickplayGrid(duration, interval)

	lock := lockFor(key)
	lock.Lock()
	if _, statErr := os.Stat(spritePath); statErr != nil {
		if genErr := m.generateTrickplaySprite(input, spritePath, sprite); genErr != nil {
			lock.Unlock()
			slog.Warn("trickplay generation failed", "src", input, "error", genErr)
			http.Error(w, "trickplay generation failed: "+genErr.Error(), http.StatusInternalServerError)
			return
		}
	}
	lock.Unlock()

	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "public, max-age=86400, immutable")
	w.Header().Set("X-Trickplay-Interval-Seconds", strconv.FormatFloat(sprite.Interval, 'f', 2, 64))
	w.Header().Set("X-Trickplay-Cols", strconv.Itoa(sprite.Cols))
	w.Header().Set("X-Trickplay-Rows", strconv.Itoa(sprite.Rows))
	w.Header().Set("X-Trickplay-Count", strconv.Itoa(sprite.Count))
	http.ServeFile(w, r, spritePath)
}

// computeTrickplayGrid picks a tile grid sized to exactly hold every sample
// ffmpeg's `fps=1/interval` filter will emit for the given duration, so the
// tile filter always completes on EOF flush without hanging or truncating.
func computeTrickplayGrid(durationSeconds, interval float64) trickplaySprite {
	count := int(math.Floor(durationSeconds/interval)) + 1
	if count < 1 {
		count = 1
	}
	if count > 400 {
		// Long runtimes: widen the interval instead of an unbounded sprite sheet.
		interval = durationSeconds / 399
		count = 400
	}
	cols := int(math.Ceil(math.Sqrt(float64(count))))
	if cols < 1 {
		cols = 1
	}
	rows := int(math.Ceil(float64(count) / float64(cols)))
	return trickplaySprite{Interval: interval, Cols: cols, Rows: rows, Count: count}
}

func (m *Module) generateTrickplaySprite(input, outPath string, sprite trickplaySprite) error {
	tmp := outPath + ".tmp"
	vf := fmt.Sprintf("fps=1/%.4f,scale=160:-1,tile=%dx%d", sprite.Interval, sprite.Cols, sprite.Rows)
	args := []string{
		"-hide_banner", "-loglevel", "error",
		"-i", input,
		"-vf", vf,
		"-frames:v", "1",
		"-q:v", "4",
		"-y", tmp,
	}
	cmd := exec.Command(m.getFFmpegBin(), args...) //nolint:gosec // ffmpeg paths come from operator-controlled media library
	out, err := cmd.CombinedOutput()
	if err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return os.Rename(tmp, outPath)
}
