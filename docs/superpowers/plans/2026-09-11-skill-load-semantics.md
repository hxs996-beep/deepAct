# Skill 从"激活"改为"加载"语义，移除语义匹配器 — 实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** 把技能机制从"激活"语义（引擎持久注入方法论到 stable zone + 每轮一次 LLM 语义匹配）改为"加载"语义（模型通过 `load_skill` 工具按需加载技能全文），消除每轮一次的多余 LLM 调用。

**架构：** `activate_skill` 工具改名 `load_skill`，执行语义从"改状态 + 持久注入 stable zone"改为"返回技能全文作为当轮 tool result"；删除语义匹配器（`SkillMatcher`/`SemanticMatcher`）与全部激活状态（`ActiveSkillName`/`ActiveSkillContent`/`matchedSkillsContent`/`activatedSkills`/`lastActivatedSkill`/`SetActiveSkill`）及引擎自动逻辑（链式自动激活、意图转移自动去激活）；`/<name>` 显式命令改为当轮注入全文；技能目录措辞改为"摘要仅供选择，加载前不得遵循"。对齐 deepseek-harness `tool-skill` 设计（规格 `docs/superpowers/specs/2026-09-11-skill-load-semantics-design.md`）。

**技术栈：** Go（engine / context / cmd / ui 包），标准库。

---

## 文件结构

| 文件 | 职责 | 操作 |
|---|---|---|
| `skill/matcher.go` | `SkillMatcher` 接口 | 删除 |
| `skill/matcher_llm.go` | `SemanticMatcher`（每轮 LLM 调用） | 删除 |
| `skill/matcher_test.go` | matcher 测试 | 删除 |
| `engine/agent.go:13-23,100-104,161-184` | 工具常量/参数/规格 | 改名 + 改描述 |
| `engine/turn.go:454-495,965-972,1378-1449,738-745` | 工具拦截/执行/handoff 注入 | 改造 + 删除 handoff 注入 |
| `engine/loop.go:31-93,196,205,316,404-435,438-449,643-661,1396-1452,1463-1469,1687-1689,1843-1865` | 引擎字段/语义匹配/命令/自动逻辑 | 删除 + 改造 |
| `engine/types.go:249-251` | 激活状态字段 | 删除 |
| `engine/interfaces.go:21-28` | `ContextBuilder` 接口 | 移除 `SetActiveSkill` |
| `context/builder.go:25,59-74,151-153,226-268,305-314` | stable zone 注入/Block B | 删除激活相关 |
| `cmd/run.go:146-183,330-346,358` | 技能目录措辞/匹配器注入 | 改造 + 删除 |
| `ui/model.go:642-646` | `skill_activated` 事件消费 | 删除 |
| `engine/turn_activate_skill_test.go` | load_skill 测试 | 改造 |
| `engine/skill_gate_removal_test.go:46` | 测试预置 | 适配 |
| `context/builder_test.go:161-166,364-411` | 激活相关用例 | 删除 |
| `engine/collab_test.go:81,124,410` | 测试预置 | 适配 |
| `engine/steer_test.go:110,168,190` | 测试预置/stub | 适配 |
| `engine/roundtable_test.go:159,297` | 测试预置 | 适配 |
| `ui/nodelabel_test.go:12,29` | 工具名引用 | 改名 |
| `engine/summarizeargs_test.go:172` | 工具名引用 | 改名 |

---

### 任务 1：删除 SkillMatcher（skill 包 3 文件 + engine 字段 + cmd 注入）

**文件：**
- 删除：`skill/matcher.go`、`skill/matcher_llm.go`、`skill/matcher_test.go`
- 修改：`engine/loop.go:31-44,46-93,186-206`
- 修改：`cmd/run.go:330-346,348-360`

- [ ] **步骤 1：删除 skill 包 3 个 matcher 文件**

```bash
rm skill/matcher.go skill/matcher_llm.go skill/matcher_test.go
```

- [ ] **步骤 2：删除 engine/loop.go 的 SkillMatcher 引用**

删除 `EngineDeps.SkillMatcher` 字段（`engine/loop.go:41`）：
```go
	Agents       *AgentRegistry
	Skills       *skill.Registry
	SkillMatcher skill.SkillMatcher   // ← 删除此行
	Router       ModelRouter
```

删除 `Engine.skillMatcher` 字段（`engine/loop.go:56`）：
```go
	skills       *skill.Registry
	skillMatcher skill.SkillMatcher   // ← 删除此行
	router       ModelRouter
```

删除 `NewEngine` 中的赋值（`engine/loop.go:196`）：
```go
		skills:          deps.Skills,
		skillMatcher:    deps.SkillMatcher,   // ← 删除此行
		router:          deps.Router,
```

- [ ] **步骤 3：删除 cmd/run.go 的 matchFn 构建与 deps 注入**

删除 `cmd/run.go:330-346` 整块（matchFn + skillMatcher 构建，含上方注释块 321-329 一并删除）。

