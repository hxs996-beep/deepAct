package builtin

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/deepact/deepact/tools"
)

// TestBashTool_Run_CanceledContext guards the run-cancellation contract used by
// the engine: when the run context is propagated (ToolContext.Ctx), cancelling
// it must terminate an in-flight command instead of letting it outlive the Run.
// Without this, a cancelled run could append a stale tool result into a later
// run's history and corrupt the message sequence.
func TestBashTool_Run_CanceledContext(t *testing.T) {
	tool := NewBashTool()

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()

	input, _ := json.Marshal(bashInput{Command: "sleep 30", Timeout: 60})
	start := time.Now()
	result, _ := tool.Run(tools.ToolContext{Ctx: ctx, WorkDir: t.TempDir()}, input)
	elapsed := time.Since(start)

	if elapsed > 10*time.Second {
		t.Fatalf("Run did not honor context cancellation: took %s", elapsed)
	}
	if result.Status != tools.StatusError {
		t.Errorf("status = %q, want %q after cancellation", result.Status, tools.StatusError)
	}
}
