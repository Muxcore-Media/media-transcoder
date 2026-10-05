package internal

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	transcodev1 "github.com/Muxcore-Media/media-transcoder/proto/transcodev1"
)

// policyFixture is a module whose media roots are one library dir; outside
// holds files that must never be reachable.
type policyFixture struct {
	m       *Module
	lib     string // the only media root
	sibling string // <parent>/lib2: shares the "lib" prefix
	outside string
	outDir  string // TRANSCODER_OUTPUT_DIR
}

func writeFile(t *testing.T, p string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("video"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func newPolicyFixture(t *testing.T) policyFixture {
	t.Helper()
	base := t.TempDir()
	f := policyFixture{
		lib:     filepath.Join(base, "lib"),
		sibling: filepath.Join(base, "lib2"),
		outside: filepath.Join(base, "outside"),
		outDir:  filepath.Join(base, "out"),
	}
	for _, d := range []string{f.lib, f.sibling, f.outside, f.outDir} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	stub := filepath.Join(base, "ffmpeg")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\nexit 1\n"), 0o700); err != nil { //nolint:gosec // test stub must be executable
		t.Fatal(err)
	}
	f.m = NewModule(Config{
		DBPath:     filepath.Join(base, "db", "transcoder.db"),
		GRPCAddr:   "127.0.0.1:0",
		FFmpegBin:  stub,
		MediaRoots: []string{f.lib},
		OutputDir:  f.outDir,
	})
	ctx := context.Background()
	if err := f.m.Init(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.m.Stop(ctx) })
	return f
}

func TestResolveStreamInputPathPolicy(t *testing.T) {
	f := newPolicyFixture(t)
	inside := writeFile(t, filepath.Join(f.lib, "movie.mkv"))
	secret := writeFile(t, filepath.Join(f.outside, "secret.mkv"))
	siblingFile := writeFile(t, filepath.Join(f.sibling, "movie.mkv"))
	link := filepath.Join(f.lib, "escape.mkv")
	if err := os.Symlink(secret, link); err != nil {
		t.Fatal(err)
	}

	got, err := f.m.resolveStreamInput(inside)
	if err != nil || got != inside {
		t.Fatalf("inside: got %q err %v", got, err)
	}

	for name, src := range map[string]string{
		"outside":        secret,
		"etc":            "/etc/passwd",
		"sibling prefix": siblingFile,
		"symlink escape": link,
	} {
		if _, err := f.m.resolveStreamInput(src); !errors.Is(err, errSourceRefused) {
			t.Errorf("%s: want policy refusal, got %v", name, err)
		}
	}
	for name, src := range map[string]string{
		"empty":    "",
		"relative": "movie.mkv",
		"dotdot":   filepath.Join(f.lib, "..", "outside", "secret.mkv"),
		"dotdot2":  f.lib + "/../outside/secret.mkv",
		"file url": "file://" + secret,
		"concat":   "concat:" + inside + "|" + secret,
		"dir":      f.lib,
	} {
		if _, err := f.m.resolveStreamInput(src); err == nil {
			t.Errorf("%s: %q accepted", name, src)
		}
	}
}

func TestResolveStreamInputNoRootsFailsClosed(t *testing.T) {
	m := NewModule(Config{DBPath: filepath.Join(t.TempDir(), "t.db")})
	p := writeFile(t, filepath.Join(t.TempDir(), "movie.mkv"))
	if _, err := m.resolveStreamInput(p); !errors.Is(err, errSourceRefused) {
		t.Fatalf("want refusal without TRANSCODER_MEDIA_ROOTS, got %v", err)
	}
}

func TestMediaRootsFromEnv(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	t.Setenv(envMediaRoots, a+string(filepath.ListSeparator)+" "+b+" ")
	t.Setenv(envOutputDir, b)
	m := NewModule(Config{DBPath: filepath.Join(t.TempDir(), "t.db")})
	if len(m.mediaRoots) != 2 || m.mediaRoots[0] != a || m.mediaRoots[1] != b || m.outputDir != b {
		t.Fatalf("roots=%v out=%q", m.mediaRoots, m.outputDir)
	}
	if _, err := m.resolveStreamInput(writeFile(t, filepath.Join(b, "x.mkv"))); err != nil {
		t.Fatal(err)
	}
}

