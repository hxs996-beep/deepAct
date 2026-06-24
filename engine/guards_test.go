package engine

import (
	"encoding/json"
	"strings"
	"testing"
)

// --- LoopGuard ---

func TestNewLoopGuard(t *testing.T) {
	g := NewLoopGuard(0)
	if g == nil {
		t.Fatal("expected non-nil LoopGuard")
	}
	if g.maxRepeats != 4 {
		t.Errorf("default maxRepeats = %d, want 4", g.maxRepeats)
	}
}

func TestNewLoopGuard_CustomMax(t *testing.T) {
	g := NewLoopGuard(3)
	if g.maxRepeats != 3 {
		t.Errorf("maxRepeats = %d, want 3", g.maxRepeats)
	}
}

func TestLoopGuard_Check_AllowsFirstCall(t *testing.T) {
	g := NewLoopGuard(3)
	call := makeToolCall("edit", `{"path":"foo.go","old_string":"a","new_string":"b"}`)
	action := g.Check(call)
	if action.Type != GuardAllow {
		t.Errorf("expected allow, got %s: %s", action.Type, action.Message)
	}
}

func TestLoopGuard_Check_BlocksAfterMaxRepeats(t *testing.T) {
	g := NewLoopGuard(2)
	call := makeToolCall("edit", `{"path":"foo.go","old_string":"a","new_string":"b"}`)

	// First call — allow
	if a := g.Check(call); a.Type != GuardAllow {
		t.Fatal("first call should be allowed")
	}

	// Second call (now at maxRepeats) — block
	action := g.Check(call)
	if action.Type != GuardBlock {
		t.Errorf("expected block after max repeats, got %s", action.Type)
	}
	if action.Message == "" {
		t.Error("block message should not be empty")
	}
}

func TestLoopGuard_Check_DifferentContentHash(t *testing.T) {
	g := NewLoopGuard(2)
	call1 := makeToolCall("edit", `{"path":"foo.go","old_string":"a","new_string":"b"}`)
	call2 := makeToolCall("edit", `{"path":"foo.go","old_string":"c","new_string":"d"}`)

	g.Check(call1)
	action := g.Check(call2)
	if action.Type != GuardAllow {
		t.Errorf("different edit on same file should be allowed, got %s", action.Type)
	}
}

func TestLoopGuard_Check_NonDestructiveTool(t *testing.T) {
	g := NewLoopGuard(2)
	call := makeToolCall("grep", `{"pattern":"foo"}`)
	// Even with many repeats, non-destructive tools aren't tracked
	for i := 0; i < 10; i++ {
		if a := g.Check(call); a.Type != GuardAllow {
			t.Fatalf("iteration %d: grep should not be tracked, got %s", i, a.Type)
		}
	}
}

func TestLoopGuard_Check_ReadWithScope(t *testing.T) {
	g := NewLoopGuard(2)
	call1 := makeToolCall("read", `{"path":"foo.go","symbol":"TestFoo"}`)
	call2 := makeToolCall("read", `{"path":"foo.go","symbol":"TestBar"}`)

	g.Check(call1)
	action := g.Check(call2)
	if action.Type != GuardAllow {
		t.Errorf("different read scopes should be allowed, got %s", action.Type)
	}
}

func TestLoopGuard_Reset(t *testing.T) {
	g := NewLoopGuard(2)
	call := makeToolCall("edit", `{"path":"foo.go","old_string":"a","new_string":"b"}`)

	g.Check(call)
	g.Check(call) // would block
	g.Reset()
	// After reset, should allow again
	if a := g.Check(call); a.Type != GuardAllow {
		t.Errorf("after reset should allow, got %s", a.Type)
	}
}

func TestLoopGuard_Reset_Nil(t *testing.T) {
	var g *LoopGuard
	g.Reset() // should not panic
}

func TestLoopGuard_Check_Nil(t *testing.T) {
	var g *LoopGuard
	call := makeToolCall("edit", `{"path":"foo.go"}`)
	action := g.Check(call)
	if action.Type != GuardAllow {
		t.Errorf("nil guard should allow, got %s", action.Type)
	}
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
		if got := extractPathField(raw); got != tt.want {
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

func TestExtractReadScopeHash(t *testing.T) {
	// No scope params → empty
	h1 := extractReadScopeHash(json.RawMessage(`{"path":"foo.go"}`))
	if h1 != "" {
		t.Errorf("no scope params should return empty, got %s", h1)
	}
	// Symbol produces deterministic hash
	h2 := extractReadScopeHash(json.RawMessage(`{"path":"foo.go","symbol":"TestFoo"}`))
	if h2 == "" {
		t.Error("expected non-empty hash for symbol")
	}
	h3 := extractReadScopeHash(json.RawMessage(`{"path":"foo.go","symbol":"TestFoo"}`))
	if h2 != h3 {
		t.Error("hash should be deterministic")
	}
	// Different symbols produce different hashes
	h4 := extractReadScopeHash(json.RawMessage(`{"path":"foo.go","symbol":"TestBar"}`))
	if h2 == h4 {
		t.Error("different symbols should produce different hashes")
	}
	// Offset produces hash
	h5 := extractReadScopeHash(json.RawMessage(`{"path":"foo.go","offset":10}`))
	if h5 == "" {
		t.Error("expected non-empty hash for offset")
	}
	// Invalid json → empty
	h6 := extractReadScopeHash(json.RawMessage(`bad`))
	if h6 != "" {
		t.Errorf("expected empty for invalid json, got %s", h6)
	}
}

func TestExtractToolKey(t *testing.T) {
	// edit tool produces key with hash
	editCall := makeToolCall("edit", `{"path":"foo.go","old_string":"a","new_string":"b"}`)
	editKey := extractToolKey(editCall)
	if !strings.HasPrefix(editKey, "edit:foo.go:") {
		t.Errorf("edit tool key should start with 'edit:foo.go:', got %q", editKey)
	}
	if len(editKey) <= len("edit:foo.go:") {
		t.Error("edit tool key should include content hash")
	}

	// read no scope → path-only key
	readNoScope := extractToolKey(makeToolCall("read", `{"path":"foo.go"}`))
	if readNoScope != "read:foo.go" {
		t.Errorf("read no scope should be 'read:foo.go', got %q", readNoScope)
	}

	// read with symbol → includes hash
	readSym := extractToolKey(makeToolCall("read", `{"path":"foo.go","symbol":"TestFoo"}`))
	if !strings.HasPrefix(readSym, "read:foo.go:") {
		t.Errorf("read with symbol should include hash, got %q", readSym)
	}

	// grep not tracked
	grepKey := extractToolKey(makeToolCall("grep", `{"pattern":"foo"}`))
	if grepKey != "" {
		t.Errorf("grep should not be tracked, got %q", grepKey)
	}

	// no path → empty
	noPath := extractToolKey(makeToolCall("edit", `{"old_string":"a","new_string":"b"}`))
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
		ID:   "call-1",
		Name: name,
		Input: json.RawMessage(inputJSON),
	}
}
