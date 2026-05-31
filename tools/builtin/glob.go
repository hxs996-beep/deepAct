package builtin

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/deepact/deepact/tools"
)

type GlobTool struct{}

func NewGlobTool() *GlobTool {
	return &GlobTool{}
}

func (t *GlobTool) Spec() tools.ToolSpec {
	return tools.ToolSpec{
		Name:        "glob",
		Description: "Find files matching a glob pattern",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"pattern":{"type":"string"},"path":{"type":"string"}},"required":["pattern"]}`),
	}
}

type globInput struct {
	Pattern string `json:"pattern"`
	Path    string `json:"path"`
}

type globMatch struct {
	Path    string
	ModTime time.Time
}

func (t *GlobTool) Run(ctx tools.ToolContext, input json.RawMessage) (tools.ToolResultEnvelope, error) {
	var payload globInput
	if err := json.Unmarshal(input, &payload); err != nil {
		return tools.ToolResultEnvelope{Status: tools.StatusError, Digest: fmt.Sprintf("invalid input: %v", err)}, err
	}
	payload.Pattern = strings.TrimSpace(payload.Pattern)
	if payload.Pattern == "" {
		err := errors.New("pattern is required")
		return tools.ToolResultEnvelope{Status: tools.StatusError, Digest: err.Error()}, err
	}
	root := payload.Path
	if root == "" {
		root = "."
	}
	safeRoot, err := resolveSafePath(ctx.WorkDir, root)
	if err != nil {
		return tools.ToolResultEnvelope{Status: tools.StatusError, Digest: err.Error()}, err
	}

	matches, err := globSearch(safeRoot, payload.Pattern)
	if err != nil {
		return tools.ToolResultEnvelope{Status: tools.StatusError, Digest: err.Error()}, err
	}
	if len(matches) == 0 {
		return tools.ToolResultEnvelope{Status: tools.StatusOK, Digest: "(no matches)"}, nil
	}
	sort.Slice(matches, func(i, j int) bool {
		return matches[i].ModTime.After(matches[j].ModTime)
	})
	if len(matches) > 100 {
		matches = matches[:100]
	}
	var builder strings.Builder
	for _, match := range matches {
		builder.WriteString(match.Path)
		builder.WriteString("\n")
	}
	return tools.ToolResultEnvelope{Status: tools.StatusOK, Digest: strings.TrimRight(builder.String(), "\n")}, nil
}

func globSearch(root, pattern string) ([]globMatch, error) {
	normalizedPattern := filepath.ToSlash(pattern)
	matches := make([]globMatch, 0, 16)

	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
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

		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if matchGlob(normalizedPattern, rel) {
			info, err := entry.Info()
			if err != nil {
				return nil
			}
			matches = append(matches, globMatch{Path: rel, ModTime: info.ModTime()})
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("glob search: %w", err)
	}
	return matches, nil
}

func matchGlob(pattern, target string) bool {
	patternSegments := splitSegments(pattern)
	targetSegments := splitSegments(target)
	return matchSegments(patternSegments, targetSegments)
}

func splitSegments(path string) []string {
	parts := strings.Split(path, "/")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if part == "" {
			continue
		}
		result = append(result, part)
	}
	return result
}

func matchSegments(pattern, target []string) bool {
	if len(pattern) == 0 {
		return len(target) == 0
	}
	if pattern[0] == "**" {
		for skip := 0; skip <= len(target); skip++ {
			if matchSegments(pattern[1:], target[skip:]) {
				return true
			}
		}
		return false
	}
	if len(target) == 0 {
		return false
	}
	if !matchSegment(pattern[0], target[0]) {
		return false
	}
	return matchSegments(pattern[1:], target[1:])
}

func matchSegment(pattern, target string) bool {
	if pattern == "*" {
		return true
	}
	pi := 0
	ti := 0
	for pi < len(pattern) {
		switch pattern[pi] {
		case '*':
			for pi < len(pattern) && pattern[pi] == '*' {
				pi++
			}
			if pi == len(pattern) {
				return true
			}
			for ti < len(target) {
				if matchSegment(pattern[pi:], target[ti:]) {
					return true
				}
				ti++
			}
			return false
		default:
			if ti >= len(target) || pattern[pi] != target[ti] {
				return false
			}
			pi++
			ti++
		}
	}
	return ti == len(target)
}