删除 `cmd/run.go:358` 的 deps 字段：
```go
		Skills:       skillReg,
		SkillMatcher: skillMatcher,   // ← 删除此行
		Router:       routing,
```

- [ ] **步骤 4：运行构建验证**

运行：`go build ./...`
预期：编译通过。若报 `cmd/run.go: "context" imported and not used`，删除 `cmd/run.go:4` 的 `"context"` 导入（matchFn 是唯一使用 `context.Context` 签名的地方，需确认后删）。

- [ ] **步骤 5：运行测试验证**

运行：`go test ./skill/... ./engine/... ./cmd/... 2>&1 | head -30`
预期：skill 包测试通过（matcher_test 已删）；engine/cmd 编译测试通过。

- [ ] **步骤 6：Commit**

```bash
git add -A
git commit -m "refactor(skill): remove semantic SkillMatcher (extra LLM call per turn)"
```

---

### 任务 2：改造工具 activate_skill → load_skill（agent.go + turn.go 拦截/执行/summarizeArgs）

**文件：**
- 修改：`engine/agent.go:13-23,100-104,161-184`
- 修改：`engine/turn.go:454-495,965-972,1378-1449`

- [ ] **步骤 1：改名常量与参数类型（agent.go）**

`engine/agent.go:18`：
```go
	HandoffToolName        = "handoff_to_agent"
	LoadSkillToolName      = "load_skill"   // 原 ActivateSkillToolName = "activate_skill"
	TaskCompleteToolName   = "task_complete"
```

`engine/agent.go:100-104`：
```go
// LoadSkillParams is the JSON schema for the load_skill tool call.
type LoadSkillParams struct {
	SkillName string `json:"skill_name"`
	Reasoning string `json:"reasoning,omitempty"`
}
```

- [ ] **步骤 2：改造工具规格 loadSkillToolSpec（agent.go:161-184）**

将 `activateSkillToolSpec` 整体改为：
```go
// loadSkillToolSpec returns the tool definition exposed to LLMs for loading
// a skill's full instructions on demand (deepseek-harness tool-skill model).
func loadSkillToolSpec() ModelTool {
	return ModelTool{
		Type: "function",
		Function: ModelToolFunction{
			Name:        LoadSkillToolName,
			Description: "Load the full instructions for an available skill. Call this with the exact skill name from the Available Skills list before acting on a task that names or clearly matches that skill. The catalog contains summaries only; do not infer or follow a skill's instructions until it has been loaded.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"skill_name": {
						"type": "string",
						"description": "Name of the skill to load, e.g. 'writing-plans'"
					},
					"reasoning": {
						"type": "string",
						"description": "Explain to the user why this skill should be loaded next"
					}
				},
				"required": ["skill_name"]
			}`),
		},
	}
}
```

- [ ] **步骤 3：改造拦截调用点（turn.go:454-495）**

`turn.go:458`：
```go
	pendingLoadMsgs := e.processLoadSkillCalls(calls)   // 原 pendingActivateMsgs := e.processActivateSkillCalls(calls)
	pendingTodoMsgs := e.processTodoWriteCalls(calls)
	pendingAskUserMsgs := e.processAskUserCalls(calls)
```

`turn.go:464-468`：
```go
	// Add load_skill tool messages AFTER the assistant message, so the
	// DeepSeek API sees the correct order: assistant(tool_calls) → tool.
	for _, msg := range pendingLoadMsgs {
		e.history = append(e.history, msg)
	}
```

`turn.go:476-495`（注释与过滤）：
```go
	// Separate handoff calls from regular tool calls.
	// load_skill is already handled by the intercept block above
	// (turn.go:363-416) — it must NOT enter regularCalls, or Execute will
	// produce a duplicate tool message ("tool not found: load_skill")
	// with the same tool_call_id, violating the API contract.
	var handoffCalls []ToolCallRequest
	var regularCalls []ToolCallRequest
	for _, call := range calls {
		if call.Name == HandoffToolName {
			handoffCalls = append(handoffCalls, call)
		} else if call.Name == LoadSkillToolName {
			continue
		} else if call.Name == TodoWriteToolName {
			continue
		} else if call.Name == AskUserToolName {
			continue
		} else {
			regularCalls = append(regularCalls, call)
		}
	}
```

- [ ] **步骤 4：改造 summarizeArgs（turn.go:965-972）**

```go
	case "skill_install", "load_skill":
		// skill_install uses "name"; load_skill uses "skill_name".
		if n, ok := m["name"].(string); ok && n != "" {
			return "install skill: " + n
		}
		if n, ok := m["skill_name"].(string); ok && n != "" {
			return "load skill: " + n
		}
