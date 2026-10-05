package internal

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/Muxcore-Media/core/sdk/go/module/netguard"
	"github.com/Muxcore-Media/core/sdk/go/module/pathguard"

	transcodev1 "github.com/Muxcore-Media/media-transcoder/proto/transcodev1"
)

// Path and source policy (NFR-SEC-008 / RULE-VAL-1, NFR-SEC-009 / RULE-VAL-2).
//
// Every media path the module reads, writes, deletes or renames on behalf of a
// caller is confined to operator-configured roots:
//
//   - TRANSCODER_MEDIA_ROOTS (path-list separated) bounds stream sources,
//     Enqueue inputs, setup library_paths and archive_path. Unset = every media
//     path is refused (fail closed).
//   - Setup library_paths are validated against the media roots at upsert and
//     re-checked at use; pipeline inputs (ProcessFile, scans, import events)
//     must lie inside the setup's (still valid) library_paths.
//   - TRANSCODER_OUTPUT_DIR bounds Enqueue output paths (default
//     <dir of TRANSCODER_DB_PATH>/output). Pipeline outputs are written next to
//     their confined input.
//
// http(s) stream sources are refused unless TRANSCODER_ALLOW_URL_SOURCES=1.
// Then TRANSCODER_URL_SOURCE_HOSTS (comma separated host[:port]) pins them to
// the internal library stream servers; without it a URL must pass the strict
// netguard UserURL profile (public destinations only).
const (
	envMediaRoots       = "TRANSCODER_MEDIA_ROOTS"
	envOutputDir        = "TRANSCODER_OUTPUT_DIR"
	envAllowURLSources  = "TRANSCODER_ALLOW_URL_SOURCES"
	envURLSourceHosts   = "TRANSCODER_URL_SOURCE_HOSTS"
	ffmpegFileProtocols = "file"
	ffmpegURLProtocols  = "file,http,https,tcp,tls"
)

// errSourceRefused marks a well-formed request refused by policy (HTTP 403).
var errSourceRefused = errors.New("refused by transcoder path/source policy")

func refused(format string, args ...any) error {
	return fmt.Errorf("%w: %s", errSourceRefused, fmt.Sprintf(format, args...))
}

func envTruthy(k string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(k))) {
	case "1", "true", "yes":
		return true
	}
	return false
}

// parsePathList splits a path-list separated env value, dropping blanks.
func parsePathList(v string) []string {
	var out []string
	for _, r := range filepath.SplitList(v) {
		if r = strings.TrimSpace(r); r != "" {
			out = append(out, r)
		}
	}
	return out
}

