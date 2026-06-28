package builtin

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/deepact/deepact/artifact"
	"github.com/deepact/deepact/tools"
)

type EditTool struct{}

func NewEditTool() *EditTool {
	return &EditTool{}
}

func (t *EditTool) Spec() tools.ToolSpec {
	return tools.ToolSpec{
		Name:        "edit",
		Description: "Search and replace text in a file",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"},"old_string":{"type":"string"},"new_string":{"type":"string"},"replace_all":{"type":"boolean"}},"required":["path","old_string","new_string"]}`),
	}
}

type editInput struct {
	Path       string `json:"path"`
	FilePath   string `json:"file_path"` // alias for path (DeepSeek sometimes emits this)
	OldString  string `json:"old_string"`
	NewString  string `json:"new_string"`
	ReplaceAll bool   `json:"replace_all"`
}

func (t *EditTool) Run(ctx tools.ToolContext, input json.RawMessage) (tools.ToolResultEnvelope, error) {
	var payload editInput
	if err := json.Unmarshal(input, &payload); err != nil {
		return tools.ToolResultEnvelope{Status: tools.StatusError, Digest: fmt.Sprintf("invalid input: %v", err)}, err
	}
	payload.Path = strings.TrimSpace(applyFilePathAlias(payload.Path, strings.TrimSpace(payload.FilePath)))
	if payload.Path == "" {
		err := errors.New("path is required")
		return tools.ToolResultEnvelope{Status: tools.StatusError, Digest: err.Error()}, err
	}

	safePath, err := resolveSafePath(ctx.WorkDir, payload.Path)
	if err != nil {
		return tools.ToolResultEnvelope{Status: tools.StatusError, Digest: err.Error()}, err
	}

	original, err := os.ReadFile(safePath)
	if err != nil {
		return tools.ToolResultEnvelope{Status: tools.StatusError, Digest: fmt.Sprintf("read file: %v", err)}, err
	}
	content := string(original)

	updated, replacedCount, matched, multiple := replaceExact(content, payload.OldString, payload.NewString, payload.ReplaceAll)
	if matched && multiple && !payload.ReplaceAll {
		err := errors.New("multiple matches found, use replace_all=true or provide more context")
		return tools.ToolResultEnvelope{Status: tools.StatusError, Digest: err.Error()}, err
	}

	if !matched {
		fuzzyUpdated, fuzzyCount, fuzzyMatched, fuzzyMultiple := replaceFuzzy(content, payload.OldString, payload.NewString, payload.ReplaceAll)
		if !fuzzyMatched {
			err := errors.New("old_string not found in file")
			return tools.ToolResultEnvelope{Status: tools.StatusError, Digest: err.Error()}, err
		}
		if fuzzyMultiple && !payload.ReplaceAll {
			err := errors.New("multiple matches found, use replace_all=true or provide more context")
			return tools.ToolResultEnvelope{Status: tools.StatusError, Digest: err.Error()}, err
		}
		updated = fuzzyUpdated
		replacedCount = fuzzyCount
	}

	// Backup original content to artifact store before writing
	backupRef := backupOriginal(safePath, original, ctx.ArtifactDir)

	if err := os.WriteFile(safePath, []byte(updated), 0o644); err != nil {
		return tools.ToolResultEnvelope{Status: tools.StatusError, Digest: fmt.Sprintf("write file: %v", err)}, err
	}

	digest := fmt.Sprintf("updated %d occurrence(s)", replacedCount)
	if backupRef != "" {
		digest += fmt.Sprintf(", backup: %s", backupRef)
	}
	// Generate unified diff and append to digest
	relPath, _ := filepath.Rel(ctx.WorkDir, safePath)
	if relPath == "" {
		relPath = payload.Path
	}
	diff := tools.GenerateUnifiedDiff(string(original), updated, relPath)
	if diff != "" {
		digest += "\n" + diff
	}
	return tools.ToolResultEnvelope{Status: tools.StatusOK, Digest: digest}, nil
}

// backupOriginal stores original file content in the artifact store for revert capability.
// Returns the artifact ref (sha256:<hex>) or empty string if storage is unavailable.
func backupOriginal(path string, content []byte, artifactDir string) string {
	if artifactDir == "" {
		return ""
	}
	store, err := artifact.New(artifactDir)
	if err != nil {
		return ""
	}
	// Use file path hash in metadata to aid searchability
	pathHash := sha256.Sum256([]byte(path))
	meta := fmt.Sprintf("backup of %s (sha256:%s)", path, hex.EncodeToString(pathHash[:]))
	_ = meta // available for future artifact metadata
	ref, _, err := store.StoreWithRedaction(content)
	if err != nil {
		return ""
	}
	return ref
}

func replaceExact(content, oldValue, newValue string, replaceAll bool) (string, int, bool, bool) {
	if oldValue == "" {
		return content, 0, false, false
	}
	count := strings.Count(content, oldValue)
	if count == 0 {
		return content, 0, false, false
	}
	if !replaceAll && count > 1 {
		return content, count, true, true
	}
	if replaceAll {
		return strings.ReplaceAll(content, oldValue, newValue), count, true, count > 1
	}
	return strings.Replace(content, oldValue, newValue, 1), 1, true, false
}

func replaceFuzzy(content, oldValue, newValue string, replaceAll bool) (string, int, bool, bool) {
	oldValue = strings.TrimRight(oldValue, " \t\r\n")
	if oldValue == "" {
		return content, 0, false, false
	}

	contentLines := strings.Split(content, "\n")
	oldLines := splitAndTrimRight(oldValue)

	trimmedLines := make([]string, len(contentLines))
	for i, line := range contentLines {
		trimmedLines[i] = strings.TrimRight(line, " \t\r")
	}

	matches := findMatches(trimmedLines, oldLines)
	if len(matches) == 0 {
		return content, 0, false, false
	}
	if !replaceAll && len(matches) > 1 {
		return content, len(matches), true, true
	}

	replacementLines := strings.Split(newValue, "\n")
	updatedLines := applyMatches(contentLines, matches, replacementLines, len(oldLines), replaceAll)
	return strings.Join(updatedLines, "\n"), len(matches), true, len(matches) > 1
}

func splitAndTrimRight(value string) []string {
	lines := strings.Split(value, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " \t\r")
	}
	return lines
}

func findMatches(trimmedLines []string, oldLines []string) []int {
	if len(oldLines) == 0 {
		return nil
	}

	matches := make([]int, 0, 1)
	maxStart := len(trimmedLines) - len(oldLines)
	for i := 0; i <= maxStart; i++ {
		matched := true
		for j := 0; j < len(oldLines); j++ {
			if trimmedLines[i+j] != oldLines[j] {
				matched = false
				break
			}
		}
		if matched {
			matches = append(matches, i)
		}
	}
	return matches
}

func applyMatches(lines []string, matches []int, replacement []string, oldLen int, replaceAll bool) []string {
	if len(matches) == 0 {
		return lines
	}
	if oldLen <= 0 {
		oldLen = 1
	}

	useMatches := matches
	if !replaceAll {
		useMatches = matches[:1]
	}

	result := make([]string, 0, len(lines))
	matchIndex := 0
	nextMatch := useMatches[matchIndex]
	for i := 0; i < len(lines); {
		if matchIndex < len(useMatches) && i == nextMatch {
			result = append(result, replacement...)
			i += oldLen
			matchIndex++
			if matchIndex < len(useMatches) {
				nextMatch = useMatches[matchIndex]
			}
			continue
		}
		result = append(result, lines[i])
		i++
	}
	return result
}
