package integsupport_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	transcodev1 "github.com/Muxcore-Media/media-transcoder/proto/transcodev1"

	"github.com/Muxcore-Media/media-transcoder/integsupport"
)

func TestNewTestModule(t *testing.T) {
	ctx := context.Background()
	m := integsupport.NewTestModule(t, integsupport.Config{})

	if err := m.Health(ctx); err != nil && !strings.Contains(err.Error(), "ffmpeg") {
		t.Fatalf("health: %v", err)
	}
	profiles, err := m.ListProfiles(ctx, &transcodev1.ListProfilesRequest{})
	if err != nil {
		t.Fatalf("list profiles: %v", err)
	}
	if len(profiles.GetProfiles()) == 0 {
		t.Fatal("expected default profiles")
	}

	dir := t.TempDir()
	in := filepath.Join(dir, "in.mkv")
	if err := os.WriteFile(in, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	enq, err := m.Enqueue(ctx, &transcodev1.EnqueueRequest{InputPath: in, OutputPath: filepath.Join(dir, "out.mkv"), ProfileId: "h264_fast"})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if _, err := m.GetJob(ctx, &transcodev1.GetJobRequest{JobId: enq.GetJobId()}); err != nil {
		t.Fatalf("get job: %v", err)
	}
	if _, err := m.CancelJob(ctx, &transcodev1.CancelJobRequest{JobId: enq.GetJobId()}); err != nil {
		t.Fatalf("cancel: %v", err)
	}
}

func TestStartLoopback(t *testing.T) {
	m := integsupport.NewTestModule(t, integsupport.Config{})
	t.Setenv("MUXCORE_GRPC_ADDR", "127.0.0.1:1")
	if err := integsupport.Start(context.Background(), m); err != nil {
		t.Fatalf("start: %v", err)
	}
	if !strings.HasPrefix(m.GRPCListenAddr(), "127.0.0.1:") {
		t.Fatalf("unexpected listen addr %q", m.GRPCListenAddr())
	}
}

func TestCancelReachesCancelled(t *testing.T) {
	ctx := context.Background()
	m := integsupport.NewTestModule(t, integsupport.Config{})
	dir := t.TempDir()
	in := filepath.Join(dir, "in.mkv")
	if err := os.WriteFile(in, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	enq, err := m.Enqueue(ctx, &transcodev1.EnqueueRequest{InputPath: in, OutputPath: filepath.Join(dir, "out.mkv"), ProfileId: "h264_fast"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.CancelJob(ctx, &transcodev1.CancelJobRequest{JobId: enq.GetJobId()}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if j, gerr := m.GetJob(ctx, &transcodev1.GetJobRequest{JobId: enq.GetJobId()}); gerr == nil && j.GetJob().GetStatus() == "cancelled" {
			time.Sleep(100 * time.Millisecond) // a late failJob must not overwrite it
			j, _ = m.GetJob(ctx, &transcodev1.GetJobRequest{JobId: enq.GetJobId()})
			if j.GetJob().GetStatus() != "cancelled" {
				t.Fatalf("status overwritten: %s", j.GetJob().GetStatus())
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("job not cancelled")
}
