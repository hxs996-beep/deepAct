package builtin

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/deepact/deepact/tools"
)

// Patch operations. op is optional and defaults to update, so the common case
// (one or more replacements) needs the fewest fields from the model.
const (
	patchOpUpdate = "update"
	patchOpAdd    = "add"
	patchOpDelete = "delete"
	patchOpMove   = "move"
)

// ApplyPatchTool applies ordered batches of file edits in one call.
//
// It exists alongside edit rather than replacing it: edit stays the precise
// single-replacement tool, apply_patch covers multi-site and multi-file
// changes, where N separate edit round-trips would cost N turns and could
// leave the working tree half-updated when one anchor fails.
type ApplyPatchTool struct{}

func NewApplyPatchTool() *ApplyPatchTool {
	return &ApplyPatchTool{}
}

func (t *ApplyPatchTool) Spec() tools.ToolSpec {
	return tools.ToolSpec{
		Name:       "apply_patch",
		Mutates:    true,
		Sequential: true, // diff-rendered: run alone and display each change
		Description: "Apply a batch of file edits in one atomic call: update several sites and/or files, or add, delete and move files. " +
			"Prefer apply_patch for multi-site changes and use edit for a single exact replacement. " +
			"Updates locate old_string with progressively looser matching (exact substring, then ignoring trailing whitespace, then ignoring leading and trailing whitespace); " +
			"new_string is inserted verbatim. Edits apply in order. If any edit fails, nothing is written.",
		Parameters: json.RawMessage(`{"type":"object","properties":{"edits":{"type":"array","description":"Ordered list of edits. All succeed or none are written.","items":{"type":"object","properties":{"op":{"type":"string","enum":["update","add","delete","move"],"description":"Defaults to update."},"path":{"type":"string","description":"File path; relative paths resolve against the working directory."},"new_path":{"type":"string","description":"op=move: destination path."},"old_string":{"type":"string","description":"op=update: text to replace; must be unique unless replace_all=true."},"new_string":{"type":"string","description":"op=update: replacement text, inserted verbatim."},"content":{"type":"string","description":"op=add: full file content."},"replace_all":{"type":"boolean","description":"op=update: replace every occurrence instead of requiring a unique match."}},"required":["path"]}}},"required":["edits"]}`),
	}
}

type applyPatchInput struct {
	Edits []patchEdit `json:"edits"`
}

type patchEdit struct {
	Op         string `json:"op"`
	Path       string `json:"path"`
	FilePath   string `json:"file_path"` // alias for path (DeepSeek sometimes emits this)
	NewPath    string `json:"new_path"`
	OldString  string `json:"old_string"`
	NewString  string `json:"new_string"`
	Content    string `json:"content"`
	ReplaceAll bool   `json:"replace_all"`
}

// patchFileState is one file's state within a single apply_patch call. Content
// evolves in memory as ordered edits land; nothing touches disk until the whole
// batch has been planned successfully.
type patchFileState struct {
	path     string // path in effect after the batch (the write target)
	display  string // label used in the result diff
	fromPath string // op=move: original path, removed after the new path is written
	original []byte // content before the batch; nil when the file did not exist
	existed  bool
	content  string
	deleted  bool
}

// patchPlan is a fully validated batch: every anchor resolved, every path
// checked, all new contents computed in memory.
type patchPlan struct {
	states []*patchFileState
	byPath map[string]*patchFileState
	edits  int
}

func (t *ApplyPatchTool) Run(ctx tools.ToolContext, input json.RawMessage) (tools.ToolResultEnvelope, error) {
	var payload applyPatchInput
	if err := json.Unmarshal(input, &payload); err != nil {
		return patchError(fmt.Errorf("invalid input: %w", err))
	}

	plan, err := buildPatchPlan(ctx, payload.Edits)
	if err != nil {
		return patchError(err)
	}

	backups, err := applyPatchPlan(plan, ctx.ArtifactDir)
	if err != nil {
		return patchError(err)
	}

	return tools.ToolResultEnvelope{Status: tools.StatusOK, Digest: renderPatchDigest(plan, backups)}, nil
}

func patchError(err error) (tools.ToolResultEnvelope, error) {
	return tools.ToolResultEnvelope{Status: tools.StatusError, Digest: err.Error()}, err
}

// buildPatchPlan validates every edit and computes the resulting content of
// each affected file. A failure here means nothing was written anywhere.
func buildPatchPlan(ctx tools.ToolContext, edits []patchEdit) (*patchPlan, error) {
	if len(edits) == 0 {
		return nil, errors.New("edits is required: provide at least one edit")
	}
	plan := &patchPlan{byPath: make(map[string]*patchFileState, len(edits))}
	for i, edit := range edits {
		if err := plan.applyEdit(ctx, i+1, edit); err != nil {
			return nil, err
		}
	}
	plan.edits = len(edits)
	return plan, nil
}

