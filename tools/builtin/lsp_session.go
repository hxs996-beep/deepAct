package builtin

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
)

// lspRPCResponse carries the result or error from a JSON-RPC response.
type lspRPCResponse struct {
	Result json.RawMessage
	Err    string // non-empty if gopls returned a JSON-RPC error
}

// lspSession manages a single gopls subprocess lifecycle per session.
// Communicates via stdin/stdout using the LSP JSON-RPC protocol.
type lspSession struct {
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	stdout   io.ReadCloser
	scanner  *bufio.Scanner
	mu       sync.Mutex
	msgID    int
	pending  map[int]chan<- lspRPCResponse
	openFiles map[string]bool // files sent via textDocument/didOpen

	closeOnce sync.Once
	done      chan struct{}
}

func newLSPSession(workDir string) (*lspSession, error) {
	// Find gopls in PATH
	goplsPath, err := exec.LookPath("gopls")
	if err != nil {
		return nil, fmt.Errorf("gopls not found in PATH: %w", err)
	}

	cmd := exec.Command(goplsPath, "serve")
	cmd.Dir = workDir

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("gopls stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("gopls stdout pipe: %w", err)
	}
	cmd.Stderr = os.Stderr // let gopls logs go to stderr

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start gopls: %w", err)
	}

	s := &lspSession{
		cmd:       cmd,
		stdin:     stdin,
		stdout:    stdout,
		scanner:   bufio.NewScanner(stdout),
		pending:   make(map[int]chan<- lspRPCResponse),
		openFiles: make(map[string]bool),
		done:      make(chan struct{}),
	}

	// Increase scanner buffer for large LSP responses (e.g., workspace/symbol)
	s.scanner.Buffer(make([]byte, 0, 256*1024), 5*1024*1024)

	// Start response reader goroutine
	go s.readResponses()

	// Send initialize request with its own timeout (gopls process lives on regardless)
	initCtx, initCancel := context.WithTimeout(context.Background(), lspTimeOut)
	defer initCancel()

	initParams := map[string]interface{}{
		"processId": os.Getpid(),
		"capabilities": map[string]interface{}{
			"textDocument": map[string]interface{}{
				"hover":            map[string]interface{}{"contentFormat": []string{"markdown", "plaintext"}},
				"definition":       map[string]interface{}{"linkSupport": true},
				"references":       map[string]interface{}{},
				"documentSymbol":   map[string]interface{}{},
				"implementation":   map[string]interface{}{},
				"callHierarchy":    map[string]interface{}{},
			},
			"workspace": map[string]interface{}{
				"symbol": map[string]interface{}{},
			},
		},
		"rootUri":      "file://" + workDir,
		"rootPath":     workDir,
	}
	if _, err := s.sendRequest(initCtx, "initialize", initParams); err != nil {
		s.close()
		return nil, fmt.Errorf("gopls initialize: %w", err)
	}

	// Send initialized notification
	if err := s.sendNotification("initialized", map[string]interface{}{}); err != nil {
		s.close()
		return nil, fmt.Errorf("gopls initialized: %w", err)
	}

	return s, nil
}

// sendRequest sends a JSON-RPC request and returns the result.
func (s *lspSession) sendRequest(ctx context.Context, method string, params interface{}) (json.RawMessage, error) {
	s.mu.Lock()
	s.msgID++
	id := s.msgID
	ch := make(chan lspRPCResponse, 1)
	s.pending[id] = ch
	s.mu.Unlock()

	msg := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	}
	if err := s.writeMessage(msg); err != nil {
		return nil, err
	}

	select {
	case resp := <-ch:
		if resp.Err != "" {
			return nil, fmt.Errorf("gopls %s: %s", method, resp.Err)
		}
		return resp.Result, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// sendNotification sends a JSON-RPC notification (no id, no response expected).
func (s *lspSession) sendNotification(method string, params interface{}) error {
	msg := map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  method,
		"params":  params,
	}
	return s.writeMessage(msg)
}

// openFile sends textDocument/didOpen for a given file.
func (s *lspSession) openFile(filePath string) error {
	s.mu.Lock()
	if s.openFiles[filePath] {
		s.mu.Unlock()
		return nil // already open
	}
	s.mu.Unlock()

	data, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("read file for LSP: %w", err)
	}

	params := map[string]interface{}{
		"textDocument": map[string]interface{}{
			"uri":        "file://" + filePath,
			"languageId": "go",
			"version":    1,
			"text":       string(data),
		},
	}
	if err := s.sendNotification("textDocument/didOpen", params); err != nil {
		return err
	}

	s.mu.Lock()
	s.openFiles[filePath] = true
	s.mu.Unlock()
	return nil
}

