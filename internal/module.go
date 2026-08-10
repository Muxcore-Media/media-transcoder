package internal

import (
	"bufio"
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/grpc"

	transcodev1 "github.com/Muxcore-Media/media-transcoder/proto/transcodev1"

	"github.com/Muxcore-Media/core/pkg/contracts"
	_ "modernc.org/sqlite"
)

type Module struct {
	transcodev1.UnimplementedTranscodeServiceServer

	mu     sync.RWMutex
	db     *sql.DB
	jobsMu sync.Mutex
	jobs   map[string]*jobState
	nextID atomic.Int64

	id           string
	dbPath       string
	grpcAddr     string
	maxConcurrent int
	jobSlots     chan struct{}
	grpcSrv      *grpc.Server
	grpcLis      net.Listener
}

type jobState struct {
	info    *transcodev1.TranscodeJob
	cancel  context.CancelFunc
	running bool
}

type Config struct {
	ID       string
	DBPath   string
	GRPCAddr string
}

func NewModule(cfg Config) *Module {
	if cfg.ID == "" {
		cfg.ID = "media-transcoder"
	}
	if cfg.DBPath == "" {
		cfg.DBPath = "/var/lib/media-transcoder/transcoder.db"
	}
	if cfg.GRPCAddr == "" {
		cfg.GRPCAddr = ":9525"
	}
	if v := os.Getenv("TRANSCODER_DB_PATH"); v != "" {
		cfg.DBPath = v
	}
	if v := os.Getenv("TRANSCODER_GRPC_ADDR"); v != "" {
		cfg.GRPCAddr = v
	}
	maxConcurrent := 2
	if v := strings.TrimSpace(os.Getenv("TRANSCODER_MAX_CONCURRENT")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			maxConcurrent = n
		}
	}
	slots := make(chan struct{}, maxConcurrent)
	for i := 0; i < maxConcurrent; i++ {
		slots <- struct{}{}
	}
	return &Module{
		id:            cfg.ID,
		dbPath:        cfg.DBPath,
		grpcAddr:      cfg.GRPCAddr,
		maxConcurrent: maxConcurrent,
		jobSlots:      slots,
		jobs:          make(map[string]*jobState),
	}
}

func (m *Module) Info() contracts.ModuleInfo {
	return contracts.ModuleInfo{
		ID:             m.id,
		Name:           "Media Transcoder",
		Version:      "0.1.3",
		Roles:          []string{"transcoder"},
		Description:    "Video transcoding via FFmpeg with GPU acceleration support, queue management, and progress tracking",
		Author:         "MuxCore",
		Capabilities:   []string{"media.transcoder", "executor.transcode", "transcoder"},
		MinCoreVersion: "0.4.0",
		HTTPAddr:       m.grpcAddr,
	}
}

