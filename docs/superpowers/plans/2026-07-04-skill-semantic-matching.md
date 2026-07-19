# Skill 语义匹配自动触发 — 实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 用 LLM 语义匹配增强现有关键词匹配，当关键词 0 命中时通过 Flash 模型理解用户意图，自动激活相关 skill。

**Architecture:** 提取 `SkillMatcher` 接口，实现 `KeywordMatcher`（现有逻辑）和 `SemanticMatcher`（LLM 调用），用 `FallbackMatcher` 组合两者。为避免 `skill` ↔ `engine` 循环依赖，`SemanticMatcher` 通过函数回调 `MatchFunc` 接收模型调用能力，由 `cmd/run.go` 适配注入。

**Tech Stack:** Go 1.26, DeepSeek API (Flash model), 现有 engine/llm 包

## Global Constraints

- 语义匹配是增强，不是依赖。任何失败都不阻塞主流程。
- 一次只激活一个 skill。
- 已有 skill 激活时不重复匹配。
- 保留现有关键词匹配逻辑，不改动其行为。
- `skill` 包不得 import `engine`（避免循环依赖）。

## File Structure

| 文件 | 职责 |
|------|------|
| `skill/matcher.go` | `SkillMatcher` 接口 + `KeywordMatcher` + `FallbackMatcher` |
| `skill/matcher_llm.go` | `SemanticMatcher`（LLM 语义匹配） |
| `skill/matcher_test.go` | 所有 matcher 的测试 |
| `engine/loop.go` | `EngineDeps` 加 `SkillMatcher` 字段；`NewEngine` 赋值；`Run()` 调用 matcher |
| `cmd/run.go` | 构造 `KeywordMatcher`、`SemanticMatcher`、`FallbackMatcher` 并注入 `EngineDeps` |

---

### Task 1: 创建 SkillMatcher 接口 + KeywordMatcher

**Files:**
- Create: `skill/matcher.go`
- Create: `skill/matcher_test.go`

**Interfaces:**
- Produces: `SkillMatcher` 接口 (`Match(ctx, userMsg, skills) *Skill`)
- Produces: `KeywordMatcher` 结构体 + `NewKeywordMatcher(reg *Registry)`

- [ ] **Step 1: 写测试 — TestKeywordMatcher_Match**

```go
// skill/matcher_test.go
package skill

import (
    "context"
    "testing"
)

func TestKeywordMatcher_Match(t *testing.T) {
    r := NewRegistry()
    r.Register(&Skill{
        Name:        "systematic-debugging",
        Description: "Use when encountering any bug",
        Keywords:    []string{"debug", "bug", "调试"},
        AutoActivateThreshold: intPtr(1),
    })
    r.Register(&Skill{
        Name:        "brainstorming",
        Description: "Use before any creative work",
        Keywords:    []string{"设计", "design"},
        AutoActivateThreshold: intPtr(1),
    })

    m := NewKeywordMatcher(r)

    // Match: 关键词命中 + 阈值达标
    got := m.Match(context.Background(), "有个bug需要调试", r.All())
    if got == nil || got.Name != "systematic-debugging" {
        t.Fatalf("expected systematic-debugging, got %v", got)
    }

    // No match: 关键词未命中
    got = m.Match(context.Background(), "帮我写个排序算法", r.All())
    if got != nil {
        t.Fatalf("expected nil, got %v", got.Name)
    }
}

func TestKeywordMatcher_NoAutoActivateThreshold(t *testing.T) {
    r := NewRegistry()
    r.Register(&Skill{
        Name:     "systematic-debugging",
        Keywords: []string{"debug", "bug"},
        // AutoActivateThreshold is nil — never auto-activate
    })

    m := NewKeywordMatcher(r)
    got := m.Match(context.Background(), "有个bug", r.All())
    if got != nil {
        t.Fatalf("expected nil (no threshold), got %v", got.Name)
    }
}

func intPtr(i int) *int { return &i }
```

- [ ] **Step 2: 运行测试 — 预期 FAIL（文件不存在）**

```bash
cd /Users/admin/gitspace/deepact && go test ./skill/ -run TestKeywordMatcher -v
```

- [ ] **Step 3: 实现 KeywordMatcher**