func (p *patchPlan) applyEdit(ctx tools.ToolContext, number int, edit patchEdit) error {
	label := fmt.Sprintf("edit %d", number)

	op := strings.TrimSpace(edit.Op)
	if op == "" {
		op = patchOpUpdate
	}
	switch op {
	case patchOpUpdate, patchOpAdd, patchOpDelete, patchOpMove:
	default:
		return fmt.Errorf("%s: unknown op %q (want %s, %s, %s or %s)",
			label, edit.Op, patchOpUpdate, patchOpAdd, patchOpDelete, patchOpMove)
	}

	rawPath := applyFilePathAlias(strings.TrimSpace(edit.Path), strings.TrimSpace(edit.FilePath))
	if rawPath == "" {
		return fmt.Errorf("%s: path is required", label)
	}
	absPath, err := resolveSafePath(ctx.WorkDir, rawPath)
	if err != nil {
		return fmt.Errorf("%s: %w", label, err)
	}

	if op == patchOpAdd {
		if _, planned := p.byPath[absPath]; planned {
			return fmt.Errorf("%s: %s already exists in this batch; use op=%s instead", label, rawPath, patchOpUpdate)
		}
		if _, err := os.Stat(absPath); err == nil {
			return fmt.Errorf("%s: %s already exists; use op=%s to modify it", label, rawPath, patchOpUpdate)
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("%s: stat %s: %w", label, rawPath, err)
		}
		p.track(&patchFileState{
			path:    absPath,
			display: patchDisplayPath(ctx.WorkDir, absPath),
			content: edit.Content,
		})
		return nil
	}

	state, err := p.loadState(ctx, absPath)
	if err != nil {
		return fmt.Errorf("%s: %w", label, err)
	}

	switch op {
	case patchOpUpdate:
		if strings.TrimSpace(edit.OldString) == "" {
			return fmt.Errorf("%s: old_string is required for op=%s", label, patchOpUpdate)
		}
		updated, err := patchReplace(state.content, edit.OldString, edit.NewString, edit.ReplaceAll)
		if err != nil {
			return fmt.Errorf("%s: %s: %w", label, rawPath, err)
		}
		state.content = updated
	case patchOpDelete:
		state.deleted = true
	case patchOpMove:
		rawNewPath := strings.TrimSpace(edit.NewPath)
		if rawNewPath == "" {
			return fmt.Errorf("%s: new_path is required for op=%s", label, patchOpMove)
		}
		absNewPath, err := resolveSafePath(ctx.WorkDir, rawNewPath)
		if err != nil {
			return fmt.Errorf("%s: %w", label, err)
		}
		state.fromPath = state.path
		state.path = absNewPath
		state.display = patchDisplayPath(ctx.WorkDir, absNewPath)
		// Also resolvable by its new path, so a later edit in the same batch can
		// target either name of the file being moved.
		p.byPath[absNewPath] = state
	}
	return nil
}

// loadState returns the planned state for absPath, reading the file from disk
// the first time it is referenced.
func (p *patchPlan) loadState(ctx tools.ToolContext, absPath string) (*patchFileState, error) {
	if state, ok := p.byPath[absPath]; ok {
		return state, nil
	}
	original, err := os.ReadFile(absPath)
	if err != nil {
		return nil, fmt.Errorf("read file: %w", err)
	}
	state := &patchFileState{
		path:     absPath,
		display:  patchDisplayPath(ctx.WorkDir, absPath),
		original: original,
		existed:  true,
		content:  string(original),
	}
	p.track(state)
	return state, nil
}

func (p *patchPlan) track(state *patchFileState) {
	p.states = append(p.states, state)
	p.byPath[state.path] = state
}

// patchDisplayPath labels a file in the result diff: relative when it sits
// inside the working directory, absolute otherwise.
func patchDisplayPath(workDir, absPath string) string {
	if workDir == "" {
		return absPath
	}
	rel, err := filepath.Rel(workDir, absPath)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return absPath
	}
	return rel
}

// patchReplace rewrites oldValue to newValue using progressively looser
// matching: exact substring, then line-wise ignoring trailing whitespace (the
// level edit already uses), then line-wise ignoring leading and trailing
// whitespace. The replacement is inserted verbatim — indentation is not
// re-anchored, matching edit's behavior.
func patchReplace(content, oldValue, newValue string, replaceAll bool) (string, error) {
	if oldValue == "" {
		return content, errors.New("old_string is empty")
	}

	if count := strings.Count(content, oldValue); count > 0 {
		if !replaceAll && count > 1 {
			return content, ambiguousAnchorError(count)
		}
		if replaceAll {
			return strings.ReplaceAll(content, oldValue, newValue), nil
		}
		return strings.Replace(content, oldValue, newValue, 1), nil
	}

	contentLines := strings.Split(content, "\n")
	oldLines := strings.Split(strings.TrimRight(oldValue, " \t\r\n"), "\n")
	replacementLines := strings.Split(newValue, "\n")

	// Level 2 mirrors edit's fallback; level 3 additionally ignores indentation.
	looserLevels := []func(string) string{
		func(s string) string { return strings.TrimRight(s, " \t\r") },
		strings.TrimSpace,
	}
	for _, normalize := range looserLevels {
		matches := findMatches(normalizeLines(contentLines, normalize), normalizeLines(oldLines, normalize))
		if len(matches) == 0 {
			continue
		}
		if !replaceAll && len(matches) > 1 {
			return content, ambiguousAnchorError(len(matches))
		}
		updated := applyMatches(contentLines, matches, replacementLines, len(oldLines), replaceAll)
		return strings.Join(updated, "\n"), nil
	}

	return content, errors.New("old_string not found in file")
}

