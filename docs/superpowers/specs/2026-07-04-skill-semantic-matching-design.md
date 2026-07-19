# Skill 语义匹配自动触发 — 设计文档

**日期**: 2026-07-04
**状态**: 设计中
**参考**: claude-code skill discovery 机制

---

## 1. 动机

### 现状问题

deepact 的 skill 自动触发依赖纯关键词子串匹配（`strings.Contains`），有两个问题：

1. **关键词覆盖不全**：用户说"渲染问题需要分析"，不含 "debug"、"bug"、"调试" 等关键词 → systematic-debugging 命中 0 个关键词 → 不激活
2. **阈值门控过严**：即使命中关键词，还需要 `auto_activate_threshold` 被显式设置且命中数达标才自动激活

### 目标

用 LLM 语义匹配增强现有关键词匹配，当关键词 0 命中时，通过 Flash 模型理解用户意图，自动激活相关 skill。

---

## 2. 设计概览

### 核心思路

```
用户输入 → 关键词匹配（快、免费）
              │
              ├─ 命中 → 按现有阈值逻辑处理
              │
              └─ 0 命中 → Flash 模型语义匹配（慢、极便宜）
                            │
                            ├─ 匹配到 → 自动激活
                            └─ 失败/超时 → 静默跳过
```

### 关键约束

- 语义匹配是**增强，不是依赖**。任何失败都不阻塞主流程。
- 一次只激活一个 skill。
- 已有 skill 激活时不重复匹配。
- 保留现有关键词匹配逻辑，不改动其行为。

---

## 3. 架构

### 新增文件

```
skill/
├── skill.go           ← 已有，不变
├── loader.go          ← 已有，不变
├── matcher.go         ← 新增：SkillMatcher 接口 + KeywordMatcher + FallbackMatcher
├── matcher_llm.go     ← 新增：SemanticMatcher（LLM 语义匹配）
└── matcher_test.go    ← 新增：测试
```

### SkillMatcher 接口

```go
// SkillMatcher matches a user message to the most relevant skill.
// Returns nil if no skill is relevant.
type SkillMatcher interface {
    Match(ctx context.Context, userMsg string, skills []*Skill) *Skill
}
```

### 实现类

| 类 | 职责 |
|------|------|
| `KeywordMatcher` | 现有逻辑：`strings.Contains` 子串匹配，返回最高分 skill |
| `SemanticMatcher` | 新逻辑：调 Flash 模型做语义匹配 |
| `FallbackMatcher` | 组合：先 Keyword，0 命中时 fallback 到 Semantic |

---

## 4. SemanticMatcher 详细设计

### 4.1 Prompt 设计

**System prompt**（固定，构造时缓存）：

```
You are a skill matching engine. Given a user's message and a list of available skills,
select the ONE skill most relevant to the user's intent. Return ONLY JSON.

Rules:
- If a skill clearly matches the user's intent, return its name.
- If NO skill is relevant, return null.
- Consider both the skill name and description.
- The user message may be in Chinese or English.
```

**User prompt**（每轮构造）：

```
User message: {userMsg}

Available skills:
- {name}: {description}
- ...

Return: {"skill": "<name>"} or {"skill": null}
```

### 4.2 调用参数

```go
type SemanticMatcher struct {
    client       ModelClient    // engine.ModelClient 接口（复用）
    modelName    string         // config.FlashModelName，默认 "deepseek-v4-flash"
    timeout      time.Duration  // 2s
    systemPrompt string         // 构造一次，缓存
}
```

- 调用方式：`Complete()`（非流式，只需短 JSON 回复）
- 超时：2s context deadline
- skills 数量：传全部（~15 个，约 500 input tokens，费用 ~$0.000001）

### 4.3 返回解析

```go
type matchResult struct {
    Skill *string `json:"skill"` // null 或 skill name
}
```

- `{"skill": "systematic-debugging"}` → 查 registry 返回 `*Skill`
- `{"skill": null}` → 返回 nil
- 非 JSON / 不存在的 name → 返回 nil，打 debug log

---

## 5. FallbackMatcher 组合逻辑

