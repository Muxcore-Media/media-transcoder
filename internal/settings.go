package internal

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/Muxcore-Media/core/pkg/contracts"
)

func (m *Module) Settings() []contracts.SettingDef {
	return m.settingsDefs()
}

func (m *Module) UpdateSetting(key, value string) error {
	return m.updateSetting(key, value)
}

func (m *Module) settingsDefs() []contracts.SettingDef {
	m.cfgMu.RLock()
	maxConcurrent := m.maxConcurrent
	scanInterval := m.scanInterval
	m.cfgMu.RUnlock()
	if scanInterval == "" {
		scanInterval = os.Getenv("TRANSCODER_SCAN_INTERVAL")
	}
	if scanInterval == "" {
		scanInterval = "6h"
	}
	return []contracts.SettingDef{
		{
			Key:         "ffmpeg_bin",
			Label:       "FFmpeg Binary",
			Type:        contracts.SettingTypeString,
			Value:       m.getFFmpegBin(),
			Description: "Path or name of ffmpeg executable (TRANSCODER_FFMPEG_BIN)",
			Group:       "Transcode",
		},
		{
			Key:         "max_concurrent",
			Label:       "Max Concurrent Jobs",
			Type:        contracts.SettingTypeString,
			Value:       strconv.Itoa(maxConcurrent),
			Description: "Maximum simultaneous FFmpeg transcode jobs (TRANSCODER_MAX_CONCURRENT)",
			Group:       "Transcode",
		},
		{
			Key:         "scan_interval",
			Label:       "Library Scan Interval",
			Type:        contracts.SettingTypeString,
			Value:       scanInterval,
			Description: "Duration between scheduled setup scans (TRANSCODER_SCAN_INTERVAL)",
			Group:       "Transcode",
		},
	}
}

func (m *Module) updateSetting(key, value string) error {
	value = strings.TrimSpace(value)
	switch key {
	case "ffmpeg_bin", "TRANSCODER_FFMPEG_BIN":
		return m.applySetting(context.Background(), "ffmpeg_bin", value, true)
	case "max_concurrent", "TRANSCODER_MAX_CONCURRENT":
		return m.applySetting(context.Background(), "max_concurrent", value, true)
	case "scan_interval", "TRANSCODER_SCAN_INTERVAL":
		return m.applySetting(context.Background(), "scan_interval", value, true)
	default:
		return fmt.Errorf("unknown setting %q", key)
	}
}

func (m *Module) getFFmpegBin() string {
	m.cfgMu.RLock()
	defer m.cfgMu.RUnlock()
	if m.ffmpegBin == "" {
		return "ffmpeg"
	}
	return m.ffmpegBin
}
