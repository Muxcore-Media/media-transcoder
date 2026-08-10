package internal

import (
	"fmt"
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
	return []contracts.SettingDef{
		{
			Key:         "ffmpeg_bin",
			Label:       "FFmpeg Binary",
			Type:        contracts.SettingTypeString,
			Value:       m.getFFmpegBin(),
			Description: "Path or name of ffmpeg executable (TRANSCODER_FFMPEG_BIN)",
			Group:       "Transcode",
		},
	}
}

func (m *Module) updateSetting(key, value string) error {
	value = strings.TrimSpace(value)
	switch key {
	case "ffmpeg_bin", "TRANSCODER_FFMPEG_BIN":
		if value == "" {
			return fmt.Errorf("ffmpeg_bin must not be empty")
		}
		m.cfgMu.Lock()
		m.ffmpegBin = value
		m.cfgMu.Unlock()
		return nil
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
