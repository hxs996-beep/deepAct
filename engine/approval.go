package engine

import (
	"fmt"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// Approval decisions a rule can carry.
const (
	ApprovalAllow     = "allow"
	ApprovalPrompt    = "prompt"
	ApprovalForbidden = "forbidden"
)

// ApprovalMode controls whether a would-be prompt actually interrupts the user.
// It never relaxes the two hard-block layers (the bash blocklist and
// judgeDanger's dangerSystem): those have no confirmation path at all.
const (
	// ApprovalModeOnRequest asks before running a project-level dangerous
	// command. This is the historical behaviour and the default.
	ApprovalModeOnRequest = "on-request"
	// ApprovalModeNever runs project-level dangerous commands without asking.
	ApprovalModeNever = "never"
)

// ApprovalRule is a user-declared rule for a command family. Matching is a
// literal token-prefix match against each command the shell would actually run
// (wrappers like sudo/command/env are unwrapped first, so a rule cannot be
// dodged by prefixing one):
//
//	[[approval.rules]]
//	program = "terraform"
//	args = ["destroy"]      # token prefix; "*" matches exactly one token
//	decision = "forbidden"  # allow | prompt | forbidden
//	reason = "生产环境禁地"
//
// Flag order and spelling are NOT normalised (args are compared token by
// token). A forbid rule meant to cover every invocation of a program should
// leave Args empty rather than enumerate forms.
type ApprovalRule struct {
	Program  string
	Args     []string
	Decision string
	Reason   string
}

// ApprovalSettings is the approval policy handed to the guard at startup.
type ApprovalSettings struct {
	// Mode is ApprovalModeOnRequest (default) or ApprovalModeNever.
	Mode string
	// Rules are user-declared rules, evaluated before the built-in judge.
	Rules []ApprovalRule
	// Learned are commands the user confirmed in earlier sessions.
	Learned []string
	// OnLearned is called when the user confirms a new command, so the host
	// can persist it. nil keeps the confirmation session-only.
	OnLearned func(command string)
}

// Configure installs the approval policy on the guard. A guard that was never
// configured behaves exactly as before: judgeDanger decides and every
// project-level threat asks.
func (g *ScopeGuard) Configure(settings ApprovalSettings) {
	if g == nil {
		return
	}
	g.approvalMode = normalizeApprovalMode(settings.Mode)
	g.approvalRules = settings.Rules
	g.onLearned = settings.OnLearned
	if g.dangerousConfirmed == nil {
		g.dangerousConfirmed = make(map[string]bool)
	}
	// Confirmed commands are remembered in the same set the live confirmation
	// writes to: one memory, seeded from disk at startup.
	for _, cmd := range settings.Learned {
		if key := normalizeApprovalCommand(cmd); key != "" {
			g.dangerousConfirmed[key] = true
		}
	}
}

// normalizeApprovalMode maps an unknown or empty mode to on-request. Failing
// towards "ask" keeps a typo in config.toml from silently turning every prompt
// into an approval.
func normalizeApprovalMode(mode string) string {
	if strings.TrimSpace(mode) == ApprovalModeNever {
		return ApprovalModeNever
	}
	return ApprovalModeOnRequest
}

// normalizeApprovalCommand is the identity of a confirmed command: whitespace
// collapsed, so re-issuing it with different spacing stays confirmed.
func normalizeApprovalCommand(cmd string) string {
	return strings.Join(strings.Fields(cmd), " ")
}

// decideApproval resolves the user policy for a command. decision is "" when no
// rule or remembered approval applies, leaving judgement to judgeDanger.
func (g *ScopeGuard) decideApproval(cmd string) (decision, reason string) {
	if g == nil {
		return "", ""
	}
	// Remembered approvals are exact: the user confirmed one command, not a
	// family of them. Widening this is a deliberate act, expressed as a rule.
	if g.dangerousConfirmed[normalizeApprovalCommand(cmd)] {
		return ApprovalAllow, ""
	}
	if len(g.approvalRules) == 0 {
		return "", ""
	}

	best := ""
	bestReason := ""
	for _, call := range approvalCommands(cmd) {
		for _, rule := range g.approvalRules {
			if !ruleMatches(rule, call) {
				continue
			}
			if approvalSeverity(rule.Decision) > approvalSeverity(best) {
				best = rule.Decision
				bestReason = rule.Reason
			}
		}
	}
	if best == "" {
		return "", ""
	}
	return best, bestReason
}

// approvalSeverity orders decisions so that a line matching several rules
// resolves to the most restrictive one.
func approvalSeverity(decision string) int {
	switch decision {
	case ApprovalForbidden:
		return 3
	case ApprovalPrompt:
		return 2
	case ApprovalAllow:
		return 1
	default:
		return 0
	}
}

// approvalCommand is one command the shell would run, after wrapper removal.
type approvalCommand struct {
	name string
	args []string
}

// approvalCommands extracts every command a shell line would execute, including
// commands inside lists, pipelines, subshells and command substitutions, plus
// one level of shell payload (`bash -c '<cmd>'`) — the same reach as
// judgeDanger, so a rule cannot be dodged by composition.
func approvalCommands(cmd string) []approvalCommand {
	file, err := syntax.NewParser().Parse(strings.NewReader(cmd), "")
	if err != nil {
		return nil
	}
	var out []approvalCommand
	collectApprovalCommands(file, &out, 0)
	return out
}

func collectApprovalCommands(node syntax.Node, out *[]approvalCommand, depth int) {
	syntax.Walk(node, func(n syntax.Node) bool {
		call, ok := n.(*syntax.CallExpr)
		if !ok {
			return true
		}
		name, args := unwrap(literalArgs(call))
		if name == "" {
			return true
		}
		*out = append(*out, approvalCommand{name: name, args: args})
		if depth < maxNestedShellDepth && isShellInterpreter(name) {
			if payload, ok := shellCommandString(args); ok {
				if nested, err := syntax.NewParser().Parse(strings.NewReader(payload), ""); err == nil {
					collectApprovalCommands(nested, out, depth+1)
				}
			}
		}
		return true
	})
}

// ruleMatches reports whether a rule covers one extracted command: program
// equality plus a token-prefix match on the arguments ("*" = any one token).
func ruleMatches(rule ApprovalRule, call approvalCommand) bool {
	if rule.Program == "" || rule.Program != call.name {
		return false
	}
	if len(rule.Args) > len(call.args) {
		return false
	}
	for i, pattern := range rule.Args {
		if pattern == "*" {
			continue
		}
		if pattern != call.args[i] {
			return false
		}
	}
	return true
}

// approvalAskMessage builds the confirmation prompt: what was judged dangerous,
// why, and how to stop being asked. The last part matters because remembered
// approvals are deliberately exact — without it, "you will be asked again for a
// slightly different command" reads as a dead end.
func approvalAskMessage(zh bool, reason, cmd string) string {
	example := "\n\nTo stop being asked for a whole command family, declare a rule in config.toml:\n" +
		"[[approval.rules]]\nprogram = \"<command>\"\nargs = [\"<token prefix>\"]\ndecision = \"allow\"  # or prompt / forbidden"
	if zh {
		example = "\n\n要长期放行同类命令，在 config.toml 声明规则：\n" +
			"[[approval.rules]]\nprogram = \"<命令>\"\nargs = [\"<token 前缀>\"]\ndecision = \"allow\"  # 或 prompt / forbidden"
	}
	return pickPrompt(zh,
		fmt.Sprintf("⚠ Dangerous command: %s\n> `%s`\n\n[Y] confirm  [N] cancel, or type an alternative suggestion for the AI to reconsider%s", reason, cmd, example),
		fmt.Sprintf("⚠ 危险命令: %s\n> `%s`\n\n[Y] 确认执行  [N] 取消，或输入其他建议让 AI 重新处理%s", reason, cmd, example))
}

// approvalBlockMessage reports a rule-driven hard block, naming the rule so the
// refusal is explainable rather than opaque.
func approvalBlockMessage(reason, cmd string) string {
	if reason == "" {
		reason = "no reason given"
	}
	return fmt.Sprintf("✗ Blocked by approval rule: %s\nFull command: %s", reason, cmd)
}
