// Package mcp implements a Model Context Protocol (MCP) client for discovering
// and calling tools exposed by MCP servers.
//
// MCP is an open protocol (https://spec.modelcontextprotocol.io) that standardizes
// how AI applications interact with external tools and data sources. This package
// supports the client side: connecting to MCP servers via stdio transport,
// performing the initialize handshake, listing tools, and calling them.
package mcp

import "encoding/json"

// ServerInfo identifies an MCP server after initialization.
type ServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// Tool describes a tool exposed by an MCP server.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// ToolResult is the result of calling an MCP tool.
type ToolResult struct {
	Content string `json:"content"`
	IsError bool   `json:"isError"`
}

// predefined errors returned by the MCP client
var (
	ErrNotInitialized = &MCPError{"server not initialized"}
	ErrToolNotFound   = &MCPError{"tool not found"}
	ErrMethodNotFound = &MCPError{"method not found"}
)

// MCPError represents a protocol-level error.
type MCPError struct {
	msg string
}

func (e *MCPError) Error() string { return e.msg }

// jsonRPCRequest is the JSON-RPC 2.0 request envelope.
type jsonRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int             `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// jsonRPCResponse is the JSON-RPC 2.0 response envelope.
type jsonRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int             `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *jsonRPCError   `json:"error,omitempty"`
}

// jsonRPCError represents a JSON-RPC protocol error.
type jsonRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}
