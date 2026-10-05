package internal

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	transcodev1 "github.com/Muxcore-Media/media-transcoder/proto/transcodev1"
)

func (m *Module) ProcessFile(ctx context.Context, req *transcodev1.ProcessFileRequest) (*transcodev1.ProcessFileResponse, error) {
	setupID := strings.TrimSpace(req.GetSetupId())
	input := filepathClean(req.GetInputPath())
	if setupID == "" || input == "" {
		return nil, fmt.Errorf("setup_id and input_path are required")
	}
	if _, err := os.Stat(input); err != nil {
		return nil, fmt.Errorf("input not found: %w", err)
	}

	m.mu.RLock()
	setup, err := m.loadSetup(ctx, setupID)
	m.mu.RUnlock()
	if err != nil {
		return nil, err
	}
	if setup == nil {
		return nil, fmt.Errorf("setup not found: %s", setupID)
	}

	runID, status, msg, err := m.startPipelineRun(ctx, setup, input)
	if err != nil {
		return nil, err
	}
	if req.GetWait() {
		if err := m.waitForPipelineRun(ctx, runID); err != nil {
			return nil, err
		}
		m.mu.RLock()
		run, _ := m.loadPipelineRun(ctx, runID)
		m.mu.RUnlock()
		if run != nil {
			status = run.GetStatus()
			if run.GetSkipReason() != "" {
				msg = run.GetSkipReason()
			} else if run.GetError() != "" {
				msg = run.GetError()
			}
		}
	}
	return &transcodev1.ProcessFileResponse{RunId: runID, Status: status, Message: msg}, nil
}

func (m *Module) ScanSetups(ctx context.Context, req *transcodev1.ScanSetupsRequest) (*transcodev1.ScanSetupsResponse, error) {
	m.mu.RLock()
	setups, err := m.loadAllSetups(ctx)
	m.mu.RUnlock()
	if err != nil {
		return nil, err
	}
	setupID := strings.TrimSpace(req.GetSetupId())
	rootOverride := filepathClean(req.GetRootPath())
	queued := 0
	for _, setup := range setups {
		if setupID != "" && setup.GetId() != setupID {
			continue
		}
		if !setup.GetEnabled() {
			continue
		}
		if setup.GetTrigger() == "manual" {
			continue
		}
		roots := setup.GetLibraryPaths()
		if rootOverride != "" {
			roots = []string{rootOverride}
		}
		for _, root := range roots {
			root = filepathClean(root)
			if root == "" {
				continue
			}
			walkErr := filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
				if walkErr != nil {
					return walkErr
				}
				if d.IsDir() {
					return nil
				}
				if !isVideoFile(path) {
					return nil
				}
				if m.hasCompletedRun(ctx, setup.GetId(), path) {
					return nil
				}
				if _, _, _, err := m.startPipelineRun(ctx, setup, path); err == nil {
					queued++
				}
				return nil
			})
			if walkErr != nil {
				slog.Warn("scan walk", "root", root, "error", walkErr)
			}
		}
	}
	return &transcodev1.ScanSetupsResponse{FilesQueued: int32(queued)}, nil
}

func (m *Module) ListPipelineRuns(ctx context.Context, req *transcodev1.ListPipelineRunsRequest) (*transcodev1.ListPipelineRunsResponse, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	page := int(req.GetPage())
	if page < 1 {
		page = 1
	}
	pageSize := int(req.GetPageSize())
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	offset := (page - 1) * pageSize

	query := `SELECT id FROM transcode_pipeline_runs`
	countQuery := `SELECT COUNT(*) FROM transcode_pipeline_runs`
	var args []any
	var where []string
	if s := strings.TrimSpace(req.GetStatus()); s != "" && s != "all" {
		where = append(where, `status = ?`)
		args = append(args, s)
	}
	if sid := strings.TrimSpace(req.GetSetupId()); sid != "" {
		where = append(where, `setup_id = ?`)
		args = append(args, sid)
	}
	if len(where) > 0 {
		clause := ` WHERE ` + strings.Join(where, ` AND `) //nolint:gosec // clause is built from fixed fragments; values are bound
		//nolint:gosec // values are bound; only static WHERE fragments are concatenated
		query += clause
		countQuery += clause
	}
	var total int
	_ = m.db.QueryRowContext(ctx, countQuery, args...).Scan(&total)
	query += ` ORDER BY created_at DESC LIMIT ? OFFSET ?`
	qargs := append(append([]any{}, args...), pageSize, offset)
	rows, err := m.db.QueryContext(ctx, query, qargs...)
	if err != nil {
		return nil, err
	}
	// Drain and close the outer rows before running per-run queries: the SQLite
	// pool has a single connection, so a nested query would block forever.
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	iterErr := rows.Err()
	_ = rows.Close()
	if iterErr != nil {
		return nil, iterErr
	}
	var runs []*transcodev1.PipelineRun
	for _, id := range ids {
		run, err := m.loadPipelineRun(ctx, id)
		if err != nil {
			return nil, err
		}
		if run != nil {
			runs = append(runs, run)
		}
	}
	return &transcodev1.ListPipelineRunsResponse{
		Runs: runs, Total: int32(total), Page: int32(page), PageSize: int32(pageSize), //nolint:gosec // pagination bounds are capped above
	}, nil
}

