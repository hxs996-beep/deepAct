package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/deepact/deepact/engine"
)

func TestLoad_ApprovalSection(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	content := []byte(`
[approval]
mode = "never"

[[approval.rules]]
program = "terraform"
args = ["destroy"]
decision = "forbidden"
reason = "生产环境禁地"

[[approval.rules]]
program = "kubectl"
decision = "prompt"
`)
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}

	f, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if f == nil {
		t.Fatal("Load returned nil for an existing file")
	}
	if f.Approval.Mode != "never" {
		t.Errorf("approval.mode = %q, want never", f.Approval.Mode)
	}
	if len(f.Approval.Rules) != 2 {
		t.Fatalf("approval.rules = %d entries, want 2", len(f.Approval.Rules))
	}
	first := f.Approval.Rules[0]
	if first.Program != "terraform" || first.Decision != "forbidden" || first.Reason != "生产环境禁地" {
		t.Errorf("rules[0] = %+v", first)
	}
	if len(first.Args) != 1 || first.Args[0] != "destroy" {
		t.Errorf("rules[0].Args = %v, want [destroy]", first.Args)
	}
	if f.Approval.Rules[1].Program != "kubectl" || len(f.Approval.Rules[1].Args) != 0 {
		t.Errorf("rules[1] = %+v", f.Approval.Rules[1])
	}
}

func TestApply_ApprovalPolicy(t *testing.T) {
	cfg := &engine.EngineConfig{}
	f := &File{Approval: approvalConfig{
		Mode: "never",
		Rules: []approvalRuleConfig{
			{Program: "terraform", Args: []string{"destroy"}, Decision: "forbidden", Reason: "禁地"},
		},
	}}

	if err := Apply(cfg, f); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if cfg.ApprovalMode != engine.ApprovalModeNever {
		t.Errorf("ApprovalMode = %q, want %q", cfg.ApprovalMode, engine.ApprovalModeNever)
	}
	if len(cfg.ApprovalRules) != 1 {
		t.Fatalf("ApprovalRules = %d, want 1", len(cfg.ApprovalRules))
	}
	rule := cfg.ApprovalRules[0]
	if rule.Program != "terraform" || rule.Decision != engine.ApprovalForbidden || rule.Reason != "禁地" {
		t.Errorf("rule = %+v", rule)
	}
	if len(rule.Args) != 1 || rule.Args[0] != "destroy" {
		t.Errorf("rule.Args = %v", rule.Args)
	}
}

// TestApply_ApprovalDefaultsDoNotClobber keeps an empty section harmless: an
// absent [approval] must leave the engine defaults (ask on project-level
// danger) untouched rather than writing a zero value over them.
func TestApply_ApprovalDefaultsDoNotClobber(t *testing.T) {
	cfg := &engine.EngineConfig{ApprovalMode: engine.ApprovalModeOnRequest}
	if err := Apply(cfg, &File{}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if cfg.ApprovalMode != engine.ApprovalModeOnRequest {
		t.Errorf("ApprovalMode = %q, want it unchanged", cfg.ApprovalMode)
	}
	if len(cfg.ApprovalRules) != 0 {
		t.Errorf("ApprovalRules = %v, want empty", cfg.ApprovalRules)
	}
}

// TestApply_ApprovalInvalidConfigFailsLoud: a typo in a security setting must
// stop startup, not be silently ignored.
func TestApply_ApprovalInvalidConfigFailsLoud(t *testing.T) {
	cases := []struct {
		name string
		file *File
		want string
	}{
		{"unknown mode", &File{Approval: approvalConfig{Mode: "yolo"}}, "mode"},
		{"unknown decision", &File{Approval: approvalConfig{Rules: []approvalRuleConfig{{Program: "x", Decision: "maybe"}}}}, "decision"},
		{"missing program", &File{Approval: approvalConfig{Rules: []approvalRuleConfig{{Decision: "allow"}}}}, "program"},
		{"missing decision", &File{Approval: approvalConfig{Rules: []approvalRuleConfig{{Program: "x"}}}}, "decision"},
		{"empty arg token", &File{Approval: approvalConfig{Rules: []approvalRuleConfig{{Program: "x", Args: []string{""}, Decision: "allow"}}}}, "args"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := Apply(&engine.EngineConfig{}, tc.file)
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want it to mention %q", err.Error(), tc.want)
			}
		})
	}
}

func TestLearnedApprovals_RoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "approvals.toml")

	if got := loadLearnedApprovalsAt(path); len(got) != 0 {
		t.Fatalf("missing file should yield no approvals, got %v", got)
	}

	for _, cmd := range []string{"rm -rf /tmp/folder", "git push --force"} {
		if err := appendLearnedApprovalAt(path, cmd); err != nil {
			t.Fatalf("append %q: %v", cmd, err)
		}
	}
	got := loadLearnedApprovalsAt(path)
	want := []string{"rm -rf /tmp/folder", "git push --force"}
	if len(got) != len(want) {
		t.Fatalf("loaded %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("approvals[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestAppendLearnedApproval_Deduplicates keeps the file from growing without
// bound when the user re-confirms a command they already approved.
func TestAppendLearnedApproval_Deduplicates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "approvals.toml")
	for i := 0; i < 3; i++ {
		if err := appendLearnedApprovalAt(path, "rm -rf /tmp/folder"); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	if got := loadLearnedApprovalsAt(path); len(got) != 1 {
		t.Fatalf("approvals = %v, want a single entry", got)
	}
}

func TestAppendLearnedApproval_IgnoresEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "approvals.toml")
	if err := appendLearnedApprovalAt(path, "   "); err != nil {
		t.Fatalf("append: %v", err)
	}
	if got := loadLearnedApprovalsAt(path); len(got) != 0 {
		t.Fatalf("approvals = %v, want none", got)
	}
}

// TestAppendLearnedApproval_QuotesSurviveRoundTrip guards the TOML encoding:
// a command containing quotes or backslashes must reload unchanged, otherwise a
// confirmation would silently stop matching.
func TestAppendLearnedApproval_QuotesSurviveRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "approvals.toml")
	cmd := `rm -rf "my dir"/x`
	if err := appendLearnedApprovalAt(path, cmd); err != nil {
		t.Fatalf("append: %v", err)
	}
	got := loadLearnedApprovalsAt(path)
	if len(got) != 1 || got[0] != cmd {
		t.Fatalf("approvals = %v, want [%q]", got, cmd)
	}
}

func TestAppendLearnedApproval_RestrictivePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "approvals.toml")
	if err := appendLearnedApprovalAt(path, "rm -rf /tmp/x"); err != nil {
		t.Fatalf("append: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("permissions = %o, want 600", perm)
	}
}
