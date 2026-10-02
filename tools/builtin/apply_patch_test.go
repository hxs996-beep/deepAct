package builtin

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/deepact/deepact/tools"
)

// runPatch is the shared harness: it marshals edits into the tool's input
// shape and runs apply_patch against dir.
func runPatch(t *testing.T, dir string, edits []patchEdit) (tools.ToolResultEnvelope, error) {
	t.Helper()
	input, err := json.Marshal(applyPatchInput{Edits: edits})
	if err != nil {
		t.Fatalf("marshal input: %v", err)
	}
	return NewApplyPatchTool().Run(tools.ToolContext{WorkDir: dir}, input)
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// TestApplyPatchTool_MultiFileMultiHunk is the core capability: one call
// changes several sites across several files.
func TestApplyPatchTool_MultiFileMultiHunk(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("alpha\nbeta\ngamma\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "b.txt"), []byte("one\ntwo\n"), 0o644)

	result, err := runPatch(t, dir, []patchEdit{
		{Path: "a.txt", OldString: "alpha", NewString: "ALPHA"},
		{Path: "a.txt", OldString: "gamma", NewString: "GAMMA"},
		{Path: "b.txt", OldString: "two", NewString: "TWO"},
	})
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if result.Status != tools.StatusOK {
		t.Fatalf("status = %q, digest: %s", result.Status, result.Digest)
	}
	if got := readFile(t, filepath.Join(dir, "a.txt")); got != "ALPHA\nbeta\nGAMMA\n" {
		t.Errorf("a.txt = %q", got)
	}
	if got := readFile(t, filepath.Join(dir, "b.txt")); got != "one\nTWO\n" {
		t.Errorf("b.txt = %q", got)
	}
}

// TestApplyPatchTool_OrderedEditsOnSameFile verifies edits apply sequentially
// against the evolving content: the second edit's anchor only exists after the
// first edit landed.
func TestApplyPatchTool_OrderedEditsOnSameFile(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("start\n"), 0o644)

	_, err := runPatch(t, dir, []patchEdit{
		{Path: "a.txt", OldString: "start", NewString: "middle"},
		{Path: "a.txt", OldString: "middle", NewString: "end"},
	})
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if got := readFile(t, filepath.Join(dir, "a.txt")); got != "end\n" {
		t.Errorf("a.txt = %q, want sequential application", got)
	}
}

// TestApplyPatchTool_FuzzyTrailingWhitespace covers looseness level 2:
// the anchor matches lines that only differ by trailing whitespace.
func TestApplyPatchTool_FuzzyTrailingWhitespace(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("line one  \nline two\n"), 0o644)

	_, err := runPatch(t, dir, []patchEdit{
		{Path: "a.txt", OldString: "line one\nline two", NewString: "ONE\nTWO"},
	})
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if got := readFile(t, filepath.Join(dir, "a.txt")); got != "ONE\nTWO\n" {
		t.Errorf("a.txt = %q", got)
	}
}

// TestApplyPatchTool_FuzzyLeadingWhitespace covers looseness level 3: the
// anchor matches lines that differ only in indentation.
//
// The replacement is inserted verbatim — indentation is NOT re-anchored to the
// matched region, exactly like edit's line-wise fallback. This test therefore
// pins the matched-and-replaced lines, not surrounding formatting: the block is
// replaced, and the caller is expected to supply new_string with the
// indentation it wants.
func TestApplyPatchTool_FuzzyLeadingWhitespace(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.go")
	os.WriteFile(path, []byte("func f() {\n\tif x {\n\t\treturn\n\t}\n}\n"), 0o644)

	// Anchor is de-indented relative to the file; the replacement is written
	// with its own indentation.
	_, err := runPatch(t, dir, []patchEdit{
		{Path: "a.go", OldString: "if x {\nreturn\n}", NewString: "\tif y {\n\t\treturn\n\t}"},
	})
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	want := "func f() {\n\tif y {\n\t\treturn\n\t}\n}\n"
	if got := readFile(t, path); got != want {
		t.Errorf("a.go = %q, want %q", got, want)
	}
}