```

- [ ] **步骤 5：改造执行函数 processLoadSkillCalls（turn.go:1378-1449）**

将 `processActivateSkillCalls` 整体改为（保留错误路径，成功路径返回全文、无任何状态写入）：
```go
// processLoadSkillCalls intercepts load_skill tool calls from the
// assistant's response. For each call, it either returns the skill's full
// content as the tool result (success) or produces an error tool message
// (bad JSON, empty name, unknown skill). Every load_skill call receives a
// tool response — this is critical because the DeepSeek API requires that
// every tool_call_id in an assistant message has a matching tool response.
// Without it, the next model call would be rejected and the session would
// be permanently stuck.
//
// The returned slice of Messages must be appended to history AFTER the
// assistant message to satisfy the API ordering:
// assistant(tool_calls) → tool(responses).
func (e *Engine) processLoadSkillCalls(calls []ToolCallRequest) []Message {
	var pendingLoadMsgs []Message
	for _, call := range calls {
		if call.Name != LoadSkillToolName {
			continue
		}
		var params LoadSkillParams
		if err := json.Unmarshal(call.Input, &params); err != nil {
			pendingLoadMsgs = append(pendingLoadMsgs, Message{
				Role:       "tool",
				ToolCallID: call.ID,
				Content:    fmt.Sprintf("Error: invalid load_skill arguments: %v", err),
				Timestamp:  time.Now(),
			})
			continue
		}
		if params.SkillName == "" {
			pendingLoadMsgs = append(pendingLoadMsgs, Message{
				Role:       "tool",
				ToolCallID: call.ID,
				Content:    "Error: load_skill requires a non-empty skill_name",
				Timestamp:  time.Now(),
			})
			continue
		}

		s := e.skills.Get(params.SkillName)
		if s == nil {
			pendingLoadMsgs = append(pendingLoadMsgs, Message{
				Role:       "tool",
				ToolCallID: call.ID,
				Content:    fmt.Sprintf("Error: skill %q not found", params.SkillName),
				Timestamp:  time.Now(),
			})
			continue
		}

		// Load semantics: return the full skill content as this turn's
		// tool result. No engine state, no persistent stable-zone injection.
		pendingLoadMsgs = append(pendingLoadMsgs, Message{
			Role:       "tool",
			ToolCallID: call.ID,
			Content:    fmt.Sprintf("[SKILL — %s]\n\n%s", s.Name, s.Content),
			Timestamp:  time.Now(),
		})
	}
	return pendingLoadMsgs
}
```

- [ ] **步骤 6：运行构建验证**

运行：`go build ./engine/`
预期：编译通过（`ActivateSkillToolName`/`ActivateSkillParams`/`activateSkillToolSpec`/`processActivateSkillCalls` 已全部替换；`activateSkillToolSpec()` 在 `toolSpecsWithHandoff` 的调用点 `turn.go:685` 改为 `loadSkillToolSpec()`）。

> 注意：`turn.go:685` 的 `specs = append(specs, activateSkillToolSpec())` 需同步改为 `specs = append(specs, loadSkillToolSpec())`。若编译报未找到 `activateSkillToolSpec`，grep 其余调用点一并替换。

- [ ] **步骤 7：Commit**

```bash
git add engine/agent.go engine/turn.go
git commit -m "feat(engine): activate_skill -> load_skill (return full content, no state)"
```

---

### 任务 3：删除激活状态字段（types.go + loop.go 字段 + Run 重置 + handoff 注入）

**文件：**
- 修改：`engine/types.go:249-251`
- 修改：`engine/loop.go:80-93,316`
- 修改：`engine/turn.go:738-745`

- [ ] **步骤 1：删除 types.go 三个状态字段**

`engine/types.go:249-251`：
```go
	PendingActivateSkill string           `json:"pending_activate_skill,omitempty"` // skill name awaiting user confirmation via activate_skill tool   ← 删除
	ActiveSkillName      string           `json:"active_skill_name,omitempty"`      // name of the currently activated skill   ← 删除
	ActiveSkillContent   string           `json:"active_skill_content,omitempty"`   // full content of the activated skill   ← 删除
```

- [ ] **步骤 2：删除 loop.go 三个引擎字段**

`engine/loop.go:80-93` 整块（含注释）：
```go
	// matchedSkillsContent holds the content of matched skills for the current Run() call.
	// It is injected into sub-agent context when a handoff occurs, so skill methodology
	// instructions are carried through to sub-agents.
	matchedSkillsContent string

	// activatedSkills tracks skill names that have been explicitly activated
	// via /skill command within the current session, to prevent duplicate
	// injection from keyword-based auto-matching.
	activatedSkills map[string]bool

	// lastActivatedSkill records the most recently activated skill name.
	// The activate_skill tool checks NextSkills of this skill to determine
	// if auto-activation (no user confirmation) is allowed.
	lastActivatedSkill string
```

- [ ] **步骤 3：删除 Run 入口重置**

`engine/loop.go:316`：
```go
	e.readProgressKeys = make(map[string]bool)
	e.matchedSkillsContent = ""   // ← 删除此行
	e.runStartAt = time.Now()
```

- [ ] **步骤 4：删除 handoff 注入**

`engine/turn.go:738-745` 整块：
```go
	// Inject matched skill content into sub-agent context
	if e.matchedSkillsContent != "" {
		if handoff.Context != "" {
			handoff.Context = e.matchedSkillsContent + "\n\n" + handoff.Context
		} else {
			handoff.Context = e.matchedSkillsContent
		}
	}