// parseCSV splits a comma separated env value, dropping blanks.
func parseCSV(v string) []string {
	var out []string
	for _, s := range strings.Split(v, ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func wrapPathErr(what, p string, err error) error {
	if errors.Is(err, pathguard.ErrOutsideRoots) {
		return fmt.Errorf("%w: %s %q is outside the allowed roots", errSourceRefused, what, p)
	}
	return fmt.Errorf("%s %q: %w", what, p, err)
}

// confineMedia confines p to TRANSCODER_MEDIA_ROOTS and returns its real path.
func (m *Module) confineMedia(what, p string) (string, error) {
	if len(m.mediaRoots) == 0 {
		return "", refused("%s refused: %s is not configured", what, envMediaRoots)
	}
	rp, err := pathguard.Confine(strings.TrimSpace(p), m.mediaRoots)
	if err != nil {
		return "", wrapPathErr(what, p, err)
	}
	return rp, nil
}

// confineOutput confines an Enqueue output path to TRANSCODER_OUTPUT_DIR.
func (m *Module) confineOutput(p string) (string, error) {
	if m.outputDir == "" {
		return "", refused("output_path refused: %s is not configured", envOutputDir)
	}
	rp, err := pathguard.Confine(strings.TrimSpace(p), []string{m.outputDir})
	if err != nil {
		return "", wrapPathErr("output_path", p, err)
	}
	return rp, nil
}

// setupRoots returns the setup's library_paths that are still inside the
// media roots; stored setups are re-checked at every use.
func (m *Module) setupRoots(setup *transcodev1.TranscodeSetup) []string {
	var roots []string
	for _, lp := range setup.GetLibraryPaths() {
		lp = strings.TrimSpace(lp)
		if lp == "" {
			continue
		}
		if _, err := m.confineMedia("library_path", lp); err != nil {
			continue
		}
		roots = append(roots, filepath.Clean(lp))
	}
	return roots
}

// confineToSetup confines p (target resolved) to the setup's library roots.
func (m *Module) confineToSetup(p string, setup *transcodev1.TranscodeSetup) (string, error) {
	roots := m.setupRoots(setup)
	if len(roots) == 0 {
		return "", refused("setup %q has no library_paths inside %s", setup.GetId(), envMediaRoots)
	}
	rp, err := pathguard.Confine(strings.TrimSpace(p), roots)
	if err != nil {
		return "", wrapPathErr("input_path", p, err)
	}
	return rp, nil
}

// confineEntry confines the directory entry p (not its symlink target) to
// roots and returns the real parent joined with the entry name, for removing
// or renaming the entry itself.
func confineEntry(what, p string, roots []string) (string, error) {
	p = strings.TrimSpace(p)
	if !filepath.IsAbs(p) || strings.ContainsRune(p, 0) {
		return "", fmt.Errorf("%w: %s %q must be an absolute path", pathguard.ErrInvalidPath, what, p)
	}
	for _, seg := range strings.Split(filepath.ToSlash(p), "/") {
		if seg == ".." {
			return "", fmt.Errorf("%w: %s %q contains a \"..\" segment", pathguard.ErrInvalidPath, what, p)
		}
	}
	clean := filepath.Clean(p)
	base := filepath.Base(clean)
	if base == string(filepath.Separator) || base == "." {
		return "", fmt.Errorf("%w: %s %q has no file name", pathguard.ErrInvalidPath, what, p)
	}
	if len(roots) == 0 {
		return "", refused("%s refused: no roots configured", what)
	}
	dir, err := pathguard.Confine(filepath.Dir(clean), roots)
	if err != nil {
		return "", wrapPathErr(what, p, err)
	}
	return filepath.Join(dir, base), nil
}

// validateSetupPaths checks caller-supplied setup paths at upsert time.
func (m *Module) validateSetupPaths(in *transcodev1.TranscodeSetup) ([]string, error) {
	var paths []string
	for _, lp := range in.GetLibraryPaths() {
		lp = strings.TrimSpace(lp)
		if lp == "" {
			continue
		}
		if _, err := m.confineMedia("library_path", lp); err != nil {
			return nil, err
		}
		paths = append(paths, filepath.Clean(lp))
	}
	archive := strings.TrimSpace(in.GetArchivePath())
	if archive != "" {
		if _, err := m.confineMedia("archive_path", archive); err != nil {
			return nil, err
		}
	} else if normalizeDisposition(in.GetSourceDisposition()) == "archive" {
		return nil, fmt.Errorf("archive_path is required for the archive source disposition")
	}
	for _, out := range in.GetOutputs() {
		if s := out.GetSuffix(); strings.ContainsAny(s, "/\\\x00") {
			return nil, fmt.Errorf("output suffix %q must not contain path separators", s)
		}
	}
	return paths, nil
}

func isURLSource(src string) bool {
	l := strings.ToLower(src)
	return strings.HasPrefix(l, "http://") || strings.HasPrefix(l, "https://")
}

// ffmpegProtocolWhitelist limits the protocols ffmpeg may open for input.
func ffmpegProtocolWhitelist(input string) string {
	if isURLSource(input) {
		return ffmpegURLProtocols
	}
	return ffmpegFileProtocols
}

// resolveStreamInput validates a playback/trickplay src: an absolute path to a
// regular file inside the media roots (returned as its real path), or — only
// when URL sources are enabled — an http(s) URL that passes netguard.
func (m *Module) resolveStreamInput(src string) (string, error) {
	src = strings.TrimSpace(src)
	if src == "" {
		return "", fmt.Errorf("src required")
	}
	if isURLSource(src) {
		if !m.allowURLSrc {
			return "", refused("http(s) src is disabled (%s)", envAllowURLSources)
		}
		var err error
		if len(m.urlSrcHosts) > 0 {
			err = netguard.ValidateURL(src, netguard.Integration, netguard.Options{
				AllowedHosts:  m.urlSrcHosts,
				AllowPrivate:  true,
				AllowLoopback: true,
			})
		} else {
			err = netguard.ValidateURL(src, netguard.UserURL, netguard.Options{})
		}
		if err != nil {
			return "", fmt.Errorf("%w: src url: %w", errSourceRefused, err)
		}
		return src, nil
	}
	if !filepath.IsAbs(src) {
		return "", fmt.Errorf("src must be an absolute path")
	}
	resolved, err := m.confineMedia("src", src)
	if err != nil {
		return "", err
	}
	st, err := os.Stat(resolved) //nolint:gosec // resolved is confined to TRANSCODER_MEDIA_ROOTS by pathguard.Confine
	if err != nil {
		return "", fmt.Errorf("src not accessible: %w", err)
	}
	if !st.Mode().IsRegular() {
		return "", fmt.Errorf("src must be a regular file")
	}
	return resolved, nil
}

// streamInputStatus maps a src validation error to an HTTP status: 403 for a
// policy refusal, 400 for a malformed or missing source.
func streamInputStatus(err error) int {
	if errors.Is(err, errSourceRefused) {
		return http.StatusForbidden
	}
	return http.StatusBadRequest
}