// TestApplyPatchTool_AmbiguousAnchorErrors verifies ambiguity is refused rather
// than guessed, leaving the file untouched.
func TestApplyPatchTool_AmbiguousAnchorErrors(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	os.WriteFile(path, []byte("foo\nbar\nfoo\n"), 0o644)

	result, err := runPatch(t, dir, []patchEdit{{Path: "a.txt", OldString: "foo", NewString: "baz"}})
	if err == nil {
		t.Fatal("expected error for ambiguous anchor")
	}
	if result.Status != tools.StatusError {
		t.Errorf("status = %q, want error", result.Status)
	}
	if got := readFile(t, path); got != "foo\nbar\nfoo\n" {
		t.Errorf("file changed on error: %q", got)
	}
}

// TestApplyPatchTool_ReplaceAllAllowsAmbiguity verifies replace_all opts into
// replacing every match.
func TestApplyPatchTool_ReplaceAllAllowsAmbiguity(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("foo\nbar\nfoo\n"), 0o644)

	_, err := runPatch(t, dir, []patchEdit{
		{Path: "a.txt", OldString: "foo", NewString: "baz", ReplaceAll: true},
	})
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if got := readFile(t, filepath.Join(dir, "a.txt")); got != "baz\nbar\nbaz\n" {
		t.Errorf("a.txt = %q", got)
	}
}

// TestApplyPatchTool_AtomicOnPartialFailure is the atomicity contract: when one
// edit cannot be located, no file in the batch is modified.
func TestApplyPatchTool_AtomicOnPartialFailure(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good.txt")
	other := filepath.Join(dir, "other.txt")
	os.WriteFile(good, []byte("hello\n"), 0o644)
	os.WriteFile(other, []byte("world\n"), 0o644)

	result, err := runPatch(t, dir, []patchEdit{
		{Path: "good.txt", OldString: "hello", NewString: "HELLO"},
		{Path: "other.txt", OldString: "not present", NewString: "x"},
	})
	if err == nil {
		t.Fatal("expected error for unlocatable anchor")
	}
	if result.Status != tools.StatusError {
		t.Errorf("status = %q, want error", result.Status)
	}
	if got := readFile(t, good); got != "hello\n" {
		t.Errorf("good.txt was modified despite batch failure: %q", got)
	}
	if got := readFile(t, other); got != "world\n" {
		t.Errorf("other.txt changed: %q", got)
	}
}

// TestApplyPatchTool_AddDeleteMove covers the three file-level operations.
func TestApplyPatchTool_AddDeleteMove(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "old.txt"), []byte("gone\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "mv.txt"), []byte("carried\n"), 0o644)

	result, err := runPatch(t, dir, []patchEdit{
		{Op: "add", Path: "sub/created.txt", Content: "brand new\n"},
		{Op: "delete", Path: "old.txt"},
		{Op: "move", Path: "mv.txt", NewPath: "sub/moved.txt"},
	})
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if result.Status != tools.StatusOK {
		t.Fatalf("status = %q, digest: %s", result.Status, result.Digest)
	}
	if got := readFile(t, filepath.Join(dir, "sub", "created.txt")); got != "brand new\n" {
		t.Errorf("created.txt = %q", got)
	}
	if got := readFile(t, filepath.Join(dir, "sub", "moved.txt")); got != "carried\n" {
		t.Errorf("moved.txt = %q", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "old.txt")); !os.IsNotExist(err) {
		t.Error("old.txt still exists")
	}
	if _, err := os.Stat(filepath.Join(dir, "mv.txt")); !os.IsNotExist(err) {
		t.Error("mv.txt still exists after move")
	}
}

// TestApplyPatchTool_ErrorsAreSpecific verifies invalid batches are rejected
// with actionable messages instead of silently doing nothing.
func TestApplyPatchTool_ErrorsAreSpecific(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "exists.txt"), []byte("x\n"), 0o644)

	cases := []struct {
		name   string
		edits  []patchEdit
		substr string
	}{
		{"empty batch", nil, "edits"},
		{"unknown op", []patchEdit{{Op: "rename", Path: "exists.txt"}}, "op"},
		{"update missing anchor", []patchEdit{{Path: "exists.txt"}}, "old_string"},
		{"add existing file", []patchEdit{{Op: "add", Path: "exists.txt", Content: "y"}}, "exists"},
		{"delete missing file", []patchEdit{{Op: "delete", Path: "nope.txt"}}, "nope.txt"},
		{"move without new_path", []patchEdit{{Op: "move", Path: "exists.txt"}}, "new_path"},
		{"update missing file", []patchEdit{{Path: "nope.txt", OldString: "a", NewString: "b"}}, "nope.txt"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := runPatch(t, dir, tc.edits)
			if err == nil {
				t.Fatalf("expected error, got status %q digest %s", result.Status, result.Digest)
			}
			if !strings.Contains(err.Error(), tc.substr) {
				t.Errorf("error = %q, want substring %q", err.Error(), tc.substr)
			}
		})
	}
}

