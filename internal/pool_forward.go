package internal

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	transcoderpoolv1 "github.com/Muxcore-Media/media-transcoder-pool/proto/gen/muxcore/transcoderpool/v1"
	transcodev1 "github.com/Muxcore-Media/media-transcoder/proto/transcodev1"
)

func (m *Module) poolEnabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("TRANSCODER_USE_POOL")))
	return v == "1" || v == "true" || v == "yes"
}

func (m *Module) poolAddr(ctx context.Context) (string, error) {
	if v := strings.TrimSpace(os.Getenv("TRANSCODER_POOL_ADDR")); v != "" {
		return v, nil
	}
	if m.mc == nil {
		return "", fmt.Errorf("pool addr not configured")
	}
	for _, cap := range []string{"transcoder.pool", "media.transcoder.pool"} {
		mods, err := m.mc.Discovery.FindByCapability(ctx, cap)
		if err != nil {
			continue
		}
		for _, mod := range mods {
			if mod.GetHttpAddr() != "" {
				return mod.GetHttpAddr(), nil
			}
		}
	}
	return "", fmt.Errorf("no transcoder pool module found")
}

func (m *Module) enqueueViaPool(ctx context.Context, req *transcodev1.EnqueueRequest, profile *transcodev1.TranscodeProfile) (string, error) {
	addr, err := m.poolAddr(ctx)
	if err != nil {
		return "", err
	}
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return "", err
	}
	defer func() { _ = conn.Close() }()
	client := transcoderpoolv1.NewTranscoderPoolServiceClient(conn)
	resp, err := client.Enqueue(ctx, &transcoderpoolv1.EnqueueRequest{
		InputPath:  req.GetInputPath(),
		OutputPath: req.GetOutputPath(),
		Profile:    profile.GetId(),
		PreferGpu:  profile.GetUseGpu(),
	})
	if err != nil {
		return "", err
	}
	if resp.GetJob() == nil || resp.GetJob().GetId() == "" {
		return "", fmt.Errorf("pool returned empty job")
	}
	return resp.GetJob().GetId(), nil
}

func (m *Module) waitForPoolJob(ctx context.Context, poolJobID, localJobID string) {
	addr, err := m.poolAddr(ctx)
	if err != nil {
		m.markPoolJobFailed(ctx, localJobID, err.Error())
		return
	}
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		m.markPoolJobFailed(ctx, localJobID, err.Error())
		return
	}
	defer func() { _ = conn.Close() }()
	client := transcoderpoolv1.NewTranscoderPoolServiceClient(conn)

	deadline := time.Now().Add(48 * time.Hour)
	for time.Now().Before(deadline) {
		resp, err := client.GetJob(ctx, &transcoderpoolv1.GetJobRequest{Id: poolJobID})
		if err != nil {
			m.markPoolJobFailed(ctx, localJobID, err.Error())
			return
		}
		job := resp.GetJob()
		if job == nil {
			m.markPoolJobFailed(ctx, localJobID, "pool job missing")
			return
		}
		switch job.GetStatus() {
		case "done":
			m.markPoolJobComplete(ctx, localJobID)
			return
		case "failed", "cancelled":
			msg := job.GetError()
			if msg == "" {
				msg = job.GetStatus()
			}
			m.markPoolJobFailed(ctx, localJobID, msg)
			return
		case "running", "assigned":
			m.markPoolJobProgress(ctx, localJobID, 0.5)
		}
		time.Sleep(2 * time.Second)
	}
	m.markPoolJobFailed(ctx, localJobID, "timed out waiting for pool job")
}

func (m *Module) markPoolJobComplete(ctx context.Context, jobID string) {
	now := time.Now().UTC().Format(time.RFC3339)
	m.mu.Lock()
	_, _ = m.db.ExecContext(ctx, `UPDATE transcode_jobs SET status = 'completed', progress = 1, completed_at = ?, updated_at = ? WHERE id = ?`, now, now, jobID)
	m.mu.Unlock()
}

func (m *Module) markPoolJobFailed(ctx context.Context, jobID, msg string) {
	now := time.Now().UTC().Format(time.RFC3339)
	m.mu.Lock()
	_, _ = m.db.ExecContext(ctx, `UPDATE transcode_jobs SET status = 'failed', error = ?, completed_at = ?, updated_at = ? WHERE id = ?`, msg, now, now, jobID)
	m.mu.Unlock()
}

func (m *Module) markPoolJobProgress(ctx context.Context, jobID string, progress float64) {
	m.mu.Lock()
	_, _ = m.db.ExecContext(ctx, `UPDATE transcode_jobs SET status = 'running', progress = ?, updated_at = ? WHERE id = ?`, progress, time.Now().UTC().Format(time.RFC3339), jobID)
	m.mu.Unlock()
}
