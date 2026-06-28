package builtin

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/deepact/deepact/tools"
)

type WriteTool struct{}

func NewWriteTool() *WriteTool {
	return &WriteTool{}
}

func (t *WriteTool) Spec() tools.ToolSpec {
	return tools.ToolSpec{
		Name:        "write",
		Description: "Write a file (create or overwrite)",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"},"content":{"type":"string"}},"required":["path","content"]}`),
	}
}

type writeInput struct {
	Path     string `json:"path"`
	FilePath string `json:"file_path"` // alias for path (DeepSeek sometimes emits this)
	Content  string `json:"content"`
}

func (t *WriteTool) Run(ctx tools.ToolContext, input json.RawMessage) (tools.ToolResultEnvelope, error) {
	var payload writeInput
	if err := json.Unmarshal(input, &payload); err != nil {
		return tools.ToolResultEnvelope{Status: tools.StatusError, Digest: fmt.Sprintf("invalid input: %v", err)}, err
	}
	payload.Path = strings.TrimSpace(applyFilePathAlias(payload.Path, strings.TrimSpace(payload.FilePath)))
	if payload.Path == "" {
		err := errors.New("path is required")
		return tools.ToolResultEnvelope{Status: tools.StatusError, Digest: err.Error()}, err
	}

	safePath, err := resolveSafePath(ctx.WorkDir, payload.Path)
	if err != nil {
		return tools.ToolResultEnvelope{Status: tools.StatusError, Digest: err.Error()}, err
	}

	dir := filepath.Dir(safePath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return tools.ToolResultEnvelope{Status: tools.StatusError, Digest: fmt.Sprintf("create dir: %v", err)}, err
	}

	// Read existing content for diff (if file already exists)
	var oldContent string
	if existing, err := os.ReadFile(safePath); err == nil {
		oldContent = string(existing)
	}

	if err := os.WriteFile(safePath, []byte(payload.Content), 0o644); err != nil {
		return tools.ToolResultEnvelope{Status: tools.StatusError, Digest: fmt.Sprintf("write file: %v", err)}, err
	}

	digest := fmt.Sprintf("wrote %d bytes", len(payload.Content))
	if oldContent != "" {
		relPath, _ := filepath.Rel(ctx.WorkDir, safePath)
		if relPath == "" {
			relPath = payload.Path
		}
		diff := tools.GenerateUnifiedDiff(oldContent, payload.Content, relPath)
		if diff != "" {
			digest += "\n" + diff
		}
	}
	return tools.ToolResultEnvelope{Status: tools.StatusOK, Digest: digest}, nil
}
