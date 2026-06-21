package mcp

import (
	"encoding/json"
	"fmt"

	"github.com/deepact/deepact/tools"
)

// ToolWrapper wraps an MCP tool as a deepact Tool, so it can be
// registered in the tool registry and called by the engine.
type ToolWrapper struct {
	Client    *Client // initialized MCP client
	MCPServer string // server name for tool name prefixing
	MCPTool   Tool    // the MCP tool definition
}

// Spec returns the tool specification for the engine.
// The tool name is prefixed with "<server>_" to avoid collisions.
func (w *ToolWrapper) Spec() tools.ToolSpec {
	return tools.ToolSpec{
		Name:        w.MCPServer + "_" + w.MCPTool.Name,
		Description: w.MCPTool.Description,
		Parameters:  w.MCPTool.InputSchema,
	}
}

// Run calls the MCP tool via the client and returns the result.
func (w *ToolWrapper) Run(ctx tools.ToolContext, input json.RawMessage) (tools.ToolResultEnvelope, error) {
	result, err := w.Client.CallTool(w.MCPTool.Name, input)
	if err != nil {
		return tools.ToolResultEnvelope{
			Status: tools.StatusError,
			Digest: fmt.Sprintf("MCP tool %q failed: %v", w.MCPTool.Name, err),
		}, err
	}
	if result.IsError {
		return tools.ToolResultEnvelope{
			Status: tools.StatusError,
			Digest: result.Content,
		}, nil
	}
	return tools.ToolResultEnvelope{
		Status: tools.StatusOK,
		Digest: result.Content,
	}, nil
}
