package mcp

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/deepact/deepact/tools"
)

// TestClient_Initialize tests that the MCP client can perform an initialize
// handshake with an MCP server and return the server's identification.
func TestClient_Initialize(t *testing.T) {
	// Simulate a stdio transport using pipes
	server := newMockMCPServer()
	client := NewClient(server)

	info, err := client.Initialize()
	if err != nil {
		t.Fatalf("Initialize failed: %v", err)
	}
	if info.Name != "mock-mcp" {
		t.Errorf("expected server name 'mock-mcp', got %q", info.Name)
	}
	if info.Version != "1.0.0" {
		t.Errorf("expected server version '1.0.0', got %q", info.Version)
	}
}

// TestClient_ListTools tests that the client can discover tools from the server.
func TestClient_ListTools(t *testing.T) {
	server := newMockMCPServer()
	client := NewClient(server)

	// Initialize first
	_, err := client.Initialize()
	if err != nil {
		t.Fatalf("Initialize failed: %v", err)
	}

	tools, err := client.ListTools()
	if err != nil {
		t.Fatalf("ListTools failed: %v", err)
	}
	if len(tools) == 0 {
		t.Fatal("expected at least one tool")
	}
	found := false
	for _, tool := range tools {
		if tool.Name == "echo" {
			found = true
			if tool.Description == "" {
				t.Error("expected non-empty description for echo tool")
			}
			break
		}
	}
	if !found {
		t.Error("expected to find 'echo' tool")
	}
}

// TestClient_CallTool tests that the client can call a tool and get a result.
func TestClient_CallTool(t *testing.T) {
	server := newMockMCPServer()
	client := NewClient(server)

	_, err := client.Initialize()
	if err != nil {
		t.Fatalf("Initialize failed: %v", err)
	}

	result, err := client.CallTool("echo", json.RawMessage(`{"message":"hello"}`))
	if err != nil {
		t.Fatalf("CallTool failed: %v", err)
	}
	if !strings.Contains(result.Content, "hello") {
		t.Errorf("expected response to contain 'hello', got %q", result.Content)
	}
}

// TestStdioTransport_RoundTrip tests the stdio transport's JSON-RPC message
// exchange using an in-memory channel-based transport (avoids io.Pipe deadlocks on Windows).
func TestStdioTransport_RoundTrip(t *testing.T) {
	transport, server := newChannelTransport()
	defer transport.Close()
	defer server.Close()

	// Server handler: echo initialize response
	serverDone := make(chan error, 1)
	go func() {
		req, err := server.ReadRequest()
		if err != nil {
			serverDone <- fmt.Errorf("server read: %w", err)
			return
		}
		if req.Method != "initialize" {
			serverDone <- fmt.Errorf("unexpected method: %s", req.Method)
			return
		}
		err = server.WriteResponse(req.ID, json.RawMessage(`{"serverInfo":{"name":"chan-test","version":"0.1.0"}}`))
		serverDone <- err
	}()

	result, err := transport.SendRequest("initialize", json.RawMessage(`{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"deepact","version":"1.0.0"}}`))
	if err != nil {
		t.Fatalf("SendRequest failed: %v", err)
	}

	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}

	var info struct {
		ServerInfo ServerInfo `json:"serverInfo"`
	}
	if err := json.Unmarshal(result, &info); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if info.ServerInfo.Name != "chan-test" {
		t.Errorf("expected 'chan-test', got %q", info.ServerInfo.Name)
	}
}

// channelTransport implements Transport over a pair of channels for in-memory testing.
// Each JSON-RPC message is sent as a raw JSON byte slice.
type channelTransport struct {
	writeCh    chan<- []byte
	readCh     <-chan []byte
	closed     chan struct{}
}

type channelServer struct {
	readCh  <-chan []byte
	writeCh chan<- []byte
}

func newChannelTransport() (*channelTransport, *channelServer) {
	toServer := make(chan []byte, 16)
	toClient := make(chan []byte, 16)
	closed := make(chan struct{})
	return &channelTransport{
			writeCh: toServer,
			readCh:  toClient,
			closed:  closed,
		}, &channelServer{
			readCh:  toServer,
			writeCh: toClient,
		}
}

func (t *channelTransport) SendRequest(method string, params json.RawMessage) (json.RawMessage, error) {
	select {
	case <-t.closed:
		return nil, fmt.Errorf("transport closed")
	default:
	}

	// Build JSON-RPC request and send as raw JSON bytes
	req := jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  method,
		Params:  params,
	}
	data, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}

	select {
	case t.writeCh <- data:
	case <-t.closed:
		return nil, fmt.Errorf("transport closed")
	}

	// Read response
	select {
	case respData := <-t.readCh:
		var resp jsonRPCResponse
		if err := json.Unmarshal(respData, &resp); err != nil {
			return nil, fmt.Errorf("parse: %w", err)
		}
		if resp.Error != nil {
			return nil, &MCPError{msg: fmt.Sprintf("JSON-RPC error %d: %s", resp.Error.Code, resp.Error.Message)}
		}
		return resp.Result, nil
	case <-t.closed:
		return nil, fmt.Errorf("transport closed")
	}
}

