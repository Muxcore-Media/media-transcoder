package internal

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/Muxcore-Media/core/pkg/contracts"
	"github.com/Muxcore-Media/core/sdk/go/client"
	eventsv1 "github.com/Muxcore-Media/core/proto/gen/muxcore/events/v1"
	transcodev1 "github.com/Muxcore-Media/media-transcoder/proto/transcodev1"
)

func (m *Module) dialCore(ctx context.Context) {
	meshAddr := os.Getenv("MUXCORE_GRPC_ADDR")
	if meshAddr == "" {
		meshAddr = "localhost:9090"
	}
	insecureMode := os.Getenv("MUXCORE_INSECURE_DISABLE_TLS") == "true" || os.Getenv("MUXCORE_GRPC_INSECURE") == "true"
	var opts []client.Option
	if insecureMode {
		opts = append(opts, client.WithInsecure())
	}
	c, err := client.Dial(meshAddr, opts...)
	if err != nil {
		slog.Warn("media-transcoder: dial core", "error", err)
		return
	}
	m.mc = c
	slog.Info("media-transcoder: connected to core mesh", "addr", meshAddr)
	m.subscribeLibraryEvents()
}

func (m *Module) subscribeLibraryEvents() {
	if m.mc == nil {
		return
	}
	types := []string{
		contracts.EventMovieFileAdded,
		contracts.EventTVEpisodeFileAdded,
		contracts.EventFileImported,
	}
	for _, et := range types {
		ch, cancel, err := m.mc.Events.Subscribe(context.Background(), et)
		if err != nil {
			slog.Warn("media-transcoder: subscribe", "type", et, "error", err)
			continue
		}
		go m.handleEventStream(et, ch, cancel)
		slog.Info("media-transcoder: subscribed", "type", et)
	}
}

func (m *Module) handleEventStream(eventType string, ch <-chan *eventsv1.Event, cancel context.CancelFunc) {
	defer cancel()
	for evt := range ch {
		path := eventFilePath(eventType, evt.GetPayload())
		if path == "" {
			continue
		}
		if !isVideoFile(path) {
			continue
		}
		m.mu.RLock()
		setups, err := m.matchSetupsForPath(path)
		m.mu.RUnlock()
		if err != nil {
			continue
		}
		for _, setup := range setups {
			if setup.GetTrigger() != "on_import" {
				continue
			}
			if m.hasCompletedRun(setup.GetId(), path) {
				continue
			}
			_, _, _, err := m.startPipelineRun(setup, path)
			if err != nil {
				slog.Warn("auto pipeline", "setup", setup.GetId(), "path", path, "error", err)
			}
		}
	}
}

func eventFilePath(eventType string, payload []byte) string {
	switch eventType {
	case contracts.EventMovieFileAdded:
		var p contracts.MovieFileAddedPayload
		if json.Unmarshal(payload, &p) == nil {
			return filepath.Clean(p.FilePath)
		}
	case contracts.EventTVEpisodeFileAdded:
		var p contracts.TVEpisodeFileAddedPayload
		if json.Unmarshal(payload, &p) == nil {
			return filepath.Clean(p.FilePath)
		}
	case contracts.EventFileImported:
		var p contracts.FileImportedPayload
		if json.Unmarshal(payload, &p) == nil {
			if p.DestinationPath != "" {
				return filepath.Clean(p.DestinationPath)
			}
			return filepath.Clean(p.OriginalPath)
		}
	}
	return ""
}

func (m *Module) scheduledScanLoop(ctx context.Context) {
	interval := 6 * time.Hour
	if v := os.Getenv("TRANSCODER_SCAN_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d >= time.Minute {
			interval = d
		}
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			scanCtx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
			_, err := m.ScanSetups(scanCtx, &transcodev1.ScanSetupsRequest{})
			cancel()
			if err != nil {
				slog.Warn("scheduled scan", "error", err)
			}
		}
	}
}
