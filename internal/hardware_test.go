package internal

import (
	"strings"
	"testing"

	transcodev1 "github.com/Muxcore-Media/media-transcoder/proto/transcodev1"
)

func TestParseStreamEncoderModeAllBackends(t *testing.T) {
	t.Parallel()
	cases := map[string]hwBackend{
		"software":     hwSoftware,
		"nvenc":        hwNVENC,
		"vaapi":        hwVAAPI,
		"qsv":          hwQSV,
		"intel":        hwQSV,
		"amf":          hwAMF,
		"amd":          hwAMF,
		"videotoolbox": hwVideoToolbox,
		"vt":           hwVideoToolbox,
		"":             hwAuto,
	}
	for in, want := range cases {
		if got := parseStreamEncoderMode(in); got != want {
			t.Fatalf("parseStreamEncoderMode(%q)=%q want %q", in, got, want)
		}
	}
}

func TestNormalizeVideoCodec(t *testing.T) {
	t.Parallel()
	if normalizeVideoCodec("HEVC") != "hevc" {
		t.Fatal("hevc")
	}
	if normalizeVideoCodec("") != "h264" {
		t.Fatal("default h264")
	}
}

func TestSoftwareEncoder(t *testing.T) {
	t.Parallel()
	if softwareEncoder("hevc") != "libx265" {
		t.Fatal("libx265")
	}
	if softwareEncoder("av1") != "libaom-av1" {
		t.Fatal("libaom-av1")
	}
}

func TestPickHWBackendExplicitFallback(t *testing.T) {
	m := NewModule(Config{})
	profile := &transcodev1.TranscodeProfile{UseGpu: true, VideoCodec: "hevc"}
	if got := m.pickHWBackend(profile, hwQSV, "hevc"); got != hwSoftware {
		t.Fatalf("missing qsv should fall back to software, got %q", got)
	}
}

func TestVideoEncodeArgsSoftwareCodecs(t *testing.T) {
	m := NewModule(Config{})
	profile := &transcodev1.TranscodeProfile{VideoCodec: "av1", Preset: "fast", Crf: 28}
	args := m.videoEncodeArgs(profile, hwSoftware)
	joined := joinArgs(args)
	if !containsAll(joined, "-c:v", "libaom-av1", "-crf", "28") {
		t.Fatalf("unexpected args: %s", joined)
	}
}

func TestVideoEncodeArgsNVENC(t *testing.T) {
	m := NewModule(Config{})
	m.hwEncoders = " V..... h264_nvenc  V..... hevc_nvenc  V..... av1_nvenc "
	profile := &transcodev1.TranscodeProfile{VideoCodec: "hevc", Preset: "medium", Crf: 24}
	args := m.videoEncodeArgs(profile, hwNVENC)
	joined := joinArgs(args)
	if !containsAll(joined, "-c:v", "hevc_nvenc", "-cq", "24") {
		t.Fatalf("unexpected args: %s", joined)
	}
}

func TestVideoEncodeArgsVAAPI(t *testing.T) {
	m := NewModule(Config{})
	m.hwEncoders = " V..... h264_vaapi  V..... hevc_vaapi "
	profile := &transcodev1.TranscodeProfile{VideoCodec: "h264", Crf: 22}
	args := m.videoEncodeArgs(profile, hwVAAPI)
	joined := joinArgs(args)
	if !containsAll(joined, "-c:v", "h264_vaapi", "-qp", "22") {
		t.Fatalf("unexpected args: %s", joined)
	}
}

func TestVideoEncodeArgsQSV(t *testing.T) {
	m := NewModule(Config{})
	m.hwEncoders = " V..... h264_qsv  V..... hevc_qsv "
	profile := &transcodev1.TranscodeProfile{VideoCodec: "h264", Preset: "fast", Crf: 23}
	args := m.videoEncodeArgs(profile, hwQSV)
	joined := joinArgs(args)
	if !containsAll(joined, "-c:v", "h264_qsv", "-global_quality", "23") {
		t.Fatalf("unexpected args: %s", joined)
	}
}

