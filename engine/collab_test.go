package engine

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
)

// --- /collab command parsing ---

func TestParseCollabCommand_Valid(t *testing.T) {
	cmd := parseCollabCommand("/collab 实现一个缓存层")
	if cmd == nil {
		t.Fatal("expected non-nil CollabCommand")
	}
	if cmd.Goal != "实现一个缓存层" {
		t.Errorf("Goal = %q, want %q", cmd.Goal, "实现一个缓存层")
	}
}

func TestParseCollabCommand_NotCollab(t *testing.T) {
	cases := []string{
		"/ratd 实现一个功能",
		"/team 实现一个功能",
		"/skills",
		"普通用户消息",
		"",
		"/",
	}
	for _, c := range cases {
		cmd := parseCollabCommand(c)
		if cmd != nil {
			t.Errorf("expected nil for %q, got %+v", c, cmd)
		}
	}
}

// --- Collab parallel research test harness ---

// decomposerMockRunner returns valid tasks JSON for the decompose phase, then
// falls back to the fixed response for worker/synthesizer phases.
type decomposerMockRunner struct {
	mockPromptRunner
	decomposed bool
}

func (m *decomposerMockRunner) RunWithPrompt(ctx context.Context, input Handoff, extraPrompt string) (*HandoffResult, error) {
	if !m.decomposed && strings.Contains(extraPrompt, "拆解员") {
		m.decomposed = true
		return &HandoffResult{Summary: `{"tasks":[{"id":"t1","title":"调研缓存","direction":"读 cache.go 梳理接口"},{"id":"t2","title":"调研选型","direction":"评估第三方库"}]}`}, nil
	}
	return m.mockPromptRunner.RunWithPrompt(ctx, input, extraPrompt)
}

// newCollabTestEngine creates a minimal engine for /collab parallel research testing.
func newCollabTestEngine(t *testing.T) *Engine {
	t.Helper()
	reg := NewAgentRegistry()
	reg.Register(&decomposerMockRunner{
		mockPromptRunner: mockPromptRunner{
			mockSimpleAgent: mockSimpleAgent{
				id:       AgentSub,
				response: "## 产出\n采用微服务架构。",
			},
		},
	})
	e := &Engine{
		agents: reg,
		state:  &TaskState{TaskID: "test-collab"},
		config: EngineConfig{},
	}
	e.collabHall = NewCollabHall(e)
	return e
}

// --- Collab parallel research state machine ---

