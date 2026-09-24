package builtin

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/deepact/deepact/artifact"
	"github.com/deepact/deepact/tools"
)

// ArtifactTool reads the full content of an artifact stored by a previous tool
// result. bash/fetch/web_search store oversized output in the artifact store and
// report a ref in their result; without this tool that ref is a dead end — the
// model is told "full output in artifact" but has no way to read it.
type ArtifactTool struct{}

func NewArtifactTool() *ArtifactTool {
	return &ArtifactTool{}
}

func (t *ArtifactTool) Spec() tools.ToolSpec {
	return tools.ToolSpec{
		Name: "artifact",
		Description: "Read the full content of an artifact stored by a previous tool result. When a tool output is too " +
			"large (truncated bash output, fetched page source, web_search results), the result carries a ref " +
			"(sha256:xxx) — pass that ref here to read the stored content. Large artifacts support offset/limit " +
			"for paging through them.",
		Parameters: json.RawMessage(`{"type":"object","properties":{"ref":{"type":"string","description":"Artifact ref (sha256:xxx) from a previous tool result"},"offset":{"type":"integer","description":"Starting line number (1-based)"},"limit":{"type":"integer","description":"Max lines to read"}},"required":["ref"]}`),
	}
}

type artifactInput struct {
	Ref    string `json:"ref"`
	Offset int    `json:"offset"`
	Limit  int    `json:"limit"`
}

func (t *ArtifactTool) Run(ctx tools.ToolContext, input json.RawMessage) (tools.ToolResultEnvelope, error) {
	var payload artifactInput
	if err := json.Unmarshal(input, &payload); err != nil {
		return tools.ToolResultEnvelope{Status: tools.StatusError, Digest: fmt.Sprintf("invalid input: %v", err)}, err
	}
	payload.Ref = strings.TrimSpace(payload.Ref)
	if payload.Ref == "" {
		err := errors.New("ref is required")
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
	if !store.Exists(payload.Ref) {
		err := fmt.Errorf("artifact not found: %s", payload.Ref)
		return tools.ToolResultEnvelope{Status: tools.StatusError, Digest: err.Error()}, err
	}
	data, err := store.Load(payload.Ref)
	if err != nil {
		return tools.ToolResultEnvelope{Status: tools.StatusError, Digest: fmt.Sprintf("load artifact: %v", err)}, err
	}

	content := string(data)
	if payload.Offset > 0 || payload.Limit > 0 {
		content = sliceContentLines(content, payload.Offset, payload.Limit)
	}
	return tools.ToolResultEnvelope{Status: tools.StatusOK, Digest: truncateContent(content)}, nil
}

// sliceContentLines returns lines [offset, offset+limit) of content, 1-based.
// offset 0 means from the first line; limit 0 means to the end.
func sliceContentLines(content string, offset, limit int) string {
	lines := strings.Split(content, "\n")
	start := 0
	if offset > 0 {
		start = offset - 1
	}
	if start >= len(lines) {
		return ""
	}
	end := len(lines)
	if limit > 0 && start+limit < end {
		end = start + limit
	}
	return strings.Join(lines[start:end], "\n")
}