func (t *channelTransport) Close() error {
	close(t.closed)
	return nil
}

func (s *channelServer) ReadRequest() (*jsonRPCRequest, error) {
	data := <-s.readCh
	var req jsonRPCRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return nil, err
	}
	return &req, nil
}

func (s *channelServer) WriteResponse(id int, result json.RawMessage) error {
	resp := jsonRPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Result:  result,
	}
	data, err := json.Marshal(resp)
	if err != nil {
		return err
	}
	s.writeCh <- data
	return nil
}

func (s *channelServer) Close() error { return nil }

// --- Config loading tests ---

func TestLoadConfig_LoadsFromProjectDir(t *testing.T) {
	dir := t.TempDir()
	cfgDir := dir + "/.deepact"
	if err := os.MkdirAll(cfgDir, 0755); err != nil {
		t.Fatal(err)
	}
	cfgPath := cfgDir + "/mcp.json"
	cfgContent := `{
		"servers": [
			{"name": "github", "command": "npx", "args": ["-y", "@modelcontextprotocol/server-github"], "env": {"GITHUB_TOKEN": "tok"}},
			{"name": "fs", "command": "node", "args": ["server.js"]}
		]
	}`
	if err := os.WriteFile(cfgPath, []byte(cfgContent), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadConfig(dir)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected non-nil config")
	}
	if len(cfg.Servers) != 2 {
		t.Fatalf("expected 2 servers, got %d", len(cfg.Servers))
	}
	if cfg.Servers[0].Name != "github" {
		t.Errorf("expected name 'github', got %q", cfg.Servers[0].Name)
	}
	if cfg.Servers[0].Command != "npx" {
		t.Errorf("expected command 'npx', got %q", cfg.Servers[0].Command)
	}
	if len(cfg.Servers[0].Args) != 2 {
		t.Errorf("expected 2 args, got %d", len(cfg.Servers[0].Args))
	}
	if cfg.Servers[0].Env["GITHUB_TOKEN"] != "tok" {
		t.Errorf("expected GITHUB_TOKEN env var")
	}
}

func TestLoadConfig_ReturnsNilWhenNoFile(t *testing.T) {
	dir := t.TempDir()
	cfg, err := LoadConfig(dir)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}
	if cfg != nil {
		t.Error("expected nil config when no file exists")
	}
}

func TestLoadConfig_RejectsInvalidConfig(t *testing.T) {
	dir := t.TempDir()
	cfgDir := dir + "/.deepact"
	if err := os.MkdirAll(cfgDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfgDir+"/mcp.json", []byte(`{"servers":[{"name":""}]}`), 0644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadConfig(dir)
	if err == nil {
		t.Fatal("expected error for empty server name")
	}
}

// --- Tool wrapper tests ---

func TestToolWrapper_Spec(t *testing.T) {
	mcpTool := Tool{
		Name:        "get_weather",
		Description: "Get current weather for a location",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"location":{"type":"string"}},"required":["location"]}`),
	}
	mockClient := newMockMCPServer()
	wrapper := &ToolWrapper{
		Client:     NewClient(mockClient),
		MCPServer:  "weather",
		MCPTool:    mcpTool,
	}

	spec := wrapper.Spec()
	if spec.Name != "weather_get_weather" {
		t.Errorf("expected name 'weather_get_weather', got %q", spec.Name)
	}
	if spec.Description != mcpTool.Description {
		t.Errorf("expected description %q, got %q", mcpTool.Description, spec.Description)
	}
	if string(spec.Parameters) != string(mcpTool.InputSchema) {
		t.Errorf("expected parameters to match input schema")
	}
}

func TestToolWrapper_Run(t *testing.T) {
	mcpTool := Tool{
		Name:        "echo",
		Description: "Echo back input",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"message":{"type":"string"}},"required":["message"]}`),
	}
	mockClient := newMockMCPServer()
	mcpClient := NewClient(mockClient)
	// Initialize the client
	if _, err := mcpClient.Initialize(); err != nil {
		t.Fatal(err)
	}

	wrapper := &ToolWrapper{
		Client:    mcpClient,
		MCPServer: "test",
		MCPTool:   mcpTool,
	}

	input := json.RawMessage(`{"message":"hello world"}`)
	env, err := wrapper.Run(tools.ToolContext{}, input)
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if env.Status != tools.StatusOK {
		t.Errorf("expected status OK, got %q", env.Status)
	}
	if !strings.Contains(env.Digest, "hello world") {
		t.Errorf("expected digest to contain 'hello world', got %q", env.Digest)
	}
}

// --- Manager tests ---

