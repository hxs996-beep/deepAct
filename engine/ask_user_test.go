package engine

import (
	"encoding/json"
	"testing"
)

func TestAskUserToolSpec(t *testing.T) {
	spec := askUserToolSpec(true)
	if spec.Function.Name != AskUserToolName {
		t.Errorf("name = %q, want %q", spec.Function.Name, AskUserToolName)
	}
	if spec.Function.Description == "" {
		t.Error("description should not be empty")
	}
	var params struct {
		Type       string   `json:"type"`
		Required   []string `json:"required"`
		Properties struct {
			Question struct {
				Type string `json:"type"`
			} `json:"question"`
			Options struct {
				Type     string `json:"type"`
				MinItems int    `json:"minItems"`
				MaxItems int    `json:"maxItems"`
				Items    struct {
					Type      string `json:"type"`
					MinLength int    `json:"minLength"`
				} `json:"items"`
			} `json:"options"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(spec.Function.Parameters, &params); err != nil {
		t.Fatalf("unmarshal parameters: %v", err)
	}
	if len(params.Required) != 1 || params.Required[0] != "question" {
		t.Errorf("required = %v, want [question]", params.Required)
	}
	if params.Properties.Question.Type != "string" {
		t.Errorf("question.type = %q, want string", params.Properties.Question.Type)
	}
	if params.Properties.Options.MinItems != 2 {
		t.Errorf("minItems = %d, want 2", params.Properties.Options.MinItems)
	}
	if params.Properties.Options.MaxItems != 6 {
		t.Errorf("maxItems = %d, want 6", params.Properties.Options.MaxItems)
	}
	if params.Properties.Options.Items.MinLength != 1 {
		t.Errorf("items.minLength = %d, want 1", params.Properties.Options.Items.MinLength)
	}
	if params.Properties.Options.Items.Type != "string" {
		t.Errorf("items.type = %q, want string", params.Properties.Options.Items.Type)
	}
}
