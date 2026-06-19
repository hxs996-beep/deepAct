package builtin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/deepact/deepact/artifact"
	"github.com/deepact/deepact/tools"
)

const bashOutputLimit = 10240

type BashTool struct {
	blocked map[string]string
}

func NewBashTool() *BashTool {
	return &BashTool{blocked: defaultBlocklist()}
}

func defaultBlocklist() map[string]string {
	return map[string]string{
		// System-level destructive commands — always hard-blocked (defense in depth)
		"rm -rf / --no-preserve-root": "destructive: system-wide delete - blocked by policy",
		"rm -rf /*":                   "destructive: system-wide delete - blocked by policy",
		"rm -rf --no-preserve":        "destructive: rm with no-preserve-root - blocked by policy",
		":(){ :|:& };:":               "destructive: fork bomb - blocked by policy",
		":() { :|:& };:":              "destructive: fork bomb - blocked by policy",
		"dd if=/dev/sd":               "destructive: raw disk write - blocked by policy",
		"dd if=/dev/":                 "destructive: raw disk write - blocked by policy",
		"mkfs.ext":                    "destructive: filesystem creation - blocked by policy",
		"mkfs.xfs":                    "destructive: filesystem creation - blocked by policy",
		"mkfs.btrfs":                  "destructive: filesystem creation - blocked by policy",
	}
}

func NewBashToolWithBlocklist(blocklist map[string]string) *BashTool {
	copied := make(map[string]string, len(blocklist))
	for key, value := range blocklist {
		copied[key] = value
	}
	return &BashTool{blocked: copied}
}

func (t *BashTool) Spec() tools.ToolSpec {
	return tools.ToolSpec{
		Name:        "bash",
		Description: "Execute shell commands",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"command":{"type":"string"},"workdir":{"type":"string"},"timeout":{"type":"integer"}},"required":["command"]}`),
	}
}

type bashInput struct {
	Command string `json:"command"`
	WorkDir string `json:"workdir"`
	Timeout int    `json:"timeout"`
}

func (t *BashTool) Run(ctx tools.ToolContext, input json.RawMessage) (tools.ToolResultEnvelope, error) {
	var payload bashInput
	if err := json.Unmarshal(input, &payload); err != nil {
		return tools.ToolResultEnvelope{Status: tools.StatusError, Digest: fmt.Sprintf("invalid input: %v", err)}, err
	}
	payload.Command = strings.TrimSpace(payload.Command)
	if payload.Command == "" {
		err := errors.New("command is required")
		return tools.ToolResultEnvelope{Status: tools.StatusError, Digest: err.Error()}, err
	}

	if reason, blocked := t.isBlocked(payload.Command); blocked {
		err := fmt.Errorf("command blocked: %s", reason)
		return tools.ToolResultEnvelope{Status: tools.StatusError, Digest: err.Error()}, err
	}

	timeout := 30 * time.Second
	if payload.Timeout > 0 {
		timeout = time.Duration(payload.Timeout) * time.Second
	}
	execCtx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	command, args := shellCommand(payload.Command)
	cmd := exec.CommandContext(execCtx, command, args...)
	if payload.WorkDir != "" {
		cmd.Dir = payload.WorkDir
	} else if ctx.WorkDir != "" {
		cmd.Dir = ctx.WorkDir
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	exitCode := exitCodeFromErr(err)
	stdoutStr, stdoutTruncated := truncateOutput(stdout.Bytes())
	stderrStr, stderrTruncated := truncateOutput(stderr.Bytes())

	artifactRef := storeFullOutput(ctx.ArtifactDir, stdout.Bytes(), stderr.Bytes(), stdoutTruncated || stderrTruncated)

	if stdoutTruncated {
		stdoutStr += " [truncated, full output in artifact]"
	}
	if stderrTruncated {
		stderrStr += " [truncated, full output in artifact]"
	}

	digest := strings.TrimSpace(stdoutStr)
	if digest == "" {
		digest = strings.TrimSpace(stderrStr)
	}
	if digest == "" {
		digest = "command completed"
	}

	if err != nil {
		if execCtx.Err() == context.DeadlineExceeded {
			err = fmt.Errorf("command timed out after %s", timeout)
			digest = err.Error()
		} else {
			err = fmt.Errorf("command failed: %w", err)
		}
	}

	return tools.ToolResultEnvelope{
		Status:      statusFromErr(err),
		Digest:      digest,
		ExitCode:    &exitCode,
		ArtifactRef: artifactRef,
	}, err
}

func (t *BashTool) isBlocked(command string) (string, bool) {
	lowered := strings.ToLower(command)
	for pattern, reason := range t.blocked {
		if pattern == "" {
			continue
		}
		if strings.Contains(lowered, strings.ToLower(pattern)) {
			if reason == "" {
				reason = "blocked by policy"
			}
			return reason, true
		}
	}
	return "", false
}

func shellCommand(command string) (string, []string) {
	if runtime.GOOS == "windows" {
		return "cmd", []string{"/C", command}
	}
	return "sh", []string{"-c", command}
}

// storeFullOutput attempts to store the full command output in the artifact store.
// Returns the artifact ref if stored successfully, empty string otherwise.
// Only stores when output was truncated or artifact dir is configured.
func storeFullOutput(artifactDir string, stdout, stderr []byte, truncated bool) string {
	if artifactDir == "" || !truncated {
		return ""
	}
	store, err := artifact.New(artifactDir)
	if err != nil {
		return ""
	}
	var combined []byte
	if len(stdout) > 0 {
		combined = append(combined, stdout...)
	}
	if len(stderr) > 0 {
		if len(combined) > 0 {
			combined = append(combined, '\n')
		}
		combined = append(combined, stderr...)
	}
	ref, _, err := store.StoreWithRedaction(combined)
	if err != nil {
		return ""
	}
	return ref
}

func truncateOutput(data []byte) (string, bool) {
	// Normalize Windows \r\n line endings — \r in terminal causes cursor to return
	// to column 0, overwriting rendered content and making lines "invisible".
	data = bytes.ReplaceAll(data, []byte("\r"), []byte{})
	if len(data) <= bashOutputLimit {
		return string(data), false
	}
	return string(data[:bashOutputLimit]), true
}

func statusFromErr(err error) string {
	if err != nil {
		return tools.StatusError
	}
	return tools.StatusOK
}

func exitCodeFromErr(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}
