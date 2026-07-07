package engine

import "testing"

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

func TestZeroToolCallHook_EnglishMessage(t *testing.T) {
	hook := &ZeroToolCallHook{MaxRetries: 3}
	result := hook.Check(StopHookContext{
		RunToolCallCount:   0,
		StopHookRetryCount: 0,
		IsChinese:          false,
	})
	if result.Block && result.Message == "" {
		t.Errorf("expected non-empty English nudge Message")
	}
}
