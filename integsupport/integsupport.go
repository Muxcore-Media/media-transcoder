// Package integsupport exposes media-transcoder internals to umbrella integration tests.
package integsupport

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/Muxcore-Media/media-transcoder/internal"
)

// Module is the media-transcoder module; its gRPC handler methods (Enqueue,
// GetJob, ListJobs, CancelJob, UpsertSetup, ListProfiles, ListPipelineRuns)
// and Health are available directly.
type Module = internal.Module

// Config configures a test module. Zero values select temp-dir and loopback defaults.
type Config = internal.Config

// NewTestModule builds and initialises a Module backed by a temp SQLite DB and a
// loopback gRPC listener (127.0.0.1:0). The playback HTTP server is also bound to
// loopback unless TRANSCODER_HTTP_ADDR is already set. Stop is registered via t.Cleanup.
func NewTestModule(t *testing.T, cfg Config) *Module {
	t.Helper()
	if cfg.DBPath == "" {
		cfg.DBPath = filepath.Join(t.TempDir(), "transcoder.db")
	}
	if cfg.GRPCAddr == "" {
		cfg.GRPCAddr = "127.0.0.1:0"
	}
	t.Setenv("TRANSCODER_DB_PATH", cfg.DBPath)
	t.Setenv("TRANSCODER_GRPC_ADDR", cfg.GRPCAddr)
	t.Setenv("TRANSCODER_HTTP_ADDR", "127.0.0.1:0")
	m := internal.NewModule(cfg)
	if err := m.Init(context.Background()); err != nil {
		t.Fatalf("integsupport: init media-transcoder: %v", err)
	}
	t.Cleanup(func() { _ = m.Stop(context.Background()) })
	return m
}

// Start serves gRPC and playback HTTP and dials the core mesh (MUXCORE_GRPC_ADDR).
func Start(ctx context.Context, m *Module) error {
	return m.Start(ctx)
}
