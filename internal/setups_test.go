package internal

import (
	"context"
	"path/filepath"
	"testing"

	transcodev1 "github.com/Muxcore-Media/media-transcoder/proto/transcodev1"
)

func TestDefaultSetupSeeded(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()
	resp, err := m.ListSetups(ctx, &transcodev1.ListSetupsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.GetSetups()) == 0 {
		t.Fatal("expected default setup")
	}
	s := resp.GetSetups()[0]
	if s.GetSourceDisposition() != "keep" {
		t.Fatalf("expected keep disposition, got %q", s.GetSourceDisposition())
	}
	if len(s.GetOutputs()) == 0 {
		t.Fatal("expected default output")
	}
}

func TestUpsertSetupMultiOutput(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()

	created, err := m.UpsertSetup(ctx, &transcodev1.UpsertSetupRequest{Setup: &transcodev1.TranscodeSetup{
		Name:              "Dual output",
		Enabled:           true,
		LibraryPaths:      []string{"/media/movies"},
		Trigger:           "manual",
		SourceDisposition: "delete",
		Outputs: []*transcodev1.SetupOutput{
			{ProfileId: "h264_fast", Suffix: "-720p", ReplaceExtension: true, Enabled: true},
			{ProfileId: "hevc_gpu", Suffix: "-hevc", ReplaceExtension: true, Enabled: true},
		},
		Steps: []*transcodev1.PipelineStep{
			{StepType: "filter.extension", ConfigJson: `{"extensions":["mkv","mp4"]}`, Enabled: true},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if created.GetSetup().GetId() == "" {
		t.Fatal("expected setup id")
	}
	if len(created.GetSetup().GetOutputs()) != 2 {
		t.Fatalf("expected 2 outputs, got %d", len(created.GetSetup().GetOutputs()))
	}
}

func TestComputeOutputPath(t *testing.T) {
	profile := &transcodev1.TranscodeProfile{Container: "mkv"}
	out := &transcodev1.SetupOutput{Suffix: "-hevc", ReplaceExtension: true}
	got := computeOutputPath("/data/Foo/Foo.mkv", out, profile)
	want := filepath.Join("/data/Foo", "Foo-hevc.mkv")
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestFilterSkipIfCodec(t *testing.T) {
	m := newTestModule(t)
	info := &probeInfo{VideoCodec: "hevc"}
	step := &transcodev1.PipelineStep{
		StepType:   "filter.skip_if_codec",
		ConfigJson: `{"codecs":["hevc","h265"]}`,
		Enabled:    true,
	}
	skip, reason, err := m.evalStep("/x.mkv", info, step)
	if err != nil {
		t.Fatal(err)
	}
	if !skip || reason == "" {
		t.Fatalf("expected skip, got skip=%v reason=%q", skip, reason)
	}
}

func TestMatchSetupsForPath(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()
	_, err := m.UpsertSetup(ctx, &transcodev1.UpsertSetupRequest{Setup: &transcodev1.TranscodeSetup{
		Name:         "Movies only",
		Enabled:      true,
		LibraryPaths: []string{"/data/movies"},
		Trigger:      "on_import",
		Outputs:      []*transcodev1.SetupOutput{{ProfileId: "h264_fast", Enabled: true}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	m.mu.RLock()
	matched, err := m.matchSetupsForPath("/data/movies/Fight Club/Fight.Club.mkv")
	m.mu.RUnlock()
	if err != nil {
		t.Fatal(err)
	}
	if len(matched) == 0 {
		t.Fatal("expected path match")
	}
}

func TestProcessFileMissingInput(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()
	setups, _ := m.ListSetups(ctx, &transcodev1.ListSetupsRequest{})
	if len(setups.GetSetups()) == 0 {
		t.Fatal("no setups")
	}
	_, err := m.ProcessFile(ctx, &transcodev1.ProcessFileRequest{
		SetupId:   setups.GetSetups()[0].GetId(),
		InputPath: "/no/such/file.mkv",
	})
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}