```

- [ ] **步骤 5：运行构建验证**

运行：`go build ./engine/`
预期：编译通过。若报 `e.state.ActiveSkillName` 等未定义，grep 确认残留引用并删除。

- [ ] **步骤 6：Commit**

```bash
git add engine/types.go engine/loop.go engine/turn.go
git commit -m "refactor(engine): remove skill activation state fields"
```

---

### 任务 4：删除引擎自动逻辑 + /<name> 改为加载语义（loop.go）

**文件：**
- 修改：`engine/loop.go:404-435,438-449,643-661,1396-1452,1463-1469,1687-1689,1843-1865`

- [ ] **步骤 1：/<name> 命令改为加载语义（loop.go:404-435）**

将 case "activate" 分支改为（保留解析与大小写回退，仅替换行为）：
```go
		case "activate":
			s := e.skills.Get(sc.name)
			if s == nil {
				// Try case-insensitive match
				for _, sk := range e.skills.All() {
					if strings.EqualFold(sk.Name, sc.name) {
						s = sk
						break
					}
				}
			}
			if s == nil {
				msg := fmt.Sprintf("Skill '%s' not found. Use `/skills` to list available skills.", sc.name)
				if zh {
					msg = fmt.Sprintf("技能 '%s' 不存在。使用 `/skills` 查看可用技能。", sc.name)
				}
				return &EngineResponse{Summary: msg, Stage: StageAct}, nil
			}
			// Load semantics: inject the full content once for this turn,
			// not persistently. Consumed at the next turn start (turn.go:80-86).
			e.pendingPinnedMessages = append(e.pendingPinnedMessages,
				fmt.Sprintf("[SKILL — %s]\n\n%s", s.Name, s.Content))

			taskText := extractTaskTextAfterSkillCmd(userMsg, sc.name)
			if taskText == "" {
				msg := fmt.Sprintf("✓ Skill `%s` loaded. Full methodology injected for this turn.", s.Name)
				if zh {
					msg = fmt.Sprintf("✓ 已加载 skill `%s`：方法论已注入当前回合。", s.Name)
				}
				return &EngineResponse{Summary: msg, Stage: StageAct}, nil
			}
			if len(e.history) > 0 {
				e.history[len(e.history)-1].Content = taskText
			}
		}
```

- [ ] **步骤 2：删除语义匹配块（loop.go:438-449）**

整块删除（含注释 438-441）：
```go
	// Skill matching: semantic auto-activation via the SkillMatcher interface.
	// The matcher (wired in cmd/run.go) sends the user message + all skill
	// descriptions to the flash model and returns the one best-matching skill,
	// or nil. When no matcher is wired, skill matching is disabled.
	if e.state.ActiveSkillName == "" && e.skillMatcher != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		matched := e.skillMatcher.Match(ctx, userMsg, e.skills.All())
		cancel()
		if matched != nil {
			e.activateSkill(matched, "semantic match")
		}
	}
```

- [ ] **步骤 3：删除意图转移自动去激活（loop.go:643-661）**

整块删除（含注释 640-642）：
```go
	// Auto-deactivate skill when user intent shifts from development to operational use.
	// This prevents skill methodology (e.g., TDD) from constraining verification
	// or ad-hoc testing after development is complete.
	if e.state.ActiveSkillName != "" && !strings.HasPrefix(strings.TrimSpace(userMsg), "/") {
		if e.detectIntentShift(userMsg) {
			skillName := e.state.ActiveSkillName
			e.deactivateSkill()
			msg := fmt.Sprintf("✓ 自动解除 skill `%s`：检测到意图从开发转向使用/验证。", skillName)
			if !zh {
				msg = fmt.Sprintf("✓ Auto-deactivated skill `%s`: intent shift from development to usage/verification.", skillName)
			}
			e.history = append(e.history, Message{Role: "user", Content: msg, Timestamp: time.Now()})
			if e.config.OnProgress != nil {
				e.config.OnProgress(ProgressEvent{
					Type:   "skill_deactivated",
					Name:   skillName,
					Detail: "auto-deactivated due to intent shift",
				})
			}
			loopLog.Printf("auto-deactivated skill %q: user intent shift detected", skillName)
		}
	}
```

- [ ] **步骤 4：删除 deactivateSkill 方法（loop.go:1396-1452）**

整块删除（`deactivateSkill` 方法 + 注释 1396-1400）。`detectIntentShift` 方法保留（可能被其他逻辑使用，删除后若编译报未使用——Go 不报未使用函数，故保留无风险）。

- [ ] **步骤 5：删除 clearSessionState 重置（loop.go:1687-1689）**

```go
	e.deactivateSkill()   // ← 删除
	e.activatedSkills = make(map[string]bool)   // ← 删除
	e.lastActivatedSkill = ""   // ← 删除
