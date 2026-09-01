package internal

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

func (m *Module) ensureSettingsTable(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS transcode_settings (
			key   TEXT PRIMARY KEY,
			value TEXT NOT NULL
		)`)
	return err
}

func (m *Module) loadSettings(ctx context.Context) error {
	if m.db == nil {
		return nil
	}
	rows, err := m.db.QueryContext(ctx, `SELECT key, value FROM transcode_settings`)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return err
		}
		if err := m.applySetting(ctx, key, value, false); err != nil {
			return err
		}
	}
	return rows.Err()
}

func (m *Module) persistSetting(ctx context.Context, key, value string) error {
	_, err := m.db.ExecContext(ctx, `
		INSERT INTO transcode_settings (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

func (m *Module) applySetting(ctx context.Context, key, value string, persist bool) error {
	key = strings.TrimSpace(key)
	value = strings.TrimSpace(value)
	switch key {
	case "ffmpeg_bin", "TRANSCODER_FFMPEG_BIN":
		if value == "" {
			return fmt.Errorf("ffmpeg_bin must not be empty")
		}
		m.cfgMu.Lock()
		m.ffmpegBin = value
		m.cfgMu.Unlock()
		if persist {
			return m.persistSetting(ctx, "ffmpeg_bin", value)
		}
	case "max_concurrent", "TRANSCODER_MAX_CONCURRENT":
		n, err := strconv.Atoi(value)
		if err != nil || n < 1 {
			return fmt.Errorf("max_concurrent must be a positive integer")
		}
		m.cfgMu.Lock()
		m.maxConcurrent = n
		m.cfgMu.Unlock()
		if persist {
			return m.persistSetting(ctx, "max_concurrent", value)
		}
	case "scan_interval", "TRANSCODER_SCAN_INTERVAL":
		if _, err := time.ParseDuration(value); err != nil {
			return fmt.Errorf("scan_interval must be a valid duration")
		}
		m.cfgMu.Lock()
		m.scanInterval = value
		m.cfgMu.Unlock()
		if persist {
			return m.persistSetting(ctx, "scan_interval", value)
		}
	default:
		return fmt.Errorf("unknown setting %q", key)
	}
	return nil
}

func (m *Module) getScanInterval() time.Duration {
	m.cfgMu.RLock()
	raw := m.scanInterval
	m.cfgMu.RUnlock()
	if raw != "" {
		if d, err := time.ParseDuration(raw); err == nil && d >= time.Minute {
			return d
		}
	}
	if v := strings.TrimSpace(os.Getenv("TRANSCODER_SCAN_INTERVAL")); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d >= time.Minute {
			return d
		}
	}
	return 6 * time.Hour
}
