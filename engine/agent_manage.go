package engine

// This file implements the Run-scoped sub-agent management surface: the main
// agent's ability to observe, wait for, message, interrupt and close the
// background jobs it dispatched. All of them are ENGINE built-ins intercepted
// in executeTurn (like agent_poll / agent_resume): they never enter the tool
// executor, and sub-agents never see them (their tool set is filtered in
// sub_agent.go). Every intercepted call MUST get exactly one tool response, or
// the next request breaks the assistant(tool_calls) → tool contract.

// --- Job view primitives -----------------------------------------------------
// A bgTask's completion is a SIGNAL (the done channel), not the state field:
// a finished-but-uncollected job keeps state == bgStateRunning so the async
// outstanding cap still holds its slot until agent_poll consumes the result.

// channelClosed reports whether ch has been closed, without consuming a value.
// A nil channel is never closed.
func channelClosed(ch <-chan struct{}) bool {
	if ch == nil {
		return false
	}
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

// agentJobSettled reports whether waiting on a job is pointless: it published a
// result, or it is suspended waiting for the user.
//
// Callers MUST hold bgMu: t.state is rewritten by background goroutines under
// that lock (suspendExistingJob / takeLeafForResume / takeParentForCascade /
// finishResumedJob).
func agentJobSettled(t *bgTask) bool {
	if t == nil {
		return true
	}
	return t.state == bgStateAwaitingUser || channelClosed(t.done)
}