```

- [ ] **步骤 6：删除 activateSkill 方法（loop.go:1843-1865）**

整块删除（`activateSkill` 方法 + 注释 1843-1844）。

- [ ] **步骤 7：删除未使用的 joinSkillNames（loop.go:1463-1469）**

```go
func joinSkillNames(skills []*skill.Skill) string {
	names := make([]string, len(skills))
	for i, s := range skills {
		names[i] = s.Name
	}
	return strings.Join(names, ", ")
}
```

- [ ] **步骤 8：运行构建验证**

运行：`go build ./engine/`
预期：编译通过。若报 `context` 未使用（loop.go:4 的 `"context"` 导入），确认 `loop.go` 其他位置是否还用 `context`（`Run(ctx context.Context)` 等必然使用，保留）。若 `skill` 导入不再使用（loop.go:16），删除。

- [ ] **步骤 9：Commit**

```bash
git add engine/loop.go
git commit -m "refactor(engine): remove skill auto-activation logic; /<name> loads once"
```

---

### 任务 5：ContextBuilder 接口与 context 包清理

**文件：**
- 修改：`engine/interfaces.go:21-28`
- 修改：`context/builder.go:25,59-74,151-153,226-268,305-314`

- [ ] **步骤 1：移除 ContextBuilder 接口的 SetActiveSkill**

`engine/interfaces.go:21-28`：
```go
type ContextBuilder interface {
	Build(state *TaskState, history []Message, toolResults []ToolResult) []ModelMessage
	EstimateTokens(messages []ModelMessage) int
}
```

- [ ] **步骤 2：删除 builder.go 字段与 SetActiveSkill 方法**

`context/builder.go:25` 删除 `activeSkillBlock` 字段：
```go
	skillsBlock          string          // built once from skill registry, cached for cache stability
	agentsBlock          string          // rendered AGENTS.md content in the stable zone; built once at startup, cached
```

删除 `context/builder.go:59-74` 的 `SetActiveSkill` 方法（含注释）。

- [ ] **步骤 3：删除 Build 中的 activeSkillBlock 注入（builder.go:142-153）**

删除注释块（142-150）与注入：
```go
	// === VOLATILE TAIL (small, changes each turn — cache miss acceptable) ===
	if a.activeSkillBlock != "" {
		messages = append(messages, engine.ModelMessage{Role: "user", Content: a.activeSkillBlock})
	}

```
改为仅保留：
```go
	// === VOLATILE TAIL (small, changes each turn — cache miss acceptable) ===
```

- [ ] **步骤 4：删除 formatTaskStateVolatile 中技能字段（builder.go:230-249）**

删除结构体字段（232-233）与赋值（248-249）：
```go
		ActiveSkillName  string              `json:"active_skill_name,omitempty"`
		SkillReminder    string              `json:"skill_reminder,omitempty"`
```
```go
		ActiveSkillName:  state.ActiveSkillName,
		SkillReminder:    skillReminder(state.ActiveSkillName),
```

- [ ] **步骤 5：删除 skillReminder 函数（builder.go:305-314）**

整块删除（含注释 305-307）。

- [ ] **步骤 6：运行构建验证**

运行：`go build ./context/ ./engine/`
预期：编译通过。`builder.go` 若报 `fmt` 未使用（SetActiveSkill 是唯一用 `fmt.Sprintf` 的地方之一——`BuildBlockB` 等可能仍用），按编译报错处理；`context/builder.go:3-14` 导入按需清理。

- [ ] **步骤 7：Commit**

```bash
git add engine/interfaces.go context/builder.go
git commit -m "refactor(context): drop active-skill stable-zone injection"
```

---

### 任务 6：技能目录措辞改为"摘要仅供选择"（cmd/run.go）

**文件：**
- 修改：`cmd/run.go:142-183`

- [ ] **步骤 1：改造 buildSkillsBlock 措辞**

将 `cmd/run.go:142-183` 的 `buildSkillsBlock` 中头部措辞（152-154）改为：
```go
	var b strings.Builder
	b.WriteString("## Available Skills\n")
	b.WriteString("以下为可用技能摘要，仅供选择。当用户明确命名某技能，或任务明显匹配某技能描述时，先调用 `load_skill` 工具加载其全文，再遵循其中指令。摘要不含完整指令，加载前不得推断或遵循。用户也可用 `/<name>` 直接加载。若当前技能到达终态需切换，调用 `load_skill` 加载下一个技能。\n\n")
	for _, s := range all {
```
（函数注释 143-145 同步更新："The model loads a skill's full instructions via the load_skill tool when the task matches."）

> 保留 `DisableModelInvocation` 过滤（156-158）与 `NextSkills` 渲染（173-178，展示 "→ Next:" 帮助模型知道链式下一步，但不再自动激活）。

- [ ] **步骤 2：运行构建验证**

运行：`go build ./cmd/`
预期：编译通过。

- [ ] **步骤 3：Commit**

```bash
git add cmd/run.go
git commit -m "docs(cmd): skills catalog says summaries-only, load before following"
```

---

### 任务 7：删除 UI skill_activated 事件消费（ui/model.go）

**文件：**
- 修改：`ui/model.go:642-646`

- [ ] **步骤 1：删除 skill_activated case**

`ui/model.go:642-646`：
```go
		case "skill_activated":
			m.messages = append(m.messages, DisplayMessage{
				Role:    "system",
				Content: fmt.Sprintf("Skill activated: **%s** — %s", msg.Name, msg.Detail),
			})
```
整块删除。

- [ ] **步骤 2：运行构建验证**

运行：`go build ./ui/`
预期：编译通过。若 `fmt` 导入不再使用（ui/model.go 中 `fmt` 必然大量使用，保留）。

- [ ] **步骤 3：Commit**

```bash
git add ui/model.go
git commit -m "refactor(ui): drop skill_activated system message"
```

---

### 任务 8：适配测试

**文件：**
- 修改：`engine/turn_activate_skill_test.go`
- 修改：`engine/skill_gate_removal_test.go:46`
- 修改：`context/builder_test.go:161-166,364-411`
- 修改：`engine/collab_test.go:81,124,410`
- 修改：`engine/steer_test.go:110,168,190`
- 修改：`engine/roundtable_test.go:159,297`
- 修改：`ui/nodelabel_test.go:12,29`
- 修改：`engine/summarizeargs_test.go:172`

- [ ] **步骤 1：改造 turn_activate_skill_test.go 为 load_skill 语义**

**重要：** 仅替换文件前 186 行（`stubContextBuilder` + 3 个 `TestProcessActivateSkillCalls_*` 测试）。保留 188 行之后的 `TestProcessHandoffResults_*` 测试不变（与本改造无关）。

替换前 186 行为：

```go
package engine

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/deepact/deepact/skill"
)