func TestHandleCollabArena_FullPipeline(t *testing.T) {
	e := newCollabTestEngine(t)
	e.state.Collab = &CollabState{
		Goal:  "实现一个缓存层",
		Phase: CollabDecompose,
	}
	resp, err := e.collabHall.handleCollabArena(context.Background())
	if err != nil {
		t.Fatalf("handleCollabArena() unexpected error: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	if e.state.Collab != nil {
		t.Errorf("collab state should be cleared on completion, got phase %v", e.state.Collab.Phase)
	}
	if !strings.Contains(resp.Summary, "并行研究完成") {
		t.Errorf("report should mention completion, got:\n%s", resp.Summary)
	}
	if !strings.Contains(resp.Summary, "实现一个缓存层") {
		t.Errorf("report should carry the goal, got:\n%s", resp.Summary)
	}
}

func TestHandleCollabArena_DecomposeFailure(t *testing.T) {
	// 用返回非 JSON 的纯 mockPromptRunner（非 decomposerMockRunner）
	reg := NewAgentRegistry()
	reg.Register(&mockPromptRunner{
		mockSimpleAgent: mockSimpleAgent{id: AgentSub, response: "## 产出\n采用微服务架构。"},
	})
	e := &Engine{
		agents: reg,
		state:  &TaskState{TaskID: "test-collab-fail"},
		config: EngineConfig{},
	}
	e.collabHall = NewCollabHall(e)
	e.state.Collab = &CollabState{
		Goal:  "实现一个缓存层",
		Phase: CollabDecompose,
	}
	_, err := e.collabHall.handleCollabArena(context.Background())
	if err == nil {
		t.Fatal("expected error when decomposer output is not valid JSON")
	}
	if e.state.Collab != nil {
		t.Errorf("collab state should be cleared on decompose failure, got phase %v", e.state.Collab.Phase)
	}
}

func TestHandleCollabArena_TasksAllCompleted(t *testing.T) {
	// capture 基建：记录所有 worker 的 MaxIterations（99）与只读工具。
	e, captor := newCaptureCollabTestEngine(t)
	e.state.Collab = &CollabState{
		Goal:  "实现一个缓存层",
		Phase: CollabDecompose,
	}
	_, err := e.collabHall.handleCollabArena(context.Background())
	if err != nil {
		t.Fatalf("handleCollabArena() unexpected error: %v", err)
	}
	if len(captor.workerCalls) != 2 {
		t.Fatalf("expected 2 worker runs, got %d", len(captor.workerCalls))
	}
	allowed := map[string]bool{"read": true, "grep": true, "glob": true, "lsp": true}
	for i, call := range captor.workerCalls {
		if call.maxIterations != collabWorkerMaxIterations {
			t.Errorf("worker %d MaxIterations = %d, want %d", i, call.maxIterations, collabWorkerMaxIterations)
		}
		for _, got := range call.tools {
			if !allowed[got] {
				t.Errorf("worker %d leaked forbidden tool %q: %v", i, got, call.tools)
			}
		}
	}
}

type collabCall struct {
	tools         []string
	maxIterations int
}

// captureCollabRunner implements RunWithPrompt, records worker calls, returns
// valid tasks JSON for the decompose phase and fixed text otherwise.
type captureCollabRunner struct {
	mockPromptRunner
	decomposed  bool
	mu          sync.Mutex
	workerCalls []collabCall
}

func (c *captureCollabRunner) RunWithPrompt(_ context.Context, input Handoff, extraPrompt string) (*HandoffResult, error) {
	if strings.Contains(extraPrompt, "拆解员") && !c.decomposed {
		c.decomposed = true
		return &HandoffResult{Summary: `{"tasks":[{"id":"t1","title":"调研缓存","direction":"读 cache.go"},{"id":"t2","title":"调研选型","direction":"评估第三方库"}]}`}, nil
	}
	if strings.Contains(extraPrompt, "研究员") {
		c.mu.Lock()
		c.workerCalls = append(c.workerCalls, collabCall{tools: input.Tools, maxIterations: input.MaxIterations})
		c.mu.Unlock()
	}
	return &HandoffResult{Summary: c.response, Conclusions: []string{c.response}}, nil
}

func newCaptureCollabTestEngine(t *testing.T) (*Engine, *captureCollabRunner) {
	t.Helper()
	captor := &captureCollabRunner{
		mockPromptRunner: mockPromptRunner{
			mockSimpleAgent: mockSimpleAgent{
				id:       AgentSub,
				response: "## 产出\n采用微服务架构。",
			},
		},
	}
	reg := NewAgentRegistry()
	reg.Register(captor)
	e := &Engine{
		agents: reg,
		state:  &TaskState{TaskID: "test-collab-capture"},
		config: EngineConfig{},
	}
	e.collabHall = NewCollabHall(e)
	return e, captor
}

// scenarioCollabRunner implements RunWithPrompt for worker-failure-tolerance
// and synthesize-fallback scenarios: emits tasks JSON on the first decomposer
// call, fails the worker whose goal mentions failDirection with an error, and
// optionally returns an empty Summary for the synthesizer (captured in synthInput).
type scenarioCollabRunner struct {
	mockPromptRunner
	decomposed     bool
	failDirection  string
	failSynthesize bool
	synthInput     string
}

func (s *scenarioCollabRunner) RunWithPrompt(_ context.Context, input Handoff, extraPrompt string) (*HandoffResult, error) {
	if strings.Contains(extraPrompt, "拆解员") && !s.decomposed {
		s.decomposed = true
		return &HandoffResult{Summary: `{"tasks":[{"id":"t1","title":"调研缓存","direction":"读 cache.go"},{"id":"t2","title":"调研选型","direction":"评估第三方库"}]}`}, nil
	}
	if strings.Contains(extraPrompt, "研究员") {
		if s.failDirection != "" && strings.Contains(input.Goal, s.failDirection) {
			return nil, fmt.Errorf("worker boom")
		}
		return &HandoffResult{Summary: s.response, Conclusions: []string{s.response}}, nil
	}
	if strings.Contains(extraPrompt, "汇总员") {
		s.synthInput = input.Goal
		if s.failSynthesize {
			return &HandoffResult{Summary: "", Conclusions: nil}, nil
		}
		return &HandoffResult{Summary: s.response, Conclusions: []string{s.response}}, nil
	}
	return &HandoffResult{Summary: s.response, Conclusions: []string{s.response}}, nil
}

func newScenarioCollabTestEngine(t *testing.T, runner *scenarioCollabRunner) *Engine {
	t.Helper()
	reg := NewAgentRegistry()
	reg.Register(runner)
	e := &Engine{
		agents: reg,
		state:  &TaskState{TaskID: "test-collab-scenario"},
		config: EngineConfig{},
	}
	e.collabHall = NewCollabHall(e)
	return e
}

func TestHandleCollabArena_WorkerFailureTolerated(t *testing.T) {
	// 规格要求：单 worker 失败不中断其他 worker，汇总输入标注失败任务。
	runner := &scenarioCollabRunner{
		mockPromptRunner: mockPromptRunner{
			mockSimpleAgent: mockSimpleAgent{
				id:       AgentSub,
				response: "## 产出\n采用微服务架构。",
			},
		},
		failDirection: "评估第三方库", // t2 失败，t1 正常
	}
	e := newScenarioCollabTestEngine(t, runner)
	e.state.Collab = &CollabState{
		Goal:  "实现一个缓存层",
		Phase: CollabDecompose,
	}
	resp, err := e.collabHall.handleCollabArena(context.Background())
	if err != nil {
		t.Fatalf("handleCollabArena() should tolerate worker failure, got error: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	if !strings.Contains(resp.Summary, "并行研究完成") {
		t.Errorf("report should mention completion, got:\n%s", resp.Summary)
	}
	if !strings.Contains(runner.synthInput, "failed") {
		t.Errorf("synthesizer input should mark the failed task, got:\n%s", runner.synthInput)
	}
	if !strings.Contains(runner.synthInput, "worker boom") {
		t.Errorf("synthesizer input should carry the worker error, got:\n%s", runner.synthInput)
	}
}

func TestHandleCollabArena_SynthesizeFallback(t *testing.T) {
	// 规格要求：汇总失败时 fallback 展示各任务结果。
	runner := &scenarioCollabRunner{
		mockPromptRunner: mockPromptRunner{
			mockSimpleAgent: mockSimpleAgent{
				id:       AgentSub,
				response: "## 产出\n采用微服务架构。",
			},
		},
		failSynthesize: true,
	}
	e := newScenarioCollabTestEngine(t, runner)
	e.state.Collab = &CollabState{
		Goal:  "实现一个缓存层",
		Phase: CollabDecompose,
	}
	resp, err := e.collabHall.handleCollabArena(context.Background())
	if err != nil {
		t.Fatalf("handleCollabArena() unexpected error: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	if !strings.Contains(resp.Summary, "并行研究完成") {
		t.Errorf("report should mention completion, got:\n%s", resp.Summary)
	}
	if !strings.Contains(resp.Summary, "各任务结果") {
		t.Errorf("report should fall back to task results, got:\n%s", resp.Summary)
	}
	if !strings.Contains(resp.Summary, "调研缓存") {
		t.Errorf("report should list task results, got:\n%s", resp.Summary)
	}
}

// --- parseCollabTasks ---

func TestParseCollabTasks_Valid(t *testing.T) {
	content := `{
		"tasks": [
			{"id": "t1", "title": "调研现有缓存实现", "direction": "定位并阅读缓存模块"},
			{"id": "t2", "title": "调研第三方选型", "direction": "评估可复用方案"}
		]
	}`
	tasks, err := parseCollabTasks(content)
	if err != nil {
		t.Fatalf("parseCollabTasks() unexpected error: %v", err)
	}
	if len(tasks) != 2 {
		t.Fatalf("got %d tasks, want 2", len(tasks))
	}
	if tasks[0].ID != "t1" || tasks[0].Direction == "" {
		t.Errorf("task[0] = %+v, want id=t1 and non-empty direction", tasks[0])
	}
}

func TestParseCollabTasks_ToleratesProse(t *testing.T) {
	content := "好的，我拆解如下：\n```json\n{\"tasks\": [{\"id\": \"t1\", \"title\": \"A\", \"direction\": \"dir A\"}, {\"id\": \"t2\", \"title\": \"B\", \"direction\": \"dir B\"}]}\n```\n以上是任务列表。"
	tasks, err := parseCollabTasks(content)
	if err != nil {
		t.Fatalf("parseCollabTasks() unexpected error: %v", err)
	}
	if len(tasks) != 2 {
		t.Fatalf("got %d tasks, want 2", len(tasks))
	}
}

func TestParseCollabTasks_TruncatesOverMax(t *testing.T) {
	var items []string
	for i := 1; i <= 8; i++ {
		items = append(items, fmt.Sprintf(`{"id": "t%d", "title": "T%d", "direction": "d%d"}`, i, i, i))
	}
	content := `{"tasks": [` + strings.Join(items, ",") + `]}`
	tasks, err := parseCollabTasks(content)
	if err != nil {
		t.Fatalf("parseCollabTasks() unexpected error: %v", err)
	}
	if len(tasks) != collabMaxTasks {
		t.Fatalf("got %d tasks, want truncated to %d", len(tasks), collabMaxTasks)
	}
	if tasks[0].ID != "t1" {
		t.Errorf("truncation must preserve order, first task = %q", tasks[0].ID)
	}
}

func TestParseCollabTasks_TooFewFails(t *testing.T) {
	content := `{"tasks": [{"id": "t1", "title": "A", "direction": "d"}]}`
	_, err := parseCollabTasks(content)
	if err == nil {
		t.Fatal("expected error for single task (< min)")
	}
}

func TestParseCollabTasks_EmptyFails(t *testing.T) {
	if _, err := parseCollabTasks(`{"tasks": []}`); err == nil {
		t.Fatal("expected error for empty tasks")
	}
	if _, err := parseCollabTasks(`no json here`); err == nil {
		t.Fatal("expected error for non-JSON content")
	}
}
