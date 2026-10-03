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
