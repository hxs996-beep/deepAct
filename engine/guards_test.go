package engine

import (
	"encoding/json"
	"strings"
	"testing"
)

// --- LoopTracker (unified counting core) ---

func TestLoopTracker_DefaultBlockAt(t *testing.T) {
	tr := NewLoopTracker(0, 0, false)
	tr.Check("k", false)
	tr.Check("k", false)
	tr.Check("k", false)
	if a := tr.Check("k", false); a.Type != GuardBlock {
		t.Fatalf("4th repeat: want block, got %s", a.Type)
	}
}

func TestLoopTracker_AllowUntilBlockAt(t *testing.T) {
	tr := NewLoopTracker(0, 3, false)
	if a := tr.Check("k", false); a.Type != GuardAllow {
		t.Fatal("1st should allow")
	}
	if a := tr.Check("k", false); a.Type != GuardAllow {
		t.Fatal("2nd should allow")
	}
	if a := tr.Check("k", false); a.Type != GuardBlock {
		t.Fatalf("3rd should block, got %s", a.Type)
	}
}

func TestLoopTracker_NudgeThenBlock(t *testing.T) {
	tr := NewLoopTracker(3, 4, false)
	tr.Check("k", false)
	tr.Check("k", false)
	if a := tr.Check("k", false); a.Type != GuardDiagnose {
		t.Fatalf("3rd: want diagnose(nudge), got %s", a.Type)
	}
	if a := tr.Check("k", false); a.Type != GuardBlock {
		t.Fatalf("4th: want block, got %s", a.Type)
	}
}

func TestLoopTracker_NoNudgeWhenNudgeAtZero(t *testing.T) {
	tr := NewLoopTracker(0, 4, false)
	tr.Check("k", false)
	tr.Check("k", false)
	if a := tr.Check("k", false); a.Type != GuardAllow {
		t.Fatalf("3rd: want allow (no nudge tier), got %s", a.Type)
	}
	if a := tr.Check("k", false); a.Type != GuardBlock {
		t.Fatalf("4th: want block, got %s", a.Type)
	}
}

func TestLoopTracker_DifferentKeysIndependent(t *testing.T) {
	tr := NewLoopTracker(0, 2, false)
	tr.Check("a", false)
	if a := tr.Check("b", false); a.Type != GuardAllow {
		t.Fatalf("different key should be independent, got %s", a.Type)
	}
	if a := tr.Check("a", false); a.Type != GuardBlock {
		t.Fatalf("same key 2nd: want block, got %s", a.Type)
	}
}

func TestLoopTracker_ResetOnSuccess(t *testing.T) {
	tr := NewLoopTracker(0, 3, true)
	tr.Check("k", false)                                // error
	tr.Check("k", false)                                // error
	if a := tr.Check("k", true); a.Type != GuardAllow { // success resets
		t.Fatalf("success should clear streak, got %s", a.Type)
	}
	tr.Check("k", false)
	tr.Check("k", false)
	tr.Check("k", false) // block
	if a := tr.Check("k", false); a.Type != GuardBlock {
		t.Fatalf("after reset + 3 errors: want block, got %s", a.Type)
	}
}

func TestLoopTracker_GlobalCounter(t *testing.T) {
	// progress: key="" single global counter, success resets it.
	tr := NewLoopTracker(4, 6, true)
	for i := 0; i < 3; i++ {
		tr.Check("", false)
	}
	if a := tr.Check("", false); a.Type != GuardDiagnose {
		t.Fatalf("4th: want nudge, got %s", a.Type)
	}
	if a := tr.Check("", true); a.Type != GuardAllow {
		t.Fatalf("progress signal should reset, got %s", a.Type)
	}
	tr.Check("", false)
	if a := tr.Check("", false); a.Type != GuardAllow {
		t.Fatalf("post-reset counting should restart, got %s", a.Type)
	}
}

func TestLoopTracker_Reset(t *testing.T) {
	tr := NewLoopTracker(0, 2, false)
	tr.Check("k", false)
	tr.Reset()
	if a := tr.Check("k", false); a.Type != GuardAllow {
		t.Fatalf("after reset should allow, got %s", a.Type)
	}
}

