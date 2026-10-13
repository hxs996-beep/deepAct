package engine

import "fmt"

// todoNudgeMessage is the task_complete tool response when completion is deferred
// because the step panel is stale (see the task_complete interception in
// turn.go). It is the channel the model reads, so it names the exact statuses to
// fix and the call to repeat.
func todoNudgeMessage(inProgress int) string {
	return fmt.Sprintf("Completion deferred: your step panel still lists %d step(s) as in_progress. Send the full snapshot first — completed for the steps that are finished, pending for the ones you deliberately left — then call task_complete again.", inProgress)
}
