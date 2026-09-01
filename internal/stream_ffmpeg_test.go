package internal

import (
	"context"
	"strings"
	"testing"

	transcodev1 "github.com/Muxcore-Media/media-transcoder/proto/transcodev1"
)

func TestResolveStreamInput(t *testing.T) {
	t.Parallel()
	m := NewModule(Config{})
	if _, err := m.resolveStreamInput(context.Background(), ""); err == nil {
		t.Fatal("expected error for empty src")
	}
	if _, err := m.resolveStreamInput(context.Background(), "relative/path.mkv"); err == nil {
		t.Fatal("expected error for relative path")
	}
	got, err := m.resolveStreamInput(context.Background(), "http://127.0.0.1:9430/stream/movies/m1")
	if err != nil {
		t.Fatal(err)
	}
	if got != "http://127.0.0.1:9430/stream/movies/m1" {
		t.Fatalf("got %q", got)
	}
	if _, err := m.resolveStreamInput(context.Background(), "https://evil.example/x.mkv"); err == nil {
		t.Fatal("expected error for non-loopback url")
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
	args := m.buildPlaybackStreamArgs(profile, "http://127.0.0.1/stream/m1", streamEncoderSoftware, 0, -1)
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
	args := m.buildPlaybackStreamArgs(profile, "http://127.0.0.1/stream/m1", streamEncoderSoftware, 611.5, -1)
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

func TestBuildPlaybackStreamArgsAudioMap(t *testing.T) {
	m := NewModule(Config{})
	profile := &transcodev1.TranscodeProfile{VideoCodec: "h264", Crf: 21}
	args := m.buildPlaybackStreamArgs(profile, "http://127.0.0.1/stream/m1", streamEncoderSoftware, 0, 3)
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
