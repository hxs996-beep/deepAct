package engine

import (
	"context"
	"strings"
	"testing"
)

// --- ProgressLoopState unit tests ---

func TestProgressLoopState_NudgeThenBlock(t *testing.T) {
	s := NewLoopTracker(4, 6, true)
	// 1st-3rd no-progress: allow (normal exploration/analysis is tolerated)
	for i := 1; i <= 3; i++ {
		if a := s.Check("", false); a.Type != GuardAllow {
			t.Fatalf("no-progress %d: want allow, got %s (%s)", i, a.Type, a.Message)
		}
	}
	// 4th: nudge
	if a := s.Check("", false); a.Type != GuardDiagnose {
		t.Fatalf("4th no-progress: want diagnose(nudge), got %s (%s)", a.Type, a.Message)
	}
	// 5th: allow (response window after the nudge)
	if a := s.Check("", false); a.Type != GuardAllow {
		t.Fatalf("5th no-progress: want allow, got %s (%s)", a.Type, a.Message)
	}
	// 6th: block
	a := s.Check("", false)
	if a.Type != GuardBlock {
		t.Fatalf("6th no-progress: want block, got %s (%s)", a.Type, a.Message)
	}
}

func TestProgressLoopState_ProgressResets(t *testing.T) {
	s := NewLoopTracker(4, 6, true)
	s.Check("", false)
	s.Check("", false)
	s.Check("", false)
	// A progress signal resets the streak.
	if a := s.Check("", true); a.Type != GuardAllow {
		t.Fatalf("progress: want allow, got %s", a.Type)
	}
	// Three more no-progress turns stay under the threshold.
	for i := 0; i < 3; i++ {
		if a := s.Check("", false); a.Type != GuardAllow {
			t.Fatalf("post-reset no-progress %d: want allow, got %s", i, a.Type)
		}
	}
}

func TestProgressLoopState_NilSafe(t *testing.T) {
	var s *LoopTracker
	if a := s.Check("", false); a.Type != GuardAllow {
		t.Errorf("nil Check: want allow, got %s", a.Type)
	}
	if a := s.Check("", true); a.Type != GuardAllow {
		t.Errorf("nil Check(progress): want allow, got %s", a.Type)
	}
	s.Reset() // must not panic
}

func TestProgressLoopState_Reset(t *testing.T) {
	s := NewLoopTracker(4, 6, true)
	for i := 0; i < 4; i++ {
		s.Check("", false)
	}
	s.Reset()
	if a := s.Check("", false); a.Type != GuardAllow {
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
		state:   &TaskState{TurnNumber: 0},
		history: []Message{{Role: "user", Content: "改"}},
		config:  EngineConfig{ModelName: "test-model"},
		guards:  &GuardSystem{loop: NewLoopTracker(0, 6, false), scope: NewScopeGuard(true)},
	}
	result, err := e.executeTurn(context.Background())
	if err != nil {
		t.Fatalf("executeTurn error: %v", err)
	}
	if !result.MadeProgress {
		t.Error("expected MadeProgress=true for successful edit call")
	}
}

func TestExecuteTurn_MadeProgress_NovelRead(t *testing.T) {
	// 方案 A：首次读到新 (path, scope) 视为进展——真实排查持续读新内容
	// 不应被 progress guard 误伤。progressKeys 留 nil 验证懒初始化。
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
		guards:  &GuardSystem{loop: NewLoopTracker(0, 6, false), scope: NewScopeGuard(false)},
	}
	result, err := e.executeTurn(context.Background())
	if err != nil {
		t.Fatalf("executeTurn error: %v", err)
	}
	if !result.MadeProgress {
		t.Error("expected MadeProgress=true for a novel read of a new file")
	}
}