// readResponses reads JSON-RPC responses from gopls stdout and dispatches them.
func (s *lspSession) readResponses() {
	defer close(s.done)

	// Use a custom scanner that reads Content-Length framed messages
	reader := newLSPReader(s.stdout)
	for {
		data, err := reader.readMessage()
		if err != nil {
			return // connection closed
		}

		var base struct {
			ID     int              `json:"id"`
			Method string           `json:"method"`
			Result json.RawMessage  `json:"result"`
			Error  *json.RawMessage `json:"error"`
		}
		if err := json.Unmarshal(data, &base); err != nil {
			continue
		}

		if base.ID > 0 {
			// This is a response to a request
			s.mu.Lock()
			ch, ok := s.pending[base.ID]
			delete(s.pending, base.ID)
			s.mu.Unlock()
			if ok {
				if base.Error != nil {
					ch <- lspRPCResponse{Err: string(*base.Error)}
				} else {
					ch <- lspRPCResponse{Result: base.Result}
				}
				close(ch)
			}
		}
	}
}

func (s *lspSession) writeMessage(msg interface{}) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	header := fmt.Sprintf("Content-Length: %d\r\n\r\n", len(data))
	if _, err := s.stdin.Write([]byte(header)); err != nil {
		return err
	}
	_, err = s.stdin.Write(data)
	return err
}

func (s *lspSession) close() {
	s.closeOnce.Do(func() {
		// Send shutdown request (best-effort)
		_ = s.sendNotification("shutdown", map[string]interface{}{})
		_ = s.sendNotification("exit", map[string]interface{}{})
		s.stdin.Close()
		s.stdout.Close()
		_ = s.cmd.Wait()
	})
}

// isAlive checks whether the gopls process is still running
// by verifying the response reader goroutine hasn't exited.
func (s *lspSession) isAlive() bool {
	select {
	case <-s.done:
		return false
	default:
		return true
	}
}

// --- LSP operations ---

type lspLocation struct {
	URI   string `json:"uri"`
	Range struct {
		Start struct {
			Line      int `json:"line"`
			Character int `json:"character"`
		} `json:"start"`
		End struct {
			Line      int `json:"line"`
			Character int `json:"character"`
		} `json:"end"`
	} `json:"range"`
}

type lspSymbol struct {
	Name          string `json:"name"`
	Kind          int    `json:"kind"`
	Detail        string `json:"detail,omitempty"`
	ContainerName string `json:"containerName,omitempty"`
	Location      lspLocation `json:"location"`
}

type lspHover struct {
	Contents json.RawMessage `json:"contents"`
	Range    json.RawMessage `json:"range,omitempty"`
}

// goToDefinition returns the definition location for a symbol at (line, char).
// line and char are 1-based (as shown in editors), converted to 0-based for LSP.
func (s *lspSession) goToDefinition(ctx context.Context, filePath string, line, char int) (json.RawMessage, error) {
	if err := s.openFile(filePath); err != nil {
		return nil, err
	}
	params := map[string]interface{}{
		"textDocument": map[string]interface{}{"uri": "file://" + filePath},
		"position":     map[string]interface{}{"line": line - 1, "character": char - 1},
	}
	result, err := s.sendRequest(ctx, "textDocument/definition", params)
	if err != nil {
		return nil, err
	}
	return result, nil
}

// findReferences returns all references to a symbol at (line, char).
func (s *lspSession) findReferences(ctx context.Context, filePath string, line, char int, includeDeclaration bool) (json.RawMessage, error) {
	if err := s.openFile(filePath); err != nil {
		return nil, err
	}
	params := map[string]interface{}{
		"textDocument": map[string]interface{}{"uri": "file://" + filePath},
		"position":     map[string]interface{}{"line": line - 1, "character": char - 1},
		"context":      map[string]interface{}{"includeDeclaration": includeDeclaration},
	}
	return s.sendRequest(ctx, "textDocument/references", params)
}

// hover returns hover information for a symbol at (line, char).
func (s *lspSession) hover(ctx context.Context, filePath string, line, char int) (json.RawMessage, error) {
	if err := s.openFile(filePath); err != nil {
		return nil, err
	}
	params := map[string]interface{}{
		"textDocument": map[string]interface{}{"uri": "file://" + filePath},
		"position":     map[string]interface{}{"line": line - 1, "character": char - 1},
	}
	return s.sendRequest(ctx, "textDocument/hover", params)
}

// documentSymbol returns all symbols in a file.
func (s *lspSession) documentSymbol(ctx context.Context, filePath string) (json.RawMessage, error) {
	if err := s.openFile(filePath); err != nil {
		return nil, err
	}
	params := map[string]interface{}{
		"textDocument": map[string]interface{}{"uri": "file://" + filePath},
	}
	return s.sendRequest(ctx, "textDocument/documentSymbol", params)
}

// workspaceSymbol searches for symbols across the entire workspace.
func (s *lspSession) workspaceSymbol(ctx context.Context, query string) (json.RawMessage, error) {
	params := map[string]interface{}{
		"query": query,
	}
	return s.sendRequest(ctx, "workspace/symbol", params)
}

// goToImplementation returns implementations of a symbol at (line, char).
func (s *lspSession) goToImplementation(ctx context.Context, filePath string, line, char int) (json.RawMessage, error) {
	if err := s.openFile(filePath); err != nil {
		return nil, err
	}
	params := map[string]interface{}{
		"textDocument": map[string]interface{}{"uri": "file://" + filePath},
		"position":     map[string]interface{}{"line": line - 1, "character": char - 1},
	}
	return s.sendRequest(ctx, "textDocument/implementation", params)
}

