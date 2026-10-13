package builtin

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	deeplogcontext "github.com/deepact/deepact/context"
	"github.com/deepact/deepact/tools"
)

// preferenceSectionHeading is the fixed section in the project AGENTS.md where
// durable user preferences are recorded. The loader injects AGENTS.md verbatim
// into the stable prompt, so one stable heading lets both humans and this tool
// find the section without extra markers.
const preferenceSectionHeading = "## User Preferences"

// legacyPreferenceHeadings are headings this tool wrote before the model-facing
// text became English-only. They are still recognized — and renamed in place —
// so an AGENTS.md written earlier does not end up with two preference sections.
var legacyPreferenceHeadings = []string{"## 用户偏好"}

// RememberPreferenceTool records a durable user preference into the project
// AGENTS.md. It exists because preferences that are never written down force the
// user to repeat the same reminder every session; the tool turns that promotion
// into one confirmed step, keeping the file format and dedup deterministic
// instead of leaving them to ad-hoc edits.
type RememberPreferenceTool struct{}

func NewRememberPreferenceTool() *RememberPreferenceTool { return &RememberPreferenceTool{} }

func (t *RememberPreferenceTool) Spec() tools.ToolSpec {
	return tools.ToolSpec{
		Name:    "remember_preference",
		Mutates: true, // writes the project-root AGENTS.md
		Description: "Append a durable user preference to the project-root AGENTS.md under the \"" + preferenceSectionHeading + "\" section, deduplicating identical bullets. " +
			"Call this only AFTER the user explicitly agreed to record it (normally by first asking via ask_user). " +
			"Record only stable preferences that will recur across future sessions, not one-off task-specific instructions. " +
			"AGENTS.md is read at session start, so a newly recorded entry takes effect from the next session.",
		Parameters: json.RawMessage(`{"type":"object","properties":{"preference":{"type":"string","description":"The preference text to record, as a single concise bullet."}},"required":["preference"]}`),
	}
}

type rememberPreferenceInput struct {
	Preference string `json:"preference"`
}

func (t *RememberPreferenceTool) Run(ctx tools.ToolContext, input json.RawMessage) (tools.ToolResultEnvelope, error) {
	var payload rememberPreferenceInput
	if err := json.Unmarshal(input, &payload); err != nil {
		return tools.ToolResultEnvelope{Status: tools.StatusError, Digest: fmt.Sprintf("invalid input: %v", err)}, err
	}
	pref := normalizePreference(payload.Preference)
	if pref == "" {
		err := errors.New("preference is required")
		return tools.ToolResultEnvelope{Status: tools.StatusError, Digest: err.Error()}, err
	}

	path := deeplogcontext.ProjectAgentsMDPath(ctx.WorkDir)
	if path == "" {
		err := errors.New("cannot resolve project AGENTS.md path")
		return tools.ToolResultEnvelope{Status: tools.StatusError, Digest: err.Error()}, err
	}

	added, err := appendPreference(path, pref)
	if err != nil {
		return tools.ToolResultEnvelope{Status: tools.StatusError, Digest: fmt.Sprintf("record preference: %v", err)}, err
	}
	if !added {
		return tools.ToolResultEnvelope{Status: tools.StatusOK, Digest: "already recorded in " + path + "; no change"}, nil
	}
	return tools.ToolResultEnvelope{Status: tools.StatusOK, Digest: fmt.Sprintf("recorded preference in %s (takes effect next session)", path)}, nil
}

// normalizePreference collapses whitespace so near-identical reminders dedup and
// a recorded bullet always stays on a single line.
func normalizePreference(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// appendPreference adds pref under preferenceSectionHeading in the AGENTS.md at
// path, creating the file and/or the section when absent. It returns false when
// the same bullet already exists. The write is atomic (temp + rename) so a crash
// never leaves a half-written AGENTS.md.
func appendPreference(path, pref string) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return false, err
	}
	updated, added := upsertPreference(string(data), pref)
	if !added {
		return false, nil
	}
	if err := writeAgentsFileAtomic(path, []byte(updated)); err != nil {
		return false, err
	}
	return true, nil
}

// upsertPreference returns content with pref added under the preference section,
// plus whether anything changed. It is pure so the section semantics can be
// tested without touching the filesystem.
func upsertPreference(content, pref string) (string, bool) {
	bullet := "- " + pref
	lines := strings.Split(content, "\n")

	heading := -1
	renamed := false
	for i, ln := range lines {
		trimmed := strings.TrimRight(ln, " \t\r")
		if trimmed == preferenceSectionHeading {
			heading = i
			break
		}
		if isLegacyPreferenceHeading(trimmed) {
			heading = i
			lines[i] = preferenceSectionHeading
			renamed = true
			break
		}
	}

	if heading < 0 {
		// No section yet: append a fresh one at the end of the file.
		out := strings.TrimRight(content, "\n")
		if out != "" {
			out += "\n\n"
		}
		return out + preferenceSectionHeading + "\n\n" + bullet + "\n", true
	}

	// Section spans from just after the heading to the next heading line.
	end := len(lines)
	for i := heading + 1; i < len(lines); i++ {
		if strings.HasPrefix(strings.TrimSpace(lines[i]), "#") {
			end = i
			break
		}
	}

	for i := heading + 1; i < end; i++ {
		if strings.HasPrefix(strings.TrimSpace(lines[i]), "- ") {
			if normalizePreference(strings.TrimPrefix(strings.TrimSpace(lines[i]), "- ")) == pref {
				return strings.Join(lines, "\n"), renamed
			}
		}
	}

	// Insert after the last non-empty line of the section so the bullet joins
	// the existing list; fall back to just after the heading.
	insert := heading + 1
	for i := heading + 1; i < end; i++ {
		if strings.TrimSpace(lines[i]) != "" {
			insert = i + 1
		}
	}

	out := make([]string, 0, len(lines)+2)
	out = append(out, lines[:insert]...)
	if insert == heading+1 {
		out = append(out, "") // one blank line between heading and first bullet
	}
	out = append(out, bullet)
	out = append(out, lines[insert:]...)
	return strings.Join(out, "\n"), true
}

// isLegacyPreferenceHeading reports whether ln is a preference heading this tool
// wrote before the English-only switch (see legacyPreferenceHeadings).
func isLegacyPreferenceHeading(ln string) bool {
	for _, h := range legacyPreferenceHeadings {
		if ln == h {
			return true
		}
	}
	return false
}

// writeAgentsFileAtomic writes data to path via a temp file + rename.
func writeAgentsFileAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
