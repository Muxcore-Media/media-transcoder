package internal

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"

	transcodev1 "github.com/Muxcore-Media/media-transcoder/proto/transcodev1"
)

//go:embed templates/community_pack.json
var communityPackJSON []byte

type stepTemplateDoc struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Steps       []struct {
		StepType   string `json:"step_type"`
		ConfigJSON string `json:"config_json"`
		SortOrder  int32  `json:"sort_order"`
		Enabled    bool   `json:"enabled"`
	} `json:"steps"`
}

func loadStepTemplates() ([]*transcodev1.StepTemplate, error) {
	var docs []stepTemplateDoc
	if err := json.Unmarshal(communityPackJSON, &docs); err != nil {
		return nil, err
	}
	out := make([]*transcodev1.StepTemplate, 0, len(docs))
	for _, doc := range docs {
		t := &transcodev1.StepTemplate{
			Id: doc.ID, Name: doc.Name, Description: doc.Description,
		}
		for i, s := range doc.Steps {
			order := s.SortOrder
			if order == 0 {
				order = int32(i)
			}
			t.Steps = append(t.Steps, &transcodev1.PipelineStep{
				StepType: s.StepType, ConfigJson: s.ConfigJSON,
				SortOrder: order, Enabled: s.Enabled || s.StepType != "",
			})
		}
		out = append(out, t)
	}
	return out, nil
}

func (m *Module) ListStepTemplates(ctx context.Context, _ *transcodev1.ListStepTemplatesRequest) (*transcodev1.ListStepTemplatesResponse, error) {
	templates, err := loadStepTemplates()
	if err != nil {
		return nil, err
	}
	return &transcodev1.ListStepTemplatesResponse{Templates: templates}, nil
}

func (m *Module) ApplyStepTemplate(ctx context.Context, req *transcodev1.ApplyStepTemplateRequest) (*transcodev1.ApplyStepTemplateResponse, error) {
	setupID := strings.TrimSpace(req.GetSetupId())
	templateID := strings.TrimSpace(req.GetTemplateId())
	if setupID == "" || templateID == "" {
		return nil, fmt.Errorf("setup_id and template_id are required")
	}
	templates, err := loadStepTemplates()
	if err != nil {
		return nil, err
	}
	var picked *transcodev1.StepTemplate
	for _, t := range templates {
		if t.GetId() == templateID {
			picked = t
			break
		}
	}
	if picked == nil {
		return nil, fmt.Errorf("template not found: %s", templateID)
	}

	m.mu.RLock()
	setup, err := m.loadSetup(setupID)
	m.mu.RUnlock()
	if err != nil {
		return nil, err
	}
	if setup == nil {
		return nil, fmt.Errorf("setup not found: %s", setupID)
	}
	if req.GetReplaceSteps() {
		setup.Steps = nil
	}
	base := int32(len(setup.GetSteps()))
	for i, step := range picked.GetSteps() {
		if step == nil {
			continue
		}
		order := step.GetSortOrder()
		if order == 0 {
			order = base + int32(i)
		}
		setup.Steps = append(setup.Steps, &transcodev1.PipelineStep{
			StepType: step.GetStepType(), ConfigJson: step.GetConfigJson(),
			SortOrder: order, Enabled: step.GetEnabled(),
		})
	}

	resp, err := m.UpsertSetup(ctx, &transcodev1.UpsertSetupRequest{Setup: setup})
	if err != nil {
		return nil, err
	}
	return &transcodev1.ApplyStepTemplateResponse{Setup: resp.GetSetup()}, nil
}
