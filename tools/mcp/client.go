package mcp

import (
	"encoding/json"
	"fmt"
)

// Client manages a session with an MCP server over a Transport.
// It handles the initialize handshake and exposes methods for
// tool discovery and execution.
type Client struct {
	transport    Transport
	initialized  bool
	serverInfo   ServerInfo
}

// NewClient creates an MCP client using the given transport.
func NewClient(transport Transport) *Client {
	return &Client{transport: transport}
}

// Initialize performs the MCP initialize handshake.
// It must be called before any other method.
func (c *Client) Initialize() (*ServerInfo, error) {
	params := map[string]interface{}{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]interface{}{},
		"clientInfo": map[string]string{
			"name":    "deepact",
			"version": "1.0.0",
		},
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("marshal initialize params: %w", err)
	}

	result, err := c.transport.SendRequest("initialize", raw)
	if err != nil {
		return nil, fmt.Errorf("initialize: %w", err)
	}

	var resp struct {
		ServerInfo ServerInfo `json:"serverInfo"`
	}
	if err := json.Unmarshal(result, &resp); err != nil {
		return nil, fmt.Errorf("parse initialize response: %w", err)
	}

	c.initialized = true
	c.serverInfo = resp.ServerInfo
	return &resp.ServerInfo, nil
}

// ListTools retrieves the list of tools exposed by the MCP server.
// Initialize must have been called first.
func (c *Client) ListTools() ([]Tool, error) {
	if !c.initialized {
		return nil, ErrNotInitialized
	}

	result, err := c.transport.SendRequest("tools/list", nil)
	if err != nil {
		return nil, fmt.Errorf("tools/list: %w", err)
	}

	var resp struct {
		Tools []Tool `json:"tools"`
	}
	if err := json.Unmarshal(result, &resp); err != nil {
		return nil, fmt.Errorf("parse tools/list response: %w", err)
	}
	return resp.Tools, nil
}

// CallTool invokes a tool on the MCP server with the given arguments.
// Initialize must have been called first.
func (c *Client) CallTool(name string, arguments json.RawMessage) (*ToolResult, error) {
	if !c.initialized {
		return nil, ErrNotInitialized
	}

	params := map[string]interface{}{
		"name":      name,
		"arguments": arguments,
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("marshal call_tool params: %w", err)
	}

	result, err := c.transport.SendRequest("tools/call", raw)
	if err != nil {
		return nil, fmt.Errorf("tools/call: %w", err)
	}

	var resp struct {
		Content []toolContent `json:"content"`
		IsError bool          `json:"isError"`
	}
	if err := json.Unmarshal(result, &resp); err != nil {
		return nil, fmt.Errorf("parse tools/call response: %w", err)
	}

	// Concatenate all text content blocks
	var text string
	for _, c := range resp.Content {
		if c.Type == "text" {
			if text != "" {
				text += "\n"
			}
			text += c.Text
		}
	}

	return &ToolResult{Content: text, IsError: resp.IsError}, nil
}

// Close shuts down the client and its transport.
func (c *Client) Close() error {
	return c.transport.Close()
}

type toolContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}
