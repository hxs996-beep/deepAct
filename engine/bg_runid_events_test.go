package engine

import (
	"context"
	"sync"
	"testing"
	"time"
)

// TestBackgroundDoneCarriesJobID: 后台完成的 agent_done 必须带自己的 job id，
// 否则 UI 只能按角色名配到"第一个还在跑的同类"，5 个并发 researcher 会互相
// 顶替。
func TestBackgroundDoneCarriesJobID(t *testing.T) {
	a := &mockAgentForHandoff{id: AgentSub, result: &HandoffResult{
		Summary: "后台调研完成", FinishReason: HandoffReasonCompleted,
	}}
	reg := NewAgentRegistry()
	reg.Register(a)
	var events []ProgressEvent
	var eventsMu sync.Mutex
	e := &Engine{
		agents:    reg,
		isChinese: true,
		state:     &TaskState{},
		config: EngineConfig{OnProgress: func(ev ProgressEvent) {
			eventsMu.Lock()
			events = append(events, ev)
			eventsMu.Unlock()
		}},
	}
	e.initBackgroundTasks()

	if _, err := e.RunSubAgent(context.Background(), HandoffToAgentParams{
		Agent: "sub", Goal: "调研", Async: true, Step: "dir-1",
	}, 0, "中文"); err != nil {
		t.Fatalf("RunSubAgent error: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		eventsMu.Lock()
		snapshot := append([]ProgressEvent(nil), events...)
		eventsMu.Unlock()
		for _, ev := range snapshot {
			if ev.Type == "agent_done" && ev.RunID == "bg-1" {
				if ev.Step != "dir-1" {
					t.Errorf("agent_done Step = %q, want dir-1", ev.Step)
				}
				if ev.Status != "ok" {
					t.Errorf("agent_done Status = %q, want ok", ev.Status)
				}
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	eventsMu.Lock()
	snapshot := append([]ProgressEvent(nil), events...)
	eventsMu.Unlock()
	t.Fatalf("no agent_done with RunID bg-1, got %+v", snapshot)
}

// TestCloseAgentEmitsTerminalEventWithRunID: 关闭任务必须用被关闭任务的 job id
// 收尾，否则 UI 里那一行永远停在 running。
func TestCloseAgentEmitsTerminalEventWithRunID(t *testing.T) {
	a := &blockingAgent{id: AgentSub, start: make(chan struct{}), released: make(chan struct{})}
	reg := NewAgentRegistry()
	reg.Register(a)
	var events []ProgressEvent
	e := &Engine{
		agents:    reg,
		isChinese: true,
		state:     &TaskState{},
		config: EngineConfig{OnProgress: func(ev ProgressEvent) {
			events = append(events, ev)
		}},
	}
	e.initBackgroundTasks()

	if _, err := e.RunSubAgent(context.Background(), HandoffToAgentParams{
		Agent: "sub", Goal: "调研", Async: true, Step: "dir-2",
	}, 0, "中文"); err != nil {
		t.Fatalf("RunSubAgent error: %v", err)
	}
	<-a.start

	if msg := e.closeAgentJob("bg-1"); msg == "" {
		t.Fatal("closeAgentJob returned an empty message")
	}

	found := false
	for _, ev := range events {
		if ev.Type == "agent_done" && ev.RunID == "bg-1" {
			found = true
			if ev.Status != "cancelled" {
				t.Errorf("agent_done Status = %q, want cancelled", ev.Status)
			}
			if ev.Step != "dir-2" {
				t.Errorf("agent_done Step = %q, want dir-2", ev.Step)
			}
		}
	}
	if !found {
		t.Fatalf("no terminal agent_done for bg-1, got %+v", events)
	}
}

// TestBackgroundDoneReportsFailure: 阻塞/失败的子代理不能渲染成成功。
func TestBackgroundDoneReportsFailure(t *testing.T) {
	if got := handoffResultStatus(&HandoffResult{Blocked: true, BlockedBy: "sub_agent_error"}); got != "error" {
		t.Errorf("blocked result status = %q, want error", got)
	}
	if got := handoffResultStatus(&HandoffResult{BlockedBy: "cancelled"}); got != "cancelled" {
		t.Errorf("cancelled result status = %q, want cancelled", got)
	}
	if got := handoffResultStatus(nil); got != "error" {
		t.Errorf("nil result status = %q, want error", got)
	}
	if got := handoffResultStatus(&HandoffResult{FinishReason: HandoffReasonCompleted}); got != "ok" {
		t.Errorf("completed result status = %q, want ok", got)
	}
}

// TestSyncSuspensionEmitsStartForTheParkedJob: 同步委派挂起后会注册一个新 handle
// （bg-N）交给模型去 agent_resume，但 UI 从没见过这个 handle 的 start。必须在
// 注册处补一条 agent_start，否则恢复完成时的 agent_done 会因 RunID 不匹配而
// 在面板里多出一行。
func TestSyncSuspensionEmitsStartForTheParkedJob(t *testing.T) {
	a := &mockAgentForHandoff{id: AgentSub, result: &HandoffResult{
		Summary:      "子代理在等你回答",
		FinishReason: HandoffReasonAwaitingUser,
		Suspended:    &SuspendedRun{Question: "选哪个？"},
	}}
	reg := NewAgentRegistry()
	reg.Register(a)
	var events []ProgressEvent
	var eventsMu sync.Mutex
	e := &Engine{
		agents:    reg,
		isChinese: true,
		state:     &TaskState{},
		config: EngineConfig{OnProgress: func(ev ProgressEvent) {
			eventsMu.Lock()
			events = append(events, ev)
			eventsMu.Unlock()
		}},
	}
	e.initBackgroundTasks()

	// 同步：不传 Async
	res, err := e.RunSubAgent(context.Background(), HandoffToAgentParams{
		Agent: "sub", Goal: "调研", Step: "dir-7",
	}, 0, "中文")
	if err != nil {
		t.Fatalf("RunSubAgent error: %v", err)
	}
	if res.RunID == "" {
		t.Fatal("a registered suspension must expose its handle")
	}

	eventsMu.Lock()
	snapshot := append([]ProgressEvent(nil), events...)
	eventsMu.Unlock()

	found := false
	for _, ev := range snapshot {
		if ev.Type != "agent_start" || ev.RunID != res.RunID {
			continue
		}
		found = true
		if ev.Step != "dir-7" {
			t.Errorf("parked agent_start Step = %q, want dir-7", ev.Step)
		}
	}
	if !found {
		t.Fatalf("no agent_start for the parked handle %q, got %+v", res.RunID, snapshot)
	}
}

// TestBlockedSyncHandoffReportsError: 被阻塞的同步子代理不能报成 ok —— 否则
// 面板用 ✓ 渲染失败，而同样结局的异步子代理渲染成 ✗。挂起（等用户回答）不是
// 失败，必须保持 ok。
func TestBlockedSyncHandoffReportsError(t *testing.T) {
	a := &mockAgentForHandoff{id: AgentSub, result: &HandoffResult{
		Summary:      "子代理被阻塞",
		Blocked:      true,
		BlockedBy:    "sub_agent_error",
		FinishReason: HandoffReasonError,
	}}
	reg := NewAgentRegistry()
	reg.Register(a)
	e := &Engine{agents: reg, isChinese: true, state: &TaskState{}}
	e.initBackgroundTasks()

	res, err := e.RunSubAgent(context.Background(), HandoffToAgentParams{
		Agent: "sub", Goal: "调研",
	}, 0, "中文")
	if err != nil {
		t.Fatalf("RunSubAgent error: %v", err)
	}
	if res.Status != "error" {
		t.Errorf("blocked handoff Status = %q, want error", res.Status)
	}
	if got := handoffEventStatus(res); got != "error" {
		t.Errorf("panel status = %q, want error", got)
	}

	// 挂起路径：Status ok，且 FinishReason 是 awaiting_user —— 保持 ok。
	if got := handoffEventStatus(ToolResult{Status: "ok", FinishReason: HandoffReasonAwaitingUser}); got != "ok" {
		t.Errorf("parked run status = %q, want ok", got)
	}
	// 取消仍然优先。
	if got := handoffEventStatus(ToolResult{Status: "cancelled"}); got != "cancelled" {
		t.Errorf("cancelled status = %q, want cancelled", got)
	}
}
