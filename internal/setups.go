package internal

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	transcodev1 "github.com/Muxcore-Media/media-transcoder/proto/transcodev1"
)

func (m *Module) ensureSetupTables(ctx context.Context, db *sql.DB) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS transcode_setups (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			enabled INTEGER NOT NULL DEFAULT 1,
			library_paths TEXT NOT NULL DEFAULT '[]',
			trigger_mode TEXT NOT NULL DEFAULT 'on_import',
			source_disposition TEXT NOT NULL DEFAULT 'keep',
			archive_path TEXT NOT NULL DEFAULT '',
			hold_for_review INTEGER NOT NULL DEFAULT 0,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS transcode_setup_outputs (
			id TEXT PRIMARY KEY,
			setup_id TEXT NOT NULL,
			profile_id TEXT NOT NULL,
			suffix TEXT NOT NULL DEFAULT '',
			replace_extension INTEGER NOT NULL DEFAULT 0,
			sort_order INTEGER NOT NULL DEFAULT 0,
			enabled INTEGER NOT NULL DEFAULT 1
		)`,
		`CREATE TABLE IF NOT EXISTS transcode_setup_steps (
			id TEXT PRIMARY KEY,
			setup_id TEXT NOT NULL,
			step_type TEXT NOT NULL,
			config_json TEXT NOT NULL DEFAULT '{}',
			sort_order INTEGER NOT NULL DEFAULT 0,
			enabled INTEGER NOT NULL DEFAULT 1
		)`,
		`CREATE TABLE IF NOT EXISTS transcode_pipeline_runs (
			id TEXT PRIMARY KEY,
			setup_id TEXT NOT NULL,
			setup_name TEXT NOT NULL DEFAULT '',
			input_path TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'queued',
			skip_reason TEXT NOT NULL DEFAULT '',
			source_disposition TEXT NOT NULL DEFAULT '',
			error TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			completed_at TEXT NOT NULL DEFAULT '',
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS transcode_pipeline_outputs (
			id TEXT PRIMARY KEY,
			run_id TEXT NOT NULL,
			output_path TEXT NOT NULL,
			profile_id TEXT NOT NULL,
			job_id TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT 'pending',
			error TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE INDEX IF NOT EXISTS idx_pipeline_runs_setup ON transcode_pipeline_runs(setup_id)`,
		`CREATE INDEX IF NOT EXISTS idx_pipeline_runs_status ON transcode_pipeline_runs(status)`,
		`CREATE INDEX IF NOT EXISTS idx_pipeline_outputs_run ON transcode_pipeline_outputs(run_id)`,
	}
	for _, stmt := range stmts {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("setup schema: %w", err)
		}
	}
	_, _ = db.ExecContext(ctx, `ALTER TABLE transcode_setups ADD COLUMN hold_for_review INTEGER NOT NULL DEFAULT 0`)
	return m.seedDefaultSetup(ctx, db)
}

func (m *Module) seedDefaultSetup(ctx context.Context, db *sql.DB) error {
	var n int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM transcode_setups`).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	now := time.Now().UTC().Format(time.RFC3339)
	setupID := "setup_hevc_keep_original"
	_, err := db.ExecContext(ctx, `
		INSERT INTO transcode_setups (id, name, enabled, library_paths, trigger_mode, source_disposition, archive_path, created_at, updated_at)
		VALUES (?, ?, 1, ?, 'on_import', 'keep', '', ?, ?)`,
		setupID, "HEVC alongside original", `["/data/media"]`, now, now)
	if err != nil {
		return err
	}
	outID := "out_hevc_gpu"
	_, err = db.ExecContext(ctx, `
		INSERT INTO transcode_setup_outputs (id, setup_id, profile_id, suffix, replace_extension, sort_order, enabled)
		VALUES (?, ?, 'hevc_gpu', '-hevc', 1, 0, 1)`, outID, setupID)
	if err != nil {
		return err
	}
	stepID := "step_skip_hevc"
	_, err = db.ExecContext(ctx, `
		INSERT INTO transcode_setup_steps (id, setup_id, step_type, config_json, sort_order, enabled)
		VALUES (?, ?, 'filter.skip_if_codec', ?, 0, 1)`, stepID, setupID, `{"codecs":["hevc","h265"]}`)
	return err
}

func (m *Module) ListSetups(ctx context.Context, _ *transcodev1.ListSetupsRequest) (*transcodev1.ListSetupsResponse, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	setups, err := m.loadAllSetups(ctx)
	if err != nil {
		return nil, err
	}
	return &transcodev1.ListSetupsResponse{Setups: setups}, nil
}

func (m *Module) GetSetup(ctx context.Context, req *transcodev1.GetSetupRequest) (*transcodev1.GetSetupResponse, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	setup, err := m.loadSetup(ctx, req.GetId())
	if err != nil {
		return nil, err
	}
	if setup == nil {
		return nil, fmt.Errorf("setup not found: %s", req.GetId())
	}
	return &transcodev1.GetSetupResponse{Setup: setup}, nil
}

func (m *Module) UpsertSetup(ctx context.Context, req *transcodev1.UpsertSetupRequest) (*transcodev1.UpsertSetupResponse, error) {
	in := req.GetSetup()
	if in == nil || strings.TrimSpace(in.GetName()) == "" {
		return nil, fmt.Errorf("setup name is required")
	}
	if len(in.GetOutputs()) == 0 {
		return nil, fmt.Errorf("at least one output profile is required")
	}
	// library_paths and archive_path must lie inside TRANSCODER_MEDIA_ROOTS
	// (re-checked again before any read, delete or rename).
	libraryPaths, err := m.validateSetupPaths(in)
	if err != nil {
		return nil, err
	}
	archivePath := strings.TrimSpace(in.GetArchivePath())
	if archivePath != "" {
		archivePath = filepath.Clean(archivePath)
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now().UTC().Format(time.RFC3339)
	id := strings.TrimSpace(in.GetId())
	if id == "" {
		id = fmt.Sprintf("ts_%d", time.Now().UnixNano())
	}
	if libraryPaths == nil {
		libraryPaths = []string{}
	}
	pathsJSON, _ := json.Marshal(libraryPaths)
	trigger := normalizeTrigger(in.GetTrigger())
	disp := normalizeDisposition(in.GetSourceDisposition())

	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	var exists int
	_ = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM transcode_setups WHERE id = ?`, id).Scan(&exists)
	if exists == 0 {
		_, err = tx.ExecContext(ctx, `
			INSERT INTO transcode_setups (id, name, enabled, library_paths, trigger_mode, source_disposition, archive_path, hold_for_review, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, in.GetName(), boolToInt(in.GetEnabled()), string(pathsJSON), trigger, disp, archivePath, boolToInt(in.GetHoldForReview()), now, now)
	} else {
		_, err = tx.ExecContext(ctx, `
			UPDATE transcode_setups SET name=?, enabled=?, library_paths=?, trigger_mode=?, source_disposition=?, archive_path=?, hold_for_review=?, updated_at=? WHERE id=?`,
			in.GetName(), boolToInt(in.GetEnabled()), string(pathsJSON), trigger, disp, archivePath, boolToInt(in.GetHoldForReview()), now, id)
	}
	if err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM transcode_setup_outputs WHERE setup_id = ?`, id); err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM transcode_setup_steps WHERE setup_id = ?`, id); err != nil {
		return nil, err
	}
	for i, out := range in.GetOutputs() {
		outID := strings.TrimSpace(out.GetId())
		if outID == "" {
			outID = fmt.Sprintf("to_%d_%d", time.Now().UnixNano(), i)
		}
		order := out.GetSortOrder()
		if order == 0 {
			order = int32(i)
		}
		_, err = tx.ExecContext(ctx, `
			INSERT INTO transcode_setup_outputs (id, setup_id, profile_id, suffix, replace_extension, sort_order, enabled)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			outID, id, out.GetProfileId(), out.GetSuffix(), boolToInt(out.GetReplaceExtension()), order, boolToInt(out.GetEnabled()))
		if err != nil {
			return nil, err
		}
	}
	for i, step := range in.GetSteps() {
		stepID := strings.TrimSpace(step.GetId())
		if stepID == "" {
			stepID = fmt.Sprintf("st_%d_%d", time.Now().UnixNano(), i)
		}
		order := step.GetSortOrder()
		if order == 0 {
			order = int32(i)
		}
		cfg := strings.TrimSpace(step.GetConfigJson())
		if cfg == "" {
			cfg = "{}"
		}
		_, err = tx.ExecContext(ctx, `
			INSERT INTO transcode_setup_steps (id, setup_id, step_type, config_json, sort_order, enabled)
			VALUES (?, ?, ?, ?, ?, ?)`,
			stepID, id, step.GetStepType(), cfg, order, boolToInt(step.GetEnabled()))
		if err != nil {
			return nil, err
		}
	}
	if commitErr := tx.Commit(); commitErr != nil {
		return nil, commitErr
	}
	setup, err := m.loadSetup(ctx, id)
	if err != nil {
		return nil, err
	}
	return &transcodev1.UpsertSetupResponse{Setup: setup}, nil
}

func (m *Module) DeleteSetup(ctx context.Context, req *transcodev1.DeleteSetupRequest) (*transcodev1.DeleteSetupResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id := req.GetId()
	_, _ = m.db.ExecContext(ctx, `DELETE FROM transcode_setup_outputs WHERE setup_id = ?`, id)
	_, _ = m.db.ExecContext(ctx, `DELETE FROM transcode_setup_steps WHERE setup_id = ?`, id)
	_, err := m.db.ExecContext(ctx, `DELETE FROM transcode_setups WHERE id = ?`, id)
	if err != nil {
		return nil, err
	}
	return &transcodev1.DeleteSetupResponse{}, nil
}

func (m *Module) loadAllSetups(ctx context.Context) ([]*transcodev1.TranscodeSetup, error) {
	rows, err := m.db.QueryContext(ctx, `SELECT id FROM transcode_setups ORDER BY name`)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if scanErr := rows.Scan(&id); scanErr != nil {
			_ = rows.Close()
			return nil, scanErr
		}
		ids = append(ids, id)
	}
	_ = rows.Close()
	var setups []*transcodev1.TranscodeSetup
	for _, id := range ids {
		s, err := m.loadSetup(ctx, id)
		if err != nil {
			return nil, err
		}
		if s != nil {
			setups = append(setups, s)
		}
	}
	return setups, nil
}

func (m *Module) loadSetup(ctx context.Context, id string) (*transcodev1.TranscodeSetup, error) {
	row := m.db.QueryRowContext(ctx, `
		SELECT id, name, enabled, library_paths, trigger_mode, source_disposition, archive_path, hold_for_review, created_at, updated_at
		FROM transcode_setups WHERE id = ?`, id)
	var setup transcodev1.TranscodeSetup
	var enabled, hold int
	var pathsJSON string
	if err := row.Scan(&setup.Id, &setup.Name, &enabled, &pathsJSON, &setup.Trigger, &setup.SourceDisposition,
		&setup.ArchivePath, &hold, &setup.CreatedAt, &setup.UpdatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	setup.Enabled = enabled != 0
	setup.HoldForReview = hold != 0
	_ = json.Unmarshal([]byte(pathsJSON), &setup.LibraryPaths)

	outRows, err := m.db.QueryContext(ctx, `
		SELECT id, profile_id, suffix, replace_extension, sort_order, enabled
		FROM transcode_setup_outputs WHERE setup_id = ? ORDER BY sort_order, id`, id)
	if err != nil {
		return nil, err
	}
	for outRows.Next() {
		var o transcodev1.SetupOutput
		var rep, en int
		if scanErr := outRows.Scan(&o.Id, &o.ProfileId, &o.Suffix, &rep, &o.SortOrder, &en); scanErr != nil {
			_ = outRows.Close()
			return nil, scanErr
		}
		o.ReplaceExtension = rep != 0
		o.Enabled = en != 0
		setup.Outputs = append(setup.Outputs, &o)
	}
	_ = outRows.Close()

	stepRows, err := m.db.QueryContext(ctx, `
		SELECT id, step_type, config_json, sort_order, enabled
		FROM transcode_setup_steps WHERE setup_id = ? ORDER BY sort_order, id`, id)
	if err != nil {
		return nil, err
	}
	defer func() { _ = stepRows.Close() }()
	for stepRows.Next() {
		var s transcodev1.PipelineStep
		var en int
		if scanErr := stepRows.Scan(&s.Id, &s.StepType, &s.ConfigJson, &s.SortOrder, &en); scanErr != nil {
			return nil, scanErr
		}
		s.Enabled = en != 0
		setup.Steps = append(setup.Steps, &s)
	}
	return &setup, nil
}

func (m *Module) matchSetupsForPath(ctx context.Context, path string) ([]*transcodev1.TranscodeSetup, error) {
	all, err := m.loadAllSetups(ctx)
	if err != nil {
		return nil, err
	}
	clean := filepathClean(path)
	var matched []*transcodev1.TranscodeSetup
	for _, s := range all {
		if !s.GetEnabled() {
			continue
		}
		for _, prefix := range s.GetLibraryPaths() {
			prefix = filepathClean(prefix)
			if prefix == "" {
				continue
			}
			if clean == prefix || strings.HasPrefix(clean, prefix+string(filepath.Separator)) {
				matched = append(matched, s)
				break
			}
		}
	}
	return matched, nil
}

func normalizeTrigger(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "manual", "scheduled", "on_import":
		return strings.ToLower(strings.TrimSpace(v))
	default:
		return "on_import"
	}
}

func normalizeDisposition(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "keep", "replace", "delete", "archive":
		return strings.ToLower(strings.TrimSpace(v))
	default:
		return "keep"
	}
}