// stubContextBuilder is a minimal ContextBuilder for testing load_skill
// interception. Other methods are no-ops.
type stubContextBuilder struct{}

func (s *stubContextBuilder) Build(_ *TaskState, _ []Message, _ []ToolResult) []ModelMessage {
	return nil
}
func (s *stubContextBuilder) EstimateTokens(_ []ModelMessage) int { return 0 }

// TestProcessLoadSkillCalls_NoOrphanedToolCalls verifies that every
// load_skill call receives a tool response message — even when the
// call is invalid (bad JSON, empty name, unknown skill). Without a response,
// the DeepSeek API rejects the next request because the assistant message
// contains a tool_call_id with no matching tool message, permanently
// stalling the session.
func TestProcessLoadSkillCalls_NoOrphanedToolCalls(t *testing.T) {
	skillReg := skill.NewRegistry()
	skillReg.Register(&skill.Skill{
		Name:        "brainstorming",
		Description: "design before code",
		Content:     "step 1...",
	})

	e := &Engine{
		skills: skillReg,
		state:  &TaskState{},
		config: EngineConfig{},
	}

	tests := []struct {
		name    string
		callID  string
		input   string
		wantErr bool
		wantSub string
	}{
		{
			name:    "bad JSON",
			callID:  "call_bad_json",
			input:   `{invalid json}`,
			wantErr: true,
			wantSub: "invalid load_skill arguments",
		},
		{
			name:    "empty skill_name",
			callID:  "call_empty_name",
			input:   `{"skill_name":""}`,
			wantErr: true,
			wantSub: "non-empty skill_name",
		},
		{
			name:    "unknown skill",
			callID:  "call_unknown",
			input:   `{"skill_name":"nonexistent"}`,
			wantErr: true,
			wantSub: `skill "nonexistent" not found`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := []ToolCallRequest{
				{ID: tt.callID, Name: LoadSkillToolName, Input: json.RawMessage(tt.input)},
			}
			msgs := e.processLoadSkillCalls(calls)
			if len(msgs) != 1 {
				t.Fatalf("expected 1 tool response, got %d — tool_call %q is orphaned", len(msgs), tt.callID)
			}
			if msgs[0].ToolCallID != tt.callID {
				t.Errorf("ToolCallID = %q, want %q", msgs[0].ToolCallID, tt.callID)
			}
			if msgs[0].Role != "tool" {
				t.Errorf("Role = %q, want %q", msgs[0].Role, "tool")
			}
			if !strings.Contains(msgs[0].Content, tt.wantSub) {
				t.Errorf("Content = %q, want substring %q", msgs[0].Content, tt.wantSub)
			}
		})
	}
}

// TestProcessLoadSkillCalls_ValidReturnsFullContent verifies that a valid
// load_skill call returns the skill's full content as the tool result and
// writes NO engine state (load semantics, not activate semantics).
func TestProcessLoadSkillCalls_ValidReturnsFullContent(t *testing.T) {
	skillReg := skill.NewRegistry()
	skillReg.Register(&skill.Skill{
		Name:        "brainstorming",
		Description: "design before code",
		Content:     "step 1...",
	})

	e := &Engine{
		skills: skillReg,
		state:  &TaskState{},
		config: EngineConfig{},
	}

	calls := []ToolCallRequest{
		{ID: "call_ok", Name: LoadSkillToolName, Input: json.RawMessage(`{"skill_name":"brainstorming"}`)},
	}
	msgs := e.processLoadSkillCalls(calls)

	if len(msgs) != 1 {
		t.Fatalf("expected 1 tool response, got %d", len(msgs))
	}
	if msgs[0].ToolCallID != "call_ok" {
		t.Errorf("ToolCallID = %q, want call_ok", msgs[0].ToolCallID)
	}
	if !strings.Contains(msgs[0].Content, "[SKILL — brainstorming]") {
		t.Errorf("Content = %q, want [SKILL — brainstorming] marker", msgs[0].Content)
	}
	if !strings.Contains(msgs[0].Content, "step 1...") {
		t.Errorf("Content = %q, want full skill content", msgs[0].Content)
	}
}