```go
// skill/matcher.go
package skill

import "context"

// SkillMatcher matches a user message to the most relevant skill.
// Returns nil if no skill meets the activation criteria.
type SkillMatcher interface {
    Match(ctx context.Context, userMsg string, skills []*Skill) *Skill
}

// KeywordMatcher uses keyword substring matching with AutoActivateThreshold.
// It wraps Registry.MatchTopSkillsWithScores and applies the threshold check.
type KeywordMatcher struct {
    reg *Registry
}

func NewKeywordMatcher(reg *Registry) *KeywordMatcher {
    return &KeywordMatcher{reg: reg}
}

// Match returns the highest-scoring skill whose keyword match count meets its
// AutoActivateThreshold. Returns nil if no skill qualifies.
func (m *KeywordMatcher) Match(_ context.Context, userMsg string, skills []*Skill) *Skill {
    matches := m.reg.MatchTopSkillsWithScores(3, userMsg)
    for _, match := range matches {
        if match.Skill.AutoActivateThreshold != nil && match.Score >= *match.Skill.AutoActivateThreshold {
            return match.Skill
        }
    }
    return nil
}
```

- [ ] **Step 4: 运行测试 — 预期 PASS**

```bash
cd /Users/admin/gitspace/deepact && go test ./skill/ -run TestKeywordMatcher -v
```

- [ ] **Step 5: Commit**

```bash
git add skill/matcher.go skill/matcher_test.go
git commit -m "feat(skill): add SkillMatcher interface and KeywordMatcher"
```

---

### Task 2: 引擎集成 KeywordMatcher（loop.go 改接）

**Files:**
- Modify: `engine/loop.go` — EngineDeps + NewEngine + Run() 中匹配逻辑

**Interfaces:**
- Consumes: `skill.SkillMatcher` (from Task 1)
- Modifies: `EngineDeps.SkillMatcher` 字段
- Modifies: `NewEngine` 赋值 `e.skillMatcher`
- Produces: `Engine.activateSkill(*skill.Skill, string)` 方法（提取现有激活逻辑）

- [ ] **Step 1: 在 EngineDeps 加 SkillMatcher 字段**

```go
// engine/loop.go — EngineDeps struct (L30-41)
type EngineDeps struct {
    Model        ModelClient
    Tools        ToolExecutor
    Policy       PolicyChecker
    Context      ContextBuilder
    Compressor   Compressor
    Session      SessionStore
    Agents       *AgentRegistry
    Skills       *skill.Registry
    SkillMatcher skill.SkillMatcher // NEW: skill matching strategy
    Router       ModelRouter
    MCPManagers  []io.Closer
}
```

- [ ] **Step 2: Engine struct 加 skillMatcher 字段 + NewEngine 赋值**

```go
// engine/loop.go — Engine struct (L43-108), add field after `skills`:
    skills       *skill.Registry
    skillMatcher skill.SkillMatcher // NEW

// engine/loop.go — NewEngine() (L128-165), add after `skills`:
    e := &Engine{
        // ... existing fields ...
        skills:       deps.Skills,
        skillMatcher: deps.SkillMatcher, // NEW
        // ...
    }
```

- [ ] **Step 3: 提取 activateSkill 方法**

在 `engine/loop.go` 末尾添加新方法，复用 L310-323（显式激活）和 L364-385（自动激活）的共同逻辑：

```go
// activateSkill activates a skill and injects its methodology into the stable zone.
// reason is a human-readable description of why the skill was activated (for logging/progress).
func (e *Engine) activateSkill(s *skill.Skill, reason string) {
    e.activatedSkills[s.Name] = true
    e.lastActivatedSkill = s.Name
    e.state.ActiveSkillName = s.Name
    e.state.ActiveSkillContent = s.Content
    e.context.SetActiveSkill(s.Name, s.Content)

    skillMsg := fmt.Sprintf(
        "✅ Skill `%s` auto-activated (%s). Full methodology now in stable zone.",
        s.Name, reason,
    )
    e.pendingPinnedMessages = append(e.pendingPinnedMessages, skillMsg)
    e.matchedSkillsContent = fmt.Sprintf("[SKILL — %s]\n\n%s", s.Name, s.Content)
    if e.config.OnProgress != nil {
        e.config.OnProgress(ProgressEvent{
            Type:   "skill_activated",
            Name:   s.Name,
            Detail: s.Description + " (" + reason + ")",
        })
    }
}
```

- [ ] **Step 4: 重构显式 /skill 激活处调用 activateSkill**

