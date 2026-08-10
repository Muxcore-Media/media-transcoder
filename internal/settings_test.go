package internal

import "testing"

func TestSettingsFFmpegBin(t *testing.T) {
	m := NewModule(Config{})
	if got := m.getFFmpegBin(); got != "ffmpeg" {
		t.Fatalf("default=%q", got)
	}
	if err := m.UpdateSetting("ffmpeg_bin", "/usr/bin/ffmpeg"); err != nil {
		t.Fatal(err)
	}
	if got := m.getFFmpegBin(); got != "/usr/bin/ffmpeg" {
		t.Fatalf("got=%q", got)
	}
	if err := m.UpdateSetting("unknown", "x"); err == nil {
		t.Fatal("expected error")
	}
}