// TestApplyPatchTool_OpDefaultsToUpdate verifies the common case needs no op
// field — the shape the model is most likely to emit.
func TestApplyPatchTool_OpDefaultsToUpdate(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("before\n"), 0o644)

	_, err := runPatch(t, dir, []patchEdit{{Path: "a.txt", OldString: "before", NewString: "after"}})
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if got := readFile(t, filepath.Join(dir, "a.txt")); got != "after\n" {
		t.Errorf("a.txt = %q", got)
	}
}

// TestApplyPatchTool_DigestShape locks the result contract the UI depends on:
// line 1 is the summary, everything after it starts with a unified diff
// (ui.isDiffContent / ui.splitDiff / ui.parseDiffHunks).
func TestApplyPatchTool_DigestShape(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("alpha\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "b.txt"), []byte("beta\n"), 0o644)

	result, err := runPatch(t, dir, []patchEdit{
		{Path: "a.txt", OldString: "alpha", NewString: "ALPHA"},
		{Path: "b.txt", OldString: "beta", NewString: "BETA"},
	})
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}

	lines := strings.Split(result.Digest, "\n")
	if len(lines) < 2 {
		t.Fatalf("digest too short: %q", result.Digest)
	}
	if strings.HasPrefix(lines[0], "---") || strings.HasPrefix(lines[0], "@@") {
		t.Errorf("first line must be the summary, got %q", lines[0])
	}
	// Mirrors ui.isDiffContent: the diff block must carry both markers.
	if !strings.Contains(result.Digest, "\n--- a/") || !strings.Contains(result.Digest, "\n+++ b/") {
		t.Errorf("digest missing unified diff markers:\n%s", result.Digest)
	}
	// Mirrors ui.splitDiff: content after line 1 must start with a file header.
	if !strings.HasPrefix(lines[1], "--- a/") {
		t.Errorf("second line = %q, want a '--- a/' file header", lines[1])
	}
	for _, want := range []string{"a.txt", "b.txt", "-alpha", "+ALPHA", "-beta", "+BETA"} {
		if !strings.Contains(result.Digest, want) {
			t.Errorf("digest missing %q:\n%s", want, result.Digest)
		}
	}
}

var backupRefRe = regexp.MustCompile(`sha256:[0-9a-f]{64}`)

// TestApplyPatchTool_BackupFeedsRevert proves the emitted backup ref is
// directly usable by the revert tool — the reason the refs are in the digest.
func TestApplyPatchTool_BackupFeedsRevert(t *testing.T) {
	dir := t.TempDir()
	artifactDir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	os.WriteFile(path, []byte("original\n"), 0o644)

	input, _ := json.Marshal(applyPatchInput{Edits: []patchEdit{
		{Path: "a.txt", OldString: "original", NewString: "changed"},
	}})
	result, err := NewApplyPatchTool().Run(tools.ToolContext{WorkDir: dir, ArtifactDir: artifactDir}, input)
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}

	ref := backupRefRe.FindString(result.Digest)
	if ref == "" {
		t.Fatalf("digest carries no backup ref:\n%s", result.Digest)
	}

	revertInput, _ := json.Marshal(revertInput{Path: path, Ref: ref})
	revertResult, err := NewRevertTool().Run(tools.ToolContext{WorkDir: dir, ArtifactDir: artifactDir}, revertInput)
	if err != nil {
		t.Fatalf("revert error: %v", err)
	}
	if revertResult.Status != tools.StatusOK {
		t.Fatalf("revert status = %q, digest: %s", revertResult.Status, revertResult.Digest)
	}
	if got := readFile(t, path); got != "original\n" {
		t.Errorf("after revert a.txt = %q, want original", got)
	}
}

// TestApplyPatchTool_NoBackupLineWithoutArtifactStore verifies the backups line
// is omitted (not emitted empty) when no store is configured.
func TestApplyPatchTool_NoBackupLineWithoutArtifactStore(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("original\n"), 0o644)

	result, err := runPatch(t, dir, []patchEdit{{Path: "a.txt", OldString: "original", NewString: "changed"}})
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if strings.Contains(result.Digest, "backups:") {
		t.Errorf("digest should not carry backups without an artifact store:\n%s", result.Digest)
	}
}
