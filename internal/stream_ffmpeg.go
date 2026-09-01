package internal

import (
	"fmt"

	transcodev1 "github.com/Muxcore-Media/media-transcoder/proto/transcodev1"
)

func (m *Module) buildPlaybackStreamArgs(profile *transcodev1.TranscodeProfile, input string, mode hwBackend, startSeconds float64, audioStreamIndex int) []string {
	encoder := m.pickStreamEncoder(profile, mode)
	args := []string{"-hide_banner", "-loglevel", "error"}
	args = append(args, m.hwAccelInputArgs(encoder)...)
	if startSeconds > 0 {
		// Input-side seek: fast (keyframe-aligned) restart point for scrubbing
		// during transcode, since the piped fmp4 output has no server-side
		// random access once written to the response body.
		args = append(args, "-ss", fmt.Sprintf("%.3f", startSeconds))
	}
	args = append(args, "-i", input)
	if startSeconds > 0 {
		// Renormalize timestamps so the fresh fmp4 init segment starts at 0;
		// the player tracks the wall-clock offset itself (X-Transcode-Start-Seconds).
		args = append(args, "-avoid_negative_ts", "make_zero")
	}

	args = append(args, "-map", "0:v:0")
	if audioStreamIndex >= 0 {
		args = append(args, "-map", fmt.Sprintf("0:%d", audioStreamIndex))
	} else {
		args = append(args, "-map", "0:a:0")
	}

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

func buildScaleExpression(maxW, maxH int32) string {
	if maxW > 0 && maxH > 0 {
		return fmt.Sprintf("'min(%d,iw)':'min(%d,ih)':force_original_aspect_ratio=decrease", maxW, maxH)
	}
	if maxH > 0 {
		return fmt.Sprintf("'iw':'min(%d,ih)':force_original_aspect_ratio=decrease", maxH)
	}
	return fmt.Sprintf("'min(%d,iw)':'ih':force_original_aspect_ratio=decrease", maxW)
}
