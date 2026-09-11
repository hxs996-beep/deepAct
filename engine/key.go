package engine

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
)

// normalizePath canonicalizes a file path so the same physical file yields one
// loop-detection key regardless of how the model addressed it (relative path,
// "./" prefix, absolute path, or via the file_path alias). Relative paths are
// resolved against workDir; without a workDir they are only Cleaned. This
// prevents the model from splitting its repeat count across path forms and
// never tripping the guard — the root cause of repeated reads.
func normalizePath(p, workDir string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	if filepath.IsAbs(p) || workDir == "" {
		return filepath.Clean(p)
	}
	return filepath.Clean(filepath.Join(workDir, p))
}

// extractToolKey extracts a unique key from a tool call for loop detection.
// Returns "toolName:path:contentHash" for destructive tools, or "" for
// exploratory tools. Content hash ensures that different edits on the same
// file (different old_string→new_string) are treated as distinct operations,
// preventing false loop detection when modifying multiple locations.
func extractToolKey(call ToolCallRequest, workDir string) string {
	path := extractPathField(call.Input, workDir)
	if path == "" {
		return ""
	}

	var contentHash string
	switch call.Name {
	case "edit":
		contentHash = extractEditContentHash(call.Input)
	case "write":
		contentHash = extractWriteContentHash(call.Input)
	case "read":
		// Human-readable scope ("", "symbol:Run", "L10-50") — aligned with
		// LastOp and ReadRecord so all three use one consistent key form.
		return "read:" + path + "::" + extractReadScope(call.Input)
	default:
		// grep/glob/bash etc. — not tracked for loops
		return ""
	}

	if contentHash == "" {
		return ""
	}

	return call.Name + ":" + path + ":" + contentHash
}

// extractEditContentHash computes sha256("old_string→new_string") from edit input.
func extractEditContentHash(input json.RawMessage) string {
	var m map[string]interface{}
	if err := json.Unmarshal(input, &m); err != nil {
		return ""
	}
	oldStr, _ := m["old_string"].(string)
	newStr, _ := m["new_string"].(string)
	h := sha256.Sum256([]byte(oldStr + "\x00" + newStr))
	return hex.EncodeToString(h[:])
}

// extractWriteContentHash computes sha256(content) from write input.
func extractWriteContentHash(input json.RawMessage) string {
	var m map[string]interface{}
	if err := json.Unmarshal(input, &m); err != nil {
		return ""
	}
	content, _ := m["content"].(string)
	if content == "" {
		return ""
	}
	h := sha256.Sum256([]byte(content))
	return hex.EncodeToString(h[:])
}

// readMultiTargetView is engine's view of a read_multi target (mirrors the
// tools/builtin readMultiTarget struct, kept unexported and local to avoid a
// tools→engine import).
type readMultiTargetView struct {
	Path   string `json:"path"`
	Symbol string `json:"symbol"`
	Offset int    `json:"offset"`
	Limit  int    `json:"limit"`
}

// parseReadMultiTargets parses the targets array from a read_multi tool call's
// input. Returns nil on error.
func parseReadMultiTargets(input json.RawMessage) []readMultiTargetView {
	var m struct {
		Targets []readMultiTargetView `json:"targets"`
	}
	if err := json.Unmarshal(input, &m); err != nil {
		return nil
	}
	return m.Targets
}

// readMultiTargetScope derives the same scope string extractReadScope would
// produce for a read_multi target (symbol first, then offset/limit range).
// Used so read_multi sub-targets share the read key space with plain reads —
// reading the same (path, scope) via either tool is recognized as a repeat.
func readMultiTargetScope(t readMultiTargetView) string {
	if t.Symbol != "" {
		return "symbol:" + t.Symbol
	}
	if t.Offset == 0 && t.Limit == 0 {
		return ""
	}
	start := t.Offset
	if start == 0 {
		start = 1
	}
	if t.Limit == 0 {
		return fmt.Sprintf("L%d-", start)
	}
	return fmt.Sprintf("L%d-%d", start, t.Limit)
}

func extractPathField(input json.RawMessage, workDir string) string {
	if len(input) == 0 {
		return ""
	}
	var m map[string]interface{}
	if err := json.Unmarshal(input, &m); err != nil {
		return ""
	}
	if p, ok := m["path"].(string); ok {
		return normalizePath(p, workDir)
	}
	if p, ok := m["file_path"].(string); ok {
		return normalizePath(p, workDir)
	}
	return ""
}
