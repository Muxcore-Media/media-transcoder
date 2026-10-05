package internal

import (
	"fmt"

	transcodev1 "github.com/Muxcore-Media/media-transcoder/proto/transcodev1"
)

func (m *Module) buildPlaybackEncodeArgs(profile *transcodev1.TranscodeProfile, input string, mode hwBackend, startSeconds float64, audioStreamIndex, subtitleStreamIndex int) []string {
	encoder := m.pickStreamEncoder(profile, mode)
	args := []string{"-hide_banner", "-loglevel", "error"}
	args = append(args, m.hwAccelInputArgs(encoder)...)
	if startSeconds > 0 {
		// Input-side seek: fast (keyframe-aligned) restart for HLS remounts
		// and the piped fMP4 fallback. The player tracks wall-clock offset.
		args = append(args, "-ss", fmt.Sprintf("%.3f", startSeconds))
	}
	// Only the protocols the source needs: a local file never opens network
	// or nested protocols (concat:, subfile:, playlists pointing elsewhere).
	args = append(args, "-protocol_whitelist", ffmpegProtocolWhitelist(input), "-i", input)
	if startSeconds > 0 {
		// Renormalize timestamps so the fresh fmp4 init segment starts at 0;
		// the player tracks the wall-clock offset itself (X-Transcode-Start-Seconds).
		args = append(args, "-avoid_negative_ts", "make_zero")
	}

	if subtitleStreamIndex >= 0 {
		// PGS / VobSub / other picture subs must be burned in. Overlay is
		// software-only, so scale on CPU even when the encoder is GPU.
		args = append(args, "-filter_complex", overlayBurnFilter(m.scaleFilter(profile, streamEncoderSoftware), subtitleStreamIndex), "-map", "[vout]")
	} else {
		args = append(args, "-map", "0:v:0")
		if vf := m.scaleFilter(profile, encoder); vf != "" {
			args = append(args, "-vf", vf)
		}
	}
	if audioStreamIndex >= 0 {
		args = append(args, "-map", fmt.Sprintf("0:%d", audioStreamIndex))
	} else {
		args = append(args, "-map", "0:a:0")
	}

	args = append(args, m.videoEncodeArgs(profile, encoder)...)

	audioCodec := profile.GetAudioCodec()
	if audioCodec == "" || audioCodec == "copy" {
		args = append(args, "-c:a", "aac", "-b:a", "192k")
	} else {
		args = append(args, "-c:a", audioCodec)
	}
	return args
}

func (m *Module) buildPlaybackStreamArgs(profile *transcodev1.TranscodeProfile, input string, mode hwBackend, startSeconds float64, audioStreamIndex, subtitleStreamIndex int) []string {
	args := m.buildPlaybackEncodeArgs(profile, input, mode, startSeconds, audioStreamIndex, subtitleStreamIndex)
	return append(args,
		"-movflags", "frag_keyframe+empty_moov+default_base_moof",
		"-f", "mp4",
		"pipe:1",
	)
}

func (m *Module) buildHLSStreamArgs(profile *transcodev1.TranscodeProfile, input string, mode hwBackend, startSeconds float64, audioStreamIndex, subtitleStreamIndex int, playlistPath, segmentPattern string) []string {
	args := m.buildPlaybackEncodeArgs(profile, input, mode, startSeconds, audioStreamIndex, subtitleStreamIndex)
	return append(args,
		"-f", "hls",
		"-hls_time", "4",
		"-hls_list_size", "0",
		"-hls_playlist_type", "event",
		"-hls_flags", "independent_segments+append_list+temp_file",
		"-hls_segment_filename", segmentPattern,
		playlistPath,
	)
}

// overlayBurnFilter composites stream N onto the video pad. scaleVF is a
// software scale=… filter or empty.
func overlayBurnFilter(scaleVF string, subtitleStreamIndex int) string {
	if scaleVF != "" {
		return fmt.Sprintf("[0:v:0]%s[vscaled];[vscaled][0:%d]overlay[vout]", scaleVF, subtitleStreamIndex)
	}
	return fmt.Sprintf("[0:v:0][0:%d]overlay[vout]", subtitleStreamIndex)
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
