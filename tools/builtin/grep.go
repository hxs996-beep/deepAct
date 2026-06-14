package builtin

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"

	"github.com/deepact/deepact/tools"
)

type GrepTool struct{}

func NewGrepTool() *GrepTool {
	return &GrepTool{}
}

func (t *GrepTool) Spec() tools.ToolSpec {
	return tools.ToolSpec{
		Name:        "grep",
		Description: "Search file contents for a pattern. Supports regex (default) or exact substring matching. Can include context lines around each match for better understanding. For finding symbol definitions, type info, or usages, prefer `lsp workspaceSymbol`/`lsp hover`/`lsp findReferences` instead — they are more precise and cheaper.",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"pattern":{"type":"string","description":"Pattern to search for (regex by default, literal substring if exact_match=true)"},"path":{"type":"string","description":"Directory or file to search (default: current dir)"},"include":{"type":"string","description":"Glob to filter files, e.g. '*.go'"},"max_results":{"type":"integer","description":"Maximum results (default 100)"},"context_lines":{"type":"integer","description":"Number of context lines before and after each match (default 0)"},"exact_match":{"type":"boolean","description":"If true, treat pattern as literal substring instead of regex"}},"required":["pattern"]}`),
	}
}

type grepInput struct {
	Pattern      string `json:"pattern"`
	Path         string `json:"path"`
	Include      string `json:"include"`
	MaxResults   int    `json:"max_results"`
	ContextLines int    `json:"context_lines"`
	ExactMatch   bool   `json:"exact_match"`
}

func (t *GrepTool) Run(ctx tools.ToolContext, input json.RawMessage) (tools.ToolResultEnvelope, error) {
	var payload grepInput
	if err := json.Unmarshal(input, &payload); err != nil {
		return tools.ToolResultEnvelope{Status: tools.StatusError, Digest: fmt.Sprintf("invalid input: %v", err)}, err
	}
	payload.Pattern = strings.TrimSpace(payload.Pattern)
	if payload.Pattern == "" {
		err := errors.New("pattern is required")
		return tools.ToolResultEnvelope{Status: tools.StatusError, Digest: err.Error()}, err
	}
	if payload.Path == "" {
		payload.Path = "."
	}
	safePath, err := resolveSafePath(ctx.WorkDir, payload.Path)
	if err != nil {
		return tools.ToolResultEnvelope{Status: tools.StatusError, Digest: err.Error()}, err
	}
	payload.Path = safePath

	if payload.MaxResults <= 0 {
		payload.MaxResults = 100
	}
	if payload.ContextLines < 0 {
		payload.ContextLines = 0
	}

	// For exact_match with ripgrep, use --fixed-strings (-F)
	if _, err := exec.LookPath("rg"); err == nil {
		output, rgErr := runRipgrep(payload)
		status := tools.StatusOK
		if rgErr != nil {
			status = tools.StatusError
		}
		return tools.ToolResultEnvelope{Status: status, Digest: output}, rgErr
	}

	output, err := runGrepFallback(payload)
	status := tools.StatusOK
	if err != nil {
		status = tools.StatusError
	}
	return tools.ToolResultEnvelope{Status: status, Digest: output}, err
}

func runRipgrep(payload grepInput) (string, error) {
	args := []string{"--json"}
	if payload.Include != "" {
		args = append(args, "--glob", payload.Include)
	}
	if payload.ExactMatch {
		args = append(args, "-F")
	}
	if payload.ContextLines > 0 {
		args = append(args, "-C", fmt.Sprintf("%d", payload.ContextLines))
	}
	args = append(args, payload.Pattern, payload.Path)

	cmd := exec.CommandContext(context.Background(), "rg", args...)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		return strings.TrimSpace(stderr.String()), fmt.Errorf("ripgrep failed: %w", err)
	}

	matches, err := parseRipgrepJSON(stdout.Bytes(), payload.MaxResults, payload.ContextLines)
	if err != nil {
		return "", err
	}
	if len(matches) == 0 {
		return "(no matches)", nil
	}
	return strings.Join(matches, "\n"), nil
}

type rgEvent struct {
	Type string `json:"type"`
	Data struct {
		Path struct {
			Text string `json:"text"`
		} `json:"path"`
		LineNumber int `json:"line_number"`
		Lines      struct {
			Text string `json:"text"`
		} `json:"lines"`
	} `json:"data"`
}

func parseRipgrepJSON(data []byte, maxResults int, contextLines int) ([]string, error) {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	matches := make([]string, 0, maxResults)
	currentFile := ""
	for scanner.Scan() {
		if len(matches) >= maxResults {
			break
		}
		var raw struct {
			Type string          `json:"type"`
			Data json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &raw); err != nil {
			return nil, fmt.Errorf("parse ripgrep output: %w", err)
		}
		if raw.Type == "begin" {
			var begin struct {
				Path struct {
					Text string `json:"text"`
				} `json:"path"`
			}
			json.Unmarshal(raw.Data, &begin)
			currentFile = begin.Path.Text
			continue
		}
		if raw.Type != "match" && raw.Type != "context" {
			continue
		}
		var event rgEvent
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			return nil, fmt.Errorf("parse rg event: %w", err)
		}
		line := strings.TrimRight(event.Data.Lines.Text, "\r\n")
		prefix := event.Data.Path.Text
		if prefix == "" {
			prefix = currentFile
		}
		if raw.Type == "context" {
			matches = append(matches, fmt.Sprintf("%s:%d:→ %s", prefix, event.Data.LineNumber, line))
		} else {
			matches = append(matches, fmt.Sprintf("%s:%d:%s", prefix, event.Data.LineNumber, line))
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read ripgrep output: %w", err)
	}
	return matches, nil
}

func runGrepFallback(payload grepInput) (string, error) {
	var pattern *regexp.Regexp
	var err error
	if payload.ExactMatch {
		pattern, err = regexp.Compile(regexp.QuoteMeta(payload.Pattern))
	} else {
		pattern, err = regexp.Compile(payload.Pattern)
	}
	if err != nil {
		return "", fmt.Errorf("compile pattern: %w", err)
	}

	root := payload.Path
	include := payload.Include
	results := make([]string, 0, payload.MaxResults)
	var resultsMu sync.Mutex
	fileCh := make(chan string, 64)
	stopCh := make(chan struct{})
	var stopOnce sync.Once
	stop := func() {
		stopOnce.Do(func() {
			close(stopCh)
		})
	}
	var wg sync.WaitGroup

	worker := func() {
		defer wg.Done()
		for path := range fileCh {
			if err := scanFileForMatches(path, pattern, &results, &resultsMu, payload.MaxResults, payload.ContextLines); err != nil {
				continue
			}
			if reachedLimit(&results, &resultsMu, payload.MaxResults) {
				stop()
				return
			}
		}
	}

	workers := 8
	if runtime.GOMAXPROCS(0) < workers {
		workers = runtime.GOMAXPROCS(0)
	}
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go worker()
	}

	go func() {
		defer close(fileCh)
		_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if entry.IsDir() {
				if shouldSkipDir(entry.Name()) {
					return filepath.SkipDir
				}
				return nil
			}
			if shouldSkipFile(entry.Name()) {
				return nil
			}
			if include != "" {
				matched, err := filepath.Match(include, entry.Name())
				if err != nil || !matched {
					return nil
				}
			}
			select {
			case <-stopCh:
				return errors.New("stop")
			case fileCh <- path:
				return nil
			}
		})
	}()

	wg.Wait()
	stop()

	resultsMu.Lock()
	defer resultsMu.Unlock()
	if len(results) == 0 {
		return "(no matches)", nil
	}
	if len(results) > payload.MaxResults {
		results = results[:payload.MaxResults]
	}
	sort.Strings(results)
	return strings.Join(results, "\n"), nil
}

func scanFileForMatches(path string, pattern *regexp.Regexp, results *[]string, mu *sync.Mutex, maxResults int, contextLines int) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()

	if isBinary(file) {
		return nil
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return err
	}

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	lineNum := 0
	// Ring buffer for context lines before a match
	var beforeBuf []string
	beforeLim := contextLines
	if beforeLim == 0 {
		beforeLim = 1 // don't allocate for no context
	}

	for scanner.Scan() {
		lineNum++
		line := scanner.Text()
		if contextLines > 0 {
			beforeBuf = append(beforeBuf, line)
			if len(beforeBuf) > contextLines+1 {
				beforeBuf = beforeBuf[1:]
			}
		}
		if pattern.MatchString(line) {
			mu.Lock()
			if len(*results) >= maxResults {
				mu.Unlock()
				return nil
			}
			// Emit context lines before the match
			if contextLines > 0 && len(beforeBuf) > 1 {
				start := len(beforeBuf) - 1 - contextLines
				if start < 0 {
					start = 0
				}
				for i := start; i < len(beforeBuf)-1; i++ {
					ctxLine := lineNum - (len(beforeBuf) - 1 - i)
					*results = append(*results, fmt.Sprintf("%s:%d:→ %s", path, ctxLine, beforeBuf[i]))
				}
			}
			*results = append(*results, fmt.Sprintf("%s:%d:%s", path, lineNum, line))
			// Emit context lines after the match (will be captured as we continue scanning)
			afterCount := 0
			if contextLines > 0 {
				for scanner.Scan() && afterCount < contextLines {
					lineNum++
					nextLine := scanner.Text()
					*results = append(*results, fmt.Sprintf("%s:%d:→ %s", path, lineNum, nextLine))
					afterCount++
				}
			}
			mu.Unlock()
			if afterCount < contextLines {
				// reached end of file after context
				break
			}
		}
	}
	return scanner.Err()
}

func reachedLimit(results *[]string, mu *sync.Mutex, maxResults int) bool {
	mu.Lock()
	defer mu.Unlock()
	return len(*results) >= maxResults
}

func isBinary(file *os.File) bool {
	buf := make([]byte, 512)
	n, err := file.Read(buf)
	if err != nil && err != io.EOF {
		return true
	}
	for _, b := range buf[:n] {
		if b == 0 {
			return true
		}
	}
	return false
}

func shouldSkipDir(name string) bool {
	switch name {
	case ".git", "node_modules", "vendor", "__pycache__":
		return true
	default:
		return false
	}
}

func shouldSkipFile(name string) bool {
	switch name {
	case ".DS_Store":
		return true
	default:
		return false
	}
}
