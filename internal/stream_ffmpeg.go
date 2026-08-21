package internal

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	transcodev1 "github.com/Muxcore-Media/media-transcoder/proto/transcodev1"
)

type streamEncoderMode string

const (
	streamEncoderAuto     streamEncoderMode = "auto"
	streamEncoderSoftware streamEncoderMode = "software"
	streamEncoderNVENC    streamEncoderMode = "nvenc"
	streamEncoderVAAPI    streamEncoderMode = "vaapi"
)

func parseStreamEncoderMode(raw string) streamEncoderMode {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "software", "sw", "cpu":
		return streamEncoderSoftware
	case "nvenc", "gpu", "cuda":
		return streamEncoderNVENC
	case "vaapi":
		return streamEncoderVAAPI
	default:
		return streamEncoderAuto
	}
}

func resolveStreamInput(src string) (string, error) {
	src = strings.TrimSpace(src)
	if src == "" {
		return "", fmt.Errorf("src required")
	}
	if strings.HasPrefix(src, "http://") || strings.HasPrefix(src, "https://") {
		u, err := url.Parse(src)
		if err != nil {
			return "", fmt.Errorf("invalid src url: %w", err)
		}
		if u.Host == "" {
			return "", fmt.Errorf("invalid src url host")
		}
		return src, nil
	}
	if !filepath.IsAbs(src) {
		return "", fmt.Errorf("src must be an absolute path or http(s) url")
	}
	st, err := os.Stat(src)
	if err != nil {
		return "", fmt.Errorf("src not accessible: %w", err)
	}
	if st.IsDir() {
		return "", fmt.Errorf("src must be a file")
	}
	return src, nil
}

func (m *Module) pickStreamEncoder(profile *transcodev1.TranscodeProfile, mode streamEncoderMode) streamEncoderMode {
	switch mode {
	case streamEncoderSoftware:
		return streamEncoderSoftware
	case streamEncoderNVENC:
		if m.hasNVENC() {
			return streamEncoderNVENC
		}
		return streamEncoderSoftware
	case streamEncoderVAAPI:
		if m.hasVAAPI() {
			return streamEncoderVAAPI
		}
		return streamEncoderSoftware
	default:
		if profile.GetUseGpu() {
			if m.hasNVENC() {
				return streamEncoderNVENC
			}
			if m.hasVAAPI() {
				return streamEncoderVAAPI
			}
		}
		return streamEncoderSoftware
	}
}

func (m *Module) buildPlaybackStreamArgs(profile *transcodev1.TranscodeProfile, input string, mode streamEncoderMode) []string {
	encoder := m.pickStreamEncoder(profile, mode)
	args := []string{"-hide_banner", "-loglevel", "error"}

	if encoder == streamEncoderVAAPI {
		device := strings.TrimSpace(os.Getenv("TRANSCODER_VAAPI_DEVICE"))
		if device == "" {
			device = "/dev/dri/renderD128"
		}
		args = append(args, "-hwaccel", "vaapi", "-hwaccel_device", device)
	}

	args = append(args, "-i", input)

	if vf := m.scaleFilter(profile, encoder); vf != "" {
		args = append(args, "-vf", vf)
	}
	args = append(args, m.videoEncodeArgs(profile, encoder)...)

	audioCodec := profile.GetAudioCodec()
	if audioCodec == "" || audioCodec == "copy" {
		args = append(args, "-c:a", "aac", "-b:a", "192k")
	} else {
		args = append(args, "-c:a", audioCodec)
	}

	args = append(args,
		"-movflags", "frag_keyframe+empty_moov+default_base_moof",
		"-f", "mp4",
		"pipe:1",
	)
	return args
}

func (m *Module) videoEncodeArgs(profile *transcodev1.TranscodeProfile, encoder streamEncoderMode) []string {
	crf := profile.GetCrf()
	if crf <= 0 {
		crf = 23
	}
	switch encoder {
	case streamEncoderNVENC:
		codec := "h264_nvenc"
		switch profile.GetVideoCodec() {
		case "hevc":
			codec = "hevc_nvenc"
		case "av1":
			codec = "av1_nvenc"
		}
		return []string{"-c:v", codec, "-preset", nvencPreset(profile.GetPreset()), "-cq", strconv.Itoa(int(crf))}
	case streamEncoderVAAPI:
		codec := "h264_vaapi"
		switch profile.GetVideoCodec() {
		case "hevc":
			codec = "hevc_vaapi"
		}
		return []string{"-c:v", codec, "-qp", strconv.Itoa(int(crf))}
	default:
		codec := "libx264"
		switch profile.GetVideoCodec() {
		case "hevc":
			codec = "libx265"
		case "av1":
			codec = "libaom-av1"
		}
		preset := profile.GetPreset()
		if preset == "" {
			preset = "fast"
		}
		return []string{"-c:v", codec, "-preset", preset, "-crf", strconv.Itoa(int(crf))}
	}
}

func (m *Module) scaleFilter(profile *transcodev1.TranscodeProfile, encoder streamEncoderMode) string {
	maxW := profile.GetMaxWidth()
	maxH := profile.GetMaxHeight()
	if maxW <= 0 && maxH <= 0 {
		if encoder == streamEncoderVAAPI {
			return "format=nv12,hwupload"
		}
		return ""
	}

	scaleExpr := buildScaleExpression(maxW, maxH)
	switch encoder {
	case streamEncoderVAAPI:
		return fmt.Sprintf("format=nv12,hwupload,scale_vaapi=%s", scaleExpr)
	default:
		return fmt.Sprintf("scale=%s", scaleExpr)
	}
}

func buildScaleExpression(maxW, maxH int32) string {
	if maxW > 0 && maxH > 0 {
		return fmt.Sprintf("'min(%d,iw)':'min(%d,ih)':force_original_aspect_ratio=decrease", maxW, maxH)
	}
	if maxH > 0 {
		return fmt.Sprintf("'iw':'min(%d,ih)':force_original_aspect_ratio=decrease", maxH)
	}
	return fmt.Sprintf("'min(%d,iw)':'ih':force_original_aspect_ratio=decrease", maxW)
}
