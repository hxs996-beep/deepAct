package builtin

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/deepact/deepact/tools"
)

const (
	maxReadBytes    = 1 << 20 // 1MB safety cap — refuse to read larger files
	maxReadTokens   = 25000   // max tokens to return inline; beyond this → truncate with offset/limit hint
	charsPerToken   = 4       // rough estimate: 4 chars ≈ 1 token for code

	fileUnchangedStub = "File unchanged since last read. The content from the earlier Read tool_result in this conversation is still current — refer to that instead of re-reading."
)

type ReadTool struct {
	mtimeCache sync.Map // absPath → mtimeMs (int64)
}

func NewReadTool() *ReadTool {
	return &ReadTool{}
}

func (t *ReadTool) Spec() tools.ToolSpec {
	return tools.ToolSpec{
		Name:        "read",
		Description: "Read a file from the local filesystem. Returns up to ~25000 tokens inline; larger files are truncated with guidance to use offset/limit for specific sections. Use 'symbol' to extract a named Go declaration. If the file hasn't changed since the last read, a stub is returned and you should refer to the earlier content in conversation history.",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"Path to the file"},"symbol":{"type":"string","description":"Name of Go symbol to read (function/type/struct/variable/constant). Works only for .go files. When set, offset/limit are ignored."},"offset":{"type":"integer","description":"Starting line number (1-based)"},"limit":{"type":"integer","description":"Max lines to read"}},"required":["path"]}`),
	}
}

type readInput struct {
	Path   string `json:"path"`
	Symbol string `json:"symbol"`
	Offset int    `json:"offset"`
	Limit  int    `json:"limit"`
}

func (t *ReadTool) Run(ctx tools.ToolContext, input json.RawMessage) (tools.ToolResultEnvelope, error) {
	var payload readInput
	if err := json.Unmarshal(input, &payload); err != nil {
		return tools.ToolResultEnvelope{Status: tools.StatusError, Digest: fmt.Sprintf("invalid input: %v", err)}, err
	}
	payload.Path = strings.TrimSpace(payload.Path)
	if payload.Path == "" {
		err := errors.New("path is required")
		return tools.ToolResultEnvelope{Status: tools.StatusError, Digest: err.Error()}, err
	}

	safePath, err := resolveSafePath(ctx.WorkDir, payload.Path)
	if err != nil {
		return tools.ToolResultEnvelope{Status: tools.StatusError, Digest: err.Error()}, err
	}

	// Symbol mode: extract semantic code block via Go AST (always small, no mtime check needed)
	if payload.Symbol != "" && strings.HasSuffix(safePath, ".go") {
		content, symErr := readSymbol(safePath, payload.Symbol)
		if symErr != nil {
			return tools.ToolResultEnvelope{Status: tools.StatusError, Digest: symErr.Error()}, symErr
		}
		lineCount := strings.Count(content, "\n")
		return tools.ToolResultEnvelope{Status: tools.StatusOK, Digest: fmt.Sprintf("symbol %s (%d lines)\n%s", payload.Symbol, lineCount, content)}, nil
	}

	info, err := os.Stat(safePath)
	if err != nil {
		return tools.ToolResultEnvelope{Status: tools.StatusError, Digest: fmt.Sprintf("stat file: %v", err)}, err
	}
	if info.Size() > maxReadBytes {
		return tools.ToolResultEnvelope{Status: tools.StatusError, Digest: fmt.Sprintf("file too large (%.1fMB, max 1MB). Use offset/limit to read specific sections.", float64(info.Size())/(1<<20))}, nil
	}

	// Mtime cache: if file unchanged since last full read, return stub
	if payload.Offset == 0 && payload.Limit == 0 {
		if cachedMtime, ok := t.mtimeCache.Load(safePath); ok {
			if cachedMtime == info.ModTime().UnixMilli() {
				return tools.ToolResultEnvelope{Status: tools.StatusOK, Digest: fileUnchangedStub}, nil
			}
		}
	}

	file, err := os.Open(safePath)
	if err != nil {
		return tools.ToolResultEnvelope{Status: tools.StatusError, Digest: fmt.Sprintf("open file: %v", err)}, err
	}
	defer file.Close()

	if err := detectBinary(file); err != nil {
		return tools.ToolResultEnvelope{Status: tools.StatusError, Digest: err.Error()}, err
	}

	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return tools.ToolResultEnvelope{Status: tools.StatusError, Digest: fmt.Sprintf("seek file: %v", err)}, err
	}

	// Read lines with numbering, respecting offset + limit
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	var builder strings.Builder
	lineNum := 0
	readLimit := payload.Limit
	offset := payload.Offset
	if offset < 1 {
		offset = 1
	}
	for scanner.Scan() {
		lineNum++
		if lineNum < offset {
			continue
		}
		if readLimit > 0 && lineNum >= offset+readLimit {
			break
		}
		builder.WriteString(fmt.Sprintf("%d: %s\n", lineNum, scanner.Text()))
	}
	if err := scanner.Err(); err != nil {
		return tools.ToolResultEnvelope{Status: tools.StatusError, Digest: fmt.Sprintf("read file: %v", err)}, err
	}

	content := builder.String()
	if content == "" {
		content = "(empty)"
	}

	// Update mtime cache only for full reads (no offset/limit)
	if payload.Offset == 0 && payload.Limit == 0 {
		t.mtimeCache.Store(safePath, info.ModTime().UnixMilli())
	}

	// Token-based truncation: if estimated tokens exceed limit, truncate at last complete line
	estimatedTokens := len(content) / charsPerToken
	if estimatedTokens <= maxReadTokens {
		return tools.ToolResultEnvelope{Status: tools.StatusOK, Digest: content}, nil
	}

	truncated := truncateByChars(content, maxReadTokens*charsPerToken)
	truncatedLines := strings.Count(truncated, "\n")
	digest := fmt.Sprintf("%s\n[... truncated at %d lines (~%d tokens out of ~%d estimated). Use offset/limit to read specific sections.]",
		truncated, truncatedLines, maxReadTokens, estimatedTokens)
	return tools.ToolResultEnvelope{Status: tools.StatusOK, Digest: digest}, nil
}

// truncateByChars returns content up to maxChars, stopping at the last complete line.
func truncateByChars(content string, maxChars int) string {
	if len(content) <= maxChars {
		return content
	}
	// Find the last newline before maxChars
	cut := strings.LastIndex(content[:maxChars], "\n")
	if cut < 0 {
		// No newline in the first maxChars chars — return empty with a hint
		return ""
	}
	return content[:cut]
}

func detectBinary(file *os.File) error {
	buf := make([]byte, 512)
	n, err := file.Read(buf)
	if err != nil && err != io.EOF {
		return fmt.Errorf("read file: %w", err)
	}
	for _, b := range buf[:n] {
		if b == 0 {
			return errors.New("binary file detected")
		}
	}
	return nil
}

// readSymbol extracts a named Go symbol (function, type, struct, interface, variable, constant)
// from a .go file using AST parsing. Returns just the declaration block with line numbers.
func readSymbol(path, symbolName string) (string, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
	if err != nil {
		return "", fmt.Errorf("parse file: %w", err)
	}

	var startPos, endPos token.Pos

	// Search through all top-level declarations
	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if d.Name.Name == symbolName {
				startPos = d.Pos()
				endPos = d.End()
				// Include doc comment
				if d.Doc != nil && d.Doc.Pos() < startPos {
					startPos = d.Doc.Pos()
				}
			}
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				switch s := spec.(type) {
				case *ast.TypeSpec:
					if s.Name.Name == symbolName {
						startPos = d.Pos()
						endPos = s.End()
						// Include doc comment or preceding spec's end for interface
						if d.Doc != nil && d.Doc.Pos() < startPos {
							startPos = d.Doc.Pos()
						}
					}
				case *ast.ValueSpec:
					for _, name := range s.Names {
						if name.Name == symbolName {
							startPos = d.Pos()
							endPos = s.End()
							if d.Doc != nil && d.Doc.Pos() < startPos {
								startPos = d.Doc.Pos()
							}
						}
					}
				}
			}
		}
		if startPos != 0 {
			break
		}
	}

	if startPos == 0 {
		return "", fmt.Errorf("symbol %q not found in %s", symbolName, path)
	}

	startLine := fset.Position(startPos).Line
	endLine := fset.Position(endPos).Line

	return readLineRange(path, startLine, endLine)
}

// readLineRange reads a range of lines from a file with numbered output.
func readLineRange(path string, startLine, endLine int) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open file: %w", err)
	}
	defer file.Close()

	var builder strings.Builder
	scanner := bufio.NewScanner(file)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		if lineNum > endLine {
			break
		}
		if lineNum >= startLine {
			builder.WriteString(fmt.Sprintf("%d: %s\n", lineNum, scanner.Text()))
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("read file: %w", err)
	}

	result := strings.TrimRight(builder.String(), "\n")
	if result == "" {
		return "", fmt.Errorf("empty result for lines %d-%d in %s", startLine, endLine, path)
	}
	return result, nil
}
