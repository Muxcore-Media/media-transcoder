package internal

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	transcodev1 "github.com/Muxcore-Media/media-transcoder/proto/transcodev1"
)

func isDestructiveDisposition(disp string) bool {
	switch normalizeDisposition(disp) {
	case "replace", "delete", "archive":
		return true
	default:
		return false
	}
}

func needsHoldForReview(setup *transcodev1.TranscodeSetup) bool {
	if setup == nil || !setup.GetHoldForReview() {
		return false
	}
	return isDestructiveDisposition(setup.GetSourceDisposition())
}

func (m *Module) ApprovePipelineRun(ctx context.Context, req *transcodev1.ApprovePipelineRunRequest) (*transcodev1.ApprovePipelineRunResponse, error) {
	runID := strings.TrimSpace(req.GetId())
	if runID == "" {
		return nil, fmt.Errorf("id is required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	run, err := m.loadPipelineRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	if run == nil {
		return nil, fmt.Errorf("pipeline run not found: %s", runID)
	}
	if run.GetStatus() != "pending_review" {
		return nil, fmt.Errorf("run is not pending review (status=%s)", run.GetStatus())
	}
	setup, err := m.loadSetup(ctx, run.GetSetupId())
	if err != nil {
		return nil, err
	}
	if setup == nil {
		return nil, fmt.Errorf("setup not found: %s", run.GetSetupId())
	}
	if err := m.applySourceDisposition(run.GetInputPath(), setup); err != nil {
		m.updatePipelineRunStatus(ctx, runID, "failed", "", err.Error())
		return nil, err
	}
	m.updatePipelineRunStatus(ctx, runID, "completed", "", "")
	run, _ = m.loadPipelineRun(ctx, runID)
	return &transcodev1.ApprovePipelineRunResponse{Run: run}, nil
}

func (m *Module) RejectPipelineRun(ctx context.Context, req *transcodev1.RejectPipelineRunRequest) (*transcodev1.RejectPipelineRunResponse, error) {
	runID := strings.TrimSpace(req.GetId())
	if runID == "" {
		return nil, fmt.Errorf("id is required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	run, err := m.loadPipelineRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	if run == nil {
		return nil, fmt.Errorf("pipeline run not found: %s", runID)
	}
	if run.GetStatus() != "pending_review" {
		return nil, fmt.Errorf("run is not pending review (status=%s)", run.GetStatus())
	}
	if err := m.removePipelineOutputs(run); err != nil {
		m.updatePipelineRunStatus(ctx, runID, "failed", "", err.Error())
		return nil, err
	}
	m.updatePipelineRunStatus(ctx, runID, "rejected", "operator rejected review", "")
	run, _ = m.loadPipelineRun(ctx, runID)
	return &transcodev1.RejectPipelineRunResponse{Run: run}, nil
}

// removePipelineOutputs deletes a rejected run's outputs. The stored paths
// are re-checked: only entries inside TRANSCODER_MEDIA_ROOTS or
// TRANSCODER_OUTPUT_DIR are removed.
func (m *Module) removePipelineOutputs(run *transcodev1.PipelineRun) error {
	roots := append(append([]string{}, m.mediaRoots...), m.outputDir)
	for _, out := range run.GetOutputs() {
		path := strings.TrimSpace(out.GetOutputPath())
		if path == "" {
			continue
		}
		entry, err := confineEntry("output", path, roots)
		if err != nil {
			return fmt.Errorf("remove output refused: %w", err)
		}
		if err := os.Remove(entry); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove output %q: %w", path, err)
		}
	}
	return nil
}

func (m *Module) updatePipelineRunStatus(ctx context.Context, runID, status, skipReason, errMsg string) {
	now := time.Now().UTC().Format(time.RFC3339)
	_, _ = m.db.ExecContext(ctx, `
		UPDATE transcode_pipeline_runs SET status = ?, skip_reason = ?, error = ?, completed_at = ?, updated_at = ? WHERE id = ?`,
		status, skipReason, errMsg, now, now, runID)
}
