package engine

import (
	"strings"
	"testing"
)

// configuredGuard builds a ScopeGuard with an approval policy, mirroring how
// NewEngine wires EngineConfig into the guard.
func configuredGuard(settings ApprovalSettings) *ScopeGuard {
	g := NewScopeGuard()
	g.Configure(settings)
	return g
}

// TestApprovalPrecedence locks the judgement chain. The order is the safety
// contract: irreversible system-level danger is never overridable, user rules
// sit below it and above the default project-level prompt, and the approval
// mode only decides whether a would-be prompt actually interrupts.
func TestApprovalPrecedence(t *testing.T) {
	tests := []struct {
		name     string
		settings ApprovalSettings
		cmd      string
		want     string
	}{
		{
			name: "no policy: benign command allowed",
			cmd:  "ls -la",
			want: GuardAllow,
		},
		{
			name: "no policy: project-level danger asks",
			cmd:  "rm -rf /tmp/folder",
			want: GuardAskUser,
		},
		{
			name: "no policy: system-level danger blocked",
			cmd:  "rm -rf /",
			want: GuardBlock,
		},
		{
			name:     "allow rule cannot override system-level danger",
			settings: ApprovalSettings{Rules: []ApprovalRule{{Program: "rm", Decision: ApprovalAllow}}},
			cmd:      "rm -rf /",
			want:     GuardBlock,
		},
		{
			name:     "forbidden rule blocks a command the judge does not know",
			settings: ApprovalSettings{Rules: []ApprovalRule{{Program: "terraform", Args: []string{"destroy"}, Decision: ApprovalForbidden, Reason: "生产环境禁地"}}},
			cmd:      "terraform destroy -auto-approve",
			want:     GuardBlock,
		},
		{
			name:     "forbidden rule is not bypassed by a wrapper",
			settings: ApprovalSettings{Rules: []ApprovalRule{{Program: "terraform", Args: []string{"destroy"}, Decision: ApprovalForbidden}}},
			cmd:      "sudo terraform destroy",
			want:     GuardBlock,
		},
		{
			name:     "allow rule silences the project-level prompt",
			settings: ApprovalSettings{Rules: []ApprovalRule{{Program: "rm", Args: []string{"-rf", "/tmp/folder"}, Decision: ApprovalAllow}}},
			cmd:      "rm -rf /tmp/folder",
			want:     GuardAllow,
		},
		{
			name:     "allow rule for one argument list does not cover another",
			settings: ApprovalSettings{Rules: []ApprovalRule{{Program: "rm", Args: []string{"-rf", "/tmp/folder"}, Decision: ApprovalAllow}}},
			cmd:      "rm -rf /tmp/other",
			want:     GuardAskUser,
		},
		{
			name:     "prompt rule asks for a command the judge does not know",
			settings: ApprovalSettings{Rules: []ApprovalRule{{Program: "kubectl", Args: []string{"delete"}, Decision: ApprovalPrompt, Reason: "集群操作"}}},
			cmd:      "kubectl delete pod api-1",
			want:     GuardAskUser,
		},
		{
			name:     "learned approval allows the confirmed command",
			settings: ApprovalSettings{Learned: []string{"rm -rf /tmp/folder"}},
			cmd:      "rm -rf /tmp/folder",
			want:     GuardAllow,
		},
		{
			name:     "learned approval matches the same command with different spacing",
			settings: ApprovalSettings{Learned: []string{"rm -rf /tmp/folder"}},
			cmd:      "rm    -rf   /tmp/folder",
			want:     GuardAllow,
		},
		{
			name:     "learned approval does not broaden to other arguments",
			settings: ApprovalSettings{Learned: []string{"rm -rf /tmp/folder"}},
			cmd:      "rm -rf /tmp/other",
			want:     GuardAskUser,
		},
		{
			name:     "learned approval cannot cover system-level danger",
			settings: ApprovalSettings{Learned: []string{"rm -rf /"}},
			cmd:      "rm -rf /",
			want:     GuardBlock,
		},
		{
			name:     "mode never allows project-level danger without asking",
			settings: ApprovalSettings{Mode: ApprovalModeNever},
			cmd:      "rm -rf /tmp/folder",
			want:     GuardAllow,
		},
		{
			name:     "mode never still blocks system-level danger",
			settings: ApprovalSettings{Mode: ApprovalModeNever},
			cmd:      "rm -rf /",
			want:     GuardBlock,
		},
		{
			name:     "mode never still honours forbidden rules",
			settings: ApprovalSettings{Mode: ApprovalModeNever, Rules: []ApprovalRule{{Program: "terraform", Args: []string{"destroy"}, Decision: ApprovalForbidden}}},
			cmd:      "terraform destroy",
			want:     GuardBlock,
		},
		{
			name:     "mode never turns a prompt rule into a silent allow",
			settings: ApprovalSettings{Mode: ApprovalModeNever, Rules: []ApprovalRule{{Program: "kubectl", Args: []string{"delete"}, Decision: ApprovalPrompt}}},
			cmd:      "kubectl delete pod api-1",
			want:     GuardAllow,
		},
		{
			name:     "unknown mode falls back to asking, never to silent approval",
			settings: ApprovalSettings{Mode: "yolo"},
			cmd:      "rm -rf /tmp/folder",
			want:     GuardAskUser,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := configuredGuard(tt.settings)
			if action := g.CheckTool(bashCall(tt.cmd)); action.Type != tt.want {
				t.Errorf("CheckTool(%q) = %s, want %s (message: %s)", tt.cmd, action.Type, tt.want, action.Message)
			}
		})
	}
}

