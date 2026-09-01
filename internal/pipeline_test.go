package internal

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	transcodev1 "github.com/Muxcore-Media/media-transcoder/proto/transcodev1"
)

func TestApplySourceDispositionReplace(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()
	dir := t.TempDir()
	src := filepath.Join(dir, "movie.mkv")
	out := filepath.Join(dir, "movie-hevc.mkv")
	if err := os.WriteFile(src, []byte("source"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(out, []byte("transcoded"), 0o644); err != nil {
		t.Fatal(err)
	}

	runID := "pr_replace"
	now := "2026-01-01T00:00:00Z"
	setup := &transcodev1.TranscodeSetup{SourceDisposition: "replace"}
	_, err := m.db.Exec(`
		INSERT INTO transcode_pipeline_runs (id, setup_id, setup_name, input_path, status, source_disposition, created_at, updated_at)
		VALUES (?, 's1', 'Replace', ?, 'pending_review', 'replace', ?, ?)`, runID, src, now, now)
	if err != nil {
		t.Fatal(err)
	}
	_, err = m.db.Exec(`
		INSERT INTO transcode_pipeline_outputs (id, run_id, output_path, profile_id, job_id, status)
		VALUES ('po1', ?, ?, 'h264_fast', 'job1', 'completed')`, runID, out)
	if err != nil {
		t.Fatal(err)
	}

	if err := m.applySourceDisposition(ctx, runID, src, setup); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "transcoded" {
		t.Fatalf("source content=%q", data)
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatal("output sidecar should be moved onto source path")
	}
}

func TestRemuxSidecarPreservesSource(t *testing.T) {
	m := newTestModule(t)
	dir := t.TempDir()
	src := filepath.Join(dir, "clip.mkv")
	if err := os.WriteFile(src, []byte("source"), 0o644); err != nil {
		t.Fatal(err)
	}
	info := &probeInfo{Container: "mkv", Extension: ".mkv", SizeBytes: 6}
	step := &transcodev1.PipelineStep{
		StepType:   "action.remux",
		ConfigJson: `{"container":"mp4"}`,
		Enabled:    true,
	}
	skip, reason, probePath, err := m.evalStep(src, info, step)
	if err != nil {
		t.Skipf("ffmpeg remux unavailable: %v", err)
	}
	if skip {
		t.Fatalf("unexpected skip: %s", reason)
	}
	if probePath == "" {
		t.Fatal("expected remux sidecar path")
	}
	if _, err := os.Stat(src); err != nil {
		t.Fatal("original source should remain after remux sidecar")
	}
	if _, err := os.Stat(probePath); err != nil {
		t.Fatal("sidecar should exist")
	}
}

func TestCancelJobFromDBOnly(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()
	now := "2026-01-01T00:00:00Z"
	jobID := "tj_cancel_db"
	_, err := m.db.Exec(`INSERT INTO transcode_jobs (id, input_path, output_path, profile_id, profile_name, status, created_at, updated_at)
		VALUES (?, '/in.mkv', '/out.mkv', 'h264_fast', 'H.264 Fast', 'queued', ?, ?)`, jobID, now, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.CancelJob(ctx, &transcodev1.CancelJobRequest{JobId: jobID}); err != nil {
		t.Fatal(err)
	}
	row := m.db.QueryRowContext(ctx, `SELECT status FROM transcode_jobs WHERE id = ?`, jobID)
	var status string
	if err := row.Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "cancelled" {
		t.Fatalf("status=%q", status)
	}
}

func TestSettingsPersistAcrossReload(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "settings.db")
	m := NewModule(Config{DBPath: dbPath, GRPCAddr: ":0"})
	ctx := context.Background()
	if err := m.Init(ctx); err != nil {
		t.Fatal(err)
	}
	if err := m.UpdateSetting("ffmpeg_bin", "/opt/ffmpeg"); err != nil {
		t.Fatal(err)
	}
	if err := m.UpdateSetting("scan_interval", "30m"); err != nil {
		t.Fatal(err)
	}
	_ = m.Stop(ctx)

	m2 := NewModule(Config{DBPath: dbPath, GRPCAddr: ":0"})
	if err := m2.Init(ctx); err != nil {
		t.Fatal(err)
	}
	defer m2.Stop(ctx)
	if got := m2.getFFmpegBin(); got != "/opt/ffmpeg" {
		t.Fatalf("ffmpeg_bin=%q", got)
	}
	if got := m2.getScanInterval(); got != 30*60*1e9 {
		t.Fatalf("scan_interval=%v", got)
	}
}

func TestProbeFileRequiresCodec(t *testing.T) {
	m := newTestModule(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.txt")
	if err := os.WriteFile(path, []byte("not video"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := m.probeFile(path); err == nil {
		t.Fatal("expected probe failure for non-video file")
	}
}

func TestGetFFprobeBinFromFFmpeg(t *testing.T) {
	m := NewModule(Config{FFmpegBin: "/usr/local/bin/ffmpeg"})
	if got := m.getFFprobeBin(); got != "/usr/local/bin/ffprobe" {
		t.Fatalf("got=%q", got)
	}
	t.Setenv("TRANSCODER_FFPROBE_BIN", "/custom/ffprobe")
	if got := m.getFFprobeBin(); got != "/custom/ffprobe" {
		t.Fatalf("override=%q", got)
	}
}