func (m *Module) GetPipelineRun(ctx context.Context, req *transcodev1.GetPipelineRunRequest) (*transcodev1.GetPipelineRunResponse, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	run, err := m.loadPipelineRun(ctx, req.GetId())
	if err != nil {
		return nil, err
	}
	if run == nil {
		return nil, fmt.Errorf("pipeline run not found: %s", req.GetId())
	}
	return &transcodev1.GetPipelineRunResponse{Run: run}, nil
}

func (m *Module) startPipelineRun(ctx context.Context, setup *transcodev1.TranscodeSetup, inputPath string) (runID, status, msg string, err error) { //nolint:contextcheck // runs are bound to the module lifecycle context
	inputPath = filepathClean(inputPath)
	now := time.Now().UTC().Format(time.RFC3339)
	runID = fmt.Sprintf("pr_%d", time.Now().UnixNano())

	bgCtx, ok := m.bgAcquire()
	if !ok {
		return "", "", "", fmt.Errorf("module is stopping")
	}
	m.mu.Lock()
	_, err = m.db.ExecContext(ctx, `
		INSERT INTO transcode_pipeline_runs (id, setup_id, setup_name, input_path, status, source_disposition, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'queued', ?, ?, ?)`,
		runID, setup.GetId(), setup.GetName(), inputPath, setup.GetSourceDisposition(), now, now)
	m.mu.Unlock()
	if err != nil {
		m.bgRelease()
		return "", "", "", err
	}

	// The run outlives the triggering RPC/event, so it is bound to the module
	// lifecycle (cancelled and awaited by Stop), not the caller's context.
	go func() {
		defer m.bgRelease()
		m.executePipelineRun(bgCtx, runID, setup, inputPath)
	}()
	return runID, "queued", "pipeline started", nil
}

func (m *Module) executePipelineRun(ctx context.Context, runID string, setup *transcodev1.TranscodeSetup, inputPath string) {
	skipReason, err := m.evaluatePipelineFilters(inputPath, setup.GetSteps())
	if err != nil {
		m.finishPipelineRun(ctx, runID, "failed", "", err.Error())
		return
	}
	if skipReason != "" {
		m.finishPipelineRun(ctx, runID, "skipped", skipReason, "")
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)
	m.mu.Lock()
	_, _ = m.db.ExecContext(ctx, `UPDATE transcode_pipeline_runs SET status = 'running', updated_at = ? WHERE id = ?`, now, runID)
	m.mu.Unlock()

	var pendingJobs []string
	for _, out := range setup.GetOutputs() {
		if !out.GetEnabled() {
			continue
		}
		profile := m.loadProfileLocked(ctx, out.GetProfileId())
		if profile == nil {
			m.finishPipelineRun(ctx, runID, "failed", "", fmt.Sprintf("profile not found: %s", out.GetProfileId()))
			return
		}
		outputPath := computeOutputPath(inputPath, out, profile)
		if outputPath == inputPath {
			m.finishPipelineRun(ctx, runID, "failed", "", "output path equals input path")
			return
		}
		outID := fmt.Sprintf("po_%d", time.Now().UnixNano())
		m.mu.Lock()
		_, _ = m.db.ExecContext(ctx, `
			INSERT INTO transcode_pipeline_outputs (id, run_id, output_path, profile_id, status)
			VALUES (?, ?, ?, ?, 'queued')`, outID, runID, outputPath, out.GetProfileId())
		m.mu.Unlock()

		resp, err := m.Enqueue(ctx, &transcodev1.EnqueueRequest{
			InputPath:  inputPath,
			OutputPath: outputPath,
			ProfileId:  out.GetProfileId(),
		})
		if err != nil {
			m.mu.Lock()
			_, _ = m.db.ExecContext(ctx, `UPDATE transcode_pipeline_outputs SET status = 'failed', error = ? WHERE id = ?`, err.Error(), outID)
			m.mu.Unlock()
			m.finishPipelineRun(ctx, runID, "failed", "", err.Error())
			return
		}
		jobID := resp.GetJobId()
		m.mu.Lock()
		_, _ = m.db.ExecContext(ctx, `UPDATE transcode_pipeline_outputs SET job_id = ?, status = 'running' WHERE id = ?`, jobID, outID)
		m.mu.Unlock()
		m.trackPipelineJob(runID, jobID)
		pendingJobs = append(pendingJobs, jobID)
	}

	if len(pendingJobs) == 0 {
		m.finishPipelineRun(ctx, runID, "failed", "", "no enabled outputs")
		return
	}
	m.waitForJobsAndFinalize(ctx, runID, setup, inputPath, pendingJobs)
}

