package builtin

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/deepact/deepact/tools"
)

const readMultiMaxTargets = 8

type ReadMultiTool struct{}

func NewReadMultiTool() *ReadMultiTool {
	return &ReadMultiTool{}
}

func (t *ReadMultiTool) Spec() tools.ToolSpec {
	return tools.ToolSpec{
		Name: "read_multi",
		Description: "Read up to 8 file targets in one call, executed in parallel. Use this for fan-out " +
			"exploration: when you need to understand several files/symbols/directions at once, list them " +
			"as targets instead of issuing many single reads. Each target supports path + optional " +
			"symbol/offset/limit, same semantics as `read`. Prefer this over chained single reads when you " +
			"have 2+ independent things to look at.",
		Parameters: json.RawMessage(`{"type":"object","properties":{"targets":{"type":"array","items":{"type":"object","properties":{"path":{"type":"string","description":"Path to the file"},"symbol":{"type":"string","description":"Name of Go symbol to read (function/type/struct/variable/constant). Works only for .go files. When set, offset/limit are ignored."},"offset":{"type":"integer","description":"Starting line number (1-based)"},"limit":{"type":"integer","description":"Max lines to read"}},"required":["path"]},"maxItems":8,"minItems":1}},"required":["targets"]}`),
	}
}

type readMultiTarget struct {
	Path   string `json:"path"`
	Symbol string `json:"symbol"`
	Offset int    `json:"offset"`
	Limit  int    `json:"limit"`
}

type readMultiInput struct {
	Targets []readMultiTarget `json:"targets"`
}

// readMultiResult is one target's outcome, stored by input index.
type readMultiResult struct {
	header string
	body   string
	scope  string // canonical scope string for ReadHistory: "", "symbol:X", "L{N}-{M}", "L{N}-end"
	err    error
}

func (t *ReadMultiTool) Run(ctx tools.ToolContext, input json.RawMessage) (tools.ToolResultEnvelope, error) {
	var payload readMultiInput
	if err := json.Unmarshal(input, &payload); err != nil {
		return tools.ToolResultEnvelope{Status: tools.StatusError, Digest: fmt.Sprintf("invalid input: %v", err)}, err
	}
	if len(payload.Targets) == 0 {
		err := fmt.Errorf("at least one target required")
		return tools.ToolResultEnvelope{Status: tools.StatusError, Digest: err.Error()}, err
	}
	if len(payload.Targets) > readMultiMaxTargets {
		err := fmt.Errorf("too many targets: %d (max %d)", len(payload.Targets), readMultiMaxTargets)
		return tools.ToolResultEnvelope{Status: tools.StatusError, Digest: err.Error()}, err
	}

	results := make([]readMultiResult, len(payload.Targets))
	var wg sync.WaitGroup
	for i, tgt := range payload.Targets {
		wg.Add(1)
		go func(i int, tgt readMultiTarget) {
			defer wg.Done()
			results[i] = fetchTarget(ctx.WorkDir, i+1, tgt)
		}(i, tgt)
	}
	wg.Wait()

	var b strings.Builder
	b.WriteString("<!-- read_multi targets: ")
	parts := make([]string, len(payload.Targets))
	for i, tgt := range payload.Targets {
		parts[i] = tgt.Path + "::" + results[i].scope
	}
	b.WriteString(strings.Join(parts, " | "))
	b.WriteString(" -->\n")
	b.WriteString(fmt.Sprintf("ReadMulti: %d targets (parallel)\n", len(payload.Targets)))
	b.WriteString(strings.Repeat("─", 32) + "\n")
	for _, r := range results {
		b.WriteString(r.header + "\n")
		if r.err != nil {
			b.WriteString(fmt.Sprintf("ERROR: %v\n", r.err))
		} else {
			b.WriteString(r.body + "\n")
		}
		b.WriteString("\n")
	}
	return tools.ToolResultEnvelope{Status: tools.StatusOK, Digest: b.String()}, nil
}

// fetchTarget resolves the path and fetches content for one target.
// idx is the 1-based input order, used in the result header.
func fetchTarget(workDir string, idx int, tgt readMultiTarget) readMultiResult {
	tgt.Path = strings.TrimSpace(tgt.Path)
	r := readMultiResult{header: fmt.Sprintf("=== [%d] %s ===", idx, tgt.Path)}
	safePath, err := resolveSafePath(workDir, tgt.Path)
	if err != nil {
		r.err = err
		return r
	}

	// Determine mode and set scope/header up front so a failed fetch still
	// emits a header (the error replaces the body, not the header).
	switch {
	case tgt.Symbol != "" && strings.HasSuffix(safePath, ".go"):
		r.scope = "symbol:" + tgt.Symbol
		r.header = fmt.Sprintf("=== [%d] %s [symbol:%s] ===", idx, tgt.Path, tgt.Symbol)
		content, err := readSymbol(safePath, tgt.Symbol)
		if err != nil {
			r.err = err
			return r
		}
		lc := strings.Count(content, "\n")
		r.body = fmt.Sprintf("symbol %s (%d lines)\n%s", tgt.Symbol, lc, content)
	case tgt.Offset > 0 || tgt.Limit > 0:
		lo := tgt.Offset
		if lo < 1 {
			lo = 1
		}
		if tgt.Limit > 0 {
			r.scope = fmt.Sprintf("L%d-%d", lo, lo+tgt.Limit-1)
		} else {
			r.scope = fmt.Sprintf("L%d-end", lo)
		}
		r.header = fmt.Sprintf("=== [%d] %s [%s] ===", idx, tgt.Path, r.scope)
		content, err := readLinesContent(safePath, tgt.Offset, tgt.Limit)
		if err != nil {
			r.err = err
			return r
		}
		r.body = content
	default:
		r.scope = ""
		r.header = fmt.Sprintf("=== [%d] %s (full) ===", idx, tgt.Path)
		content, err := readFullContent(safePath)
		if err != nil {
			r.err = err
			return r
		}
		r.body = content
	}
	return r
}