func ambiguousAnchorError(count int) error {
	return fmt.Errorf("old_string matches %d places; add more surrounding context or set replace_all=true", count)
}

func normalizeLines(lines []string, normalize func(string) string) []string {
	normalized := make([]string, len(lines))
	for i, line := range lines {
		normalized[i] = normalize(line)
	}
	return normalized
}

// applyPatchPlan writes the planned states in order. On a write failure it
// rolls back everything already written and reports any rollback failure rather
// than hiding it.
func applyPatchPlan(plan *patchPlan, artifactDir string) (map[*patchFileState]string, error) {
	backups := make(map[*patchFileState]string, len(plan.states))
	if artifactDir != "" {
		for _, state := range plan.states {
			if !state.existed {
				continue
			}
			if ref := backupOriginal(state.path, state.original, artifactDir); ref != "" {
				backups[state] = ref
			}
		}
	}

	written := make([]*patchFileState, 0, len(plan.states))
	for _, state := range plan.states {
		if err := writePatchState(state); err != nil {
			if failures := rollbackPatchStates(written); len(failures) > 0 {
				return nil, fmt.Errorf("%w (rollback incomplete: %s)", err, strings.Join(failures, "; "))
			}
			return nil, err
		}
		written = append(written, state)
	}
	return backups, nil
}

func writePatchState(state *patchFileState) error {
	if state.deleted {
		if err := os.Remove(state.path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("delete %s: %w", state.display, err)
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(state.path), 0o755); err != nil {
		return fmt.Errorf("create directory for %s: %w", state.display, err)
	}
	if err := os.WriteFile(state.path, []byte(state.content), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", state.display, err)
	}
	if state.fromPath != "" && state.fromPath != state.path {
		if err := os.Remove(state.fromPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("move %s: remove original: %w", state.display, err)
		}
	}
	return nil
}

// rollbackPatchStates restores the written files in reverse order, returning a
// description of every failure so the caller can surface a partial rollback.
func rollbackPatchStates(written []*patchFileState) []string {
	var failures []string
	for i := len(written) - 1; i >= 0; i-- {
		state := written[i]
		if state.fromPath != "" {
			if err := os.WriteFile(state.fromPath, state.original, 0o644); err != nil {
				failures = append(failures, fmt.Sprintf("restore %s: %v", state.fromPath, err))
			}
			if err := os.Remove(state.path); err != nil && !os.IsNotExist(err) {
				failures = append(failures, fmt.Sprintf("remove %s: %v", state.path, err))
			}
			continue
		}
		if !state.existed {
			if err := os.Remove(state.path); err != nil && !os.IsNotExist(err) {
				failures = append(failures, fmt.Sprintf("remove %s: %v", state.path, err))
			}
			continue
		}
		if err := os.WriteFile(state.path, state.original, 0o644); err != nil {
			failures = append(failures, fmt.Sprintf("restore %s: %v", state.path, err))
		}
	}
	return failures
}

// renderPatchDigest builds the result the model and the UI both consume:
// summary line, then the unified diffs (the UI renders hunks from exactly this
// shape — see ui.isDiffContent / ui.splitDiff), then the backup refs that feed
// the revert tool.
func renderPatchDigest(plan *patchPlan, backups map[*patchFileState]string) string {
	var b strings.Builder
	added, removed := 0, 0
	var diffs strings.Builder

	for _, state := range plan.states {
		before := string(state.original)
		after := state.content
		if state.deleted {
			after = ""
		}
		diff := tools.GenerateUnifiedDiff(before, after, state.display)
		if diff == "" {
			continue
		}
		added += countDiffLines(diff, "+")
		removed += countDiffLines(diff, "-")
		diffs.WriteString(diff)
	}

	fmt.Fprintf(&b, "applied %d edit(s) across %d file(s) (+%d -%d)", plan.edits, len(plan.states), added, removed)

	if diffs.Len() > 0 {
		b.WriteString("\n")
		b.WriteString(diffs.String())
	}

	refs := make([]string, 0, len(backups))
	for _, state := range plan.states {
		if ref, ok := backups[state]; ok {
			refs = append(refs, fmt.Sprintf("%s=%s", state.display, ref))
		}
	}
	if len(refs) > 0 {
		b.WriteString("backups: ")
		b.WriteString(strings.Join(refs, ", "))
		b.WriteString("\n")
	}

	return strings.TrimRight(b.String(), "\n")
}

// countDiffLines counts added or removed lines, excluding the +++/--- file
// headers so the reported totals match what the diff actually changes.
func countDiffLines(diff, prefix string) int {
	count := 0
	header := strings.Repeat(prefix, 3)
	for _, line := range strings.Split(diff, "\n") {
		if strings.HasPrefix(line, prefix) && !strings.HasPrefix(line, header) {
			count++
		}
	}
	return count
}