// prepareCallHierarchy returns call hierarchy items at (line, char).
func (s *lspSession) prepareCallHierarchy(ctx context.Context, filePath string, line, char int) (json.RawMessage, error) {
	if err := s.openFile(filePath); err != nil {
		return nil, err
	}
	params := map[string]interface{}{
		"textDocument": map[string]interface{}{"uri": "file://" + filePath},
		"position":     map[string]interface{}{"line": line - 1, "character": char - 1},
	}
	return s.sendRequest(ctx, "textDocument/prepareCallHierarchy", params)
}

// incomingCalls returns all callers of the function at (line, char).
// Requires prepareCallHierarchy first.
func (s *lspSession) incomingCalls(ctx context.Context, filePath string, line, char int) (json.RawMessage, error) {
	// First prepare call hierarchy to get the item
	items, err := s.prepareCallHierarchy(ctx, filePath, line, char)
	if err != nil {
		return nil, err
	}

	// The result is either a single item or an array
	var itemsArr []json.RawMessage
	if err := json.Unmarshal(items, &itemsArr); err != nil {
		// Try single item
		var single json.RawMessage
		if err2 := json.Unmarshal(items, &single); err2 != nil {
			return nil, fmt.Errorf("no call hierarchy item at this position")
		}
		itemsArr = []json.RawMessage{single}
	}
	if len(itemsArr) == 0 {
		return nil, fmt.Errorf("no call hierarchy item at this position")
	}

	// Use the first item to request incoming calls
	var item json.RawMessage
	if err := json.Unmarshal(itemsArr[0], &item); err != nil {
		return nil, err
	}
	params := map[string]interface{}{
		"item": item,
	}
	return s.sendRequest(ctx, "callHierarchy/incomingCalls", params)
}

// outgoingCalls returns all functions called by the function at (line, char).
func (s *lspSession) outgoingCalls(ctx context.Context, filePath string, line, char int) (json.RawMessage, error) {
	// First prepare call hierarchy
	items, err := s.prepareCallHierarchy(ctx, filePath, line, char)
	if err != nil {
		return nil, err
	}

	var itemsArr []json.RawMessage
	if err := json.Unmarshal(items, &itemsArr); err != nil {
		var single json.RawMessage
		if err2 := json.Unmarshal(items, &single); err2 != nil {
			return nil, fmt.Errorf("no call hierarchy item at this position")
		}
		itemsArr = []json.RawMessage{single}
	}
	if len(itemsArr) == 0 {
		return nil, fmt.Errorf("no call hierarchy item at this position")
	}

	var item json.RawMessage
	if err := json.Unmarshal(itemsArr[0], &item); err != nil {
		return nil, err
	}
	params := map[string]interface{}{
		"item": item,
	}
	return s.sendRequest(ctx, "callHierarchy/outgoingCalls", params)
}

// --- LSP message reader ---

// lspReader reads Content-Length framed messages from an LSP server.
type lspReader struct {
	reader  *bufio.Reader
	buf     []byte
}

func newLSPReader(r io.Reader) *lspReader {
	return &lspReader{reader: bufio.NewReaderSize(r, 256*1024)}
}

func (lr *lspReader) readMessage() ([]byte, error) {
	// Read headers until empty line
	contentLength := 0
	for {
		line, err := lr.reader.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = line[:len(line)-1] // trim \n
		if line == "" || line == "\r" {
			break
		}
		if _, err := fmt.Sscanf(line, "Content-Length: %d", &contentLength); err == nil {
			// found content length
		}
	}

	if contentLength == 0 {
		return nil, io.EOF
	}

	data := make([]byte, contentLength)
	_, err := io.ReadFull(lr.reader, data)
	if err != nil {
		return nil, err
	}
	return data, nil
}

// lspManager manages LSP sessions keyed by session ID.
type lspManager struct {
	mu       sync.Mutex
	sessions map[string]*lspSession
}

var globalLSPManager = &lspManager{sessions: make(map[string]*lspSession)}

func (m *lspManager) getOrCreate(sessionID, workDir string) (*lspSession, error) {
	m.mu.Lock()
	if s, ok := m.sessions[sessionID]; ok {
		if s.isAlive() {
			m.mu.Unlock()
			return s, nil
		}
		// Dead session — clean up and recreate
		s.close()
		delete(m.sessions, sessionID)
	}
	m.mu.Unlock()

	s, err := newLSPSession(workDir)
	if err != nil {
		return nil, err
	}

	m.mu.Lock()
	m.sessions[sessionID] = s
	m.mu.Unlock()
	return s, nil
}

func (m *lspManager) closeSession(sessionID string) {
	m.mu.Lock()
	s, ok := m.sessions[sessionID]
	delete(m.sessions, sessionID)
	m.mu.Unlock()
	if ok {
		s.close()
	}
}