func TestResolveStreamInputURLPolicy(t *testing.T) {
	m := NewModule(Config{DBPath: filepath.Join(t.TempDir(), "t.db")})
	for _, src := range []string{"http://127.0.0.1:9430/stream/movies/m1", "https://example.com/a.mkv"} {
		if _, err := m.resolveStreamInput(src); !errors.Is(err, errSourceRefused) {
			t.Fatalf("URL src must be refused by default: %q %v", src, err)
		}
	}

	t.Setenv(envAllowURLSources, "1")
	m = NewModule(Config{DBPath: filepath.Join(t.TempDir(), "t.db")})
	if _, err := m.resolveStreamInput("https://example.com/a.mkv"); err != nil {
		t.Fatalf("public URL with URL sources enabled: %v", err)
	}
	for _, src := range []string{
		"http://127.0.0.1:9430/stream/movies/m1",
		"http://169.254.169.254/latest/meta-data/",
		"http://media-movies:9430/stream/movies/m1",
		"http://10.0.0.5/a.mkv",
		"http://user:pw@example.com/a.mkv",
	} {
		if _, err := m.resolveStreamInput(src); !errors.Is(err, errSourceRefused) {
			t.Errorf("UserURL profile must refuse %q, got %v", src, err)
		}
	}

	t.Setenv(envURLSourceHosts, "media-movies:9430, media-tvshows:9450")
	m = NewModule(Config{DBPath: filepath.Join(t.TempDir(), "t.db")})
	for _, src := range []string{"http://media-movies:9430/stream/movies/m1", "http://media-tvshows:9450/stream/tv/e1"} {
		if _, err := m.resolveStreamInput(src); err != nil {
			t.Errorf("allow-listed %q: %v", src, err)
		}
	}
	for _, src := range []string{
		"http://media-movies:9999/stream/movies/m1",
		"http://core:9430/",
		"https://example.com/a.mkv",
		"http://169.254.169.254:9430/",
	} {
		if _, err := m.resolveStreamInput(src); !errors.Is(err, errSourceRefused) {
			t.Errorf("host allow-list must refuse %q, got %v", src, err)
		}
	}
}

func TestPlaybackHandlersRefuseUnconfinedSrc(t *testing.T) {
	f := newPolicyFixture(t)
	secret := writeFile(t, filepath.Join(f.outside, "secret.mkv"))
	srv := httptest.NewServer(f.m.playbackMux())
	defer srv.Close()
	for _, path := range []string{"/stream/transcode", "/stream/hls", "/stream/trickplay"} {
		for _, src := range []string{secret, "/etc/passwd", "http://127.0.0.1:1/x", "https://example.com/a.mkv"} {
			resp, err := http.Get(srv.URL + path + "?duration=10&src=" + url.QueryEscape(src))
			if err != nil {
				t.Fatal(err)
			}
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusForbidden {
				t.Errorf("%s src=%s: status %d, want 403", path, src, resp.StatusCode)
			}
		}
	}
}

func TestFFmpegProtocolWhitelist(t *testing.T) {
	m := NewModule(Config{})
	p := &transcodev1.TranscodeProfile{VideoCodec: "h264", AudioCodec: "copy"}
	check := func(args []string, want string) {
		t.Helper()
		j := strings.Join(args, " ")
		if !strings.Contains(j, "-protocol_whitelist "+want+" -i ") {
			t.Fatalf("want -protocol_whitelist %s before -i: %s", want, j)
		}
	}
	check(m.buildPlaybackStreamArgs(p, "/lib/a.mkv", streamEncoderSoftware, 0, -1, -1), "file")
	check(m.buildPlaybackStreamArgs(p, "http://media-movies:9430/stream/movies/1", streamEncoderSoftware, 0, -1, -1), ffmpegURLProtocols)
	check(m.buildHLSStreamArgs(p, "/lib/a.mkv", streamEncoderSoftware, 0, -1, -1, "/c/index.m3u8", "/c/seg_%05d.ts"), "file")
	check(m.buildFFmpegArgs(p, "/lib/a.mkv", "/out/a.mkv"), "file")
}

