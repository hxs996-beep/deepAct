package engine

import (
	"context"
	"testing"
)

// stubStopHook is a controllable StopHook stub for framework tests.
type stubStopHook struct {
	result StopHookResult
}

func (s stubStopHook) Check(context.Context, StopHookContext) StopHookResult {
	return s.result
}

func TestRunStopHooks_FirstBlockingResult(t *testing.T) {
	e := &Engine{
		stopHooks: []StopHook{
			stubStopHook{result: StopHookResult{Block: true, Message: "nudge", Reason: "test"}},
		},
	}
	result := e.runStopHooks(context.Background(), StopHookContext{})
	if !result.Block {
		t.Errorf("expected Block=true when a registered hook blocks")
	}
	if result.Message != "nudge" {
		t.Errorf("expected Message='nudge', got %q", result.Message)
	}
	if result.Reason != "test" {
		t.Errorf("expected Reason='test', got %q", result.Reason)
	}
}

func TestRunStopHooks_NoHooksRegistered(t *testing.T) {
	e := &Engine{}
	result := e.runStopHooks(context.Background(), StopHookContext{
		RunToolCallCount: 0,
	})
	if result.Block {
		t.Errorf("expected Block=false when no hooks registered")
	}
	if result.Exhausted {
		t.Errorf("expected Exhausted=false when no hooks registered")
	}
}

func TestRunStopHooks_HookPassesThrough(t *testing.T) {
	e := &Engine{
		stopHooks: []StopHook{
			stubStopHook{result: StopHookResult{}},
		},
	}
	result := e.runStopHooks(context.Background(), StopHookContext{
		RunToolCallCount: 5,
	})
	if result.Block {
		t.Errorf("expected Block=false when hook passes through")
	}
}

func TestRunStopHooks_AwaitUserPriority(t *testing.T) {
	// AwaitUser takes priority over continuing to later hooks.
	e := &Engine{
		stopHooks: []StopHook{
			stubStopHook{result: StopHookResult{AwaitUser: true, Reason: "question_to_user"}},
			stubStopHook{result: StopHookResult{Block: true, Message: "later", Reason: "later_hook"}},
		},
	}
	result := e.runStopHooks(context.Background(), StopHookContext{})
	if !result.AwaitUser {
		t.Errorf("expected AwaitUser=true to take priority, got AwaitUser=%v Block=%v", result.AwaitUser, result.Block)
	}
	if result.Reason != "question_to_user" {
		t.Errorf("expected Reason='question_to_user', got %q", result.Reason)
	}
}

func TestRunStopHooks_ExhaustedAccumulates(t *testing.T) {
	e := &Engine{
		stopHooks: []StopHook{
			stubStopHook{result: StopHookResult{Exhausted: true}},
		},
	}
	result := e.runStopHooks(context.Background(), StopHookContext{})
	if result.Block {
		t.Errorf("expected Block=false when hook only exhausts")
	}
	if !result.Exhausted {
		t.Errorf("expected Exhausted=true when a hook reports exhaustion")
	}
}

func TestSetStopHooks(t *testing.T) {
	e := &Engine{}
	e.SetStopHooks([]StopHook{stubStopHook{result: StopHookResult{}}})
	if len(e.stopHooks) != 1 {
		t.Errorf("expected 1 hook registered, got %d", len(e.stopHooks))
	}
}

func TestSetStopHooks_EmptyClears(t *testing.T) {
	e := &Engine{}
	e.SetStopHooks([]StopHook{stubStopHook{result: StopHookResult{}}})
	e.SetStopHooks(nil)
	if len(e.stopHooks) != 0 {
		t.Errorf("expected 0 hooks after clearing, got %d", len(e.stopHooks))
	}
}