func (m *Module) Init(ctx context.Context) error {
	dir := filepath.Dir(m.dbPath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create db dir: %w", err)
	}

	db, err := sql.Open("sqlite", m.dbPath)
	if err != nil {
		return fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)

	if _, err := db.ExecContext(ctx, `PRAGMA journal_mode=WAL`); err != nil {
		db.Close()
		return fmt.Errorf("enable WAL: %w", err)
	}
	if _, err := db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS transcode_profiles (
			id         TEXT PRIMARY KEY,
			name       TEXT NOT NULL UNIQUE,
			video_codec TEXT NOT NULL DEFAULT 'h264',
			audio_codec TEXT NOT NULL DEFAULT 'copy',
			preset     TEXT NOT NULL DEFAULT 'medium',
			crf        INTEGER NOT NULL DEFAULT 23,
			max_width  INTEGER NOT NULL DEFAULT 0,
			max_height INTEGER NOT NULL DEFAULT 0,
			use_gpu    INTEGER NOT NULL DEFAULT 0,
			container  TEXT NOT NULL DEFAULT 'mkv',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)
	`); err != nil {
		db.Close()
		return fmt.Errorf("create profiles table: %w", err)
	}
	if _, err := db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS transcode_jobs (
			id           TEXT PRIMARY KEY,
			input_path   TEXT NOT NULL,
			output_path  TEXT NOT NULL,
			profile_id   TEXT NOT NULL,
			profile_name TEXT NOT NULL DEFAULT '',
			status       TEXT NOT NULL DEFAULT 'queued',
			progress     REAL NOT NULL DEFAULT 0,
			fps          REAL NOT NULL DEFAULT 0,
			error        TEXT DEFAULT '',
			started_at   TEXT,
			completed_at TEXT,
			created_at   TEXT NOT NULL,
			updated_at   TEXT NOT NULL
		)
	`); err != nil {
		db.Close()
		return fmt.Errorf("create jobs table: %w", err)
	}
	if _, err := db.ExecContext(ctx, `
		CREATE INDEX IF NOT EXISTS idx_jobs_status ON transcode_jobs(status)
	`); err != nil {
		db.Close()
		return fmt.Errorf("create index: %w", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT OR IGNORE INTO transcode_profiles (id, name, video_codec, audio_codec, preset, crf, container, created_at, updated_at)
		VALUES ('h264_fast', 'H.264 Fast', 'h264', 'copy', 'fast', 23, 'mkv', ?, ?)
	`, time.Now().UTC().Format(time.RFC3339), time.Now().UTC().Format(time.RFC3339)); err != nil {
		db.Close()
		return fmt.Errorf("insert default profile: %w", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT OR IGNORE INTO transcode_profiles (id, name, video_codec, audio_codec, preset, crf, max_height, use_gpu, container, created_at, updated_at)
		VALUES ('hevc_gpu', 'HEVC GPU', 'hevc', 'copy', 'medium', 28, 1080, 1, 'mkv', ?, ?)
	`, time.Now().UTC().Format(time.RFC3339), time.Now().UTC().Format(time.RFC3339)); err != nil {
		db.Close()
		return fmt.Errorf("insert gpu profile: %w", err)
	}

	m.mu.Lock()
	m.db = db
	m.mu.Unlock()

	lis, err := net.Listen("tcp", m.grpcAddr)
	if err != nil {
		db.Close()
		return fmt.Errorf("listen gRPC: %w", err)
	}
	m.grpcLis = lis

	slog.Info("media-transcoder initialized", "db", m.dbPath, "grpc", m.grpcAddr)
	return nil
}

func (m *Module) Start(ctx context.Context) error {
	m.grpcSrv = grpc.NewServer()
	transcodev1.RegisterTranscodeServiceServer(m.grpcSrv, m)

	go func() {
		slog.Info("media-transcoder gRPC started", "addr", m.grpcAddr)
		if err := m.grpcSrv.Serve(m.grpcLis); err != nil {
			slog.Error("media-transcoder gRPC error", "error", err)
		}
	}()
	return nil
}

func (m *Module) Stop(ctx context.Context) error {
	m.jobsMu.Lock()
	for _, js := range m.jobs {
		if js.cancel != nil {
			js.cancel()
		}
	}
	m.jobsMu.Unlock()

	if m.grpcSrv != nil {
		m.grpcSrv.GracefulStop()
	}
	m.mu.Lock()
	if m.db != nil {
		m.db.Close()
		m.db = nil
	}
	m.mu.Unlock()
	slog.Info("media-transcoder stopped")
	return nil
}

func (m *Module) Health(ctx context.Context) error {
	m.mu.RLock()
	db := m.db
	m.mu.RUnlock()
	if db == nil {
		return fmt.Errorf("not initialized")
	}
	if err := db.PingContext(ctx); err != nil {
		return err
	}
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return fmt.Errorf("ffmpeg not found in PATH")
	}
	return nil
}

// ── Profile CRUD ───────────────────────────────────────────────

func (m *Module) ListProfiles(ctx context.Context, req *transcodev1.ListProfilesRequest) (*transcodev1.ListProfilesResponse, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return &transcodev1.ListProfilesResponse{Profiles: m.loadProfiles()}, nil
}