```go
// engine/loop.go — L309-332（/skill activate 分支），替换为：
    e.activateSkill(s, "explicit /"+s.Name+" command")
    e.state.ActiveSkillName = s.Name
    e.state.ActiveSkillContent = s.Content
    // → 改为：
    e.activateSkill(s, "explicit /"+s.Name+" command")
```

注意：原来的 L309-310（`e.activatedSkills[s.Name] = true` 和 `e.lastActivatedSkill = s.Name`）现在由 `activateSkill` 内部处理，需要删除。L312-313 的 `e.state.ActiveSkillName`/`ActiveSkillContent` 设置也由 `activateSkill` 处理。保留 L315-332 的 `e.context.SetActiveSkill` 调用逻辑… 等等，`activateSkill` 已经包含了 `e.context.SetActiveSkill`，所以 L316 也要删。

实际替换范围 L309-332（整个激活块）：

```go
        case "activate":
            s := e.skills.Get(sc.name)
            // ... case-insensitive fallback ...
            if s == nil {
                // ... error handling unchanged ...
            }
            // OLD: explicit activation block (L309-332)
            // NEW:
            e.activateSkill(s, "explicit /"+s.Name+" command")

            taskText := extractTaskTextAfterSkillCmd(userMsg, sc.name)
            // ... rest unchanged ...
```

- [ ] **Step 5: 重构自动匹配处调用 SkillMatcher**

将 L348-405 的整个块替换为：

```go
    // Skill matching: keyword-based auto-activation + semantic fallback.
    if e.state.ActiveSkillName == "" && e.skillMatcher != nil {
        ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
        matched := e.skillMatcher.Match(ctx, userMsg, e.skills.All())
        cancel()
        if matched != nil {
            e.activateSkill(matched, "keyword match")
        } else {
            // Show keyword-based suggestions even if no auto-activation.
            matches := e.skills.MatchTopSkillsWithScores(3, userMsg)
            if len(matches) > 0 {
                e.showSkillSuggestions(matches)
            }
        }
    }
```

同时提取 `showSkillSuggestions` 方法（L388-405 逻辑）：

```go
func (e *Engine) showSkillSuggestions(matches []skill.SkillMatch) {
    var sb strings.Builder
    if e.isChinese {
        sb.WriteString("## 建议的技能\n以下技能可能适合当前任务：\n\n")
    } else {
        sb.WriteString("## Suggested Skills\nSkills that may be relevant:\n\n")
    }
    for _, m := range matches {
        sb.WriteString(fmt.Sprintf("- **%s**: %s\n", m.Skill.Name, m.Skill.Description))
    }
    if e.isChinese {
        sb.WriteString("\n使用 `/<skillname>` 激活，或让模型用 `activate_skill` tool 建议。")
    } else {
        sb.WriteString("\nUse `/<skillname>` to activate, or ask the model to suggest via `activate_skill` tool.")
    }
    e.pendingPinnedMessages = append(e.pendingPinnedMessages, sb.String())
}
```

- [ ] **Step 6: 运行现有测试 — 预期 PASS（行为不变）**

```bash
cd /Users/admin/gitspace/deepact && go test ./engine/... ./skill/... -v -count=1
```

- [ ] **Step 7: Commit**

```bash
git add engine/loop.go
git commit -m "refactor(engine): extract SkillMatcher interface, wire KeywordMatcher into Engine"
```

---

### Task 3: 创建 SemanticMatcher（LLM 语义匹配）

**Files:**
- Create: `skill/matcher_llm.go`
- Modify: `skill/matcher_test.go`（追加测试）

**Interfaces:**
- Produces: `SemanticMatcher` 结构体 + `NewSemanticMatcher(fn MatchFunc, modelName string)`
- Produces: `MatchFunc` 类型：`func(ctx context.Context, systemMsg, userMsg string) (string, error)`

- [ ] **Step 1: 写测试 — TestSemanticMatcher 系列**