// TestProcessLoadSkillCalls_Mixed verifies that when load_skill calls
// are mixed with regular tool calls, only load_skill calls get responses
// from processLoadSkillCalls (regular calls are handled elsewhere).
func TestProcessLoadSkillCalls_Mixed(t *testing.T) {
	skillReg := skill.NewRegistry()
	skillReg.Register(&skill.Skill{
		Name:        "brainstorming",
		Description: "design before code",
		Content:     "step 1...",
	})

	e := &Engine{
		skills: skillReg,
		state:  &TaskState{},
		config: EngineConfig{},
	}

	calls := []ToolCallRequest{
		{ID: "call_read", Name: "read", Input: json.RawMessage(`{"path":"foo.go"}`)},
		{ID: "call_bad", Name: LoadSkillToolName, Input: json.RawMessage(`{"skill_name":"nope"}`)},
		{ID: "call_ok", Name: LoadSkillToolName, Input: json.RawMessage(`{"skill_name":"brainstorming"}`)},
		{ID: "call_grep", Name: "grep", Input: json.RawMessage(`{"pattern":"foo"}`)},
	}

	msgs := e.processLoadSkillCalls(calls)

	if len(msgs) != 2 {
		t.Fatalf("expected 2 tool responses (for load_skill only), got %d", len(msgs))
	}

	ids := map[string]bool{}
	for _, m := range msgs {
		ids[m.ToolCallID] = true
	}
	if !ids["call_bad"] {
		t.Error("missing tool response for call_bad (unknown skill)")
	}
	if !ids["call_ok"] {
		t.Error("missing tool response for call_ok (valid skill)")
	}
	if ids["call_read"] {
		t.Error("regular call 'read' should not get a response from processLoadSkillCalls")
	}
	if ids["call_grep"] {
		t.Error("regular call 'grep' should not get a response from processLoadSkillCalls")
	}
}
```

> 保留原文件 `engine/turn_activate_skill_test.go` 中与 handoff 结果处理相关的测试（`TestProcessHandoffResults_*`），它们与本改造无关。

- [ ] **步骤 2：适配 skill_gate_removal_test.go:46**

`engine/skill_gate_removal_test.go:46`：
```go
		state:     &TaskState{TurnNumber: 5, ActiveSkillName: "systematic-debugging"},
```
改为：
```go
		state:     &TaskState{TurnNumber: 5},
```

- [ ] **步骤 3：删除 context/builder_test.go 激活用例**

删除 `context/builder_test.go:161-166` 的 "with active skill" 用例：
```go
		{
			name: "with active skill",
			state: &engine.TaskState{
				ActiveSkillName: "test-driven-development",
				TurnNumber:      1,
			},
			want: `"active_skill_name":"test-driven-development"`,
		},
```

删除 `context/builder_test.go:364-411` 的 `TestBuild_ActiveSkillInTail` 函数（含 `stubContextBuilder` 若在该文件定义——不，stub 在 turn_activate_skill_test.go，本文件只删测试函数）。

- [ ] **步骤 4：删除测试中 activatedSkills 预置行**

`engine/collab_test.go:81,124,410`：删除 `activatedSkills: make(map[string]bool),` 行。
`engine/steer_test.go:110,190`：删除 `activatedSkills: make(map[string]bool),` 行。
`engine/roundtable_test.go:159,297`：删除 `activatedSkills: make(map[string]bool),` 行。

`engine/steer_test.go:168`：删除 `func (steerContextBuilder) SetActiveSkill(_, _ string) {}` 方法。

- [ ] **步骤 5：改工具名引用**

`ui/nodelabel_test.go:12`：`ToolNode{Name: "activate_skill", ...}` → `"load_skill"`。
`ui/nodelabel_test.go:29`：`{Name: "activate_skill", Detail: "", Icon: "[*]", Done: true}` → `"load_skill"`。
`engine/summarizeargs_test.go:172`：`"activate_skill"` → `"load_skill"`。

- [ ] **步骤 6：新增 buildSkillsBlock 措辞测试**

创建 `cmd/run_test.go`（新文件，cmd 包目前无测试文件），验证目录措辞为"加载前不得遵循"且不包含"BLOCKING REQUIREMENT"旧文案、过滤 `DisableModelInvocation`：

```go
package cmd

import (
	"strings"
	"testing"

	"github.com/deepact/deepact/skill"
)

