package mcp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
)

// Transport is the interface for communicating with an MCP server.
// Implementations include stdio (child process stdin/stdout) and
// potentially HTTP/SSE in the future.
type Transport interface {
	// SendRequest sends a JSON-RPC request to the MCP server and
	// returns the result payload (the "result" field of the response).
	// Method-level errors (JSON-RPC error responses) are returned as error.
	SendRequest(method string, params json.RawMessage) (json.RawMessage, error)

	// Close shuts down the transport connection.
	Close() error
}

// StdioTransport implements the Transport interface over a pair of
// io.ReadCloser (stdout from server) and io.WriteCloser (stdin to server).
// It uses JSON-RPC 2.0 for message framing with newline-delimited JSON (NDJSON).
type StdioTransport struct {
	reader    *bufio.Reader
	writer    io.WriteCloser
	readerCloser io.Closer
	reqID     atomic.Int64
	mu        sync.Mutex // serializes writes
	closed    atomic.Bool
}

// NewStdioTransport creates a transport that reads from r and writes to w.
// r is typically the server's stdout (the client reads responses from here),
// w is typically the server's stdin (the client writes requests here).
func NewStdioTransport(w io.WriteCloser, r io.ReadCloser) *StdioTransport {
	return &StdioTransport{
		reader:       bufio.NewReader(r),
		writer:       w,
		readerCloser: r,
	}
}

// SendRequest sends a JSON-RPC 2.0 request and waits for the response.
func (t *StdioTransport) SendRequest(method string, params json.RawMessage) (json.RawMessage, error) {
	if t.closed.Load() {
		return nil, fmt.Errorf("transport closed")
	}

	id := int(t.reqID.Add(1))

	req := jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      id,
		Method:  method,
		Params:  params,
	}

	data, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	t.mu.Lock()
	_, err = t.writer.Write(data)
	if err == nil {
		_, err = t.writer.Write([]byte("\n"))
	}
	t.mu.Unlock()
	if err != nil {
		return nil, fmt.Errorf("write request: %w", err)
	}

	// Read response: NDJSON — one JSON object per line
	line, err := t.reader.ReadBytes('\n')
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	var resp jsonRPCResponse
	if err := json.Unmarshal(line, &resp); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}

	if resp.Error != nil {
		return nil, &MCPError{msg: fmt.Sprintf("JSON-RPC error %d: %s", resp.Error.Code, resp.Error.Message)}
	}

	return resp.Result, nil
}

// Close shuts down the transport.
func (t *StdioTransport) Close() error {
	t.closed.Store(true)
	wErr := t.writer.Close()
	rErr := t.readerCloser.Close()
	if wErr != nil {
		return wErr
	}
	return rErr
}