// TestApprovalRuleMatching pins the matcher semantics: literal token-prefix
// matching against each command the shell would actually run.
func TestApprovalRuleMatching(t *testing.T) {
	tests := []struct {
		name     string
		rule     ApprovalRule
		cmd      string
		wantType string
	}{
		{
			name:     "program alone matches any arguments",
			rule:     ApprovalRule{Program: "terraform", Decision: ApprovalForbidden},
			cmd:      "terraform plan",
			wantType: GuardBlock,
		},
		{
			name:     "wildcard matches one token",
			rule:     ApprovalRule{Program: "kubectl", Args: []string{"delete", "*"}, Decision: ApprovalPrompt},
			cmd:      "kubectl delete pod",
			wantType: GuardAskUser,
		},
		{
			name:     "wildcard requires a token to match",
			rule:     ApprovalRule{Program: "kubectl", Args: []string{"delete", "*"}, Decision: ApprovalPrompt},
			cmd:      "kubectl delete",
			wantType: GuardAllow,
		},
		{
			name:     "quoted argument is compared unquoted",
			rule:     ApprovalRule{Program: "rm", Args: []string{"-rf", "/tmp/folder"}, Decision: ApprovalAllow},
			cmd:      `rm "-rf" "/tmp/folder"`,
			wantType: GuardAllow,
		},
		{
			name:     "rule applies inside a command list",
			rule:     ApprovalRule{Program: "terraform", Args: []string{"destroy"}, Decision: ApprovalForbidden},
			cmd:      "echo start && terraform destroy",
			wantType: GuardBlock,
		},
		{
			name:     "rule applies inside a shell payload",
			rule:     ApprovalRule{Program: "terraform", Args: []string{"destroy"}, Decision: ApprovalForbidden},
			cmd:      `bash -c "terraform destroy"`,
			wantType: GuardBlock,
		},
		{
			name:     "non-matching program leaves judgement to the judge",
			rule:     ApprovalRule{Program: "terraform", Decision: ApprovalForbidden},
			cmd:      "ls -la",
			wantType: GuardAllow,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := configuredGuard(ApprovalSettings{Rules: []ApprovalRule{tt.rule}})
			if action := g.CheckTool(bashCall(tt.cmd)); action.Type != tt.wantType {
				t.Errorf("CheckTool(%q) = %s, want %s (message: %s)", tt.cmd, action.Type, tt.wantType, action.Message)
			}
		})
	}
}