```go
// 追加到 skill/matcher_test.go

func TestSemanticMatcher_Match(t *testing.T) {
    r := NewRegistry()
    r.Register(&Skill{
        Name:        "systematic-debugging",
        Description: "Use when encountering any bug, test failure, or unexpected behavior",
    })

    // Mock LLM returns valid JSON
    mockFn := func(ctx context.Context, systemMsg, userMsg string) (string, error) {
        return `{"skill": "systematic-debugging"}`, nil
    }
    m := NewSemanticMatcher(mockFn, "test-model")
    got := m.Match(context.Background(), "渲染问题需要分析", r.All())
    if got == nil || got.Name != "systematic-debugging" {
        t.Fatalf("expected systematic-debugging, got %v", got)
    }
}

func TestSemanticMatcher_NoMatch(t *testing.T) {
    r := NewRegistry()
    r.Register(&Skill{Name: "debug", Description: "debug stuff"})

    mockFn := func(ctx context.Context, systemMsg, userMsg string) (string, error) {
        return `{"skill": null}`, nil
    }
    m := NewSemanticMatcher(mockFn, "test-model")
    got := m.Match(context.Background(), "hello world", r.All())
    if got != nil {
        t.Fatalf("expected nil, got %v", got.Name)
    }
}

func TestSemanticMatcher_Timeout(t *testing.T) {
    r := NewRegistry()
    r.Register(&Skill{Name: "debug", Description: "debug"})

    mockFn := func(ctx context.Context, systemMsg, userMsg string) (string, error) {
        // Simulate timeout via context cancellation
        <-ctx.Done()
        return "", ctx.Err()
    }
    m := NewSemanticMatcher(mockFn, "test-model")
    ctx, cancel := context.WithTimeout(context.Background(), 1*time.Millisecond)
    defer cancel()
    got := m.Match(ctx, "test", r.All())
    if got != nil {
        t.Fatalf("expected nil on timeout, got %v", got)
    }
}

func TestSemanticMatcher_BadJSON(t *testing.T) {
    r := NewRegistry()
    r.Register(&Skill{Name: "debug", Description: "debug"})

    mockFn := func(ctx context.Context, systemMsg, userMsg string) (string, error) {
        return `not json at all`, nil
    }
    m := NewSemanticMatcher(mockFn, "test-model")
    got := m.Match(context.Background(), "test", r.All())
    if got != nil {
        t.Fatalf("expected nil on bad JSON, got %v", got.Name)
    }
}

func TestSemanticMatcher_UnknownSkill(t *testing.T) {
    r := NewRegistry()
    r.Register(&Skill{Name: "debug", Description: "debug"})

    mockFn := func(ctx context.Context, systemMsg, userMsg string) (string, error) {
        return `{"skill": "nonexistent-skill"}`, nil
    }
    m := NewSemanticMatcher(mockFn, "test-model")
    got := m.Match(context.Background(), "test", r.All())
    if got != nil {
        t.Fatalf("expected nil for unknown skill, got %v", got.Name)
    }
}

func TestSemanticMatcher_EmptyModelName(t *testing.T) {
    r := NewRegistry()
    m := NewSemanticMatcher(nil, "") // empty model name disables
    got := m.Match(context.Background(), "test", r.All())
    if got != nil {
        t.Fatalf("expected nil when disabled, got %v", got.Name)
    }
}
```

注意：需要在测试文件顶部加 `"time"` import。

- [ ] **Step 2: 运行测试 — 预期 FAIL**

```bash
cd /Users/admin/gitspace/deepact && go test ./skill/ -run TestSemanticMatcher -v
```

- [ ] **Step 3: 实现 SemanticMatcher**

```go
// skill/matcher_llm.go
package skill

import (
    "context"
    "encoding/json"
    "fmt"
    "strings"
    "time"
)

// MatchFunc is a callback that sends system+user messages to an LLM
// and returns the raw response text. Implementations should handle
// model selection, API calls, and error handling.
type MatchFunc func(ctx context.Context, systemMsg, userMsg string) (string, error)

// SemanticMatcher uses an LLM to semantically match a user message
// to the most relevant skill.
type SemanticMatcher struct {
    match     MatchFunc
    modelName string
    timeout   time.Duration
}

// NewSemanticMatcher creates a semantic matcher. If modelName is empty,
// the matcher is disabled (Match always returns nil). match is the LLM
// callback — typically wraps engine.ModelClient.Complete.
func NewSemanticMatcher(match MatchFunc, modelName string) *SemanticMatcher {
    return &SemanticMatcher{
        match:     match,
        modelName: modelName,
        timeout:   2 * time.Second,
    }
}

type matchResult struct {
    Skill *string `json:"skill"`
}

const semanticSystemPrompt = `You are a skill matching engine. Given a user's message and a list of available skills, select the ONE skill most relevant to the user's intent. Return ONLY JSON.

