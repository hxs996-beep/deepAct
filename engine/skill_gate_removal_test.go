package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/deepact/deepact/skill"
)

// TestExecuteTurn_SkillHardGateRemoved_EditProceeds locks the removal of the
// skill HARD-GATE. Previously, when a skill with a block_all gate (e.g.
// systematic-debugging) was active and SkillGatePassed was false, every
// edit/write call was blocked by the engine — even though the skill's own
// methodology prompt already governs behavior ("no fixes without root cause
// investigation"). This caused the "agent keeps spinning in the red phase
// without acting" bug: the agent tried to write its failing test, got
// HARD-GATE-blocked, and could only loop on todo_write/read. With the gate
// removed, the harness stays thin and the skill prompt is the authority:
// the edit call must NOT be intercepted with a HARD-GATE tool message.
func TestExecuteTurn_SkillHardGateRemoved_EditProceeds(t *testing.T) {
	skillReg := skill.NewRegistry()
	skillReg.Register(&skill.Skill{
		Name:        "systematic-debugging",
		Description: "debug before fix",
		Content:     "NO FIXES WITHOUT ROOT CAUSE INVESTIGATION FIRST",
	})

	e := &Engine{
		model: &stubStreamModel{chunks: []ModelChunk{
			{
				Delta: "现在写入红灯测试",
				ToolCalls: []ModelToolCall{
					{ID: "call_1", Type: "function", Function: ModelFunctionCall{
						Name:      "edit",
						Arguments: `{"path":"engine/types.go","old_string":"old","new_string":"new"}`,
					}},
				},
				FinishReason: "tool_calls",
			},
		}},
		context:   &stubContextBuilder{},
		tools:     stubToolExecutor{},
		skills:    skillReg,
		guards:    &GuardSystem{loop: NewLoopGuard("", 6), scope: NewScopeGuard(false)},
		state:     &TaskState{TurnNumber: 5},
		history:   []Message{{Role: "user", Content: "修改代码"}},
		config:    EngineConfig{ModelName: "test-model"},
		isChinese: true,
	}

	_, err := e.executeTurn(context.Background())
	if err != nil {
		t.Fatalf("executeTurn error: %v", err)
	}
	// The edit must not be recorded as a HARD-GATE block in history.
	for _, msg := range e.history {
		if msg.Role == "tool" && strings.Contains(msg.Content, "HARD-GATE") {
			t.Fatalf("edit was HARD-GATE blocked: %q", msg.Content)
		}
	}
}