func TestEnqueueConfinesInputAndOutput(t *testing.T) {
	f := newPolicyFixture(t)
	ctx := context.Background()
	in := writeFile(t, filepath.Join(f.lib, "movie.mkv"))
	secret := writeFile(t, filepath.Join(f.outside, "secret.mkv"))
	link := filepath.Join(f.lib, "escape.mkv")
	if err := os.Symlink(secret, link); err != nil {
		t.Fatal(err)
	}
	outLink := filepath.Join(f.outDir, "linkdir")
	if err := os.Symlink(f.outside, outLink); err != nil {
		t.Fatal(err)
	}
	good := filepath.Join(f.outDir, "movie.mkv")

	cases := []struct{ name, in, out string }{
		{"input outside roots", secret, good},
		{"input /etc", "/etc/passwd", good},
		{"input symlink escape", link, good},
		{"input sibling prefix", writeFile(t, filepath.Join(f.sibling, "m.mkv")), good},
		{"input dotdot", f.lib + "/../outside/secret.mkv", good},
		{"output in library", in, filepath.Join(f.lib, "out.mkv")},
		{"output outside", in, filepath.Join(f.outside, "out.mkv")},
		{"output sibling prefix", in, f.outDir + "2/out.mkv"},
		{"output dotdot", in, f.outDir + "/../outside/out.mkv"},
		{"output via symlinked dir", in, filepath.Join(outLink, "out.mkv")},
		{"output /etc", in, "/etc/cron.d/x"},
	}
	for _, c := range cases {
		if _, err := f.m.Enqueue(ctx, &transcodev1.EnqueueRequest{InputPath: c.in, OutputPath: c.out, ProfileId: "h264_fast"}); err == nil {
			t.Errorf("%s: accepted", c.name)
		}
	}
	resp, err := f.m.Enqueue(ctx, &transcodev1.EnqueueRequest{InputPath: in, OutputPath: good, ProfileId: "h264_fast"})
	if err != nil || resp.GetJobId() == "" {
		t.Fatalf("confined enqueue: %v", err)
	}
}

