package internal

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	transcodev1 "github.com/Muxcore-Media/media-transcoder/proto/transcodev1"
)

func TestResolveStreamInput(t *testing.T) {
	t.Parallel()
	if _, err := resolveStreamInput(""); err == nil {
		t.Fatal("expected error for empty src")
	}
	if _, err := resolveStreamInput("relative/path.mkv"); err == nil {
		t.Fatal("expected error for relative path")
	}
	got, err := resolveStreamInput("http://127.0.0.1:9430/stream/movies/m1")
	if err != nil {
		t.Fatal(err)
	}
	if got != "http://127.0.0.1:9430/stream/movies/m1" {
		t.Fatalf("got %q", got)
	}
}

func TestBuildPlaybackStreamArgsSoftware(t *testing.T) {
	m := NewModule(Config{})
	profile := &transcodev1.TranscodeProfile{
		VideoCodec: "h264",
		AudioCodec: "copy",
		Preset:     "fast",
		Crf:        21,
		MaxHeight:  1080,
	}
	args := m.buildPlaybackStreamArgs(profile, "http://127.0.0.1/stream/m1", streamEncoderSoftware, 0, -1, -1)
	joined := strings.Join(args, " ")
	for _, want := range []string{"-i", "http://127.0.0.1/stream/m1", "-c:v", "libx264", "-crf", "21", "-c:a", "aac", "-f", "mp4", "pipe:1", "frag_keyframe"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in %s", want, joined)
		}
	}
	if strings.Contains(joined, "-ss") {
		t.Fatalf("did not expect -ss with startSeconds=0: %s", joined)
	}
}

func TestBuildPlaybackStreamArgsSeek(t *testing.T) {
	m := NewModule(Config{})
	profile := &transcodev1.TranscodeProfile{
		VideoCodec: "h264",
		AudioCodec: "copy",
		Preset:     "fast",
		Crf:        21,
	}
	args := m.buildPlaybackStreamArgs(profile, "http://127.0.0.1/stream/m1", streamEncoderSoftware, 611.5, -1, -1)
	joined := strings.Join(args, " ")
	for _, want := range []string{"-ss 611.500", "-avoid_negative_ts make_zero"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in %s", want, joined)
		}
	}
	ssIdx := indexOf(args, "-ss")
	iIdx := indexOf(args, "-i")
	if ssIdx < 0 || iIdx < 0 || ssIdx > iIdx {
		t.Fatalf("-ss must precede -i for fast input seek: %v", args)
	}
}

func TestBuildHLSStreamArgs(t *testing.T) {
	m := NewModule(Config{})
	profile := &transcodev1.TranscodeProfile{
		VideoCodec: "h264",
		AudioCodec: "copy",
		Preset:     "fast",
		Crf:        21,
		MaxHeight:  1080,
	}
	args := m.buildHLSStreamArgs(profile, "http://127.0.0.1/stream/m1", streamEncoderSoftware, 0, -1, -1, "/tmp/index.m3u8", "/tmp/seg_%05d.ts")
	joined := strings.Join(args, " ")
	for _, want := range []string{"-i", "http://127.0.0.1/stream/m1", "-c:v", "libx264", "-f", "hls", "-hls_playlist_type", "event", "/tmp/index.m3u8"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in %s", want, joined)
		}
	}
	if strings.Contains(joined, "pipe:1") || strings.Contains(joined, "-ss") {
		t.Fatalf("hls from 0 must not pipe or input-seek: %s", joined)
	}
}

func TestBuildHLSStreamArgsSeek(t *testing.T) {
	m := NewModule(Config{})
	profile := &transcodev1.TranscodeProfile{VideoCodec: "h264", AudioCodec: "copy", Preset: "fast", Crf: 21}
	args := m.buildHLSStreamArgs(profile, "http://127.0.0.1/stream/m1", streamEncoderSoftware, 611.5, -1, -1, "/tmp/index.m3u8", "/tmp/seg_%05d.ts")
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "-ss 611.500") || !strings.Contains(joined, "-f hls") || !strings.Contains(joined, "-avoid_negative_ts make_zero") {
		t.Fatalf("hls seek remount: %s", joined)
	}
}

func TestHLSSessionKeyStable(t *testing.T) {
	t.Parallel()
	a := hlsSessionKey("http://x/m1", "h264_fast", streamEncoderSoftware, 720, 3, -1, 0)
	b := hlsSessionKey("http://x/m1", "h264_fast", streamEncoderSoftware, 720, 3, -1, 0)
	c := hlsSessionKey("http://x/m1", "h264_fast", streamEncoderSoftware, 1080, 3, -1, 0)
	d := hlsSessionKey("http://x/m1", "h264_fast", streamEncoderSoftware, 720, 3, -1, hlsStartBucket(611.5))
	if a != b || len(a) != 32 {
		t.Fatalf("stable hex key: %q %q", a, b)
	}
	if a == c {
		t.Fatal("quality change must change session key")
	}
	if a == d {
		t.Fatal("seek remount must change session key")
	}
	if hlsStartBucket(611.5) != hlsStartBucket(611.9) || hlsStartBucket(0) != 0 {
		t.Fatalf("4s buckets: %d %d", hlsStartBucket(611.5), hlsStartBucket(611.9))
	}
	if hlsStartBucket(611.5) == hlsStartBucket(616) {
		t.Fatal("next 4s bucket must differ")
	}
}

func TestWaitForFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "seg_00000.ts")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go func() {
		time.Sleep(80 * time.Millisecond)
		_ = os.WriteFile(path, []byte("ts"), 0o600)
	}()
	if err := waitForFile(ctx, path, time.Second); err != nil {
		t.Fatal(err)
	}
}

func TestValidHLSPath(t *testing.T) {
	t.Parallel()
	if !validHLSKey("0123456789abcdef0123456789abcdef") {
		t.Fatal("hex key")
	}
	if validHLSKey("../etc") || validHLSKey("ABC") {
		t.Fatal("rejected key")
	}
	if !validHLSFile("index.m3u8") || !validHLSFile("seg_00000.ts") || !validHLSFile("seg_12.ts") {
		t.Fatal("valid files")
	}
	if validHLSFile("../x.ts") || validHLSFile("index.m3u8.bak") {
		t.Fatal("rejected file")
	}
}

func TestBuildPlaybackStreamArgsAudioMap(t *testing.T) {
	m := NewModule(Config{})
	profile := &transcodev1.TranscodeProfile{VideoCodec: "h264", Crf: 21}
	args := m.buildPlaybackStreamArgs(profile, "http://127.0.0.1/stream/m1", streamEncoderSoftware, 0, 3, -1)
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "-map 0:v:0") && !strings.Contains(joined, "-map") {
		t.Fatalf("missing video map: %s", joined)
	}
	if !strings.Contains(joined, "-map 0:3") {
		t.Fatalf("missing audio stream map: %s", joined)
	}
}

func indexOf(args []string, want string) int {
	for i, a := range args {
		if a == want {
			return i
		}
	}
	return -1
}

func TestBuildPlaybackStreamArgsNVENC(t *testing.T) {
	m := NewModule(Config{})
	m.hwEncoders = "hevc_nvenc"
	profile := &transcodev1.TranscodeProfile{
		VideoCodec: "hevc",
		Preset:     "medium",
		Crf:        24,
		UseGpu:     true,
	}
	args := append([]string{"-hide_banner"}, m.videoEncodeArgs(profile, streamEncoderNVENC)...)
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "hevc_nvenc") {
		t.Fatalf("expected hevc_nvenc in %s", joined)
	}
}

func TestBuildPlaybackStreamArgsSubtitleBurn(t *testing.T) {
	m := NewModule(Config{})
	profile := &transcodev1.TranscodeProfile{VideoCodec: "h264", Crf: 21, MaxHeight: 720}
	args := m.buildPlaybackStreamArgs(profile, "http://127.0.0.1/stream/m1", streamEncoderSoftware, 0, -1, 4)
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "-filter_complex") {
		t.Fatalf("missing filter_complex for burn-in: %s", joined)
	}
	if !strings.Contains(joined, "[0:4]overlay[vout]") {
		t.Fatalf("missing overlay of stream 4: %s", joined)
	}
	if !strings.Contains(joined, "-map [vout]") {
		t.Fatalf("missing burned video map: %s", joined)
	}
	if strings.Contains(joined, "-map 0:v:0") {
		t.Fatalf("raw video map must not be used when burning: %s", joined)
	}
}

func TestOverlayBurnFilter(t *testing.T) {
	t.Parallel()
	if got := overlayBurnFilter("", 3); got != "[0:v:0][0:3]overlay[vout]" {
		t.Fatalf("got %q", got)
	}
	if got := overlayBurnFilter("scale='iw':'min(720,ih)':force_original_aspect_ratio=decrease", 2); !strings.Contains(got, "[vscaled][0:2]overlay[vout]") {
		t.Fatalf("got %q", got)
	}
}

func TestParseStreamEncoderMode(t *testing.T) {
	t.Parallel()
	if parseStreamEncoderMode("nvenc") != streamEncoderNVENC {
		t.Fatal("nvenc")
	}
	if parseStreamEncoderMode("software") != streamEncoderSoftware {
		t.Fatal("software")
	}
	if parseStreamEncoderMode("") != streamEncoderAuto {
		t.Fatal("auto default")
	}
}

func TestPickStreamEncoderExplicitSoftware(t *testing.T) {
	m := NewModule(Config{})
	profile := &transcodev1.TranscodeProfile{UseGpu: true}
	if got := m.pickStreamEncoder(profile, streamEncoderSoftware); got != streamEncoderSoftware {
		t.Fatalf("got %q", got)
	}
}