func TestLoopTracker_NilSafe(t *testing.T) {
	var tr *LoopTracker
	if a := tr.Check("k", false); a.Type != GuardAllow {
		t.Fatalf("nil Check: want allow, got %s", a.Type)
	}
	tr.Reset() // must not panic
}

func TestExtractPathField(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"empty", ``, ""},
		{"invalid json", `not json`, ""},
		{"path key", `{"path":"foo.go"}`, "foo.go"},
		{"file_path key", `{"file_path":"bar.go"}`, "bar.go"},
		{"no path", `{"pattern":"foo"}`, ""},
		{"empty path", `{"path":""}`, ""},
	}
	for _, tt := range tests {
		raw := json.RawMessage(tt.input)
		if got := extractPathField(raw, ""); got != tt.want {
			t.Errorf("%s: extractPathField = %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestExtractEditContentHash(t *testing.T) {
	// Valid input produces a deterministic non-empty hash
	h1 := extractEditContentHash(json.RawMessage(`{"old_string":"a","new_string":"b"}`))
	if h1 == "" {
		t.Error("expected non-empty hash for valid input")
	}
	// Same input → same hash
	h2 := extractEditContentHash(json.RawMessage(`{"old_string":"a","new_string":"b"}`))
	if h1 != h2 {
		t.Error("hash should be deterministic")
	}
	// Different input → different hash
	h3 := extractEditContentHash(json.RawMessage(`{"old_string":"a","new_string":"c"}`))
	if h1 == h3 {
		t.Error("different inputs should produce different hashes")
	}
	// Invalid json → empty
	h4 := extractEditContentHash(json.RawMessage(`not json`))
	if h4 != "" {
		t.Errorf("invalid json should produce empty, got %s", h4)
	}
}

func TestExtractWriteContentHash(t *testing.T) {
	// Non-empty content produces hash
	h1 := extractWriteContentHash(json.RawMessage(`{"content":"hello world"}`))
	if h1 == "" {
		t.Error("expected non-empty hash for valid content")
	}
	// Same input → same hash
	h2 := extractWriteContentHash(json.RawMessage(`{"content":"hello world"}`))
	if h1 != h2 {
		t.Error("hash should be deterministic")
	}
	// Empty content → empty hash
	h3 := extractWriteContentHash(json.RawMessage(`{"content":""}`))
	if h3 != "" {
		t.Errorf("expected empty for empty content, got %s", h3)
	}
	// Missing content → empty hash
	h4 := extractWriteContentHash(json.RawMessage(`{"path":"foo.go"}`))
	if h4 != "" {
		t.Errorf("expected empty for missing content, got %s", h4)
	}
	// Invalid json → empty
	h5 := extractWriteContentHash(json.RawMessage(`bad`))
	if h5 != "" {
		t.Errorf("expected empty for invalid json, got %s", h5)
	}
}

func TestExtractToolKey(t *testing.T) {
	// edit tool produces key with hash
	editCall := makeToolCall("edit", `{"path":"foo.go","old_string":"a","new_string":"b"}`)
	editKey := extractToolKey(editCall, "")
	if !strings.HasPrefix(editKey, "edit:foo.go:") {
		t.Errorf("edit tool key should start with 'edit:foo.go:', got %q", editKey)
	}
	if len(editKey) <= len("edit:foo.go:") {
		t.Error("edit tool key should include content hash")
	}

	// read no scope → "read:path::"
	readNoScope := extractToolKey(makeToolCall("read", `{"path":"foo.go"}`), "")
	if readNoScope != "read:foo.go::" {
		t.Errorf("read no scope should be 'read:foo.go::', got %q", readNoScope)
	}

	// read with symbol → "read:path::symbol:Name"
	readSym := extractToolKey(makeToolCall("read", `{"path":"foo.go","symbol":"TestFoo"}`), "")
	if readSym != "read:foo.go::symbol:TestFoo" {
		t.Errorf("read with symbol should be 'read:foo.go::symbol:TestFoo', got %q", readSym)
	}

	// read with offset/limit → "read:path::L<off>-<limit>"
	readRange := extractToolKey(makeToolCall("read", `{"path":"foo.go","offset":10,"limit":50}`), "")
	if readRange != "read:foo.go::L10-50" {
		t.Errorf("read with offset/limit should be 'read:foo.go::L10-50', got %q", readRange)
	}

	// grep not tracked
	grepKey := extractToolKey(makeToolCall("grep", `{"pattern":"foo"}`), "")
	if grepKey != "" {
		t.Errorf("grep should not be tracked, got %q", grepKey)
	}

	// no path → empty
	noPath := extractToolKey(makeToolCall("edit", `{"old_string":"a","new_string":"b"}`), "")
	if noPath != "" {
		t.Errorf("edit without path should return empty, got %q", noPath)
	}
}

// --- ScopeGuard ---

func TestNewScopeGuard(t *testing.T) {
	g := NewScopeGuard(false)
	if g == nil {
		t.Fatal("expected non-nil ScopeGuard")
	}
	if g.dangerousConfirmed == nil {
		t.Error("dangerousConfirmed map should be initialized")
	}
}

func TestScopeGuard_ConfirmDangerous(t *testing.T) {
	g := NewScopeGuard(false)
	g.ConfirmDangerous("rm -rf /tmp")
	if !g.dangerousConfirmed["rm -rf /tmp"] {
		t.Error("expected command to be confirmed")
	}
	if g.dangerousPending != "" {
		t.Errorf("dangerousPending should be cleared, got %q", g.dangerousPending)
	}
}

func TestScopeGuard_ConfirmDangerous_Empty(t *testing.T) {
	g := NewScopeGuard(false)
	g.ConfirmDangerous("")
	// should not panic, should not add empty key
}

func TestScopeGuard_DangerousPending(t *testing.T) {
	g := NewScopeGuard(false)
	if got := g.DangerousPending(); got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}

func TestScopeGuard_CheckTool_AutoConfirm(t *testing.T) {
	g := NewScopeGuard(true) // auto-confirm
	call := makeToolCall("edit", `{"path":"foo.go","old_string":"a","new_string":"b"}`)
	action := g.CheckTool(call, &TaskState{ConfirmedScope: false})
	if action.Type != GuardAllow {
		t.Errorf("auto-confirm should allow edit without scope check, got %s", action.Type)
	}
}

func TestScopeGuard_CheckTool_AutoConfirmWithBash(t *testing.T) {
	g := NewScopeGuard(true) // auto-confirm
	// Even with autoConfirm, bash dangerous patterns are still checked (Layer 1)
	call := makeToolCall("bash", `{"command":"rm -rf /tmp"}`)
	action := g.CheckTool(call, &TaskState{})
	if action.Type != GuardAskUser {
		t.Errorf("bash dangerous patterns checked before autoConfirm, got %s", action.Type)
	}
}

func TestScopeGuard_CheckTool_NilState(t *testing.T) {
	g := NewScopeGuard(false)
	call := makeToolCall("edit", `{"path":"foo.go"}`)
	action := g.CheckTool(call, nil)
	if action.Type != GuardAllow {
		t.Errorf("nil state should allow, got %s", action.Type)
	}
}

func TestScopeGuard_CheckTool_ConfirmedScope(t *testing.T) {
	g := NewScopeGuard(false)
	call := makeToolCall("edit", `{"path":"foo.go"}`)
	action := g.CheckTool(call, &TaskState{ConfirmedScope: true})
	if action.Type != GuardAllow {
		t.Errorf("confirmed scope should allow, got %s", action.Type)
	}
}

func TestScopeGuard_CheckTool_UnconfirmedScope(t *testing.T) {
	g := NewScopeGuard(false)
	call := makeToolCall("edit", `{"path":"foo.go"}`)
	action := g.CheckTool(call, &TaskState{ConfirmedScope: false})
	if action.Type != GuardAskUser {
		t.Errorf("unconfirmed scope destruct should ask user, got %s", action.Type)
	}
}

func TestScopeGuard_CheckTool_NonDestructiveUnconfirmed(t *testing.T) {
	g := NewScopeGuard(false)
	call := makeToolCall("grep", `{"pattern":"foo"}`)
	action := g.CheckTool(call, &TaskState{ConfirmedScope: false})
	if action.Type != GuardAllow {
		t.Errorf("non-destructive should allow even without scope, got %s", action.Type)
	}
}

func TestIsDestructiveTool(t *testing.T) {
	tests := []struct {
		name string
		call string
		want bool
	}{
		{"edit", "edit", true},
		{"write", "write", true},
		{"bash", "bash", true},
		{"grep", "grep", false},
		{"read", "read", false},
		{"empty", "", false},
	}
	for _, tt := range tests {
		if got := isDestructiveTool(tt.call); got != tt.want {
			t.Errorf("isDestructiveTool(%q) = %v, want %v", tt.call, got, tt.want)
		}
	}
}

func TestCheckDangerousBash_SystemLevel(t *testing.T) {
	g := NewScopeGuard(false)
	systemCmds := []string{
		"rm -rf / --no-preserve-root",
		"dd if=/dev/sda of=/dev/null",
		"mkfs.ext4 /dev/sda1",
	}
	for _, cmd := range systemCmds {
		call := makeToolCall("bash", `{"command":"`+cmd+`"}`)
		action := g.CheckTool(call, nil)
		if action.Type != GuardBlock {
			t.Errorf("system-level cmd %q should be hard-blocked, got %s", cmd, action.Type)
		}
	}
}

func TestCheckDangerousBash_ProjectLevel(t *testing.T) {
	g := NewScopeGuard(false)
	call := makeToolCall("bash", `{"command":"rm -rf /tmp/folder"}`)
	action := g.CheckTool(call, nil)
	if action.Type != GuardAskUser {
		t.Errorf("project-level cmd should ask user, got %s", action.Type)
	}
}

func TestCheckDangerousBash_AlreadyConfirmed(t *testing.T) {
	g := NewScopeGuard(false)
	// Simulate user confirming "rm -rf /tmp/folder"
	g.ConfirmDangerous("rm -rf /tmp/folder")

	call := makeToolCall("bash", `{"command":"rm -rf /tmp/folder"}`)
	action := g.CheckTool(call, nil)
	if action.Type != GuardAllow {
		t.Errorf("confirmed command should be allowed, got %s", action.Type)
	}
}

func TestCheckDangerousBash_SafeCmd(t *testing.T) {
	g := NewScopeGuard(false)
	call := makeToolCall("bash", `{"command":"ls -la"}`)
	action := g.CheckTool(call, nil)
	if action.Type != GuardAllow {
		t.Errorf("safe cmd should be allowed, got %s", action.Type)
	}
}

func TestCheckDangerousBash_InvalidJSON(t *testing.T) {
	g := NewScopeGuard(false)
	call := makeToolCall("bash", `not json`)
	action := g.CheckTool(call, nil)
	if action.Type != GuardAllow {
		t.Errorf("invalid json should be allowed, got %s", action.Type)
	}
}

func TestCheckDangerousBash_EmptyCommand(t *testing.T) {
	g := NewScopeGuard(false)
	call := makeToolCall("bash", `{"command":""}`)
	action := g.CheckTool(call, nil)
	if action.Type != GuardAllow {
		t.Errorf("empty command should be allowed, got %s", action.Type)
	}
}

func TestCheckDangerousBash_NormalizedWhitespace(t *testing.T) {
	g := NewScopeGuard(false)
	// Extra spaces should be normalized
	call := makeToolCall("bash", `{"command":"rm  -rf   /tmp/folder"}`)
	action := g.CheckTool(call, nil)
	if action.Type != GuardAskUser {
		t.Errorf("whitespace-normalized dangerous cmd should be caught, got %s", action.Type)
	}
}

// --- GuardAction ---

func TestGuardActionConstants(t *testing.T) {
	if GuardAllow != "allow" {
		t.Errorf("GuardAllow = %q, want 'allow'", GuardAllow)
	}
	if GuardBlock != "block" {
		t.Errorf("GuardBlock = %q, want 'block'", GuardBlock)
	}
	if GuardAskUser != "ask_user" {
		t.Errorf("GuardAskUser = %q, want 'ask_user'", GuardAskUser)
	}
}

// helpers

func makeToolCall(name string, inputJSON string) ToolCallRequest {
	return ToolCallRequest{
		ID:    "call-1",
		Name:  name,
		Input: json.RawMessage(inputJSON),
	}
}