Rules:
- If a skill clearly matches the user's intent, return its name.
- If NO skill is relevant, return null.
- Consider both the skill name and description.
- The user message may be in Chinese or English.`

// Match runs semantic matching via LLM. Returns nil on any failure (timeout,
// bad response, unknown skill) — the caller should fall back gracefully.
func (m *SemanticMatcher) Match(ctx context.Context, userMsg string, skills []*Skill) *Skill {
    if m.modelName == "" || m.match == nil || len(skills) == 0 {
        return nil
    }

    // Build skills list for the prompt
    var skillList strings.Builder
    for _, s := range skills {
        skillList.WriteString(fmt.Sprintf("- %s: %s\n", s.Name, s.Description))
    }

    userPrompt := fmt.Sprintf(
        "User message: %s\n\nAvailable skills:\n%s\nReturn: {\"skill\": \"<name>\"} or {\"skill\": null}",
        userMsg, skillList.String(),
    )

    // Apply timeout
    matchCtx, cancel := context.WithTimeout(ctx, m.timeout)
    defer cancel()

    raw, err := m.match(matchCtx, semanticSystemPrompt, userPrompt)
    if err != nil {
        return nil
    }

    var result matchResult
    if err := json.Unmarshal([]byte(raw), &result); err != nil {
        return nil
    }
    if result.Skill == nil {
        return nil
    }

    // Look up skill by name from the provided list
    name := *result.Skill
    for _, s := range skills {
        if strings.EqualFold(s.Name, name) {
            return s
        }
    }
    return nil
}
```

- [ ] **Step 4: 运行测试 — 预期 PASS**

```bash
cd /Users/admin/gitspace/deepact && go test ./skill/ -run TestSemanticMatcher -v
```

- [ ] **Step 5: Commit**

```bash
git add skill/matcher_llm.go skill/matcher_test.go
git commit -m "feat(skill): add SemanticMatcher with LLM-based skill matching"
```

---

### Task 4: 创建 FallbackMatcher + cmd/run.go 接线

**Files:**
- Modify: `skill/matcher.go`（追加 FallbackMatcher）
- Modify: `cmd/run.go`（构造 matcher 并注入 EngineDeps）
- Modify: `skill/matcher_test.go`（追加集成测试）

**Interfaces:**
- Consumes: `KeywordMatcher` (Task 1), `SemanticMatcher` (Task 3)
- Produces: `FallbackMatcher` + `NewFallbackMatcher(kw, sem)`
- Consumes (cmd/run.go): `engine.ModelClient` → 适配为 `skill.MatchFunc`

- [ ] **Step 1: 写 FallbackMatcher 测试**

```go
// 追加到 skill/matcher_test.go

func TestFallbackMatcher_KeywordFirst(t *testing.T) {
    r := NewRegistry()
    r.Register(&Skill{
        Name:        "systematic-debugging",
        Keywords:    []string{"debug", "bug"},
        AutoActivateThreshold: intPtr(1),
    })

    kw := NewKeywordMatcher(r)
    // semantic should never be called — verify by making it return a different skill
    semCalled := false
    sem := NewSemanticMatcher(func(ctx context.Context, sys, usr string) (string, error) {
        semCalled = true
        return `{"skill": "brainstorming"}`, nil
    }, "test-model")

    fb := NewFallbackMatcher(kw, sem)
    got := fb.Match(context.Background(), "有个bug", r.All())
    if got == nil || got.Name != "systematic-debugging" {
        t.Fatalf("expected systematic-debugging, got %v", got)
    }
    if semCalled {
        t.Fatal("semantic matcher should not have been called when keyword matches")
    }
}

func TestFallbackMatcher_SemanticFallback(t *testing.T) {
    r := NewRegistry()
    r.Register(&Skill{
        Name:        "systematic-debugging",
        Keywords:    []string{"debug"},
        AutoActivateThreshold: intPtr(1),
    })
    r.Register(&Skill{
        Name:        "brainstorming",
        Keywords:    []string{"design"},
        AutoActivateThreshold: intPtr(1),
    })

    kw := NewKeywordMatcher(r)
    sem := NewSemanticMatcher(func(ctx context.Context, sys, usr string) (string, error) {
        return `{"skill": "systematic-debugging"}`, nil
    }, "test-model")

    fb := NewFallbackMatcher(kw, sem)
    got := fb.Match(context.Background(), "渲染问题需要分析", r.All())
    if got == nil || got.Name != "systematic-debugging" {
        t.Fatalf("expected systematic-debugging via semantic fallback, got %v", got)
    }
}

func TestFallbackMatcher_SemanticDisabled(t *testing.T) {
    r := NewRegistry()
    r.Register(&Skill{Name: "debug", Keywords: []string{"debug"}, AutoActivateThreshold: intPtr(1)})

    kw := NewKeywordMatcher(r)
    sem := NewSemanticMatcher(nil, "") // disabled

    fb := NewFallbackMatcher(kw, sem)
    got := fb.Match(context.Background(), "渲染问题需要分析", r.All())
    if got != nil {
        t.Fatalf("expected nil when keyword misses and semantic disabled, got %v", got.Name)
    }
}
```

