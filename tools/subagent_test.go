package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/deepact/deepact/engine"
)

func TestSubAgentTool_Spec_DynamicEnum(t *testing.T) {
	tool := NewSubAgentTool(
		func(ctx context.Context, p engine.HandoffToAgentParams, d int, l string) (engine.ToolResult, error) {
			return engine.ToolResult{}, nil
		},
		func(ctx context.Context, p engine.HandoffToAgentParams, d int, l string) (engine.ToolResult, error) {
			return engine.ToolResult{}, nil
		},
		func() []string { return []string{"sub", "team-lead"} },
		2,
	)
	spec := tool.Spec()
	var params struct {
		Properties struct {
			Agent struct {
				Enum []string `json:"enum"`
			} `json:"agent"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(spec.Parameters, &params); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(params.Properties.Agent.Enum) != 2 || params.Properties.Agent.Enum[1] != "team-lead" {
		t.Errorf("enum = %v, want [sub team-lead]", params.Properties.Agent.Enum)
	}
}

func TestSubAgentTool_Run_DepthDispatch(t *testing.T) {
	var mainCalled, nestedCalled bool
	tool := NewSubAgentTool(
		func(ctx context.Context, p engine.HandoffToAgentParams, d int, l string) (engine.ToolResult, error) {
			mainCalled = true
			return engine.ToolResult{Status: "ok", Digest: "main"}, nil
		},
		func(ctx context.Context, p engine.HandoffToAgentParams, d int, l string) (engine.ToolResult, error) {
			nestedCalled = true
			return engine.ToolResult{Status: "ok", Digest: "nested"}, nil
		},
		func() []string { return []string{"sub"} },
		3, // maxDepth=3 so depth 2 still dispatches to nested
	)

	// depth 0 → main backend
	_, _ = tool.Run(ToolContext{Depth: 0, UserLang: "中文", Ctx: context.Background()},
		json.RawMessage(`{"agent":"sub","goal":"g"}`))
	if !mainCalled || nestedCalled {
		t.Errorf("depth 0: mainCalled=%v nestedCalled=%v, want main only", mainCalled, nestedCalled)
	}

	// depth 2 (maxDepth=3) → nested backend
	mainCalled = false
	nestedCalled = false
	_, _ = tool.Run(ToolContext{Depth: 2, Ctx: context.Background()},
		json.RawMessage(`{"agent":"sub","goal":"g"}`))
	if mainCalled || !nestedCalled {
		t.Errorf("depth 2: mainCalled=%v nestedCalled=%v, want nested backend only", mainCalled, nestedCalled)
	}
}

func TestSubAgentTool_Run_QuestionsPassthrough(t *testing.T) {
	tool := NewSubAgentTool(
		func(ctx context.Context, p engine.HandoffToAgentParams, d int, l string) (engine.ToolResult, error) {
			return engine.ToolResult{Status: "ok", Digest: "d", Questions: []string{"Q?"}}, nil
		},
		func(ctx context.Context, p engine.HandoffToAgentParams, d int, l string) (engine.ToolResult, error) {
			return engine.ToolResult{}, nil
		},
		func() []string { return []string{"sub"} },
		2,
	)
	env, err := tool.Run(ToolContext{Depth: 0, Ctx: context.Background()},
		json.RawMessage(`{"agent":"sub","goal":"g"}`))
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if len(env.Questions) != 1 || env.Questions[0] != "Q?" {
		t.Errorf("Questions = %v, want [Q?]", env.Questions)
	}
}

func TestSubAgentTool_Run_MaxDepthRejected(t *testing.T) {
	tool := NewSubAgentTool(
		func(ctx context.Context, p engine.HandoffToAgentParams, d int, l string) (engine.ToolResult, error) {
			t.Fatal("main backend must not be called at depth > 0")
			return engine.ToolResult{}, nil
		},
		func(ctx context.Context, p engine.HandoffToAgentParams, d int, l string) (engine.ToolResult, error) {
			return engine.ToolResult{Status: "ok", Digest: "nested"}, nil
		},
		func() []string { return []string{"sub"} },
		2,
	)
	env, err := tool.Run(ToolContext{Depth: 3, Ctx: context.Background()},
		json.RawMessage(`{"agent":"sub","goal":"g"}`))
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if env.Status != "error" {
		t.Errorf("Status = %q, want error (max depth)", env.Status)
	}
	if !strings.Contains(env.Digest, "Max nesting depth") {
		t.Errorf("Digest = %q, want contain Max nesting depth", env.Digest)
	}
}
