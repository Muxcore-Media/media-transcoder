package internal

import (
	"context"
	"log/slog"
	"time"

	transcodev1 "github.com/Muxcore-Media/media-transcoder/proto/transcodev1"
)

func (m *Module) resumePendingWork(ctx context.Context) {
	m.resumePendingJobs(ctx)
	m.resumePendingPipelineRuns(ctx)
}

func (m *Module) resumePendingJobs(ctx context.Context) {
	m.mu.RLock()
	rows, err := m.db.QueryContext(ctx, `
		SELECT id, input_path, output_path, profile_id, profile_name, status, created_at, updated_at
		FROM transcode_jobs WHERE status IN ('queued', 'running') ORDER BY created_at`)
	m.mu.RUnlock()
	if err != nil {
		slog.Warn("resume jobs query", "error", err)
		return
	}
	defer func() { _ = rows.Close() }()

	type pendingJob struct {
		id, input, output, profileID, profileName, status, createdAt, updatedAt string
	}
	var pending []pendingJob
	for rows.Next() {
		var p pendingJob
		if scanErr := rows.Scan(&p.id, &p.input, &p.output, &p.profileID, &p.profileName, &p.status, &p.createdAt, &p.updatedAt); scanErr != nil {
			slog.Warn("resume jobs scan", "error", scanErr)
			return
		}
		pending = append(pending, p)
	}

	for _, p := range pending {
		if p.status == "running" {
			now := time.Now().UTC().Format(time.RFC3339)
			m.mu.Lock()
			_, _ = m.db.ExecContext(ctx, `UPDATE transcode_jobs SET status = 'queued', error = 'requeued after restart', updated_at = ? WHERE id = ?`, now, p.id)
			m.mu.Unlock()
		}

		m.mu.RLock()
		profile := m.loadProfile(ctx, p.profileID)
		m.mu.RUnlock()
		if profile == nil {
			m.failJobByID(ctx, p.id, "profile not found after restart")
			continue
		}

		js := &jobState{
			info: &transcodev1.TranscodeJob{
				Id: p.id, InputPath: p.input, OutputPath: p.output,
				ProfileId: p.profileID, ProfileName: p.profileName,
				Status: "queued", CreatedAt: p.createdAt, UpdatedAt: p.updatedAt,
			},
		}
		m.jobsMu.Lock()
		m.jobs[p.id] = js
		m.jobsMu.Unlock()
		go m.runJob(ctx, p.id, profile) //nolint:gosec // resume worker outlives request context
	}
}

func (m *Module) failJobByID(ctx context.Context, jobID, errMsg string) {
	now := time.Now().UTC().Format(time.RFC3339)
	m.mu.Lock()
	_, _ = m.db.ExecContext(ctx, `UPDATE transcode_jobs SET status = 'failed', error = ?, completed_at = ?, updated_at = ? WHERE id = ?`, errMsg, now, now, jobID)
	m.mu.Unlock()
}

func (m *Module) resumePendingPipelineRuns(ctx context.Context) {
	m.mu.RLock()
	rows, err := m.db.QueryContext(ctx, `
		SELECT id, setup_id, input_path, status FROM transcode_pipeline_runs
		WHERE status IN ('queued', 'running') ORDER BY created_at`)
	m.mu.RUnlock()
	if err != nil {
		slog.Warn("resume pipeline runs query", "error", err)
		return
	}
	defer func() { _ = rows.Close() }()

	type pendingRun struct {
		id, setupID, inputPath, status string
	}
	var pending []pendingRun
	for rows.Next() {
		var p pendingRun
		if scanErr := rows.Scan(&p.id, &p.setupID, &p.inputPath, &p.status); scanErr != nil {
			slog.Warn("resume pipeline scan", "error", scanErr)
			return
		}
		pending = append(pending, p)
	}

	for _, p := range pending {
		m.mu.RLock()
		setup, loadErr := m.loadSetup(ctx, p.setupID)
		m.mu.RUnlock()
		if loadErr != nil || setup == nil {
			m.finishPipelineRun(ctx, p.id, "failed", "", "setup not found after restart")
			continue
		}

		if p.status == "running" {
			var jobIDs []string
			outRows, qErr := m.db.QueryContext(ctx, `SELECT job_id FROM transcode_pipeline_outputs WHERE run_id = ? AND job_id != ''`, p.id)
			if qErr == nil {
				for outRows.Next() {
					var jobID string
					if scanErr := outRows.Scan(&jobID); scanErr == nil && jobID != "" {
						jobIDs = append(jobIDs, jobID)
					}
				}
				_ = outRows.Close()
			}
			if len(jobIDs) > 0 {
				go m.waitForJobsAndFinalize(ctx, p.id, setup, p.inputPath, jobIDs) //nolint:gosec // resume goroutine
				continue
			}
		}
		go m.executePipelineRun(ctx, p.id, setup, p.inputPath) //nolint:gosec // resume goroutine
	}
}
