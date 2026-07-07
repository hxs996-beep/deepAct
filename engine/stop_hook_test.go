package engine

import (
	"strings"
	"testing"
)

func TestZeroToolCallHook_BlocksWhenNoToolCalls(t *testing.T) {
	hook := &ZeroToolCallHook{MaxRetries: 3}
	result := hook.Check(StopHookContext{
		RunToolCallCount:   0,
		StopHookRetryCount: 0,
		IsChinese:          true,
	})
	if !result.Block {
		t.Errorf("expected Block=true when runToolCallCount=0")
	}
	if result.Reason != "zero_tool_calls" {
		t.Errorf("expected Reason='zero_tool_calls', got %q", result.Reason)
	}
	if result.Message == "" {
		t.Errorf("expected non-empty nudge Message")
	}
}

func TestZeroToolCallHook_PassesWhenToolsCalled(t *testing.T) {
	hook := &ZeroToolCallHook{MaxRetries: 3}
	result := hook.Check(StopHookContext{
		RunToolCallCount:   1,
		StopHookRetryCount: 0,
		IsChinese:          true,
	})
	if result.Block {
		t.Errorf("expected Block=false when runToolCallCount>0")
	}
}

func TestZeroToolCallHook_PassesAfterMaxRetries(t *testing.T) {
	hook := &ZeroToolCallHook{MaxRetries: 3}
	result := hook.Check(StopHookContext{
		RunToolCallCount:   0,
		StopHookRetryCount: 3,
		IsChinese:          true,
	})
	if result.Block {
		t.Errorf("expected Block=false when retryCount>=maxRetries")
	}
}

func TestZeroToolCallHook_DefaultMaxRetries(t *testing.T) {
	hook := &ZeroToolCallHook{} // MaxRetries=0 → default 3
	result := hook.Check(StopHookContext{
		RunToolCallCount:   0,
		StopHookRetryCount: 2,
		IsChinese:          true,
	})
	if !result.Block {
		t.Errorf("expected Block=true when retryCount=2 < default maxRetries=3")
	}
}

func TestZeroToolCallHook_NegativeMaxRetries(t *testing.T) {
	hook := &ZeroToolCallHook{MaxRetries: -1} // negative → default 3
	result := hook.Check(StopHookContext{
		RunToolCallCount:   0,
		StopHookRetryCount: 2,
		IsChinese:          true,
	})
	if !result.Block {
		t.Errorf("expected Block=true when MaxRetries=-1 defaults to 3 and retryCount=2 < 3")
	}
}

func TestZeroToolCallHook_EnglishMessage(t *testing.T) {
	hook := &ZeroToolCallHook{MaxRetries: 3}
	result := hook.Check(StopHookContext{
		RunToolCallCount:   0,
		StopHookRetryCount: 0,
		IsChinese:          false,
	})
	if !result.Block {
		t.Errorf("expected Block=true")
	}
	if result.Message == "" {
		t.Errorf("expected non-empty English nudge Message")
	}
	if strings.ContainsAny(result.Message, "请完成目标描述") {
		t.Errorf("expected English message, got Chinese: %q", result.Message)
	}
}

func TestRunStopHooks_FirstBlockingResult(t *testing.T) {
	e := &Engine{
		stopHooks: []StopHook{
			&ZeroToolCallHook{MaxRetries: 3},
		},
	}
	result := e.runStopHooks(StopHookContext{
		RunToolCallCount:   0,
		StopHookRetryCount: 0,
		IsChinese:          true,
	})
	if !result.Block {
		t.Errorf("expected Block=true when runToolCallCount=0")
	}
}

func TestRunStopHooks_NoHooksRegistered(t *testing.T) {
	e := &Engine{}
	result := e.runStopHooks(StopHookContext{
		RunToolCallCount: 0,
	})
	if result.Block {
		t.Errorf("expected Block=false when no hooks registered")
	}
}

func TestRunStopHooks_HookPassesThrough(t *testing.T) {
	e := &Engine{
		stopHooks: []StopHook{
			&ZeroToolCallHook{MaxRetries: 3},
		},
	}
	result := e.runStopHooks(StopHookContext{
		RunToolCallCount: 5,
	})
	if result.Block {
		t.Errorf("expected Block=false when runToolCallCount>0")
	}
}

func TestSetStopHooks(t *testing.T) {
	e := &Engine{}
	e.SetStopHooks([]StopHook{&ZeroToolCallHook{MaxRetries: 3}})
	if len(e.stopHooks) != 1 {
		t.Errorf("expected 1 hook registered, got %d", len(e.stopHooks))
	}
}
