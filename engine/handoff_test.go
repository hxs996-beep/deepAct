package engine

import (
	"context"
	"strings"
	"testing"
	"time"
)

type mockAgentForHandoff struct {
	id     AgentID
	result *HandoffResult
	err    error
}

func (m *mockAgentForHandoff) ID() AgentID { return m.id }
func (m *mockAgentForHandoff) Spec() AgentSpec {
	return AgentSpec{ID: m.id, Description: "mock"}
}
func (m *mockAgentForHandoff) Run(_ context.Context, _ Handoff) (*HandoffResult, error) {
	return m.result, m.err
}

func TestRunSubAgent_EngineBackend_Completed(t *testing.T) {
	reg := NewAgentRegistry()
	reg.Register(&mockAgentForHandoff{id: AgentSub, result: &HandoffResult{
		Summary: "分析完成", FinishReason: HandoffReasonCompleted,
	}})
	e := &Engine{agents: reg, isChinese: true, state: &TaskState{}}

	res, err := e.RunSubAgent(context.Background(), HandoffToAgentParams{
		Agent: "sub", Goal: "分析 X",
	}, 0, "中文")
	if err != nil {
		t.Fatalf("RunSubAgent error: %v", err)
	}
	if res.Status != "ok" {
		t.Errorf("Status = %q, want ok", res.Status)
	}
	if !strings.Contains(res.Digest, "分析完成") {
		t.Errorf("Digest = %q, want contain 分析完成", res.Digest)
	}
	if res.FinishReason != HandoffReasonCompleted {
		t.Errorf("FinishReason = %q, want completed", res.FinishReason)
	}
}

func TestRunSubAgent_EngineBackend_AgentNotFound(t *testing.T) {
	e := &Engine{agents: NewAgentRegistry(), isChinese: true, state: &TaskState{}}
	res, err := e.RunSubAgent(context.Background(), HandoffToAgentParams{
		Agent: "nope", Goal: "x",
	}, 0, "中文")
	if err != nil {
		t.Fatalf("RunSubAgent error: %v", err)
	}
	if res.Status != "error" {
		t.Errorf("Status = %q, want error", res.Status)
	}
}

func TestRunSubAgent_EngineBackend_NilRegistry(t *testing.T) {
	e := &Engine{isChinese: true, state: &TaskState{}}
	res, err := e.RunSubAgent(context.Background(), HandoffToAgentParams{
		Agent: "sub", Goal: "x",
	}, 0, "中文")
	if err != nil {
		t.Fatalf("RunSubAgent error: %v", err)
	}
	if res.Status != "error" || res.Digest != "no agent registry configured" {
		t.Errorf("got %q %q, want error no agent registry configured", res.Status, res.Digest)
	}
}

func TestRunSubAgent_EngineBackend_BubblesQuestions(t *testing.T) {
	reg := NewAgentRegistry()
	reg.Register(&mockAgentForHandoff{id: AgentSub, result: &HandoffResult{
		Summary: "需要信息", FinishReason: HandoffReasonAwaitingUser,
		Questions: []string{"数据库连接串是什么？"},
	}})
	e := &Engine{agents: reg, isChinese: true, state: &TaskState{}}

	res, err := e.RunSubAgent(context.Background(), HandoffToAgentParams{
		Agent: "sub", Goal: "配置 DB",
	}, 0, "中文")
	if err != nil {
		t.Fatalf("RunSubAgent error: %v", err)
	}
	if res.FinishReason != HandoffReasonAwaitingUser {
		t.Errorf("FinishReason = %q, want awaiting_user", res.FinishReason)
	}
	if len(res.Questions) != 1 || res.Questions[0] != "数据库连接串是什么？" {
		t.Errorf("Questions = %v", res.Questions)
	}
	if e.pendingAskUser == nil || e.pendingAskUser.Question != "数据库连接串是什么？" {
		t.Errorf("pendingAskUser = %+v, want question set", e.pendingAskUser)
	}
}

func TestSubAgentRunnerRunSubAgent_Nested(t *testing.T) {
	reg := NewAgentRegistry()
	reg.Register(&mockAgentForHandoff{id: AgentSub, result: &HandoffResult{
		Summary: "嵌套完成", FinishReason: HandoffReasonCompleted,
	}})
	runner := &SubAgentRunner{model: &stubCompleteModel{resp: "ok"}, tools: stubToolExecutor{}, registry: reg, modelName: "test"}

	res, err := runner.RunSubAgent(context.Background(), HandoffToAgentParams{
		Agent: "sub", Goal: "子任务",
	}, 1, "中文")
	if err != nil {
		t.Fatalf("RunSubAgent error: %v", err)
	}
	if res.Status != "ok" {
		t.Errorf("Status = %q, want ok", res.Status)
	}
	if res.Digest != "代理完成： 嵌套完成\n" {
		t.Errorf("Digest = %q, want Chinese completed digest", res.Digest)
	}
}

