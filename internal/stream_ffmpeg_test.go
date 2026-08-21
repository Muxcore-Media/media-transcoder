package internal

import (
	"strings"
	"testing"

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
	args := m.buildPlaybackStreamArgs(profile, "http://127.0.0.1/stream/m1", streamEncoderSoftware)
	joined := strings.Join(args, " ")
	for _, want := range []string{"-i", "http://127.0.0.1/stream/m1", "-c:v", "libx264", "-crf", "21", "-c:a", "aac", "-f", "mp4", "pipe:1", "frag_keyframe"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in %s", want, joined)
		}
	}
}

func TestBuildPlaybackStreamArgsNVENC(t *testing.T) {
	m := NewModule(Config{})
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
