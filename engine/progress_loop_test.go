package engine

import (
	"context"
	"strings"
	"testing"
)

// --- ProgressLoopState unit tests ---

func TestProgressLoopState_NudgeThenBlock(t *testing.T) {
	s := NewProgressLoopState(6)
	// 1st-3rd no-progress: allow (normal exploration/analysis is tolerated)
	for i := 1; i <= 3; i++ {
		if a := s.Check(false); a.Type != GuardAllow {
			t.Fatalf("no-progress %d: want allow, got %s (%s)", i, a.Type, a.Message)
		}
	}
	// 4th: nudge
	if a := s.Check(false); a.Type != GuardDiagnose {
		t.Fatalf("4th no-progress: want diagnose(nudge), got %s (%s)", a.Type, a.Message)
	}
	// 5th: allow (response window after the nudge)
	if a := s.Check(false); a.Type != GuardAllow {
		t.Fatalf("5th no-progress: want allow, got %s (%s)", a.Type, a.Message)
	}
	// 6th: block
	a := s.Check(false)
	if a.Type != GuardBlock {
		t.Fatalf("6th no-progress: want block, got %s (%s)", a.Type, a.Message)
	}
}

func TestProgressLoopState_ProgressResets(t *testing.T) {
	s := NewProgressLoopState(6)
	s.Check(false)
	s.Check(false)
	s.Check(false)
	// A progress signal resets the streak.
	if a := s.Check(true); a.Type != GuardAllow {
		t.Fatalf("progress: want allow, got %s", a.Type)
	}
	// Three more no-progress turns stay under the threshold.
	for i := 0; i < 3; i++ {
		if a := s.Check(false); a.Type != GuardAllow {
			t.Fatalf("post-reset no-progress %d: want allow, got %s", i, a.Type)
		}
	}
}

func TestProgressLoopState_NilSafe(t *testing.T) {
	var s *ProgressLoopState
	if a := s.Check(false); a.Type != GuardAllow {
		t.Errorf("nil Check: want allow, got %s", a.Type)
	}
	if a := s.Check(true); a.Type != GuardAllow {
		t.Errorf("nil Check(progress): want allow, got %s", a.Type)
	}
	s.Reset() // must not panic
}

func TestProgressLoopState_Reset(t *testing.T) {
	s := NewProgressLoopState(6)
	for i := 0; i < 4; i++ {
		s.Check(false)
	}
	s.Reset()
	if a := s.Check(false); a.Type != GuardAllow {
		t.Fatalf("after reset 1st: want allow, got %s", a.Type)
	}
}

func TestBuildProgressMessages(t *testing.T) {
	zh := buildProgressNudge(true)
	if !strings.Contains(zh, "未产生任何代码修改") {
		t.Errorf("zh nudge missing directive: %s", zh)
	}
	zhBlock := buildProgressBlockMsg(true)
	if !strings.Contains(zhBlock, "无进展循环") || !strings.Contains(zhBlock, "请澄清") {
		t.Errorf("zh block missing clarify: %s", zhBlock)
	}
	en := buildProgressNudge(false)
	if strings.Contains(en, "未产生任何代码修改") {
		t.Errorf("en nudge should be English: %s", en)
	}
}

// --- executeTurn MadeProgress ---

func TestExecuteTurn_MadeProgress_EditSuccess(t *testing.T) {
	e := &Engine{
		model: &stubStreamModel{chunks: []ModelChunk{{
			Delta: "修改文件",
			ToolCalls: []ModelToolCall{{ID: "c1", Type: "function", Function: ModelFunctionCall{
				Name: "edit", Arguments: `{"path":"a.go","old_string":"x","new_string":"y"}`,
			}}},
			FinishReason: "tool_calls",
		}}},
		context: &stubContextBuilder{},
		tools:   &recordingToolExecutor{},
		state:   &TaskState{TurnNumber: 0, AnalysisReportConfirmed: true},
		history: []Message{{Role: "user", Content: "改"}},
		config:  EngineConfig{ModelName: "test-model"},
		guards:  &GuardSystem{loop: NewLoopGuard("", 6), scope: NewScopeGuard(true)},
	}
	result, err := e.executeTurn(context.Background())
	if err != nil {
		t.Fatalf("executeTurn error: %v", err)
	}
	if !result.MadeProgress {
		t.Error("expected MadeProgress=true for successful edit call")
	}
}