// TestConfirmDangerousLearnsCommand verifies the (i)+(iii) decision: what the
// user confirms is remembered verbatim (never broadened), and the host is told
// so it can persist it for future sessions.
func TestConfirmDangerousLearnsCommand(t *testing.T) {
	var learned []string
	g := configuredGuard(ApprovalSettings{OnLearned: func(cmd string) { learned = append(learned, cmd) }})

	// Before confirming, the command asks.
	if action := g.CheckTool(bashCall("rm -rf /tmp/folder")); action.Type != GuardAskUser {
		t.Fatalf("before confirm: got %s, want %s", action.Type, GuardAskUser)
	}
	confirmed := g.DangerousPending()
	if confirmed == "" {
		t.Fatal("DangerousPending() is empty after an ask")
	}

	g.ConfirmDangerous(confirmed)

	if len(learned) != 1 || learned[0] != confirmed {
		t.Fatalf("OnLearned got %v, want [%q]", learned, confirmed)
	}
	if action := g.CheckTool(bashCall("rm -rf /tmp/folder")); action.Type != GuardAllow {
		t.Errorf("after confirm: got %s, want %s", action.Type, GuardAllow)
	}
	// The learned approval is exact: a variant still asks.
	if action := g.CheckTool(bashCall("rm -rf /tmp/other")); action.Type != GuardAskUser {
		t.Errorf("variant after confirm: got %s, want %s", action.Type, GuardAskUser)
	}
}

// TestConfirmDangerous_NoCallbackStillRemembers: a nil callback means
// session-only memory, not a crash and not a silent loss of the confirmation.
func TestConfirmDangerous_NoCallbackStillRemembers(t *testing.T) {
	g := configuredGuard(ApprovalSettings{})
	g.ConfirmDangerous("rm -rf /tmp/folder")
	if action := g.CheckTool(bashCall("rm -rf /tmp/folder")); action.Type != GuardAllow {
		t.Errorf("got %s, want %s", action.Type, GuardAllow)
	}
}

// TestAskMessageExplainsHowToDeclareRules covers (iii): the confirmation prompt
// must teach the user how to stop being asked, otherwise "learned approvals are
// exact" reads as a dead end.
func TestAskMessageExplainsHowToDeclareRules(t *testing.T) {
	g := configuredGuard(ApprovalSettings{})
	action := g.CheckTool(bashCall("rm -rf /tmp/folder"))
	if action.Type != GuardAskUser {
		t.Fatalf("got %s, want %s", action.Type, GuardAskUser)
	}
	for _, want := range []string{"approval.rules", "config.toml"} {
		if !strings.Contains(action.Message, want) {
			t.Errorf("ask message should mention %q, got:\n%s", want, action.Message)
		}
	}
}

// TestRuleReasonSurfacesInAsk keeps rules explainable: the user must see why
// they were interrupted, not just that they were.
func TestRuleReasonSurfacesInAsk(t *testing.T) {
	g := configuredGuard(ApprovalSettings{Rules: []ApprovalRule{
		{Program: "kubectl", Args: []string{"delete"}, Decision: ApprovalPrompt, Reason: "集群操作需确认"},
	}})
	action := g.CheckTool(bashCall("kubectl delete pod api-1"))
	if action.Type != GuardAskUser {
		t.Fatalf("got %s, want %s", action.Type, GuardAskUser)
	}
	if !strings.Contains(action.Message, "集群操作需确认") {
		t.Errorf("ask message should carry the rule reason, got:\n%s", action.Message)
	}
}

// TestRuleReasonSurfacesInBlock is the same for hard blocks.
func TestRuleReasonSurfacesInBlock(t *testing.T) {
	g := configuredGuard(ApprovalSettings{Rules: []ApprovalRule{
		{Program: "terraform", Args: []string{"destroy"}, Decision: ApprovalForbidden, Reason: "生产环境禁地"},
	}})
	action := g.CheckTool(bashCall("terraform destroy"))
	if action.Type != GuardBlock {
		t.Fatalf("got %s, want %s", action.Type, GuardBlock)
	}
	if !strings.Contains(action.Message, "生产环境禁地") {
		t.Errorf("block message should carry the rule reason, got:\n%s", action.Message)
	}
}
