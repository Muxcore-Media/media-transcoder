package internal

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"

	transcodev1 "github.com/Muxcore-Media/media-transcoder/proto/transcodev1"
)

type hwBackend string

const (
	hwAuto         hwBackend = "auto"
	hwSoftware     hwBackend = "software"
	hwNVENC        hwBackend = "nvenc"
	hwVAAPI        hwBackend = "vaapi"
	hwQSV          hwBackend = "qsv"
	hwAMF          hwBackend = "amf"
	hwVideoToolbox hwBackend = "videotoolbox"
)

type streamEncoderMode = hwBackend

const (
	streamEncoderAuto     = hwAuto
	streamEncoderSoftware = hwSoftware
	streamEncoderNVENC    = hwNVENC
	streamEncoderVAAPI    = hwVAAPI
)

type hwCodecMap map[string]string

type hwBackendSpec struct {
	Type        string
	DisplayName string
	Codecs      hwCodecMap
}

var hwBackendCatalog = []hwBackendSpec{
	{
		Type:        "nvenc",
		DisplayName: "NVIDIA GPU",
		Codecs:      hwCodecMap{"h264": "h264_nvenc", "hevc": "hevc_nvenc", "av1": "av1_nvenc"},
	},
	{
		Type:        "vaapi",
		DisplayName: "VAAPI",
		Codecs:      hwCodecMap{"h264": "h264_vaapi", "hevc": "hevc_vaapi", "av1": "av1_vaapi"},
	},
	{
		Type:        "qsv",
		DisplayName: "Intel Quick Sync",
		Codecs:      hwCodecMap{"h264": "h264_qsv", "hevc": "hevc_qsv", "av1": "av1_qsv"},
	},
	{
		Type:        "amf",
		DisplayName: "AMD AMF",
		Codecs:      hwCodecMap{"h264": "h264_amf", "hevc": "hevc_amf", "av1": "av1_amf"},
	},
	{
		Type:        "videotoolbox",
		DisplayName: "Apple VideoToolbox",
		Codecs:      hwCodecMap{"h264": "h264_videotoolbox", "hevc": "hevc_videotoolbox", "av1": "av1_videotoolbox"},
	},
}

func parseStreamEncoderMode(raw string) hwBackend {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "software", "sw", "cpu":
		return hwSoftware
	case "nvenc", "gpu", "cuda", "nvidia":
		return hwNVENC
	case "vaapi":
		return hwVAAPI
	case "qsv", "intel", "quicksync":
		return hwQSV
	case "amf", "amd":
		return hwAMF
	case "videotoolbox", "vt", "apple", "mac":
		return hwVideoToolbox
	default:
		return hwAuto
	}
}

func (m *Module) ffmpegEncoders() string {
	m.hwOnce.Do(func() {
		out, err := exec.Command(m.getFFmpegBin(), "-encoders", "-hide_banner").Output()
		if err == nil {
			m.hwEncoders = string(out)
		}
	})
	return m.hwEncoders
}

func (m *Module) hasEncoder(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	return strings.Contains(m.ffmpegEncoders(), name)
}

func (m *Module) backendAvailable(backend hwBackend) bool {
	spec, ok := hwBackendByType(string(backend))
	if !ok {
		return false
	}
	for _, enc := range spec.Codecs {
		if m.hasEncoder(enc) {
			return true
		}
	}
	return false
}

func (m *Module) backendSupportsCodec(backend hwBackend, videoCodec string) bool {
	enc := m.backendEncoder(backend, normalizeVideoCodec(videoCodec))
	return enc != "" && m.hasEncoder(enc)
}

func hwBackendByType(typeName string) (hwBackendSpec, bool) {
	for _, spec := range hwBackendCatalog {
		if spec.Type == typeName {
			return spec, true
		}
	}
	return hwBackendSpec{}, false
}

func normalizeVideoCodec(codec string) string {
	switch strings.ToLower(strings.TrimSpace(codec)) {
	case "hevc", "h265":
		return "hevc"
	case "av1":
		return "av1"
	default:
		return "h264"
	}
}

