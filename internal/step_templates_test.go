package internal

import (
	"context"
	"testing"

	transcodev1 "github.com/Muxcore-Media/media-transcoder/proto/transcodev1"
)

func TestListStepTemplates(t *testing.T) {
	templates, err := loadStepTemplates()
	if err != nil {
		t.Fatal(err)
	}
	if len(templates) < 2 {
		t.Fatalf("expected community pack templates, got %d", len(templates))
	}
}

func TestApplyStepTemplate(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()

	created, err := m.UpsertSetup(ctx, &transcodev1.UpsertSetupRequest{Setup: &transcodev1.TranscodeSetup{
		Name: "Template Target", Enabled: true, LibraryPaths: []string{"/media"},
		Outputs: []*transcodev1.SetupOutput{{ProfileId: "h264_fast", Suffix: "-test", Enabled: true}},
	}})
	if err != nil {
		t.Fatal(err)
	}

	resp, err := m.ApplyStepTemplate(ctx, &transcodev1.ApplyStepTemplateRequest{
		SetupId: created.GetSetup().GetId(), TemplateId: "skip_hevc",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.GetSetup().GetSteps()) == 0 {
		t.Fatal("expected steps merged into setup")
	}
}
