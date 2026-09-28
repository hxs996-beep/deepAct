package engine

import "testing"

// Fixtures mirror the real producers: Block S (context/prompt.go), AGENTS.md
// (context/agents.go), the skills list (engine/loop.go) and pinned messages
// ([SKILL — x], [Background jobs]) from engine/loop.go / engine/turn.go.
const (
	blockSZH = "# Block S：会话上下文（固定）\n\n## 环境\n- 操作系统: darwin\n- 架构: arm64\n\n## 代码库结构\n以下是项目目录树快照。\n"
	blockSEN = "# Block S: Session Context (Stable)\n\n## Environment\n- OS: darwin\n- Arch: arm64\n"
	agentsZH = "## Project Conventions (AGENTS.md)\n\n### AGENTS.md\n\n项目规则：用标准库\n"
	skillsZH = "## 可用的 Skills\n\n- collab: 并行研究\n- ratd: 红队测试\n"
	skillPin = "[SKILL — collab]\n\n## 编排（模型自主）\n\n0. 规划阶段\n"
	bgPin    = "[Background jobs] bg-1 (sub): 调研 — running. Continue your work; use agent_poll(job_id) to fetch results."
)

func TestStripInternalPromptEcho(t *testing.T) {
	tests := []struct {
		name     string
		in       string
		injected []string
		want     string
	}{
		{
			name:     "no injected blocks -> unchanged",
			in:       "我已经分析了代码，发现 bug 在第 42 行。",
			injected: nil,
			want:     "我已经分析了代码，发现 bug 在第 42 行。",
		},
		{
			// Regression for the coverage gap: the hand-written list covered
			// "## 环境" but not "## 代码库结构" / "## Project Conventions".
			name:     "strip block S echo incl. codebase tree and agents headers",
			in:       "# Block S：会话上下文（固定）\n\n## 代码库结构\n以下是项目目录树快照。\n\n### AGENTS.md\n项目规则：用标准库\n\n真实结论。",
			injected: []string{blockSZH, agentsZH},
			want:     "真实结论。",
		},
		{
			name:     "strip english block S echo",
			in:       "# Block S: Session Context (Stable)\n\n## Environment\n- OS: darwin\n- Arch: arm64\n\nthe real conclusion.",
			injected: []string{blockSEN},
			want:     "the real conclusion.",
		},
		{
			name:     "strip pinned skill marker echo",
			in:       "[SKILL — collab]\n\n真实结论。",
			injected: []string{skillPin},
			want:     "真实结论。",
		},
		{
			// Regression for the false-deletion bug: these headers used to sit
			// in the fixed list (no producer since the block was removed), so a
			// legitimate answer section was silently deleted.
			name:     "preserve answer heading that is no longer injected",
			in:       "## Task State\n- 已完成 A\n\n真实结论。",
			injected: []string{blockSZH},
			want:     "## Task State\n- 已完成 A\n\n真实结论。",
		},
		{
			name:     "preserve answer heading resembling an injected one",
			in:       "## 环境与依赖\n- 无\n\n真实结论。",
			injected: []string{blockSZH},
			want:     "## 环境与依赖\n- 无\n\n真实结论。",
		},
		{
			name:     "preserve heading outside the injected set",
			in:       "# 身份\n\n真实结论。",
			injected: nil,
			want:     "# 身份\n\n真实结论。",
		},
		{
			// Known gap (宁可漏删): a single-line pinned message is not a
			// standalone bracketed marker, so it is never treated as a header —
			// stripping it would risk deleting the sentence that follows it.
			name:     "single-line pinned message preserved",
			in:       bgPin + "\n\n真实结论。",
			injected: []string{bgPin},
			want:     bgPin + "\n\n真实结论。",
		},
		{
			name:     "pure internal echo -> empty",
			in:       "# Block S：会话上下文（固定）\n\n",
			injected: []string{blockSZH},
			want:     "",
		},
		{
			name:     "multiple internal blocks with real text between",
			in:       "## 环境\n- 操作系统: darwin\n\n中间结论。\n[SKILL — collab]\n\n结尾。",
			injected: []string{blockSZH, skillPin},
			want:     "中间结论。\n结尾。",
		},
		{
			// Known gap, kept as approved (宁可漏删): this block's heading is
			// followed by a blank line, and the strip runs "until the next blank
			// line" — so the heading goes and the block body survives. Reworking
			// that semantics was judged out of scope for this fix.
			name:     "skills block heading stripped, body after blank line kept",
			in:       "## 可用的 Skills\n\n- collab: 并行研究\n\n真实结论。",
			injected: []string{skillsZH},
			want:     "- collab: 并行研究\n\n真实结论。",
		},
		{
			name:     "empty content -> empty",
			in:       "",
			injected: []string{blockSZH},
			want:     "",
		},
	}
	for _, tt := range tests {
		got := stripInternalPromptEcho(tt.in, tt.injected)
		if got != tt.want {
			t.Errorf("%s:\n got = %q\nwant = %q", tt.name, got, tt.want)
		}
	}
}

// TestInternalEchoHeaders_LineQualification pins the derivation rule: only
// structural lines of the injected blocks become headers, and matching is by
// whole line.
func TestInternalEchoHeaders_LineQualification(t *testing.T) {
	block := "# Block S：会话上下文（固定）\n### AGENTS.md\n####### too deep\n#hashtag\n[SESSION ARCHIVE]\n[Background jobs] bg-1: running.\n- 普通列表项\n"
	headers := internalEchoHeaders([]string{block})

	qualify := []string{"# Block S：会话上下文（固定）", "### AGENTS.md", "[SESSION ARCHIVE]"}
	for _, h := range qualify {
		if !headers[h] {
			t.Errorf("%q should be a header", h)
		}
	}
	reject := []string{"####### too deep", "#hashtag", "[Background jobs] bg-1: running.", "- 普通列表项"}
	for _, r := range reject {
		if headers[r] {
			t.Errorf("%q should NOT be a header", r)
		}
	}
}