func softwareEncoder(videoCodec string) string {
	switch normalizeVideoCodec(videoCodec) {
	case "hevc":
		return "libx265"
	case "av1":
		return "libaom-av1"
	default:
		return "libx264"
	}
}

func (m *Module) backendEncoder(backend hwBackend, videoCodec string) string {
	spec, ok := hwBackendByType(string(backend))
	if !ok {
		return ""
	}
	enc, ok := spec.Codecs[normalizeVideoCodec(videoCodec)]
	if !ok {
		return ""
	}
	return enc
}

func defaultHWAutoOrder() []hwBackend {
	switch runtime.GOOS {
	case "darwin":
		return []hwBackend{hwVideoToolbox, hwNVENC, hwQSV, hwVAAPI, hwAMF}
	case "windows":
		return []hwBackend{hwNVENC, hwQSV, hwAMF, hwVAAPI, hwVideoToolbox}
	default:
		return []hwBackend{hwNVENC, hwVAAPI, hwQSV, hwAMF, hwVideoToolbox}
	}
}

func (m *Module) pickHWBackend(profile *transcodev1.TranscodeProfile, mode hwBackend, videoCodec string) hwBackend {
	if mode == hwSoftware {
		return hwSoftware
	}
	if mode != hwAuto && mode != "" {
		if m.backendSupportsCodec(mode, videoCodec) {
			return mode
		}
		return hwSoftware
	}
	if profile != nil && profile.GetUseGpu() {
		for _, backend := range defaultHWAutoOrder() {
			if m.backendSupportsCodec(backend, videoCodec) {
				return backend
			}
		}
	}
	return hwSoftware
}

func (m *Module) pickStreamEncoder(profile *transcodev1.TranscodeProfile, mode hwBackend) hwBackend {
	videoCodec := "h264"
	if profile != nil && profile.GetVideoCodec() != "" {
		videoCodec = profile.GetVideoCodec()
	}
	return m.pickHWBackend(profile, mode, videoCodec)
}

func (m *Module) detectHardwareDevices() []*transcodev1.HardwareDevice {
	var devices []*transcodev1.HardwareDevice
	for _, spec := range hwBackendCatalog {
		for _, enc := range spec.Codecs {
			if !m.hasEncoder(enc) {
				continue
			}
			devices = append(devices, &transcodev1.HardwareDevice{
				Name:      spec.DisplayName,
				Type:      spec.Type,
				Available: true,
				Encoder:   enc,
			})
		}
	}
	return devices
}

func profileCRF(profile *transcodev1.TranscodeProfile) int {
	crf := int32(23)
	if profile != nil && profile.GetCrf() > 0 {
		crf = profile.GetCrf()
	}
	return int(crf)
}

func profilePreset(profile *transcodev1.TranscodeProfile) string {
	if profile == nil || profile.GetPreset() == "" {
		return "fast"
	}
	return profile.GetPreset()
}

func vaapiDevice() string {
	device := strings.TrimSpace(os.Getenv("TRANSCODER_VAAPI_DEVICE"))
	if device == "" {
		device = "/dev/dri/renderD128"
	}
	return device
}

func qsvDevice() string {
	device := strings.TrimSpace(os.Getenv("TRANSCODER_QSV_DEVICE"))
	if device == "" {
		device = vaapiDevice()
	}
	return device
}

func (m *Module) hwAccelInputArgs(backend hwBackend) []string {
	switch backend {
	case hwVAAPI:
		return []string{"-hwaccel", "vaapi", "-hwaccel_device", vaapiDevice()}
	case hwQSV:
		return []string{
			"-init_hw_device", "qsv=hw@" + qsvDevice(),
			"-filter_hw_device", "hw",
			"-hwaccel", "qsv",
			"-hwaccel_device", "hw",
		}
	case hwVideoToolbox:
		return []string{"-hwaccel", "videotoolbox"}
	case hwAMF:
		if runtime.GOOS == "windows" {
			return []string{"-hwaccel", "d3d11va"}
		}
	}
	return nil
}