func TestBuildSkillsBlock_LoadSemanticsWording(t *testing.T) {
	reg := skill.NewRegistry()
	reg.Register(&skill.Skill{Name: "brainstorming", Description: "design before code"})
	reg.Register(&skill.Skill{Name: "hidden", Description: "no model", DisableModelInvocation: true})

	got := buildSkillsBlock(reg.All())

	if !strings.Contains(got, "load_skill") {
		t.Errorf("catalog must mention load_skill tool, got %q", got)
	}
	if strings.Contains(got, "BLOCKING REQUIREMENT") {
		t.Errorf("old activate semantics wording must be gone, got %q", got)
	}
	if !strings.Contains(got, "brainstorming") {
		t.Errorf("catalog must list invocable skill, got %q", got)
	}
	if strings.Contains(got, "hidden") {
		t.Errorf("DisableModelInvocation skill must not appear in catalog, got %q", got)
	}
}

func TestBuildSkillsBlock_Empty(t *testing.T) {
	if got := buildSkillsBlock(nil); got != "" {
		t.Errorf("expected empty block for no skills, got %q", got)
	}
}
```

> 注意：`cmd` 包测试会引入 `skill` 依赖（已通过 `cmd/run.go` 导入，无循环依赖）。

- [ ] **步骤 7：运行全部测试**

运行：`go test ./... 2>&1 | tail -40`
预期：全部 PASS。若有残留编译错误（如其他测试引用已删字段），grep 修复。

- [ ] **步骤 8：Commit**

```bash
git add engine/ context/ ui/ cmd/
git commit -m "test: adapt to load_skill semantics, drop activation assertions"
```

---

### 任务 9：全量验证与回归

**文件：** 无（验证）

- [ ] **步骤 1：构建全项目**

运行：`go build ./...`
预期：成功，无输出。

- [ ] **步骤 2：运行完整测试套件（含 race）**

运行：`go test -race ./... 2>&1 | tail -50`
预期：全部 PASS，无 race 报告。

- [ ] **步骤 3：grep 残留引用**

运行：
```bash
grep -rn "activate_skill\|ActivateSkillToolName\|ActivateSkillParams\|activateSkillToolSpec\|processActivateSkillCalls\|matchedSkillsContent\|activatedSkills\|lastActivatedSkill\|ActiveSkillName\|ActiveSkillContent\|SetActiveSkill\|SkillMatcher\|skillReminder\|activeSkillBlock\|PendingActivateSkill\|skill_activated\|skill_deactivated\|joinSkillNames" --include="*.go" .
```
预期：仅剩 `skill_activated`/`skill_deactivated` 若在 `engine/interfaces.go` ProgressEvent 类型注释或 `ui` 类型定义中作为字符串字面量出现（如 ProgressEvent.Type 常量）。若出现于测试或生产代码引用，逐一评估——`activate_skill` 若在旧 session 数据/注释中保留可接受，但生产代码引用必须清除。

- [ ] **步骤 4：Commit（如有残留清理）**

```bash
git add -A
git commit -m "chore: final cleanup after skill load-semantics refactor" || echo "no changes"
```

- [ ] **步骤 5：最终确认**

运行：`git log --oneline -10`
预期：看到本计划的全部 commit 序列。

---

## 自检结果

**规格覆盖度：**
- 语义匹配器删除（规格 §3 项 3）→ 任务 1 ✅
- 工具改名 + 加载语义（§变更 1/2）→ 任务 2 ✅
- 激活状态删除（§移除清单）→ 任务 3、4、5 ✅
- `/<name>` 加载语义（§变更 4）→ 任务 4 步骤 1 ✅
- 目录措辞（§变更 3）→ 任务 6 ✅
- UI 事件消费（§移除清单补充）→ 任务 7 ✅
- 测试适配（§测试变更）→ 任务 8 ✅
- skill/skill.go 包注释（§移除清单补充）→ 归入任务 1 附注：**遗漏**——需在任务 1 或任务 4 后更新 `skill/skill.go:1-7` 包注释为 load 语义。

**占位符扫描：** 无 "TODO"/"待定"；每个代码步骤含完整代码。

**类型一致性：** `LoadSkillToolName`/`LoadSkillParams`/`loadSkillToolSpec`/`processLoadSkillCalls` 在任务 2 定义后各任务统一使用；`pendingLoadMsgs` 命名一致。

**内联修复（已并入计划）：**
- `skill/skill.go:1-7` 包注释更新为 load 语义（在任务 4 完成后追加，避免与字段删除冲突）：

```go
// Package skill provides a methodology skill system for guiding agent behavior.
//
// Skills are composable methodology templates (like "brainstorming", "debugging")
// that can be loaded by the user via /<skillname> commands or by the model via
// the load_skill tool. Available skills are listed in the stable system prompt
// block so the model can decide which methodology to apply.
```

- `turn.go:685` `activateSkillToolSpec()` → `loadSkillToolSpec()` 调用点（并入任务 2 步骤 6 注意）。
