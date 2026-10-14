# 回复语言跟随用户输入 实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** 修掉英文单语改造后"输出全是英文"的行为回归，让模型产出的全部面向用户文本（叙述、结论、`todo_write` 步骤文案、`ask_user` 问题与选项、子代理报告）与用户输入的语言一致。

**架构：** 只改提示词层，不触碰引擎与 UI。核心是三处修正：① 把语言锚点从"第一条消息"（实际指向注入的英文 Block S）改为"用户自己打字的请求"；② 删掉描述已删机制的 `locked` 不实陈述，改为行为化祈使；③ 把覆盖面从 `Reply` 扩到全部面向用户的产出，并显式解耦"英文提示词/工具输出"与"回复语言"。

**技术栈：** Go（`github.com/deepact/deepact`）；提示词以 `//go:embed` 打包（`context/promptset/promptset.go:15`）；测试用标准库 `testing` + `strings`。

**规格：** `docs/superpowers/specs/2026-10-14-reply-language-follows-user-design.md`

---

## 文件结构

| 文件 | 职责 | 变更 |
|---|---|---|
| `context/promptset/en/system.md` | 主系统提示词（模型可见） | `# Reply format` 一节内，把第 95 行那一条 bullet 换成两条 |
| `context/promptset/en/sub_agent.md` | 子代理提示词后缀（追加在主系统提示词之后） | `## How to work` 末尾新增一条 bullet |
| `context/promptset/promptset_test.go` | 提示词内容的守卫测试（`Get()` 的断言集中在 `context/promptset/promptset_test.go`；全仓只有它与 `context/builder_test.go` 引用 `promptset`） | 新增 2 个用例 |

不新增文件：规则仍归属既有的两节提示词，单一事实源，不引入新的包或常量。

**已核实的约束（不要偏离）：**
- 全仓无任何现有测试断言旧句子文本（`grep -rn "locked from the user\|Reply in the user" --include=*_test.go .` 零命中），因此替换是安全的。
- `engine/sub_agent.go:1207-1217` 的 `stableSystemPrompt()` 拼接顺序为 `prompts.System + Examples + Language Pack + sub_agent.md`，主提示词规则已被子代理继承；本次是在尾部就近补锚点，不是补缺口。
- 规则中嵌套的反引号标识符（`todo_write`、`ask_user`）在 `system.md` 里是普通文本，不参与 Go 语法，无需转义。

---

### 任务 1：主系统提示词的语言规则

**文件：**
- 修改：`context/promptset/en/system.md:95`
- 测试：`context/promptset/promptset_test.go`（在文件末尾追加）

- [ ] **步骤 1：编写失败的测试**

在 `context/promptset/promptset_test.go` 末尾追加（`strings` 与 `testing` 已在文件头导入，无需改 import）：

```go
// TestSystemPrompt_RepliesInUserLanguage locks the session-language rule: the
// language is read from the messages the user types (not from injected context
// blocks, tool output, or the English prompt), the first request fixes it for the
// whole session, and it covers every user-visible word the model produces.
// The stale claim about a session-language lock mechanism (removed in 76f90f2)
// must not come back: it describes a mechanism that no longer exists.
func TestSystemPrompt_RepliesInUserLanguage(t *testing.T) {
	p := Get().System
	for _, want := range []string{"Reply in the user's language", "everything the user reads"} {
		if !strings.Contains(p, want) {
			t.Errorf("system prompt must carry the reply-language rule (missing %q)", want)
		}
	}
	if strings.Contains(p, "locked from the user's first message") {
		t.Error("system prompt must not claim a session-language lock: that mechanism was removed, so the claim is false")
	}
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：
```bash
go test ./context/promptset/ -run TestSystemPrompt_RepliesInUserLanguage -v
```
预期：FAIL，且**两条断言同时报错**（此刻新句尚未写入、旧句仍在）：
```
--- FAIL: TestSystemPrompt_RepliesInUserLanguage
    system prompt must carry the reply-language rule (missing "everything the user reads")
    system prompt must not claim a session-language lock: that mechanism was removed, so the claim is false
```

- [ ] **步骤 3：改写提示词**

把 `context/promptset/en/system.md:95` 这一行：

```
- **Reply in the user's language.** The session language is locked from the user's first message; keep using that language for the whole session even if later messages or tool output are in another language.
```

替换为：

```
- **Reply in the user's language.** The session language comes from the messages the user types — the first request fixes it and it holds for the whole session, even if later messages or tool output switch language. Read it from what the user writes, not from injected context blocks, tool output, or this prompt: those are English by design and say nothing about the reply language.
- **Write everything the user reads in that language**: narration, conclusions, `todo_write` step text, `ask_user` questions and options, and the sub-agent reports you relay — not only the final answer.
```

位置不变（仍在 `# Reply format` 一节的第 2 条位置），不新增小节、不动其余 bullet。

- [ ] **步骤 4：运行测试验证通过**