```go
type FallbackMatcher struct {
    keyword  *KeywordMatcher
    semantic *SemanticMatcher
}

func (m *FallbackMatcher) Match(ctx context.Context, userMsg string, skills []*Skill) *Skill {
    // Step 1: 关键词匹配（快、免费）
    if s := m.keyword.Match(userMsg, skills); s != nil {
        return s
    }
    // Step 2: 语义匹配（慢、极便宜）
    return m.semantic.Match(ctx, userMsg, skills)
}
```

---

## 6. 引擎集成

### 6.1 EngineDeps 改动

```go
// engine/interfaces.go 或 engine/types.go
type EngineDeps struct {
    // ... 现有字段 ...
    SkillMatcher SkillMatcher  // 新增
}
```

### 6.2 loop.go 改动

将 L348-405 的内联匹配逻辑替换为：

```go
// 旧代码块替换为调用 matcher
if e.state.ActiveSkillName == "" {
    matched := e.skillMatcher.Match(ctx, userMsg, e.skills.All())
    if matched != nil {
        e.activateSkill(matched, "auto, keyword/semantic match")
    } else {
        // 关键词低分匹配时仍展示建议列表（复用现有逻辑）
        e.showKeywordSuggestions(userMsg)
    }
}
```

将 L364-385 的激活逻辑提取为独立方法 `activateSkill(skill, reason)`，关键词匹配和语义匹配共用。

### 6.3 cmd/run.go 改动

```go
// buildEngineDeps() 中
keywordMatcher := skill.NewKeywordMatcher(skillReg)
semanticMatcher := skill.NewSemanticMatcher(client, config.FlashModelName)
skillMatcher := skill.NewFallbackMatcher(keywordMatcher, semanticMatcher)

deps := engine.EngineDeps{
    // ... 现有字段 ...
    SkillMatcher: skillMatcher,
}
```

---

## 7. 降级矩阵

| 场景 | 行为 |
|------|------|
| Flash 模型名未配置（空字符串） | SemanticMatcher 不初始化，FallbackMatcher 只用 Keyword |
| API 调用超时（>2s） | ctx 取消 → 返回 nil，debug log |
| API 返回非 JSON | 解析失败 → 返回 nil，debug log |
| 返回的 skill name 不在 registry | 查 registry 失败 → 返回 nil，debug log |
| 关键词已命中 | 不走语义匹配 |
| 已有 skill 激活中 | 跳过所有匹配 |

---

## 8. 测试策略

```
skill/matcher_test.go
├── TestKeywordMatcher_Match             ← 关键词命中
├── TestKeywordMatcher_NoMatch           ← 0 命中
├── TestSemanticMatcher_Match            ← mock LLM 返回正确 JSON
├── TestSemanticMatcher_NoMatch          ← mock LLM 返回 null
├── TestSemanticMatcher_Timeout          ← mock 超时
├── TestSemanticMatcher_BadJSON          ← mock 返回垃圾
├── TestSemanticMatcher_UnknownSkill     ← mock 返回不存在的 name
├── TestFallbackMatcher_KeywordFirst     ← 关键词命中，不调 LLM
├── TestFallbackMatcher_SemanticFallback ← 关键词 0 命中，调 LLM
└── TestFallbackMatcher_SemanticDisabled ← Flash 模型未配置，只用关键词
```

SemanticMatcher 的测试通过 mock `ModelClient` 接口实现，不依赖真实 API。

---

## 9. 实现计划

| 步骤 | 内容 | 文件 | 验证 |
|------|------|------|------|
| Step 1 | 提取 `SkillMatcher` 接口 + `KeywordMatcher` | `skill/matcher.go` | 现有测试全绿 |
| Step 1b | loop.go 改用 matcher 调用 | `engine/loop.go` | 行为不变 |
| Step 2 | 新增 `SemanticMatcher` | `skill/matcher_llm.go` | mock 测试覆盖 5 个场景 |
| Step 3 | 新增 `FallbackMatcher` + 集成 | `skill/matcher.go`, `engine/loop.go`, `cmd/run.go` | 端到端验证 |
| Step 3b | 用原始输入 "渲染问题需要分析" 验证 | 手动测试 | 匹配到 systematic-debugging |

---

## 10. 不做的

- 条件技能（paths frontmatter）：本次不实现，边际收益小
- 多 skill 同时激活：保持一次一个
- 修改现有 TOML skill 文件：不改动