func TestHandoffToAgentParams_AsyncField(t *testing.T) {
	// async 字段必须存在于 handoff spec 的 parameters JSON 中。
	spec := handoffToolSpec(false)
	if !strings.Contains(string(spec.Function.Parameters), `"async"`) {
		t.Fatalf("handoff spec parameters must contain async field, got: %s", spec.Function.Parameters)
	}
}

func TestAgentPollToolSpec_Exists(t *testing.T) {
	spec := agentPollToolSpec(false)
	if spec.Function.Name != AgentPollToolName {
		t.Fatalf("Name = %q, want %q", spec.Function.Name, AgentPollToolName)
	}
	if !strings.Contains(string(spec.Function.Parameters), `"job_id"`) {
		t.Fatalf("agent_poll parameters must contain job_id, got: %s", spec.Function.Parameters)
	}
}

func TestHandoffReasonAsyncRunning_Exists(t *testing.T) {
	if HandoffReasonAsyncRunning == "" {
		t.Fatal("HandoffReasonAsyncRunning must be non-empty")
	}
}

type blockingAgent struct {
	id       AgentID
	start    chan struct{}
	released chan struct{}
	result   *HandoffResult
}

func (m *blockingAgent) ID() AgentID { return m.id }
func (m *blockingAgent) Spec() AgentSpec {
	return AgentSpec{ID: m.id, Description: "blocking mock"}
}
func (m *blockingAgent) Run(ctx context.Context, _ Handoff) (*HandoffResult, error) {
	close(m.start)
	select {
	case <-m.released:
	case <-ctx.Done():
		return &HandoffResult{Summary: "(cancelled)", FinishReason: HandoffReasonCancelled}, ctx.Err()
	}
	return m.result, nil
}

func TestRunSubAgent_AsyncDispatch_RegistersAndRuns(t *testing.T) {
	a := &blockingAgent{id: AgentSub, start: make(chan struct{}), released: make(chan struct{})}
	reg := NewAgentRegistry()
	reg.Register(a)
	e := &Engine{agents: reg, isChinese: true, state: &TaskState{}}
	e.initBackgroundTasks()

	res, err := e.RunSubAgent(context.Background(), HandoffToAgentParams{
		Agent: "sub", Goal: "后台调研", Async: true,
	}, 0, "中文")
	if err != nil {
		t.Fatalf("RunSubAgent error: %v", err)
	}
	if res.Status != "ok" {
		t.Fatalf("Status = %q, want ok", res.Status)
	}
	if res.FinishReason != HandoffReasonAsyncRunning {
		t.Fatalf("FinishReason = %q, want async_running", res.FinishReason)
	}
	if !strings.Contains(res.Digest, "bg-1") {
		t.Fatalf("Digest = %q, want contain job_id bg-1", res.Digest)
	}
	select {
	case <-a.start:
	case <-time.After(2 * time.Second):
		t.Fatal("background goroutine did not start")
	}
	e.bgMu.Lock()
	n := len(e.bgTasks)
	e.bgMu.Unlock()
	if n != 1 {
		t.Fatalf("bgTasks len = %d, want 1", n)
	}
	close(a.released)
}

func TestCancelBackgroundTasks_OnRunEnd(t *testing.T) {
	a := &blockingAgent{id: AgentSub, start: make(chan struct{}), released: make(chan struct{})}
	reg := NewAgentRegistry()
	reg.Register(a)
	e := &Engine{agents: reg, isChinese: true, state: &TaskState{}}
	e.initBackgroundTasks()

	_, err := e.RunSubAgent(context.Background(), HandoffToAgentParams{
		Agent: "sub", Goal: "后台任务", Async: true,
	}, 0, "中文")
	if err != nil {
		t.Fatalf("RunSubAgent error: %v", err)
	}
	<-a.start // 确保 goroutine 已启动并阻塞

	e.cancelBackgroundTasks()

	e.bgMu.Lock()
	n := len(e.bgTasks)
	e.bgMu.Unlock()
	if n != 0 {
		t.Fatalf("bgTasks len after cancel = %d, want 0", n)
	}
}