- [ ] **Step 2: 运行测试 — 预期 FAIL**

```bash
cd /Users/admin/gitspace/deepact && go test ./skill/ -run TestFallbackMatcher -v
```

- [ ] **Step 3: 实现 FallbackMatcher**

```go
// 追加到 skill/matcher.go

// FallbackMatcher tries keyword matching first, then falls back to semantic matching.
// This ensures zero-cost keyword matches are prioritized, and LLM calls are only
// made when necessary.
type FallbackMatcher struct {
    keyword  *KeywordMatcher
    semantic *SemanticMatcher
}

func NewFallbackMatcher(keyword *KeywordMatcher, semantic *SemanticMatcher) *FallbackMatcher {
    return &FallbackMatcher{keyword: keyword, semantic: semantic}
}

func (m *FallbackMatcher) Match(ctx context.Context, userMsg string, skills []*Skill) *Skill {
    // Step 1: try keyword matching (fast, free)
    if s := m.keyword.Match(ctx, userMsg, skills); s != nil {
        return s
    }
    // Step 2: fall back to semantic matching (slow, cheap)
    if m.semantic != nil {
        return m.semantic.Match(ctx, userMsg, skills)
    }
    return nil
}
```

- [ ] **Step 4: 运行测试 — 预期 PASS**

```bash
cd /Users/admin/gitspace/deepact && go test ./skill/ -run TestFallbackMatcher -v
```

- [ ] **Step 5: 运行全部 skill 测试 — 预期全绿**

```bash
cd /Users/admin/gitspace/deepact && go test ./skill/ -v -count=1
```

- [ ] **Step 6: 修改 cmd/run.go — 构造 matcher 并注入 EngineDeps**

在 `buildEngineDeps()` 中，在 `deps := engine.EngineDeps{...}` 之前插入：

```go
    // Build skill matcher: keyword-first with LLM semantic fallback.
    // The semantic matcher wraps the model client for flash-model calls.
    kwMatcher := skill.NewKeywordMatcher(skillReg)
    var semMatcher *skill.SemanticMatcher
    if config.FlashModelName != "" && client != nil {
        // Adapt engine.ModelClient.Complete → skill.MatchFunc
        matchFn := func(ctx context.Context, systemMsg, userMsg string) (string, error) {
            req := engine.ModelRequest{
                Model: config.FlashModelName,
                Messages: []engine.ModelMessage{
                    {Role: "system", Content: systemMsg},
                    {Role: "user", Content: userMsg},
                },
                Temperature: 0,
                MaxTokens:   64,
                JsonMode:    false,
            }
            resp, err := client.Complete(ctx, req)
            if err != nil {
                return "", err
            }
            return resp.Message.Content, nil
        }
        semMatcher = skill.NewSemanticMatcher(matchFn, config.FlashModelName)
    }
    skillMatcher := skill.NewFallbackMatcher(kwMatcher, semMatcher)
```

然后在 `deps := engine.EngineDeps{...}` 中加入：

```go
    deps := engine.EngineDeps{
        // ... existing fields ...
        SkillMatcher: skillMatcher,
        // ...
    }
```

- [ ] **Step 7: 编译验证**

```bash
cd /Users/admin/gitspace/deepact && go build ./...
```

- [ ] **Step 8: 运行全量测试**

```bash
cd /Users/admin/gitspace/deepact && go test ./... -count=1 2>&1 | tail -20
```

- [ ] **Step 9: Commit**

```bash
git add skill/matcher.go skill/matcher_test.go cmd/run.go
git commit -m "feat(skill): add FallbackMatcher and wire into engine via cmd/run.go"
```
