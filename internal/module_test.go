package internal

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	transcodev1 "github.com/Muxcore-Media/media-transcoder/proto/transcodev1"
)

func newTestModule(t *testing.T) *Module {
	t.Helper()
	m := NewModule(Config{
		DBPath:   filepath.Join(t.TempDir(), "transcoder.db"),
		GRPCAddr: ":0",
	})
	ctx := context.Background()
	if err := m.Init(ctx); err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { m.Stop(ctx) })
	return m
}

func TestModuleInfo(t *testing.T) {
	m := NewModule(Config{})
	info := m.Info()
	if info.ID == "" {
		t.Error("module ID must not be empty")
	}
	if info.Version == "" {
		t.Error("module version must not be empty")
	}
}

func TestDefaultProfiles(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()

	profiles, err := m.ListProfiles(ctx, &transcodev1.ListProfilesRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(profiles.Profiles) < 2 {
		t.Errorf("expected at least 2 default profiles, got %d", len(profiles.Profiles))
	}
}

func TestCreateProfile(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()

	created, err := m.CreateProfile(ctx, &transcodev1.CreateProfileRequest{
		Name:       "Test 4K HEVC",
		VideoCodec: "hevc",
		AudioCodec: "copy",
		Preset:     "slow",
		Crf:        22,
		MaxHeight:  2160,
		Container:  "mkv",
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.Profile.Name != "Test 4K HEVC" {
		t.Errorf("expected Test 4K HEVC, got %s", created.Profile.Name)
	}
	if created.Profile.Crf != 22 {
		t.Errorf("expected crf 22, got %d", created.Profile.Crf)
	}
}

func TestUpdateProfile(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()

	created, _ := m.CreateProfile(ctx, &transcodev1.CreateProfileRequest{
		Name: "Original", VideoCodec: "h264",
	})
	updated, err := m.UpdateProfile(ctx, &transcodev1.UpdateProfileRequest{
		Id: created.Profile.Id, Name: "Updated", VideoCodec: "hevc", Preset: "fast",
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Profile.Name != "Updated" {
		t.Errorf("expected Updated, got %s", updated.Profile.Name)
	}
	if updated.Profile.VideoCodec != "hevc" {
		t.Errorf("expected hevc, got %s", updated.Profile.VideoCodec)
	}
}

func TestDeleteProfile(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()

	created, _ := m.CreateProfile(ctx, &transcodev1.CreateProfileRequest{Name: "Test"})
	before, _ := m.ListProfiles(ctx, &transcodev1.ListProfilesRequest{})
	m.DeleteProfile(ctx, &transcodev1.DeleteProfileRequest{Id: created.Profile.Id})
	after, _ := m.ListProfiles(ctx, &transcodev1.ListProfilesRequest{})

	if len(after.Profiles) != len(before.Profiles)-1 {
		t.Errorf("expected 1 fewer profile, got %d", len(after.Profiles))
	}
}

func TestEnqueueMissingFile(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()

	_, err := m.Enqueue(ctx, &transcodev1.EnqueueRequest{
		InputPath:  "/nonexistent/file.mkv",
		OutputPath: "/tmp/out.mkv",
		ProfileId:  "h264_fast",
	})
	if err == nil {
		t.Fatal("expected error for nonexistent input")
	}
}

func TestEnqueueMissingProfile(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()

	_, err := m.Enqueue(ctx, &transcodev1.EnqueueRequest{
		InputPath:  filepath.Join(t.TempDir(), "test.mkv"),
		OutputPath: "/tmp/out.mkv",
		ProfileId:  "nonexistent",
	})
	if err == nil {
		t.Fatal("expected error for nonexistent profile")
	}
}

func TestListJobsEmpty(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()

	jobs, err := m.ListJobs(ctx, &transcodev1.ListJobsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if jobs.Total != 0 {
		t.Errorf("expected 0 jobs, got %d", jobs.Total)
	}
}

func TestCancelJobNotFound(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()

	_, err := m.CancelJob(ctx, &transcodev1.CancelJobRequest{JobId: "nonexistent"})
	if err == nil {
		t.Fatal("expected error for nonexistent job")
	}
}

func TestDetectHardware(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()

	hw, err := m.DetectHardware(ctx, &transcodev1.DetectHardwareRequest{})
	if err != nil {
		t.Fatal(err)
	}
	_ = hw.Devices // May be empty; just shouldn't error
}

func TestBuildFFmpegArgs(t *testing.T) {
	m := NewModule(Config{})
	profile := &transcodev1.TranscodeProfile{
		Name: "Test", VideoCodec: "h264", AudioCodec: "copy",
		Preset: "medium", Crf: 23, Container: "mkv",
	}
	args := m.buildFFmpegArgs(profile, "/in.mkv", "/out.mkv")
	if len(args) == 0 {
		t.Fatal("expected non-empty args")
	}

	hasInput := false
	hasOutput := false
	for _, a := range args {
		if a == "/in.mkv" {
			hasInput = true
		}
		if a == "/out.mkv" {
			hasOutput = true
		}
	}
	if !hasInput {
		t.Error("expected input path in args")
	}
	if !hasOutput {
		t.Error("expected output path in args")
	}
}

func TestParseTimeToSec(t *testing.T) {
	tests := []struct {
		h, m, s, ms string
		want        float64
	}{
		{"0", "0", "10", "0", 10},
		{"1", "30", "0", "0", 5400},
		{"0", "0", "0", "500", 5},
	}
	for _, tt := range tests {
		got := parseTimeToSec(tt.h, tt.m, tt.s, tt.ms)
		if got != tt.want {
			t.Errorf("parseTimeToSec(%q,%q,%q,%q) = %v, want %v", tt.h, tt.m, tt.s, tt.ms, got, tt.want)
		}
	}
}

func TestNVENCPreset(t *testing.T) {
	tests := []struct {
		in, out string
	}{
		{"fast", "p1"},
		{"medium", "p4"},
		{"slow", "p6"},
		{"veryslow", "p7"},
		{"unknown", "p4"},
	}
	for _, tt := range tests {
		got := nvencPreset(tt.in)
		if got != tt.out {
			t.Errorf("nvencPreset(%q) = %q, want %q", tt.in, got, tt.out)
		}
	}
}

func TestLifecycle(t *testing.T) {
	m := NewModule(Config{
		DBPath:   filepath.Join(t.TempDir(), "lifecycle.db"),
		GRPCAddr: ":0",
	})
	ctx := context.Background()
	if err := m.Init(ctx); err != nil {
		t.Fatal(err)
	}
	if err := m.Start(ctx); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if err := m.Stop(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestHealth(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()
	err := m.Health(ctx)
	if err != nil && !strings.Contains(err.Error(), "ffmpeg") {
		t.Fatalf("unexpected health error: %v", err)
	}
}

func TestDefaultProfileContainerIsMKV(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()
	profiles, err := m.ListProfiles(ctx, &transcodev1.ListProfilesRequest{})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range profiles.Profiles {
		if p.Id == "h264_fast" && p.Container != "mkv" {
			t.Fatalf("h264_fast container=%q want mkv", p.Container)
		}
	}
}

func TestCapabilitiesIncludeWorkflowRef(t *testing.T) {
	info := NewModule(Config{}).Info()
	found := false
	for _, c := range info.Capabilities {
		if c == "transcoder" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected transcoder capability for media-transcode DAG, got %v", info.Capabilities)
	}
	if info.HTTPAddr != ":9525" {
		t.Fatalf("default addr=%q want :9525", info.HTTPAddr)
	}
}

func TestMaxConcurrentFromEnv(t *testing.T) {
	t.Setenv("TRANSCODER_MAX_CONCURRENT", "3")
	m := NewModule(Config{DBPath: filepath.Join(t.TempDir(), "c.db"), GRPCAddr: ":0"})
	if m.maxConcurrent != 3 {
		t.Fatalf("maxConcurrent=%d want 3", m.maxConcurrent)
	}
	if cap(m.jobSlots) != 3 {
		t.Fatalf("jobSlots cap=%d want 3", cap(m.jobSlots))
	}
}