func TestExecuteTurn_MadeProgress_RepeatedRead(t *testing.T) {
	// 重复读同一 (path, scope) 不产生新进展——配合 ReadLoopState 仍能捕获
	// "反复读同一内容"的空转。
	e := &Engine{
		model: &stubStreamModel{chunks: []ModelChunk{{
			Delta: "重读文件",
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
		guards:  &GuardSystem{loop: NewLoopTracker(0, 6, false), scope: NewScopeGuard(false)},
		// 该 (path, scope) 本轮已读过：key 形式与 read 的 LastOp 一致
		// "read:path::scope"。
		progressKeys: map[string]bool{"read:a.go::": true},
	}
	result, err := e.executeTurn(context.Background())
	if err != nil {
		t.Fatalf("executeTurn error: %v", err)
	}
	if result.MadeProgress {
		t.Error("expected MadeProgress=false for a repeated read of the same scope")
	}
}

func TestExecuteTurn_MadeProgress_NovelReadMulti(t *testing.T) {
	// read_multi 的任一新 target 均视为进展。
	e := &Engine{
		model: &stubStreamModel{chunks: []ModelChunk{{
			Delta: "批量读",
			ToolCalls: []ModelToolCall{{ID: "c1", Type: "function", Function: ModelFunctionCall{
				Name: "read_multi", Arguments: `{"targets":[{"path":"x.go","symbol":"Run"}]}`,
			}}},
			FinishReason: "tool_calls",
		}}},
		context: &stubContextBuilder{},
		tools:   &recordingToolExecutor{},
		state:   &TaskState{TurnNumber: 0},
		history: []Message{{Role: "user", Content: "看"}},
		config:  EngineConfig{ModelName: "test-model"},
		guards:  &GuardSystem{loop: NewLoopTracker(0, 6, false), scope: NewScopeGuard(false)},
	}
	result, err := e.executeTurn(context.Background())
	if err != nil {
		t.Fatalf("executeTurn error: %v", err)
	}
	if !result.MadeProgress {
		t.Error("expected MadeProgress=true for read_multi with a novel target")
	}
}

func TestExecuteTurn_MadeProgress_ReadKeyRecordedWithEdit(t *testing.T) {
	// 回归：同轮 edit + 新 read 时，read key 必须被记录（不能被进度
	// 计算的提前退出跳过），否则下一轮重复读同一文件会被误判为"新读"
	// 而错误重置 progress 计数。
	e := &Engine{
		model: &stubStreamModel{chunks: []ModelChunk{{
			Delta: "修改并读取",
			ToolCalls: []ModelToolCall{
				{ID: "c1", Type: "function", Function: ModelFunctionCall{
					Name: "edit", Arguments: `{"path":"a.go","old_string":"x","new_string":"y"}`,
				}},
				{ID: "c2", Type: "function", Function: ModelFunctionCall{
					Name: "read", Arguments: `{"path":"b.go"}`,
				}},
			},
			FinishReason: "tool_calls",
		}}},
		context: &stubContextBuilder{},
		tools:   &recordingToolExecutor{},
		state:   &TaskState{TurnNumber: 0},
		history: []Message{{Role: "user", Content: "改"}},
		config:  EngineConfig{ModelName: "test-model"},
		guards:  &GuardSystem{loop: NewLoopTracker(0, 6, false), scope: NewScopeGuard(true)},
	}
	// 第一轮：edit + 新 read b.go → 有进展，且 b.go 的 key 应被记录。
	r1, err := e.executeTurn(context.Background())
	if err != nil {
		t.Fatalf("executeTurn error: %v", err)
	}
	if !r1.MadeProgress {
		t.Error("expected MadeProgress=true for turn with edit + novel read")
	}
	if !e.progressKeys["read:b.go::"] {
		t.Error("expected read key read:b.go:: to be recorded even when edit already set progress")
	}
	// 第二轮：重复读 b.go（无其他操作）→ 不应算进展（key 已在第一轮记录）。
	e.model = &stubStreamModel{chunks: []ModelChunk{{
		Delta: "重读 b.go",
		ToolCalls: []ModelToolCall{{ID: "c3", Type: "function", Function: ModelFunctionCall{
			Name: "read", Arguments: `{"path":"b.go"}`,
		}}},
		FinishReason: "tool_calls",
	}}}
	r2, err := e.executeTurn(context.Background())
	if err != nil {
		t.Fatalf("executeTurn error: %v", err)
	}
	if r2.MadeProgress {
		t.Error("expected MadeProgress=false when re-reading a file already recorded in a prior turn")
	}
}

func TestExecuteTurn_MadeProgress_NovelGrep(t *testing.T) {
	e := &Engine{
		model: &stubStreamModel{chunks: []ModelChunk{{
			Delta: "搜索",
			ToolCalls: []ModelToolCall{{ID: "c1", Type: "function", Function: ModelFunctionCall{
				Name: "grep", Arguments: `{"pattern":"LoopTracker"}`,
			}}},
			FinishReason: "tool_calls",
		}}},
		context: &stubContextBuilder{},
		tools:   &recordingToolExecutor{},
		state:   &TaskState{TurnNumber: 0},
		history: []Message{{Role: "user", Content: "搜"}},
		config:  EngineConfig{ModelName: "test-model"},
		guards:  &GuardSystem{loop: NewLoopTracker(0, 6, false), scope: NewScopeGuard(false)},
	}
	result, err := e.executeTurn(context.Background())
	if err != nil {
		t.Fatalf("executeTurn error: %v", err)
	}
	if !result.MadeProgress {
		t.Error("expected MadeProgress=true for a novel grep pattern")
	}
}

func TestExecuteTurn_MadeProgress_RepeatedGrep(t *testing.T) {
	e := &Engine{
		model: &stubStreamModel{chunks: []ModelChunk{{
			Delta: "重复搜索",
			ToolCalls: []ModelToolCall{{ID: "c1", Type: "function", Function: ModelFunctionCall{
				Name: "grep", Arguments: `{"pattern":"LoopTracker"}`,
			}}},
			FinishReason: "tool_calls",
		}}},
		context: &stubContextBuilder{},
		tools:   &recordingToolExecutor{},
		state:   &TaskState{TurnNumber: 0},
		history: []Message{{Role: "user", Content: "搜"}},
		config:  EngineConfig{ModelName: "test-model"},
		guards:  &GuardSystem{loop: NewLoopTracker(0, 6, false), scope: NewScopeGuard(false)},
		// 该 pattern 本轮已搜过：key 形式 "grep:<pattern>:"
		progressKeys: map[string]bool{"grep:LoopTracker:": true},
	}
	result, err := e.executeTurn(context.Background())
	if err != nil {
		t.Fatalf("executeTurn error: %v", err)
	}
	if result.MadeProgress {
		t.Error("expected MadeProgress=false for a repeated grep of the same pattern")
	}
}

// --- Run integration: novel reads are progress; no-progress loops are caught ---

// novelReadTurnChunks builds one turn that updates todos and reads a NEW
// file — legitimate investigation that must NOT trip the progress guard.
// 方案 A：持续读取新内容视为任务推进。
func novelReadTurnChunks(id, path string) []ModelChunk {
	return []ModelChunk{{
		Delta: "先更新 todo，然后读取新文件",
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

// noProgressTurnChunks builds one turn that only updates todos and greps —
// no novel read, no code change. The "复述 + todo + search" loop the
// progress guard must still catch: no new content is discovered and nothing
// is modified.
func noProgressTurnChunks(id, pattern string) []ModelChunk {
	return []ModelChunk{{
		Delta: "先更新 todo，然后搜索",
		ToolCalls: []ModelToolCall{
			{ID: id + "_todo", Type: "function", Function: ModelFunctionCall{
				Name: TodoWriteToolName, Arguments: `{"todos":[{"content":"步骤一","status":"in_progress"}]}`,
			}},
			{ID: id + "_grep", Type: "function", Function: ModelFunctionCall{
				Name: "grep", Arguments: `{"pattern":"` + pattern + `"}`,
			}},
		},
		FinishReason: "tool_calls",
		Usage:        &ModelUsage{},
	}}
}

func TestRun_ProgressLoop_NovelReadsAllowed(t *testing.T) {
	// 方案 A：持续读取不同文件是合法排查推进，不得触发 progress 守卫。
	// 这是用户报告的真实场景（分析/排查任务被误判为无进展循环）。
	model := &multiTurnModel{turns: [][]ModelChunk{
		novelReadTurnChunks("t1", "a.go"),
		novelReadTurnChunks("t2", "b.go"),
		novelReadTurnChunks("t3", "c.go"),
		novelReadTurnChunks("t4", "d.go"),
		novelReadTurnChunks("t5", "e.go"),
		novelReadTurnChunks("t6", "f.go"),
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
			loop:  NewLoopTracker(0, 6, false),
			scope: NewScopeGuard(false),
		},
		readLoop:     NewLoopTracker(3, 4, false),
		progressLoop: NewLoopTracker(4, 6, true),
		isChinese:    true,
	}
	resp, err := e.Run(context.Background(), "排查问题")
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if resp.Blocked {
		t.Fatalf("expected NOT blocked for novel reads, got Blocked=%v Summary=%q", resp.Blocked, resp.Summary)
	}
}

func TestRun_ProgressLoop_NoNovelReadBlocked(t *testing.T) {
	// 每轮 todo + 重复同一 pattern 的 grep（无新读、无新信息、无改动）仍是无进展：
	// 首轮 novel grep 算进展后，相同搜索不再产生新信息，第 7 轮（连续 6 轮无进展）应 block。
	model := &multiTurnModel{turns: [][]ModelChunk{
		noProgressTurnChunks("t1", "foo"),
		noProgressTurnChunks("t2", "foo"),
		noProgressTurnChunks("t3", "foo"),
		noProgressTurnChunks("t4", "foo"),
		noProgressTurnChunks("t5", "foo"),
		noProgressTurnChunks("t6", "foo"),
		noProgressTurnChunks("t7", "foo"),
	}}
	e := &Engine{
		model:   model,
		context: &stubContextBuilder{},
		tools:   &recordingToolExecutor{},
		state:   &TaskState{TurnNumber: 0},
		history: []Message{},
		config:  EngineConfig{ModelName: "test-model"},
		guards: &GuardSystem{
			loop:  NewLoopTracker(0, 6, false),
			scope: NewScopeGuard(false),
		},
		readLoop:     NewLoopTracker(3, 4, false),
		progressLoop: NewLoopTracker(4, 6, true),
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
		noProgressTurnChunks("t1", "foo"),
		noProgressTurnChunks("t2", "bar"),
		noProgressTurnChunks("t3", "baz"),
		[]ModelChunk{ // t4: a successful bash call — progress signal resets the streak
			{Delta: "执行命令",
				ToolCalls: []ModelToolCall{{ID: "t4_bash", Type: "function", Function: ModelFunctionCall{
					Name: "bash", Arguments: `{"command":"go test ./..."}`,
				}}},
				FinishReason: "tool_calls",
				Usage:        &ModelUsage{},
			},
		},
		noProgressTurnChunks("t5", "qux"),
		noProgressTurnChunks("t6", "quux"),
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
			loop:  NewLoopTracker(0, 6, false),
			scope: NewScopeGuard(true),
		},
		readLoop:     NewLoopTracker(3, 4, false),
		progressLoop: NewLoopTracker(4, 6, true),
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
