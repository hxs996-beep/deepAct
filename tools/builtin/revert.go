package builtin

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/deepact/deepact/artifact"
	"github.com/deepact/deepact/tools"
)

type RevertTool struct{}

func NewRevertTool() *RevertTool {
	return &RevertTool{}
}

func (t *RevertTool) Spec() tools.ToolSpec {
	return tools.ToolSpec{
		Name:        "revert",
		Description: "Revert a file to a previous version stored in the artifact store. The ref comes from the 'backup:' field in a previous edit tool result. Use this to undo an edit when the result is incorrect.",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"Path to the file"},"ref":{"type":"string","description":"Artifact ref (sha256:xxx) from a previous edit backup"}},"required":["path","ref"]}`),
	}
}

type revertInput struct {
	Path string `json:"path"`
	Ref  string `json:"ref"`
}

func (t *RevertTool) Run(ctx tools.ToolContext, input json.RawMessage) (tools.ToolResultEnvelope, error) {
	var payload revertInput
	if err := json.Unmarshal(input, &payload); err != nil {
		return tools.ToolResultEnvelope{Status: tools.StatusError, Digest: fmt.Sprintf("invalid input: %v", err)}, err
	}
	payload.Path = strings.TrimSpace(payload.Path)
	payload.Ref = strings.TrimSpace(payload.Ref)
	if payload.Path == "" || payload.Ref == "" {
		err := errors.New("path and ref are required")
		return tools.ToolResultEnvelope{Status: tools.StatusError, Digest: err.Error()}, err
	}

	safePath, err := resolveSafePath(ctx.WorkDir, payload.Path)
	if err != nil {
		return tools.ToolResultEnvelope{Status: tools.StatusError, Digest: err.Error()}, err
	}

	if ctx.ArtifactDir == "" {
		err := errors.New("artifact store not configured")
		return tools.ToolResultEnvelope{Status: tools.StatusError, Digest: err.Error()}, err
	}

	store, err := artifact.New(ctx.ArtifactDir)
	if err != nil {
		return tools.ToolResultEnvelope{Status: tools.StatusError, Digest: fmt.Sprintf("open artifact store: %v", err)}, err
	}

	// Load the original content from artifact
	original, err := store.Load(payload.Ref)
	if err != nil {
		return tools.ToolResultEnvelope{Status: tools.StatusError, Digest: fmt.Sprintf("load artifact: %v", err)}, err
	}

	// Write the original content back
	if err := os.WriteFile(safePath, original, 0o644); err != nil {
		return tools.ToolResultEnvelope{Status: tools.StatusError, Digest: fmt.Sprintf("write file: %v", err)}, err
	}

	digest := fmt.Sprintf("reverted %s from artifact %s (%d bytes)", payload.Path, payload.Ref, len(original))
	return tools.ToolResultEnvelope{Status: tools.StatusOK, Digest: digest}, nil
}