func (m *Module) waitForJobsAndFinalize(ctx context.Context, runID string, setup *transcodev1.TranscodeSetup, inputPath string, jobIDs []string) {
	deadline := time.Now().Add(48 * time.Hour)
	for time.Now().Before(deadline) {
		allDone := true
		anyFailed := false
		failMsg := ""
		for _, jobID := range jobIDs {
			m.mu.RLock()
			row := m.db.QueryRowContext(ctx, `SELECT status, error FROM transcode_jobs WHERE id = ?`, jobID)
			var st, errMsg string
			_ = row.Scan(&st, &errMsg)
			m.mu.RUnlock()
			switch st {
			case "completed":
			case "failed", "cancelled":
				anyFailed = true
				failMsg = errMsg
				if failMsg == "" {
					failMsg = st
				}
			default:
				allDone = false
			}
		}
		if anyFailed {
			m.finishPipelineRun(ctx, runID, "failed", "", failMsg)
			return
		}
		if allDone {
			if needsHoldForReview(setup) {
				m.finishPipelineRun(ctx, runID, "pending_review", "", "")
				return
			}
			if err := m.applySourceDisposition(inputPath, setup); err != nil {
				m.finishPipelineRun(ctx, runID, "failed", "", err.Error())
				return
			}
			m.finishPipelineRun(ctx, runID, "completed", "", "")
			return
		}
		if !sleepCtx(ctx, 2*time.Second) {
			return // module stopping; the run stays 'running' and no DB access follows
		}
	}
	m.finishPipelineRun(ctx, runID, "failed", "", "timed out waiting for transcode jobs")
}

func (m *Module) waitForPipelineRun(ctx context.Context, runID string) error {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			m.mu.RLock()
			run, _ := m.loadPipelineRun(ctx, runID)
			m.mu.RUnlock()
			if run == nil {
				return fmt.Errorf("run not found")
			}
			switch run.GetStatus() {
			case "completed", "skipped", "failed", "pending_review", "rejected":
				return nil
			}
		}
	}
}

func (m *Module) finishPipelineRun(ctx context.Context, runID, status, skipReason, errMsg string) {
	now := time.Now().UTC().Format(time.RFC3339)
	m.mu.Lock()
	_, _ = m.db.ExecContext(ctx, `
		UPDATE transcode_pipeline_runs SET status = ?, skip_reason = ?, error = ?, completed_at = ?, updated_at = ? WHERE id = ?`,
		status, skipReason, errMsg, now, now, runID)
	m.mu.Unlock()
	slog.Info("pipeline run finished", "run", runID, "status", status)
}

func (m *Module) evaluatePipelineFilters(inputPath string, steps []*transcodev1.PipelineStep) (skipReason string, err error) {
	info, err := m.probeFile(inputPath)
	if err != nil {
		return "", err
	}
	for _, step := range steps {
		if step == nil || !step.GetEnabled() {
			continue
		}
		skip, reason, err := m.evalStep(inputPath, info, step)
		if err != nil {
			return "", err
		}
		if skip {
			return reason, nil
		}
	}
	return "", nil
}

type probeInfo struct {
	VideoCodec string
	Container  string
	Extension  string
	SizeBytes  int64
}

