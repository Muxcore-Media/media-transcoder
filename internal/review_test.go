package internal

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	transcodev1 "github.com/Muxcore-Media/media-transcoder/proto/transcodev1"
)

func TestNeedsHoldForReview(t *testing.T) {
	tests := []struct {
		name  string
		setup *transcodev1.TranscodeSetup
		want  bool
	}{
		{"keep no hold", &transcodev1.TranscodeSetup{HoldForReview: true, SourceDisposition: "keep"}, false},
		{"replace hold", &transcodev1.TranscodeSetup{HoldForReview: true, SourceDisposition: "replace"}, true},
		{"delete no hold", &transcodev1.TranscodeSetup{HoldForReview: false, SourceDisposition: "delete"}, false},
		{"archive hold", &transcodev1.TranscodeSetup{HoldForReview: true, SourceDisposition: "archive"}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := needsHoldForReview(tc.setup); got != tc.want {
				t.Fatalf("needsHoldForReview() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestHoldForReviewSetupRoundTrip(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()
	resp, err := m.UpsertSetup(ctx, &transcodev1.UpsertSetupRequest{Setup: &transcodev1.TranscodeSetup{
		Name:              "Review replace",
		Enabled:           true,
		LibraryPaths:      []string{"/media"},
		Trigger:           "manual",
		SourceDisposition: "replace",
		HoldForReview:     true,
		Outputs:           []*transcodev1.SetupOutput{{ProfileId: "h264_fast", Enabled: true}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := m.GetSetup(ctx, &transcodev1.GetSetupRequest{Id: resp.GetSetup().GetId()})
	if err != nil {
		t.Fatal(err)
	}
	if !got.GetSetup().GetHoldForReview() {
		t.Fatal("expected hold_for_review persisted")
	}
}

func TestApproveRejectPipelineRun(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()
	dir := t.TempDir()
	src := filepath.Join(dir, "movie.mkv")
	if err := os.WriteFile(src, []byte("source"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "movie-hevc.mkv")
	if err := os.WriteFile(out, []byte("transcoded"), 0o644); err != nil {
		t.Fatal(err)
	}

	setupResp, err := m.UpsertSetup(ctx, &transcodev1.UpsertSetupRequest{Setup: &transcodev1.TranscodeSetup{
		Name:              "Review delete",
		Enabled:           true,
		LibraryPaths:      []string{dir},
		Trigger:           "manual",
		SourceDisposition: "delete",
		HoldForReview:     true,
		Outputs:           []*transcodev1.SetupOutput{{ProfileId: "h264_fast", Enabled: true}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	setup := setupResp.GetSetup()
	runID := "pr_test_review"
	now := "2026-01-01T00:00:00Z"
	_, err = m.db.Exec(`
		INSERT INTO transcode_pipeline_runs (id, setup_id, setup_name, input_path, status, source_disposition, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'pending_review', ?, ?, ?)`,
		runID, setup.GetId(), setup.GetName(), src, setup.GetSourceDisposition(), now, now)
	if err != nil {
		t.Fatal(err)
	}
	_, err = m.db.Exec(`
		INSERT INTO transcode_pipeline_outputs (id, run_id, output_path, profile_id, job_id, status)
		VALUES ('po1', ?, ?, 'h264_fast', 'job1', 'completed')`, runID, out)
	if err != nil {
		t.Fatal(err)
	}

	rejectResp, err := m.RejectPipelineRun(ctx, &transcodev1.RejectPipelineRunRequest{Id: runID})
	if err != nil {
		t.Fatal(err)
	}
	if rejectResp.GetRun().GetStatus() != "rejected" {
		t.Fatalf("expected rejected, got %q", rejectResp.GetRun().GetStatus())
	}
	if _, err := os.Stat(src); err != nil {
		t.Fatal("source should remain after reject")
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatal("output should be removed after reject")
	}

	// Re-seed pending run for approve path.
	_, _ = m.db.Exec(`DELETE FROM transcode_pipeline_runs WHERE id = ?`, runID)
	_, _ = m.db.Exec(`DELETE FROM transcode_pipeline_outputs WHERE run_id = ?`, runID)
	if err := os.WriteFile(out, []byte("transcoded"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = m.db.Exec(`
		INSERT INTO transcode_pipeline_runs (id, setup_id, setup_name, input_path, status, source_disposition, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'pending_review', ?, ?, ?)`,
		runID, setup.GetId(), setup.GetName(), src, setup.GetSourceDisposition(), now, now)
	if err != nil {
		t.Fatal(err)
	}
	_, err = m.db.Exec(`
		INSERT INTO transcode_pipeline_outputs (id, run_id, output_path, profile_id, job_id, status)
		VALUES ('po1', ?, ?, 'h264_fast', 'job1', 'completed')`, runID, out)
	if err != nil {
		t.Fatal(err)
	}

	approveResp, err := m.ApprovePipelineRun(ctx, &transcodev1.ApprovePipelineRunRequest{Id: runID})
	if err != nil {
		t.Fatal(err)
	}
	if approveResp.GetRun().GetStatus() != "completed" {
		t.Fatalf("expected completed, got %q", approveResp.GetRun().GetStatus())
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatal("source should be deleted after approve with delete disposition")
	}
	if _, err := os.Stat(out); err != nil {
		t.Fatal("output should remain after approve")
	}
}