运行：
```bash
go test ./context/promptset/ -v
```
预期：PASS（4 个既有用例 + 新增用例全部通过）

- [ ] **步骤 5：Commit**

```bash
git add context/promptset/en/system.md context/promptset/promptset_test.go
git commit -m "fix(prompt): anchor reply language on the user's own messages"
```

---

### 任务 2：子代理提示词的语言规则

**文件：**
- 修改：`context/promptset/en/sub_agent.md:6`（在 `## How to work` 末条之后插入）
- 测试：`context/promptset/promptset_test.go`（在文件末尾追加）

- [ ] **步骤 1：编写失败的测试**

在 `context/promptset/promptset_test.go` 末尾追加：

```go
// TestSubAgentPrompt_RepliesInUserLanguage locks the sub-agent language rule:
// sub-agent streams render in the UI and their summaries are relayed to the user
// by the parent, so they must follow the user's language rather than the
// English prompt and tool output they work with.
func TestSubAgentPrompt_RepliesInUserLanguage(t *testing.T) {
	p := Get().SubAgent
	if !strings.Contains(p, "in the user's language") {
		t.Errorf("sub-agent prompt must instruct reporting in the user's language, got %q", p)
	}
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：
```bash
go test ./context/promptset/ -run TestSubAgentPrompt_RepliesInUserLanguage -v
```
预期：FAIL，输出含
`sub-agent prompt must instruct reporting in the user's language`

- [ ] **步骤 3：改写提示词**

在 `context/promptset/en/sub_agent.md` 的第 6 行

```
- After completing all necessary exploration and modification, declare completion explicitly.
```

之后插入一行（成为 `## How to work` 的最后一条，其后保留原有的空行）：

```
- Write your report in the user's language — the language of the user's own messages — not the language of this prompt or of tool output.
```

- [ ] **步骤 4：运行测试验证通过**

运行：
```bash
go test ./context/promptset/ -v
```
预期：PASS（全部用例）

- [ ] **步骤 5：Commit**

```bash
git add context/promptset/en/sub_agent.md context/promptset/promptset_test.go
git commit -m "fix(prompt): make sub-agents report in the user's language"
```

---

### 任务 3：全量校验与人工冒烟交接

**文件：** 无（只运行校验命令）

- [ ] **步骤 1：确认旧句不再出现在生产代码里**

运行：
```bash
grep -rn "locked from the user's first message" --include=*.go --include=*.md . | grep -v "^./docs/"
```
预期：无输出（只剩 `docs/` 下的设计与审查报告提及它）。

- [ ] **步骤 2：编译与静态检查**

运行：
```bash
go build ./... && go vet ./...
```
预期：两条命令均无输出、exit 0。

- [ ] **步骤 3：全量测试**

运行：
```bash
go test -count=1 ./...
```
预期：全部包 `ok`。（本次改动只触及提示词文本，`context/builder_test.go`、`cmd/run_test.go` 断言的是 Block S 与技能区块，不受影响。）

- [ ] **步骤 4：把人工冒烟交给用户（唯一的真验收）**

自动化测试只能锁住"提示词文本没被误删/改写"，**无法验证模型行为**。向用户交付以下冒烟脚本并要求其执行：

1. 新开一个 DeepAct 会话，**第一条消息用中文**提问（例如"看下 README"）。
2. 检查：回复正文、叙述、`todo_write` 的步骤文案、`ask_user` 的问题与选项**全部为中文**。
3. 在同一会话中途粘贴一段英文（例如一段英文 issue 描述）继续提问。
4. 检查：回复**仍是中文**（会话语言由第一条消息锁定）。
5. 若第 2 步或第 4 步仍是英文：按规格「不自洽点与取舍」的升级路径处理 —— 把 Block S 的 `role` 从 `user` 改为 `system`，或在 Block S 首行声明它是注入的会话上下文而非用户发言；不要继续往提示词里加句子。

---

## 自检

**规格覆盖度**（对照 `docs/superpowers/specs/2026-10-14-reply-language-follows-user-design.md` 逐节核对）：

| 规格条目 | 对应任务 |
|---|---|
| 1. `system.md` 的 `# Reply format` 两条 bullet | 任务 1 步骤 3 |
| 2. `sub_agent.md` 的 `## How to work` 新增一条 | 任务 2 步骤 3 |
| 3. 守卫测试两例（含"不含 locked"断言） | 任务 1/2 步骤 1 |
| 4. 范围边界（不改引擎/UI/其他英文资产） | 计划中无对应改动即为落实 |
| 5. 验证（build/vet/test + 人工冒烟） | 任务 3 |

无遗漏。

**占位符扫描：** 无"待定/TODO/后续实现"；每个代码步骤都给出了完整文本与确切命令。

**类型一致性：** 本计划不新增或修改任何 Go 符号、类型、函数签名；测试只调用既有导出项 `Get()`（`context/promptset/promptset.go:29`）的 `System`/`SubAgent` 字段。无命名漂移风险。