func (m *Module) evalStep(inputPath string, info *probeInfo, step *transcodev1.PipelineStep) (skip bool, reason string, err error) {
	cfg := map[string]any{}
	_ = json.Unmarshal([]byte(step.GetConfigJson()), &cfg)
	switch step.GetStepType() {
	case "filter.skip_if_codec":
		for _, c := range stringList(cfg["codecs"]) {
			if codecMatches(info.VideoCodec, c) {
				return true, fmt.Sprintf("already %s", c), nil
			}
		}
	case "filter.skip_if_container":
		for _, c := range stringList(cfg["containers"]) {
			if strings.EqualFold(info.Container, c) {
				return true, fmt.Sprintf("already %s container", c), nil
			}
		}
	case "filter.min_size_mb":
		minMB := floatFrom(cfg["min_mb"])
		if minMB > 0 && float64(info.SizeBytes) < minMB*1024*1024 {
			return true, fmt.Sprintf("below min size %.0f MB", minMB), nil
		}
	case "filter.max_size_mb":
		maxMB := floatFrom(cfg["max_mb"])
		if maxMB > 0 && float64(info.SizeBytes) > maxMB*1024*1024 {
			return true, fmt.Sprintf("above max size %.0f MB", maxMB), nil
		}
	case "filter.extension":
		allowed := stringList(cfg["extensions"])
		if len(allowed) > 0 && !containsFold(allowed, info.Extension) {
			return true, "extension not allowed", nil
		}
	case "filter.skip_if_output_exists":
		suffix := fmt.Sprint(cfg["suffix"])
		if suffix != "" {
			candidate := strings.TrimSuffix(inputPath, info.Extension) + suffix + info.Extension
			if _, err := os.Stat(candidate); err == nil {
				return true, "output already exists", nil
			}
		}
	case "action.remux":
		container := strings.ToLower(fmt.Sprint(cfg["container"]))
		if container != "" && !strings.EqualFold(info.Container, container) {
			if err := m.remuxContainer(inputPath, container); err != nil {
				return false, "", err
			}
			info.Container = container
			info.Extension = "." + container
		}
	}
	return false, "", nil
}

func (m *Module) remuxContainer(inputPath, container string) error {
	dir := filepath.Dir(inputPath)
	base := strings.TrimSuffix(filepath.Base(inputPath), filepath.Ext(inputPath))
	tmp := filepath.Join(dir, base+".remux."+container)
	args := []string{"-i", inputPath, "-c", "copy", "-y", tmp}
	cmd := exec.Command(m.getFFmpegBin(), args...) //nolint:gosec // ffmpeg paths come from operator-controlled media library
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("remux: %w: %s", err, strings.TrimSpace(string(out)))
	}
	if err := os.Rename(tmp, filepath.Join(dir, base+"."+container)); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if filepath.Join(dir, base+"."+container) != inputPath {
		_ = os.Remove(inputPath)
	}
	return nil
}

func (m *Module) probeFile(path string) (*probeInfo, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	ext := strings.ToLower(filepath.Ext(path))
	info := &probeInfo{
		Container: strings.TrimPrefix(ext, "."),
		SizeBytes: st.Size(),
		Extension: ext,
	}
	args := []string{"-v", "error", "-select_streams", "v:0", "-show_entries", "stream=codec_name", "-of", "default=nw=1", path}
	cmd := exec.Command("ffprobe", args...) //nolint:gosec // ffprobe reads operator-controlled media paths
	out, err := cmd.Output()
	if err == nil {
		line := strings.TrimSpace(string(out))
		if idx := strings.Index(line, "="); idx >= 0 {
			info.VideoCodec = strings.TrimSpace(line[idx+1:])
		}
	}
	return info, nil
}

func (m *Module) applySourceDisposition(inputPath string, setup *transcodev1.TranscodeSetup) error {
	switch normalizeDisposition(setup.GetSourceDisposition()) {
	case "keep":
		return nil
	case "delete":
		return os.Remove(inputPath)
	case "archive":
		archiveDir := filepathClean(setup.GetArchivePath())
		if archiveDir == "" {
			return fmt.Errorf("archive_path required for archive disposition")
		}
		if err := os.MkdirAll(archiveDir, 0o750); err != nil {
			return err
		}
		dest := filepath.Join(archiveDir, filepath.Base(inputPath))
		return os.Rename(inputPath, dest)
	case "replace":
		return os.Remove(inputPath)
	default:
		return nil
	}
}