func (m *Module) videoEncodeArgs(profile *transcodev1.TranscodeProfile, backend hwBackend) []string {
	crf := profileCRF(profile)
	videoCodec := normalizeVideoCodec("")
	if profile != nil {
		videoCodec = normalizeVideoCodec(profile.GetVideoCodec())
	}

	if backend == hwSoftware || !m.backendSupportsCodec(backend, videoCodec) {
		preset := profilePreset(profile)
		return []string{
			"-c:v", softwareEncoder(videoCodec),
			"-preset", preset,
			"-crf", strconv.Itoa(crf),
		}
	}

	enc := m.backendEncoder(backend, videoCodec)
	switch backend {
	case hwNVENC:
		return []string{"-c:v", enc, "-preset", nvencPreset(profilePreset(profile)), "-cq", strconv.Itoa(crf)}
	case hwVAAPI:
		return []string{"-c:v", enc, "-qp", strconv.Itoa(crf)}
	case hwQSV:
		return []string{"-c:v", enc, "-preset", qsvPreset(profilePreset(profile)), "-global_quality", strconv.Itoa(crf)}
	case hwAMF:
		qp := strconv.Itoa(crf)
		return []string{
			"-c:v", enc,
			"-quality", amfQuality(profilePreset(profile)),
			"-rc", "cqp",
			"-qp_i", qp,
			"-qp_p", qp,
		}
	case hwVideoToolbox:
		return []string{"-c:v", enc, "-q:v", strconv.Itoa(vtQuality(crf))}
	default:
		return []string{"-c:v", softwareEncoder(videoCodec), "-preset", profilePreset(profile), "-crf", strconv.Itoa(crf)}
	}
}

func qsvPreset(preset string) string {
	switch preset {
	case "fast":
		return "veryfast"
	case "medium":
		return "medium"
	case "slow", "veryslow":
		return "slow"
	default:
		return "medium"
	}
}

func amfQuality(preset string) string {
	switch preset {
	case "fast":
		return "speed"
	case "slow", "veryslow":
		return "quality"
	default:
		return "balanced"
	}
}

func vtQuality(crf int) int {
	q := 100 - crf
	if q < 1 {
		return 1
	}
	if q > 100 {
		return 100
	}
	return q
}

func nvencPreset(preset string) string {
	switch preset {
	case "fast":
		return "p1"
	case "medium":
		return "p4"
	case "slow":
		return "p6"
	case "veryslow":
		return "p7"
	default:
		return "p4"
	}
}

func (m *Module) scaleFilter(profile *transcodev1.TranscodeProfile, backend hwBackend) string {
	maxW := int32(0)
	maxH := int32(0)
	if profile != nil {
		maxW = profile.GetMaxWidth()
		maxH = profile.GetMaxHeight()
	}
	scaleExpr := buildScaleExpression(maxW, maxH)

	switch backend {
	case hwVAAPI:
		if scaleExpr == "" {
			return "format=nv12,hwupload"
		}
		return fmt.Sprintf("format=nv12,hwupload,scale_vaapi=%s", scaleExpr)
	case hwQSV:
		if scaleExpr == "" {
			return ""
		}
		return fmt.Sprintf("scale_qsv=%s", scaleExpr)
	default:
		if scaleExpr == "" {
			return ""
		}
		return fmt.Sprintf("scale=%s", scaleExpr)
	}
}

func (m *Module) buildFFmpegArgs(profile *transcodev1.TranscodeProfile, input, output string) []string {
	videoCodec := "h264"
	if profile != nil && profile.GetVideoCodec() != "" {
		videoCodec = profile.GetVideoCodec()
	}
	backend := m.pickHWBackend(profile, hwAuto, videoCodec)

	args := []string{"-hide_banner", "-y", "-progress", "pipe:1"}
	args = append(args, m.hwAccelInputArgs(backend)...)
	args = append(args, "-i", input)

	if vf := m.scaleFilter(profile, backend); vf != "" {
		args = append(args, "-vf", vf)
	}
	args = append(args, m.videoEncodeArgs(profile, backend)...)

	audioCodec := "copy"
	if profile != nil {
		audioCodec = profile.GetAudioCodec()
	}
	if audioCodec == "" || audioCodec == "copy" {
		args = append(args, "-c:a", "copy")
	} else {
		args = append(args, "-c:a", audioCodec)
	}

	args = append(args, output)
	return args
}