func (m *Module) CreateProfile(ctx context.Context, req *transcodev1.CreateProfileRequest) (*transcodev1.CreateProfileResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now().UTC().Format(time.RFC3339)
	id := fmt.Sprintf("tp_%d", time.Now().UnixNano())
	_, err := m.db.Exec(`INSERT INTO transcode_profiles (id, name, video_codec, audio_codec, preset, crf, max_width, max_height, use_gpu, container, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, req.GetName(), req.GetVideoCodec(), req.GetAudioCodec(), req.GetPreset(),
		req.GetCrf(), req.GetMaxWidth(), req.GetMaxHeight(), boolToInt(req.GetUseGpu()),
		req.GetContainer(), now, now)
	if err != nil {
		return nil, fmt.Errorf("create profile: %w", err)
	}
	return &transcodev1.CreateProfileResponse{Profile: m.loadProfile(id)}, nil
}

func (m *Module) UpdateProfile(ctx context.Context, req *transcodev1.UpdateProfileRequest) (*transcodev1.UpdateProfileResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now().UTC().Format(time.RFC3339)
	_, err := m.db.Exec(`UPDATE transcode_profiles SET name=?, video_codec=?, audio_codec=?, preset=?, crf=?, max_width=?, max_height=?, use_gpu=?, container=?, updated_at=? WHERE id=?`,
		req.GetName(), req.GetVideoCodec(), req.GetAudioCodec(), req.GetPreset(),
		req.GetCrf(), req.GetMaxWidth(), req.GetMaxHeight(), boolToInt(req.GetUseGpu()),
		req.GetContainer(), now, req.GetId())
	if err != nil {
		return nil, fmt.Errorf("update profile: %w", err)
	}
	p := m.loadProfile(req.GetId())
	if p == nil {
		return nil, fmt.Errorf("profile not found")
	}
	return &transcodev1.UpdateProfileResponse{Profile: p}, nil
}

func (m *Module) DeleteProfile(ctx context.Context, req *transcodev1.DeleteProfileRequest) (*transcodev1.DeleteProfileResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.db.Exec(`DELETE FROM transcode_profiles WHERE id = ?`, req.GetId())
	return &transcodev1.DeleteProfileResponse{}, nil
}

// ── Job Management ─────────────────────────────────────────────

func (m *Module) Enqueue(ctx context.Context, req *transcodev1.EnqueueRequest) (*transcodev1.EnqueueResponse, error) {
	if req.GetInputPath() == "" || req.GetOutputPath() == "" {
		return nil, fmt.Errorf("input_path and output_path are required")
	}
	if _, err := os.Stat(req.GetInputPath()); os.IsNotExist(err) {
		return nil, fmt.Errorf("input file not found: %s", req.GetInputPath())
	}

	m.mu.RLock()
	profile := m.loadProfile(req.GetProfileId())
	m.mu.RUnlock()
	if profile == nil {
		return nil, fmt.Errorf("profile not found: %s", req.GetProfileId())
	}

	now := time.Now().UTC().Format(time.RFC3339)
	id := fmt.Sprintf("tj_%d", time.Now().UnixNano())

	m.mu.Lock()
	m.db.Exec(`INSERT INTO transcode_jobs (id, input_path, output_path, profile_id, profile_name, status, created_at, updated_at) VALUES (?, ?, ?, ?, ?, 'queued', ?, ?)`,
		id, req.GetInputPath(), req.GetOutputPath(), profile.GetId(), profile.GetName(), now, now)
	m.mu.Unlock()

	js := &jobState{
		info: &transcodev1.TranscodeJob{
			Id: id, InputPath: req.GetInputPath(), OutputPath: req.GetOutputPath(),
			ProfileId: profile.GetId(), ProfileName: profile.GetName(),
			Status: "queued", CreatedAt: now, UpdatedAt: now,
		},
	}
	m.jobsMu.Lock()
	m.jobs[id] = js
	m.jobsMu.Unlock()

	go m.runJob(id, profile)
	return &transcodev1.EnqueueResponse{JobId: id}, nil
}

func (m *Module) CancelJob(ctx context.Context, req *transcodev1.CancelJobRequest) (*transcodev1.CancelJobResponse, error) {
	m.jobsMu.Lock()
	js, ok := m.jobs[req.GetJobId()]
	m.jobsMu.Unlock()
	if !ok {
		return nil, fmt.Errorf("job not found: %s", req.GetJobId())
	}
	if js.cancel != nil {
		js.cancel()
	}
	m.mu.Lock()
	m.db.Exec(`UPDATE transcode_jobs SET status = 'cancelled', updated_at = ? WHERE id = ?`, time.Now().UTC().Format(time.RFC3339), req.GetJobId())
	m.mu.Unlock()
	return &transcodev1.CancelJobResponse{}, nil
}

func (m *Module) ListJobs(ctx context.Context, req *transcodev1.ListJobsRequest) (*transcodev1.ListJobsResponse, error) {
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

	query := `SELECT id, input_path, output_path, profile_id, profile_name, status, progress, fps, error, started_at, completed_at, created_at, updated_at FROM transcode_jobs`
	countQuery := `SELECT COUNT(*) FROM transcode_jobs`
	var args []any
	var where []string

	filter := req.GetStatus()
	if filter != "" && filter != "all" {
		where = append(where, `status = ?`)
		args = append(args, filter)
	}
	if len(where) > 0 {
		clause := ` WHERE ` + strings.Join(where, ` AND `)
		query += clause
		countQuery += clause
	}

	var total int
	m.db.QueryRowContext(ctx, countQuery, args...).Scan(&total)
	query += ` ORDER BY created_at DESC LIMIT ? OFFSET ?`
	qargs := append(args, pageSize, offset)

	rows, err := m.db.QueryContext(ctx, query, qargs...)
	if err != nil {
		return nil, fmt.Errorf("query jobs: %w", err)
	}
	defer rows.Close()

	var jobs []*transcodev1.TranscodeJob
	for rows.Next() {
		j := scanJob(rows)
		if j != nil {
			jobs = append(jobs, j)
		}
	}
	return &transcodev1.ListJobsResponse{Jobs: jobs, Total: int32(total), Page: int32(page), PageSize: int32(pageSize)}, nil
}

func (m *Module) GetJob(ctx context.Context, req *transcodev1.GetJobRequest) (*transcodev1.GetJobResponse, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	row := m.db.QueryRow(`SELECT id, input_path, output_path, profile_id, profile_name, status, progress, fps, error, started_at, completed_at, created_at, updated_at FROM transcode_jobs WHERE id = ?`, req.GetJobId())
	job := scanJobRow(row)
	if job == nil {
		return nil, fmt.Errorf("job not found: %s", req.GetJobId())
	}
	return &transcodev1.GetJobResponse{Job: job}, nil
}

// ── Hardware Detection ─────────────────────────────────────────

func (m *Module) DetectHardware(ctx context.Context, req *transcodev1.DetectHardwareRequest) (*transcodev1.DetectHardwareResponse, error) {
	var devices []*transcodev1.HardwareDevice

	if hasNVENC() {
		devices = append(devices,
			&transcodev1.HardwareDevice{Name: "NVIDIA GPU", Type: "nvenc", Available: true, Encoder: "h264_nvenc"},
			&transcodev1.HardwareDevice{Name: "NVIDIA GPU", Type: "nvenc", Available: true, Encoder: "hevc_nvenc"},
			&transcodev1.HardwareDevice{Name: "NVIDIA GPU", Type: "nvenc", Available: true, Encoder: "av1_nvenc"},
		)
	}
	if hasVAAPI() {
		devices = append(devices,
			&transcodev1.HardwareDevice{Name: "VAAPI", Type: "vaapi", Available: true, Encoder: "h264_vaapi"},
			&transcodev1.HardwareDevice{Name: "VAAPI", Type: "vaapi", Available: true, Encoder: "hevc_vaapi"},
		)
	}

	slog.Info("hardware detection", "devices", len(devices))
	return &transcodev1.DetectHardwareResponse{Devices: devices}, nil
}

func hasNVENC() bool {
	cmd := exec.Command("ffmpeg", "-encoders", "-hide_banner")
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), "nvenc")
}

func hasVAAPI() bool {
	cmd := exec.Command("ffmpeg", "-encoders", "-hide_banner")
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), "vaapi")
}

// ── FFmpeg Execution ───────────────────────────────────────────

func (m *Module) runJob(jobID string, profile *transcodev1.TranscodeProfile) {
	select {
	case <-m.jobSlots:
	case <-time.After(24 * time.Hour):
		m.jobsMu.Lock()
		js := m.jobs[jobID]
		m.jobsMu.Unlock()
		if js != nil {
			m.failJob(js, "timed out waiting for job slot")
		}
		return
	}
	defer func() { m.jobSlots <- struct{}{} }()

	m.jobsMu.Lock()
	js, ok := m.jobs[jobID]
	m.jobsMu.Unlock()
	if !ok {
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	js.cancel = cancel
	js.running = true
	js.info.Status = "running"
	now := time.Now().UTC().Format(time.RFC3339)
	js.info.StartedAt = now

	m.mu.Lock()
	m.db.Exec(`UPDATE transcode_jobs SET status = 'running', started_at = ?, updated_at = ? WHERE id = ?`, now, now, jobID)
	m.mu.Unlock()

	args := buildFFmpegArgs(profile, js.info.InputPath, js.info.OutputPath)
	cmd := exec.CommandContext(ctx, "ffmpeg", args...)

	stderr, err := cmd.StderrPipe()
	if err != nil {
		m.failJob(js, fmt.Sprintf("pipe error: %v", err))
		return
	}

	if err := cmd.Start(); err != nil {
		m.failJob(js, fmt.Sprintf("start error: %v", err))
		return
	}

	m.monitorProgress(js, stderr)
	err = cmd.Wait()
	if err != nil {
		if ctx.Err() != nil {
			m.failJob(js, "cancelled")
		} else {
			m.failJob(js, fmt.Sprintf("ffmpeg error: %v", err))
		}
		return
	}

	m.completeJob(js)
}

func (m *Module) monitorProgress(js *jobState, stderr ioReadCloser) {
	scanner := bufio.NewScanner(stderr)
	reProgress := regexp.MustCompile(`time=(\d+):(\d+):(\d+)\.(\d+)`)
	reFPS := regexp.MustCompile(`fps=\s*(\d+\.?\d*)`)
	reDuration := regexp.MustCompile(`Duration: (\d+):(\d+):(\d+)\.(\d+)`)

	var durationSec float64

	for scanner.Scan() {
		line := scanner.Text()

		if d := reDuration.FindStringSubmatch(line); len(d) >= 5 {
			durationSec = parseTimeToSec(d[1], d[2], d[3], d[4])
		}
		if p := reProgress.FindStringSubmatch(line); len(p) >= 5 {
			currentSec := parseTimeToSec(p[1], p[2], p[3], p[4])
			if durationSec > 0 {
				progress := currentSec / durationSec
				if progress > 1.0 {
					progress = 1.0
				}
				js.info.Progress = progress
			}
		}
		if f := reFPS.FindStringSubmatch(line); len(f) >= 2 {
			if fps, err := strconv.ParseFloat(f[1], 64); err == nil {
				js.info.Fps = fps
			}
		}
	}
}

func (m *Module) failJob(js *jobState, errMsg string) {
	now := time.Now().UTC().Format(time.RFC3339)
	js.info.Status = "failed"
	js.info.Error = errMsg
	js.info.CompletedAt = now
	js.info.UpdatedAt = now
	js.running = false

	m.mu.Lock()
	m.db.Exec(`UPDATE transcode_jobs SET status = 'failed', error = ?, completed_at = ?, updated_at = ? WHERE id = ?`, errMsg, now, now, js.info.Id)
	m.mu.Unlock()

	slog.Error("transcode failed", "job", js.info.Id, "error", errMsg)
}

func (m *Module) completeJob(js *jobState) {
	now := time.Now().UTC().Format(time.RFC3339)
	js.info.Status = "completed"
	js.info.Progress = 1.0
	js.info.CompletedAt = now
	js.info.UpdatedAt = now
	js.running = false

	m.mu.Lock()
	m.db.Exec(`UPDATE transcode_jobs SET status = 'completed', progress = 1.0, completed_at = ?, updated_at = ? WHERE id = ?`, now, now, js.info.Id)
	m.mu.Unlock()

	slog.Info("transcode completed", "job", js.info.Id, "output", js.info.OutputPath)
}

func parseTimeToSec(h, m, s, ms string) float64 {
	hh, _ := strconv.ParseFloat(h, 64)
	mm, _ := strconv.ParseFloat(m, 64)
	ss, _ := strconv.ParseFloat(s, 64)
	mss, _ := strconv.ParseFloat(ms, 64)
	return hh*3600 + mm*60 + ss + mss/100
}

func buildFFmpegArgs(profile *transcodev1.TranscodeProfile, input, output string) []string {
	args := []string{"-i", input, "-y", "-progress", "pipe:1"}

	if profile.GetUseGpu() && hasNVENC() {
		switch profile.GetVideoCodec() {
		case "hevc":
			args = append(args, "-c:v", "hevc_nvenc")
		case "av1":
			args = append(args, "-c:v", "av1_nvenc")
		default:
			args = append(args, "-c:v", "h264_nvenc")
		}
		args = append(args, "-preset", nvencPreset(profile.GetPreset()))
		if profile.GetCrf() > 0 {
			args = append(args, "-cq", strconv.Itoa(int(profile.GetCrf())))
		}
	} else {
		switch profile.GetVideoCodec() {
		case "hevc":
			args = append(args, "-c:v", "libx265")
		case "av1":
			args = append(args, "-c:v", "libaom-av1")
		default:
			args = append(args, "-c:v", "libx264")
		}
		args = append(args, "-preset", profile.GetPreset())
		if profile.GetCrf() > 0 {
			args = append(args, "-crf", strconv.Itoa(int(profile.GetCrf())))
		}
	}

	if profile.GetMaxWidth() > 0 || profile.GetMaxHeight() > 0 {
		filter := fmt.Sprintf("scale='min(%d,iw)':min'(%d,ih)':force_original_aspect_ratio=decrease", profile.GetMaxWidth(), profile.GetMaxHeight())
		if profile.GetMaxWidth() == 0 {
			filter = fmt.Sprintf("scale='iw':min'(%d,ih)':force_original_aspect_ratio=decrease", profile.GetMaxHeight())
		}
		if profile.GetMaxHeight() == 0 {
			filter = fmt.Sprintf("scale='min'(%d,iw)':ih':force_original_aspect_ratio=decrease", profile.GetMaxWidth())
		}
		args = append(args, "-vf", filter)
	}

	audioCodec := profile.GetAudioCodec()
	if audioCodec == "" || audioCodec == "copy" {
		args = append(args, "-c:a", "copy")
	} else {
		args = append(args, "-c:a", audioCodec)
	}

	args = append(args, output)
	return args
}

func nvencPreset(preset string) string {
	switch preset {
	case "fast":
		return "p1"
	case "medium":
		return "p4"
	case "slow":
		return "p6"
	case "veryslow":
		return "p7"
	default:
		return "p4"
	}
}

// ── DB Load Helpers ────────────────────────────────────────────

func (m *Module) loadProfiles() []*transcodev1.TranscodeProfile {
	rows, err := m.db.Query(`SELECT id, name, video_codec, audio_codec, preset, crf, max_width, max_height, use_gpu, container, created_at, updated_at FROM transcode_profiles ORDER BY name`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var profiles []*transcodev1.TranscodeProfile
	for rows.Next() {
		p := scanProfile(rows)
		if p != nil {
			profiles = append(profiles, p)
		}
	}
	return profiles
}

func (m *Module) loadProfile(id string) *transcodev1.TranscodeProfile {
	row := m.db.QueryRow(`SELECT id, name, video_codec, audio_codec, preset, crf, max_width, max_height, use_gpu, container, created_at, updated_at FROM transcode_profiles WHERE id = ?`, id)
	return scanProfileRow(row)
}

func scanProfile(rows *sql.Rows) *transcodev1.TranscodeProfile {
	var id, name, vcodec, acodec, preset, container, createdAt, updatedAt string
	var crf, maxW, maxH int64
	var useGPU int
	if err := rows.Scan(&id, &name, &vcodec, &acodec, &preset, &crf, &maxW, &maxH, &useGPU, &container, &createdAt, &updatedAt); err != nil {
		return nil
	}
	return &transcodev1.TranscodeProfile{
		Id: id, Name: name, VideoCodec: vcodec, AudioCodec: acodec,
		Preset: preset, Crf: int32(crf), MaxWidth: int32(maxW), MaxHeight: int32(maxH),
		UseGpu: useGPU != 0, Container: container, CreatedAt: createdAt, UpdatedAt: updatedAt,
	}
}

func scanProfileRow(row *sql.Row) *transcodev1.TranscodeProfile {
	var id, name, vcodec, acodec, preset, container, createdAt, updatedAt string
	var crf, maxW, maxH int64
	var useGPU int
	if err := row.Scan(&id, &name, &vcodec, &acodec, &preset, &crf, &maxW, &maxH, &useGPU, &container, &createdAt, &updatedAt); err != nil {
		return nil
	}
	return &transcodev1.TranscodeProfile{
		Id: id, Name: name, VideoCodec: vcodec, AudioCodec: acodec,
		Preset: preset, Crf: int32(crf), MaxWidth: int32(maxW), MaxHeight: int32(maxH),
		UseGpu: useGPU != 0, Container: container, CreatedAt: createdAt, UpdatedAt: updatedAt,
	}
}

func scanJob(rows *sql.Rows) *transcodev1.TranscodeJob {
	var id, inPath, outPath, profID, profName, status, startedAt, completedAt, createdAt, updatedAt, errStr string
	var progress, fps float64
	if err := rows.Scan(&id, &inPath, &outPath, &profID, &profName, &status, &progress, &fps, &errStr, &startedAt, &completedAt, &createdAt, &updatedAt); err != nil {
		return nil
	}
	return &transcodev1.TranscodeJob{
		Id: id, InputPath: inPath, OutputPath: outPath,
		ProfileId: profID, ProfileName: profName,
		Status: status, Progress: progress, Fps: fps, Error: errStr,
		StartedAt: startedAt, CompletedAt: completedAt,
		CreatedAt: createdAt, UpdatedAt: updatedAt,
	}
}

func scanJobRow(row *sql.Row) *transcodev1.TranscodeJob {
	var id, inPath, outPath, profID, profName, status, startedAt, completedAt, createdAt, updatedAt, errStr string
	var progress, fps float64
	if err := row.Scan(&id, &inPath, &outPath, &profID, &profName, &status, &progress, &fps, &errStr, &startedAt, &completedAt, &createdAt, &updatedAt); err != nil {
		return nil
	}
	return &transcodev1.TranscodeJob{
		Id: id, InputPath: inPath, OutputPath: outPath,
		ProfileId: profID, ProfileName: profName,
		Status: status, Progress: progress, Fps: fps, Error: errStr,
		StartedAt: startedAt, CompletedAt: completedAt,
		CreatedAt: createdAt, UpdatedAt: updatedAt,
	}
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// ── Interface types for testability ────────────────────────────

type ioReadCloser = ioReadCloserImpl

type ioReadCloserImpl interface {
	Read([]byte) (int, error)
	Close() error
}

var _ contracts.Module = (*Module)(nil)
