package engine

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestAgentPollToolSpec_Registered(t *testing.T) {
	specs := (&Engine{tools: stubToolExecutor{}}).toolSpecsWithHandoff()
	found := false
	for _, s := range specs {
		if s.Function.Name == AgentPollToolName {
			found = true
		}
	}
	if !found {
		t.Fatal("toolSpecsWithHandoff must include agent_poll")
	}
}

func TestProcessAgentPollCalls_StatusFlow(t *testing.T) {
	reg := NewAgentRegistry()
	reg.Register(&mockAgentForHandoff{id: AgentSub, result: &HandoffResult{
		Summary: "结果A", FinishReason: HandoffReasonCompleted,
	}})
	e := &Engine{agents: reg, isChinese: true, state: &TaskState{}}
	e.initBackgroundTasks()

	_, err := e.RunSubAgent(context.Background(), HandoffToAgentParams{
		Agent: "sub", Goal: "g", Async: true,
	}, 0, "中文")
	if err != nil {
		t.Fatalf("RunSubAgent error: %v", err)
	}
	var jobID string
	e.bgMu.Lock()
	for id := range e.bgTasks {
		jobID = id
	}
	e.bgMu.Unlock()
	if jobID == "" {
		t.Fatal("no background task registered")
	}

	// 等待后台完成（mock 同步返回，结果应立即可读）。
	// 注意：不能用 <-task.result 消费结果——那会让 processAgentPollCalls
	// 永远看到空 channel 而报 "still running"。改用 len 探测，不消费。
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		e.bgMu.Lock()
		task := e.bgTasks[jobID]
		e.bgMu.Unlock()
		if task != nil && len(task.result) > 0 {
			goto done
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("background result never arrived")
done:

	// agent_poll 应返回 done 并移除任务。
	msgs := e.processAgentPollCalls([]ToolCallRequest{{
		ID: "tc1", Name: AgentPollToolName, Input: mustJSON(map[string]string{"job_id": jobID}),
	}})
	if len(msgs) != 1 {
		t.Fatalf("processAgentPollCalls returned %d messages, want 1", len(msgs))
	}
	if !strings.Contains(msgs[0].Content, "done") || !strings.Contains(msgs[0].Content, "结果A") {
		t.Errorf("poll done message = %q, want contain done + 结果A", msgs[0].Content)
	}
	e.bgMu.Lock()
	_, stillThere := e.bgTasks[jobID]
	e.bgMu.Unlock()
	if stillThere {
		t.Error("job should be removed after done poll")
	}
}

func TestProcessAgentPollCalls_NotFound(t *testing.T) {
	e := &Engine{isChinese: true, state: &TaskState{}}
	msgs := e.processAgentPollCalls([]ToolCallRequest{{
		ID: "tc1", Name: AgentPollToolName, Input: mustJSON(map[string]string{"job_id": "bg-999"}),
	}})
	if len(msgs) != 1 {
		t.Fatalf("got %d messages, want 1", len(msgs))
	}
	if !strings.Contains(msgs[0].Content, "not found") {
		t.Errorf("not-found message = %q, want contain 'not found'", msgs[0].Content)
	}
}

func TestBackgroundJobsPinnedInjection(t *testing.T) {
	e := &Engine{isChinese: true, state: &TaskState{}}
	e.initBackgroundTasks()
	e.bgMu.Lock()
	e.bgTasks["bg-1"] = &bgTask{id: "bg-1", agent: "researcher", goal: "调研缓存"}
	e.bgMu.Unlock()

	e.pendingPinnedMessages = nil
	e.injectBackgroundJobsSummary()
	if len(e.pendingPinnedMessages) != 1 {
		t.Fatalf("pinned messages = %d, want 1", len(e.pendingPinnedMessages))
	}
	if !strings.Contains(e.pendingPinnedMessages[0], "bg-1") || !strings.Contains(e.pendingPinnedMessages[0], "调研缓存") {
		t.Errorf("pinned summary = %q, want contain bg-1 + goal", e.pendingPinnedMessages[0])
	}
}
