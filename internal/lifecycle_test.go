package internal

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	transcodev1 "github.com/Muxcore-Media/media-transcoder/proto/transcodev1"
)

// newSetupAndInput creates a manual setup with one output and a dummy input file.
func newSetupAndInput(t *testing.T, m *Module) (*transcodev1.TranscodeSetup, string) {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "movie.mkv")
	if err := os.WriteFile(src, []byte("source"), 0o600); err != nil {
		t.Fatal(err)
	}
	resp, err := m.UpsertSetup(context.Background(), &transcodev1.UpsertSetupRequest{Setup: &transcodev1.TranscodeSetup{
		Name:         "lifecycle",
		Enabled:      true,
		LibraryPaths: []string{dir},
		Trigger:      "manual",
		Outputs:      []*transcodev1.SetupOutput{{ProfileId: "h264_fast", Enabled: true}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return resp.GetSetup(), src
}

// Regression: ListPipelineRuns ran loadPipelineRun while its outer rows were
// still open; with SetMaxOpenConns(1) that blocked forever.
func TestListPipelineRunsNoDeadlock(t *testing.T) {
	m := newTestModule(t)
	setup, src := newSetupAndInput(t, m)
	now := "2026-01-01T00:00:00Z"
	for _, id := range []string{"pr_a", "pr_b"} {
		if _, err := m.db.Exec(`
			INSERT INTO transcode_pipeline_runs (id, setup_id, setup_name, input_path, status, source_disposition, created_at, updated_at)
			VALUES (?, ?, ?, ?, 'completed', 'keep', ?, ?)`, id, setup.GetId(), setup.GetName(), src, now, now); err != nil {
			t.Fatal(err)
		}
		if _, err := m.db.Exec(`
			INSERT INTO transcode_pipeline_outputs (id, run_id, output_path, profile_id, job_id, status)
			VALUES (?, ?, '/tmp/out.mkv', 'h264_fast', 'job1', 'completed')`, "po_"+id, id); err != nil {
			t.Fatal(err)
		}
	}

	type result struct {
		resp *transcodev1.ListPipelineRunsResponse
		err  error
	}
	done := make(chan result, 1)
	go func() {
		r, err := m.ListPipelineRuns(context.Background(), &transcodev1.ListPipelineRunsRequest{})
		done <- result{r, err}
	}()
	select {
	case r := <-done:
		if r.err != nil {
			t.Fatal(r.err)
		}
		if len(r.resp.GetRuns()) != 2 || r.resp.GetTotal() != 2 {
			t.Fatalf("want 2 runs, got %d (total %d)", len(r.resp.GetRuns()), r.resp.GetTotal())
		}
		for _, run := range r.resp.GetRuns() {
			if len(run.GetOutputs()) != 1 {
				t.Fatalf("run %s: want 1 output, got %d", run.GetId(), len(run.GetOutputs()))
			}
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ListPipelineRuns deadlocked")
	}
}

// Regression: the pipeline finalizer goroutine kept polling after Stop closed
// the DB (nil-DB panic). Stop must cancel and await it.
func TestStopWithPipelineRunInFlight(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "fakeffmpeg.sh")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexec sleep 60\n"), 0o700); err != nil { //nolint:gosec // test script must be executable
		t.Fatal(err)
	}
	t.Setenv("TRANSCODER_FFMPEG_BIN", bin)
	m := newTestModule(t)
	setup, src := newSetupAndInput(t, m)

	resp, err := m.ProcessFile(context.Background(), &transcodev1.ProcessFileRequest{SetupId: setup.GetId(), InputPath: src})
	if err != nil {
		t.Fatal(err)
	}

	// Wait until the run is genuinely in flight (job tracked, finalizer polling).
	deadline := time.Now().Add(5 * time.Second)
	for {
		m.mu.RLock()
		run, _ := m.loadPipelineRun(context.Background(), resp.GetRunId())
		m.mu.RUnlock()
		if run != nil && run.GetStatus() == "running" && len(run.GetOutputs()) == 1 && run.GetOutputs()[0].GetJobId() != "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("run never reached running state: %+v", run)
		}
		time.Sleep(20 * time.Millisecond)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	start := time.Now()
	if err := m.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("Stop took %v", d)
	}

	// Every background goroutine must already be gone.
	drained := make(chan struct{})
	go func() { m.jobWG.Wait(); close(drained) }()
	select {
	case <-drained:
	case <-time.After(time.Second):
		t.Fatal("background goroutines still running after Stop")
	}
	if m.db != nil {
		t.Fatal("db should be closed after Stop")
	}
	// Outlast one finalizer poll interval to catch a late DB touch (-race / panic).
	time.Sleep(2500 * time.Millisecond)

	// New background work is refused after Stop.
	if _, _, _, err := m.startPipelineRun(context.Background(), setup, src); err == nil {
		t.Fatal("startPipelineRun should fail after Stop")
	}
}