func TestUpsertSetupValidatesPaths(t *testing.T) {
	f := newPolicyFixture(t)
	ctx := context.Background()
	base := func() *transcodev1.TranscodeSetup {
		return &transcodev1.TranscodeSetup{
			Name: "s", Enabled: true, Trigger: "manual",
			LibraryPaths: []string{filepath.Join(f.lib, "movies")},
			Outputs:      []*transcodev1.SetupOutput{{ProfileId: "h264_fast", Enabled: true}},
		}
	}
	bad := map[string]func(s *transcodev1.TranscodeSetup){
		"library outside":        func(s *transcodev1.TranscodeSetup) { s.LibraryPaths = []string{f.outside} },
		"library root /":         func(s *transcodev1.TranscodeSetup) { s.LibraryPaths = []string{"/"} },
		"library sibling prefix": func(s *transcodev1.TranscodeSetup) { s.LibraryPaths = []string{f.sibling} },
		"library dotdot":         func(s *transcodev1.TranscodeSetup) { s.LibraryPaths = []string{f.lib + "/../outside"} },
		"library relative":       func(s *transcodev1.TranscodeSetup) { s.LibraryPaths = []string{"movies"} },
		"archive outside": func(s *transcodev1.TranscodeSetup) {
			s.SourceDisposition, s.ArchivePath = "archive", f.outside
		},
		"archive missing": func(s *transcodev1.TranscodeSetup) { s.SourceDisposition = "archive" },
		"suffix separator": func(s *transcodev1.TranscodeSetup) {
			s.Outputs = []*transcodev1.SetupOutput{{ProfileId: "h264_fast", Suffix: "/../../x", Enabled: true}}
		},
	}
	for name, mut := range bad {
		s := base()
		mut(s)
		if _, err := f.m.UpsertSetup(ctx, &transcodev1.UpsertSetupRequest{Setup: s}); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	s := base()
	s.SourceDisposition, s.ArchivePath = "archive", filepath.Join(f.lib, "archive")
	if _, err := f.m.UpsertSetup(ctx, &transcodev1.UpsertSetupRequest{Setup: s}); err != nil {
		t.Fatalf("valid setup: %v", err)
	}
}

// storeSetup writes a setup straight into the DB, as a pre-fix or tampered
// row would be, bypassing UpsertSetup validation.
func storeSetup(t *testing.T, m *Module, id, libraryJSON, disposition, archive string) *transcodev1.TranscodeSetup {
	t.Helper()
	now := "2026-01-01T00:00:00Z"
	if _, err := m.db.Exec(`INSERT INTO transcode_setups (id, name, enabled, library_paths, trigger_mode, source_disposition, archive_path, created_at, updated_at)
		VALUES (?, ?, 1, ?, 'manual', ?, ?, ?, ?)`, id, id, libraryJSON, disposition, archive, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := m.db.Exec(`INSERT INTO transcode_setup_outputs (id, setup_id, profile_id, suffix, replace_extension, sort_order, enabled)
		VALUES (?, ?, 'h264_fast', '-x', 0, 0, 1)`, "o_"+id, id); err != nil {
		t.Fatal(err)
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, err := m.loadSetup(context.Background(), id)
	if err != nil || s == nil {
		t.Fatalf("load setup: %v", err)
	}
	return s
}

func TestProcessFileConfinedToSetupLibrary(t *testing.T) {
	f := newPolicyFixture(t)
	ctx := context.Background()
	movies := filepath.Join(f.lib, "movies")
	shows := writeFile(t, filepath.Join(f.lib, "shows", "e1.mkv"))
	secret := writeFile(t, filepath.Join(f.outside, "secret.mkv"))
	resp, err := f.m.UpsertSetup(ctx, &transcodev1.UpsertSetupRequest{Setup: &transcodev1.TranscodeSetup{
		Name: "movies", Enabled: true, Trigger: "scheduled", LibraryPaths: []string{movies},
		Outputs: []*transcodev1.SetupOutput{{ProfileId: "h264_fast", Enabled: true}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	id := resp.GetSetup().GetId()
	for name, in := range map[string]string{
		"other library in media roots": shows,
		"outside media roots":          secret,
		"dotdot":                       movies + "/../shows/e1.mkv",
	} {
		if _, err := f.m.ProcessFile(ctx, &transcodev1.ProcessFileRequest{SetupId: id, InputPath: in}); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}

	// A stored setup whose library path is no longer (or never was) inside
	// the media roots is inert: re-checked at use.
	tampered := storeSetup(t, f.m, "ts_tampered", `["`+f.outside+`"]`, "keep", "")
	if _, err := f.m.ProcessFile(ctx, &transcodev1.ProcessFileRequest{SetupId: tampered.GetId(), InputPath: secret}); err == nil {
		t.Fatal("tampered setup: input outside media roots accepted")
	}
	if _, _, _, err := f.m.startPipelineRun(ctx, tampered, secret); err == nil {
		t.Fatal("startPipelineRun accepted an input outside media roots")
	}
	if _, err := f.m.ScanSetups(ctx, &transcodev1.ScanSetupsRequest{SetupId: id, RootPath: f.outside}); err == nil {
		t.Fatal("ScanSetups accepted a root override outside the setup library")
	}
}

func TestSourceDispositionRefusedOutsideRoots(t *testing.T) {
	f := newPolicyFixture(t)
	secret := writeFile(t, filepath.Join(f.outside, "secret.mkv"))

	// delete: tampered setup library + run input outside the media roots.
	del := storeSetup(t, f.m, "ts_del", `["`+f.outside+`"]`, "delete", "")
	if err := f.m.applySourceDisposition(secret, del); err == nil {
		t.Fatal("delete outside roots accepted")
	}
	// delete: valid setup, input outside its library (stored run row).
	okSetup := storeSetup(t, f.m, "ts_ok", `["`+f.lib+`"]`, "delete", "")
	if err := f.m.applySourceDisposition(secret, okSetup); err == nil {
		t.Fatal("delete of a path outside the setup library accepted")
	}
	if err := f.m.applySourceDisposition(f.lib+"/../outside/secret.mkv", okSetup); err == nil {
		t.Fatal("delete via .. accepted")
	}
	// archive: tampered archive_path outside the media roots.
	inLib := writeFile(t, filepath.Join(f.lib, "a.mkv"))
	arch := storeSetup(t, f.m, "ts_arch", `["`+f.lib+`"]`, "archive", f.outside)
	if err := f.m.applySourceDisposition(inLib, arch); err == nil {
		t.Fatal("archive into a dir outside roots accepted")
	}
	for _, p := range []string{secret, inLib} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("%s must survive refused dispositions: %v", p, err)
		}
	}
	if _, err := os.Stat(filepath.Join(f.outside, "a.mkv")); !os.IsNotExist(err) {
		t.Fatal("archive wrote outside roots")
	}

	// Within roots both still work.
	arch2 := storeSetup(t, f.m, "ts_arch2", `["`+f.lib+`"]`, "archive", filepath.Join(f.lib, "archive"))
	if err := f.m.applySourceDisposition(inLib, arch2); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(f.lib, "archive", "a.mkv")); err != nil {
		t.Fatal(err)
	}
	inLib2 := writeFile(t, filepath.Join(f.lib, "b.mkv"))
	if err := f.m.applySourceDisposition(inLib2, okSetup); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(inLib2); !os.IsNotExist(err) {
		t.Fatal("delete inside roots did not remove the source")
	}
}

func TestRejectRemovesOnlyConfinedOutputs(t *testing.T) {
	f := newPolicyFixture(t)
	secret := writeFile(t, filepath.Join(f.outside, "secret.mkv"))
	run := &transcodev1.PipelineRun{Outputs: []*transcodev1.PipelineRunOutput{{OutputPath: secret}}}
	if err := f.m.removePipelineOutputs(run); err == nil {
		t.Fatal("removing an output outside roots accepted")
	}
	if _, err := os.Stat(secret); err != nil {
		t.Fatal("file outside roots was removed")
	}
	inside := writeFile(t, filepath.Join(f.outDir, "o.mkv"))
	if err := f.m.removePipelineOutputs(&transcodev1.PipelineRun{Outputs: []*transcodev1.PipelineRunOutput{{OutputPath: inside}}}); err != nil {
		t.Fatal(err)
	}
}

func TestPipelineOutputCannotEscapeInputDir(t *testing.T) {
	f := newPolicyFixture(t)
	ctx := context.Background()
	in := writeFile(t, filepath.Join(f.lib, "movie.mkv"))
	prof, err := f.m.CreateProfile(ctx, &transcodev1.CreateProfileRequest{Name: "evil", VideoCodec: "h264", Container: "/../../outside/evil"})
	if err != nil {
		t.Fatal(err)
	}
	s, err := f.m.UpsertSetup(ctx, &transcodev1.UpsertSetupRequest{Setup: &transcodev1.TranscodeSetup{
		Name: "evil", Enabled: true, Trigger: "manual", LibraryPaths: []string{f.lib},
		Outputs: []*transcodev1.SetupOutput{{ProfileId: prof.GetProfile().GetId(), ReplaceExtension: true, Enabled: true}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	resp, err := f.m.ProcessFile(wctx, &transcodev1.ProcessFileRequest{SetupId: s.GetSetup().GetId(), InputPath: in, Wait: true})
	if err != nil {
		t.Fatal(err)
	}
	if resp.GetStatus() != "failed" || !strings.Contains(resp.GetMessage(), "escapes") {
		t.Fatalf("status=%q msg=%q", resp.GetStatus(), resp.GetMessage())
	}
}