func TestVideoEncodeArgsAMF(t *testing.T) {
	m := NewModule(Config{})
	m.hwEncoders = " V..... h264_amf "
	profile := &transcodev1.TranscodeProfile{VideoCodec: "h264", Preset: "medium", Crf: 23}
	args := m.videoEncodeArgs(profile, hwAMF)
	joined := joinArgs(args)
	if !containsAll(joined, "-c:v", "h264_amf", "-rc", "cqp", "-qp_i", "23") {
		t.Fatalf("unexpected args: %s", joined)
	}
}

func TestVideoEncodeArgsVideoToolbox(t *testing.T) {
	m := NewModule(Config{})
	m.hwEncoders = " V..... h264_videotoolbox "
	profile := &transcodev1.TranscodeProfile{VideoCodec: "h264", Crf: 23}
	args := m.videoEncodeArgs(profile, hwVideoToolbox)
	joined := joinArgs(args)
	if !containsAll(joined, "-c:v", "h264_videotoolbox", "-q:v", "77") {
		t.Fatalf("unexpected args: %s", joined)
	}
}

func TestHWAccelInputArgs(t *testing.T) {
	m := NewModule(Config{})
	vaapi := joinArgs(m.hwAccelInputArgs(hwVAAPI))
	if !containsAll(vaapi, "-hwaccel", "vaapi", "-hwaccel_device", "/dev/dri/renderD128") {
		t.Fatalf("vaapi args: %s", vaapi)
	}
	qsv := joinArgs(m.hwAccelInputArgs(hwQSV))
	if !containsAll(qsv, "-init_hw_device", "-hwaccel", "qsv") {
		t.Fatalf("qsv args: %s", qsv)
	}
	vt := joinArgs(m.hwAccelInputArgs(hwVideoToolbox))
	if !containsAll(vt, "-hwaccel", "videotoolbox") {
		t.Fatalf("vt args: %s", vt)
	}
}

func TestScaleFilterVAAPIAndQSV(t *testing.T) {
	m := NewModule(Config{})
	profile := &transcodev1.TranscodeProfile{MaxHeight: 1080}
	vaapi := m.scaleFilter(profile, hwVAAPI)
	if vaapi == "" || !containsAll(vaapi, "scale_vaapi", "1080") {
		t.Fatalf("vaapi scale: %q", vaapi)
	}
	qsv := m.scaleFilter(profile, hwQSV)
	if qsv == "" || !containsAll(qsv, "scale_qsv", "1080") {
		t.Fatalf("qsv scale: %q", qsv)
	}
}

func TestBuildFFmpegArgsUsesSharedHWPath(t *testing.T) {
	m := NewModule(Config{})
	m.hwEncoders = " V..... h264_qsv "
	profile := &transcodev1.TranscodeProfile{
		VideoCodec: "h264",
		AudioCodec: "copy",
		Preset:     "fast",
		Crf:        23,
		UseGpu:     true,
		MaxHeight:  720,
	}
	args := m.buildFFmpegArgs(profile, "/in.mkv", "/out.mkv")
	joined := joinArgs(args)
	if !containsAll(joined, "-i", "/in.mkv", "/out.mkv", "h264_qsv", "scale_qsv", "-c:a", "copy") {
		t.Fatalf("unexpected offline args: %s", joined)
	}
}

func TestDetectHardwareDevicesFromEncoderList(t *testing.T) {
	m := NewModule(Config{})
	m.hwEncoders = " V..... h264_nvenc  V..... hevc_vaapi  V..... h264_qsv  V..... h264_amf  V..... h264_videotoolbox "
	devices := m.detectHardwareDevices()
	if len(devices) < 5 {
		t.Fatalf("expected multiple devices, got %d", len(devices))
	}
}

func joinArgs(args []string) string {
	return strings.Join(args, " ")
}

func containsAll(haystack string, parts ...string) bool {
	for _, p := range parts {
		if !strings.Contains(haystack, p) {
			return false
		}
	}
	return true
}