func computeOutputPath(input string, out *transcodev1.SetupOutput, profile *transcodev1.TranscodeProfile) string {
	dir := filepath.Dir(input)
	base := filepath.Base(input)
	ext := filepath.Ext(base)
	name := strings.TrimSuffix(base, ext)
	container := profile.GetContainer()
	if container == "" {
		container = strings.TrimPrefix(ext, ".")
	}
	if out.GetReplaceExtension() {
		suffix := out.GetSuffix()
		return filepath.Join(dir, name+suffix+"."+container)
	}
	suffix := out.GetSuffix()
	if suffix == "" {
		suffix = "-transcoded"
	}
	return filepath.Join(dir, name+suffix+ext)
}

func (m *Module) loadPipelineRun(ctx context.Context, id string) (*transcodev1.PipelineRun, error) {
	row := m.db.QueryRowContext(ctx, `
		SELECT id, setup_id, setup_name, input_path, status, skip_reason, source_disposition, error, created_at, completed_at
		FROM transcode_pipeline_runs WHERE id = ?`, id)
	var run transcodev1.PipelineRun
	if err := row.Scan(&run.Id, &run.SetupId, &run.SetupName, &run.InputPath, &run.Status,
		&run.SkipReason, &run.SourceDisposition, &run.Error, &run.CreatedAt, &run.CompletedAt); err != nil {
		return nil, err
	}
	outRows, err := m.db.QueryContext(ctx, `
		SELECT output_path, profile_id, job_id, status, error FROM transcode_pipeline_outputs WHERE run_id = ?`, id)
	if err != nil {
		return nil, err
	}
	defer func() { _ = outRows.Close() }()
	for outRows.Next() {
		var o transcodev1.PipelineRunOutput
		if err := outRows.Scan(&o.OutputPath, &o.ProfileId, &o.JobId, &o.Status, &o.Error); err != nil {
			return nil, err
		}
		run.Outputs = append(run.Outputs, &o)
	}
	return &run, nil
}

func (m *Module) hasCompletedRun(ctx context.Context, setupID, inputPath string) bool {
	var n int
	_ = m.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM transcode_pipeline_runs
		WHERE setup_id = ? AND input_path = ? AND status IN ('completed', 'skipped', 'pending_review')`,
		setupID, filepathClean(inputPath)).Scan(&n)
	return n > 0
}

func (m *Module) loadProfileLocked(ctx context.Context, id string) *transcodev1.TranscodeProfile {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.loadProfile(ctx, id)
}

func (m *Module) trackPipelineJob(runID, jobID string) {
	m.pipelineMu.Lock()
	defer m.pipelineMu.Unlock()
	if m.pipelineJobs == nil {
		m.pipelineJobs = make(map[string]string)
	}
	m.pipelineJobs[jobID] = runID
}

func isVideoFile(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".mkv", ".mp4", ".avi", ".mov", ".wmv", ".m4v", ".ts", ".webm":
		return true
	default:
		return false
	}
}

func filepathClean(p string) string {
	if p == "" {
		return ""
	}
	clean := filepath.Clean(p)
	if strings.HasSuffix(p, string(os.PathSeparator)) && clean != string(os.PathSeparator) {
		return clean + string(os.PathSeparator)
	}
	return clean
}

func codecMatches(have, want string) bool {
	h := strings.ToLower(strings.TrimSpace(have))
	w := strings.ToLower(strings.TrimSpace(want))
	if h == w {
		return true
	}
	if w == "hevc" && (h == "h265" || h == "hevc") {
		return true
	}
	if w == "h264" && (h == "h264" || h == "avc") {
		return true
	}
	return false
}

func stringList(v any) []string {
	switch t := v.(type) {
	case []any:
		var out []string
		for _, x := range t {
			if s := strings.TrimSpace(fmt.Sprint(x)); s != "" {
				out = append(out, s)
			}
		}
		return out
	case []string:
		return t
	default:
		return nil
	}
}

func floatFrom(v any) float64 {
	switch t := v.(type) {
	case float64:
		return t
	case int:
		return float64(t)
	case json.Number:
		f, _ := t.Float64()
		return f
	default:
		return 0
	}
}

func containsFold(list []string, want string) bool {
	for _, s := range list {
		if strings.EqualFold(strings.TrimPrefix(s, "."), strings.TrimPrefix(want, ".")) {
			return true
		}
	}
	return false
}