// testMCPServer is a minimal MCP server binary embedded in the test.
// It reads JSON-RPC from stdin and writes responses to stdout.
const testMCPServerSrc = `package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
)

type request struct {
	JSONRPC string          ` + "`json:\"jsonrpc\"`" + `
	ID      int             ` + "`json:\"id\"`" + `
	Method  string          ` + "`json:\"method\"`" + `
	Params  json.RawMessage ` + "`json:\"params,omitempty\"`" + `
}

type response struct {
	JSONRPC string          ` + "`json:\"jsonrpc\"`" + `
	ID      int             ` + "`json:\"id\"`" + `
	Result  json.RawMessage ` + "`json:\"result,omitempty\"`" + `
}

func main() {
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		line := scanner.Bytes()
		var req request
		if err := json.Unmarshal(line, &req); err != nil {
			continue
		}
		switch req.Method {
		case "initialize":
			resp := response{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result:  json.RawMessage(` + "`" + `{"protocolVersion":"2024-11-05","capabilities":{"tools":{}},"serverInfo":{"name":"test-server","version":"0.1.0"}}` + "`" + `),
			}
			data, _ := json.Marshal(resp)
			fmt.Println(string(data))
		case "tools/list":
			resp := response{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result:  json.RawMessage(` + "`" + `{"tools":[{"name":"reverse","description":"Reverses the input string","inputSchema":{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}}]}` + "`" + `),
			}
			data, _ := json.Marshal(resp)
			fmt.Println(string(data))
		case "tools/call":
			var params struct {
				Name      string          ` + "`json:\"name\"`" + `
				Arguments json.RawMessage ` + "`json:\"arguments\"`" + `
			}
			json.Unmarshal(req.Params, &params)
			var args struct {
				Text string ` + "`json:\"text\"`" + `
			}
			json.Unmarshal(params.Arguments, &args)
			reversed := reverseString(args.Text)
			resp := response{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result:  json.RawMessage(` + "`" + `{"content":[{"type":"text","text":"` + "`" + ` + reversed + ` + "`" + `"}],"isError":false}` + "`" + `),
			}
			data, _ := json.Marshal(resp)
			fmt.Println(string(data))
		}
	}
}

func reverseString(s string) string {
	runes := []rune(s)
	for i, j := 0, len(runes)-1; i < j; i, j = i+1, j-1 {
		runes[i], runes[j] = runes[j], runes[i]
	}
	return string(runes)
}
`

func TestStartServer_EndToEnd(t *testing.T) {
	// Build the test MCP server binary
	binPath := filepath.Join(t.TempDir(), "test-mcp-server"+execSuffix())
	srcPath := filepath.Join(t.TempDir(), "main.go")
	if err := os.WriteFile(srcPath, []byte(testMCPServerSrc), 0644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("go", "build", "-o", binPath, srcPath).CombinedOutput()
	if err != nil {
		t.Fatalf("build test server: %v\n%s", err, out)
	}

	cfg := ServerConfig{
		Name:    "test-server",
		Command: binPath,
	}

	manager, err := StartServer(cfg)
	if err != nil {
		t.Fatalf("StartServer failed: %v", err)
	}
	defer manager.Close()

	tools := manager.Tools()
	if len(tools) == 0 {
		t.Fatal("expected at least one tool")
	}
	if tools[0].Name != "reverse" {
		t.Errorf("expected tool 'reverse', got %q", tools[0].Name)
	}

	// Test calling the tool via the manager's client
	result, err := manager.Client.CallTool("reverse", json.RawMessage(`{"text":"hello"}`))
	if err != nil {
		t.Fatalf("CallTool failed: %v", err)
	}
	if result.Content != "olleh" {
		t.Errorf("expected 'olleh', got %q", result.Content)
	}
}

func execSuffix() string {
	if os.Getenv("GOOS") == "windows" || os.Getenv("COMSPEC") != "" {
		return ".exe"
	}
	return ""
}

// --- mock MCP server (in-memory transport) ---

// mockMCPServer implements the Transport interface by handling JSON-RPC
// messages directly in-process, simulating a real MCP server.
type mockMCPServer struct {
	initialized bool
	requestID   int
}

func newMockMCPServer() *mockMCPServer {
	return &mockMCPServer{}
}

func (m *mockMCPServer) SendRequest(method string, params json.RawMessage) (json.RawMessage, error) {
	m.requestID++
	switch method {
	case "initialize":
		m.initialized = true
		return json.RawMessage(`{
			"protocolVersion": "2024-11-05",
			"capabilities": {"tools": {}},
			"serverInfo": {"name": "mock-mcp", "version": "1.0.0"}
		}`), nil
	case "tools/list":
		if !m.initialized {
			return nil, ErrNotInitialized
		}
		return json.RawMessage(`{
			"tools": [
				{
					"name": "echo",
					"description": "Echoes back the input message",
					"inputSchema": {
						"type": "object",
						"properties": {
							"message": {"type": "string"}
						},
						"required": ["message"]
					}
				}
			]
		}`), nil
	case "tools/call":
		if !m.initialized {
			return nil, ErrNotInitialized
		}
		var args struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(params, &args); err != nil {
			return nil, err
		}
		if args.Name == "echo" {
			var input struct {
				Message string `json:"message"`
			}
			if err := json.Unmarshal(args.Arguments, &input); err != nil {
				return nil, err
			}
			return json.RawMessage(`{
				"content": [{"type": "text", "text": "echo: ` + input.Message + `"}],
				"isError": false
			}`), nil
		}
		return nil, ErrToolNotFound
	default:
		return nil, ErrMethodNotFound
	}
}

func (m *mockMCPServer) Close() error {
	return nil
}