func TestExecuteTurn_MadeProgress_ReadOnlyFalse(t *testing.T) {
	e := &Engine{
		model: &stubStreamModel{chunks: []ModelChunk{{
			Delta: "读文件",
			ToolCalls: []ModelToolCall{{ID: "c1", Type: "function", Function: ModelFunctionCall{
				Name: "read", Arguments: `{"path":"a.go"}`,
			}}},
			FinishReason: "tool_calls",
		}}},
		context: &stubContextBuilder{},
		tools:   &recordingToolExecutor{},
		state:   &TaskState{TurnNumber: 0},
		history: []Message{{Role: "user", Content: "看"}},
		config:  EngineConfig{ModelName: "test-model"},
		guards:  &GuardSystem{loop: NewLoopGuard("", 6), scope: NewScopeGuard(false)},
	}
	result, err := e.executeTurn(context.Background())
	if err != nil {
		t.Fatalf("executeTurn error: %v", err)
	}
	if result.MadeProgress {
		t.Error("expected MadeProgress=false for read-only turn")
	}
}

// --- Run integration: narration + todo_write + read loop is caught ---

// noProgressTurnChunks builds one turn that only updates todos and reads a
// (different) file — the "复述 + todo + read" loop that bypasses all
// operation-repeat guards.
func noProgressTurnChunks(id, path string) []ModelChunk {
	return []ModelChunk{{
		Delta: "先更新 todo，然后读取文件",
		ToolCalls: []ModelToolCall{
			{ID: id + "_todo", Type: "function", Function: ModelFunctionCall{
				Name: TodoWriteToolName, Arguments: `{"todos":[{"content":"步骤一","status":"in_progress"}]}`,
			}},
			{ID: id + "_read", Type: "function", Function: ModelFunctionCall{
				Name: "read", Arguments: `{"path":"` + path + `"}`,
			}},
		},
		FinishReason: "tool_calls",
		Usage:        &ModelUsage{},
	}}
}

func TestRun_ProgressLoop_ReadTodoNarrationBlocked(t *testing.T) {
	model := &multiTurnModel{turns: [][]ModelChunk{
		noProgressTurnChunks("t1", "a.go"),
		noProgressTurnChunks("t2", "b.go"),
		noProgressTurnChunks("t3", "c.go"),
		noProgressTurnChunks("t4", "d.go"),
		noProgressTurnChunks("t5", "e.go"),
		noProgressTurnChunks("t6", "f.go"),
	}}
	e := &Engine{
		model:   model,
		context: &stubContextBuilder{},
		tools:   &recordingToolExecutor{},
		state:   &TaskState{TurnNumber: 0},
		history: []Message{},
		config:  EngineConfig{ModelName: "test-model"},
		guards: &GuardSystem{
			loop:  NewLoopGuard("", 6),
			scope: NewScopeGuard(false),
		},
		readLoop:     NewReadLoopState(),
		progressLoop: NewProgressLoopState(6),
		isChinese:    true,
	}
	resp, err := e.Run(context.Background(), "实现功能")
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if !resp.Blocked {
		t.Fatalf("expected Blocked=true after 6 no-progress turns, got Blocked=%v Summary=%q", resp.Blocked, resp.Summary)
	}
	if resp.BlockedBy != "loop_guard" || resp.FinishReason != "loop_detected" {
		t.Errorf("expected BlockedBy=loop_guard FinishReason=loop_detected, got %q/%q", resp.BlockedBy, resp.FinishReason)
	}
	if !strings.Contains(resp.Summary, "无进展循环") {
		t.Errorf("expected no-progress block message, got %q", resp.Summary)
	}
}

func TestRun_ProgressLoop_EditResetsStreak(t *testing.T) {
	model := &multiTurnModel{turns: [][]ModelChunk{
		noProgressTurnChunks("t1", "a.go"),
		noProgressTurnChunks("t2", "b.go"),
		noProgressTurnChunks("t3", "c.go"),
		[]ModelChunk{ // t4: a successful bash call — progress signal resets the streak
			{Delta: "执行命令",
				ToolCalls: []ModelToolCall{{ID: "t4_bash", Type: "function", Function: ModelFunctionCall{
					Name: "bash", Arguments: `{"command":"go test ./..."}`,
				}}},
				FinishReason: "tool_calls",
				Usage:        &ModelUsage{},
			},
		},
		noProgressTurnChunks("t5", "d.go"),
		noProgressTurnChunks("t6", "e.go"),
		{{Delta: "完成。", FinishReason: "stop", Usage: &ModelUsage{}}},
	}}
	e := &Engine{
		model:   model,
		context: &stubContextBuilder{},
		tools:   &recordingToolExecutor{},
		state:   &TaskState{TurnNumber: 0},
		history: []Message{},
		config:  EngineConfig{ModelName: "test-model"},
		guards: &GuardSystem{
			loop:  NewLoopGuard("", 6),
			scope: NewScopeGuard(true),
		},
		readLoop:     NewReadLoopState(),
		progressLoop: NewProgressLoopState(6),
		isChinese:    true,
	}
	resp, err := e.Run(context.Background(), "实现功能")
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if resp.Blocked {
		t.Fatalf("expected NOT blocked after an edit progress signal, got Blocked=%v Summary=%q", resp.Blocked, resp.Summary)
	}
}
