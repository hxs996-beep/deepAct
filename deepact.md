# AGENTS.md - DeepAct Development Guidelines

> This file defines the development conventions, coding standards, and architectural
> constraints for all contributors (human and AI) working on this project.

---

## Project Identity

- **Name**: deepact (CLI binary: `deepact`)
- **Language**: Go 1.24+
- **Architecture**: Staged guarded agent loop with dual-model routing
- **Target**: Cross-platform CLI (macOS, Windows, Linux)

---

## Code Conventions

### Go Style

- Follow standard `gofmt` formatting (enforced by CI)
- Use `golangci-lint` with config in `.golangci.yml`
- Error handling: always wrap with context (`fmt.Errorf("doing X: %w", err)`)
- No `panic()` in library code; only in `main()` for unrecoverable init failures
- Interfaces: define at the consumer, not the provider
- Package naming: short, lowercase, singular (e.g., `engine`, `router`, `policy`)

### File Organization

- One primary type per file (e.g., `scorer.go` contains `Scorer`)
- Test files: `*_test.go` in the same package

### Naming

- Exported types: `PascalCase`
- Unexported: `camelCase`
- Interfaces: verb-noun or "-er" suffix (`Router`, `ContextBuilder`, `AmbiguityDetector`)
- Config structs: `XxxConfig` suffix
- Options: functional options pattern for complex constructors

---

## Architecture Rules (MANDATORY)

### Layer Dependency Rules

```
cmd/ → engine/, router/, policy/, context/
                 ↓
              tools/, llm/
                 ↓
              session/, artifact/
                 ↓
              config/ (shared, no upward deps)
```

**NEVER VIOLATE:**
- `engine/` never imports `ui/` or `cmd/`
- `tools/` never imports `engine/` (communicate via interfaces)
- `llm/` is standalone (no project-specific logic)
- `policy/` reads state but never mutates it directly
- `ui/` only consumes events from engine (observer pattern)

### Interface Boundaries

Every cross-layer call MUST go through a defined interface:
- Engine ↔ Tools: via `Tool` interface
- Engine ↔ LLM: via `ModelClient` interface
- Engine ↔ Policy: via `PolicyChecker` interface
- UI ↔ Engine: via event channel (no direct method calls)

### State Management

- **TaskState** is the single source of truth for the current task
- TaskState is immutable within a turn; only the Compactor rewrites it
- Tool results stored in Artifact Store, referenced by SHA256
- Session events are append-only (JSONL); never modify past events

---

## Design Principles

### 1. Guard Before Act

Every destructive action (file edit, shell command) must pass through:
1. Ambiguity Gate (is scope clear?)
2. Scope Guard (is this within confirmed scope?)
3. Design Guard (is the approach robust?)
4. Loop Guard (are we not in a loop?)

### 2. Structured Over Verbose

- Prefer structured JSON (TaskState) over natural language descriptions
- Response format: `{summary, changes, next_step, questions}`
- Never repeat information already in TaskState.decisions

### 3. Bookend Context Layout

- Most important info at TOP and BOTTOM of prompts
- Middle section is bounded and minimal
- Never dump entire file contents; use targeted snippets

### 4. Verify Before Trust

- Every API/symbol reference must be grounded (LSP or grep verification)
- Every plan must pass design anti-pattern checks
- Every edit must be followed by verification (lint/test/compile)

### 5. Fail Loud, Recover Gracefully

- Never silently swallow errors
- On failure: log, increment failure counter, potentially escalate model
- After 3 consecutive failures: stop, diagnose, ask user

---

## DeepSeek-Specific Rules

### Model Interaction

1. **reasoning_content**: Treat as opaque. Store verbatim. Echo exactly in next request.
2. **Tool results**: Always include full ToolResultEnvelope with matching tool_call_id.
3. **System prompt**: Keep stable across turns (enables cache hits → 98% cost reduction).
4. **Temperature**: 0.0 for code generation, 0.6 for planning, 1.0 for brainstorming.

### Known Failure Modes (Code Must Prevent)

| Failure Mode | Guard | Location |
|---|---|---|
| Over-implementation | Ambiguity Gate | `policy/ambiguity.go` |
| Lazy/stupid design | Design Guard | `policy/design_guard.go` |
| Tool call loops | Loop Guard + Tool Dedupe | `engine/guards.go` |
| Verbose repetition | TaskState dedup + forced rebase | `context/compactor.go` |
| Hallucinated APIs | Grounding requirement | `policy/design_guard.go` |
| Context degradation | Bookend layout + short prompts | `context/builder.go` |

### Prompt Engineering Rules

- Always include stop conditions: "If missing info, ask. Do not implement."
- Always include anti-pattern examples in system prompt
- Force self-challenge questions after plan generation
- Include `TaskState.decisions` with "DO NOT restate these" instruction

---

## Testing Requirements

### Unit Tests

- Every exported function must have tests
- Table-driven tests preferred
- Mock external dependencies (LLM, file system, LSP)
- Coverage target: >50% for core packages (engine/, router/, policy/)

### Test Patterns

```go
func TestAmbiguityGate_DetectsVagueRequest(t *testing.T) {
    tests := []struct {
        name    string
        input   string
        state   TaskState
        wantAmb bool
    }{
        {
            name:    "vague improve request",
            input:   "improve the config handling",
            state:   TaskState{},
            wantAmb: true,
        },
        {
            name:    "specific fix with file",
            input:   "fix the nil pointer in config/loader.go line 45",
            state:   TaskState{},
            wantAmb: false,
        },
    }
    // ...
}
```

---

## Git Conventions

### Commit Messages

```
<type>(<scope>): <description>

Types: feat, fix, refactor, docs, test, chore, perf
Scope: engine, router, policy, tools, llm, context, ui, session, config
```

Examples:
```
feat(engine): add staged loop with ambiguity gate
fix(llm): preserve reasoning_content echo in multi-turn
refactor(tools): extract ToolResultEnvelope to shared type
test(policy): add design guard anti-pattern detection tests
```

### Push Rules (MANDATORY)

- **git push 必须先询问用户** — 获得用户确认后才能执行 push
- **代码编译成功即止** — 不需要额外验证（lint、test 等），除非用户明确要求

### Branch Naming

```
feat/<short-description>
fix/<issue-number>-<short-description>
refactor/<module>-<what>
```

---

## Security Rules

### Sensitive Data

- NEVER log API keys, tokens, or credentials
- NEVER store secrets in artifacts (redact before storage)
- NEVER include `.env` or credential files in session data
- Tool outputs: scan for patterns (API keys, passwords) before storing

### Shell Execution

- Maintain allowlist of safe commands (configurable)
- Dangerous commands require explicit user confirmation
- Default deny: `rm -rf`, `git push --force`, `DROP TABLE`, `chmod 777`
- All shell execution logged in session events

### Network

- Only connect to configured API endpoints
- No arbitrary HTTP requests without user approval
- Proxy support for corporate environments

---

## Performance Guidelines

### Context Budget

- Default limit: 1M tokens (configurable via `max_budget_tokens` in config.toml)
- Unified compression: single 80% threshold triggers full compact (Flash archive)

### Streaming

- Always stream API responses (never buffer full response)
- UI updates on each streaming chunk
- Tool outputs: digest immediately, store full output async

### Startup Time

- Target: <500ms to first interactive prompt
- Lazy-load: LSP connections, MCP discovery, heavy configs
- Pre-warm: API client connection, session load

---

## Code Reading Protocol (MANDATORY)

**`read` 工具对同一文件有 4 次上限**（LoopGuard: `guards.go:68`，按路径去重计数）。超过即阻塞，每次读取必须精打细算。

### 优先级顺序（严格从左到右）

```
lsp workspaceSymbol → lsp hover/goToDefinition → read symbol=X → read offset/limit
```

| 你要做什么 | 用这个 | 为什么 |
|-----------|--------|--------|
| 找函数/类型定义在哪个文件 | `lsp workspaceSymbol <name>` | 精确定位，不读文件 |
| 查变量/函数返回值的类型 | `lsp hover file=<path> line=<n> char=<n>` | 返回类型签名，不读上下文 |
| 跳到定义处看实现 | `lsp goToDefinition file=<path> line=<n> char=<n>` | 直接跳到定义位置 |
| 读某个函数/类型的完整定义 | `read symbol=<name>` | AST 提取，只返回到该声明块 |
| 查所有调用者 | `lsp findReferences file=<path> line=<n> char=<n>` | 不读文件就找到所有引用 |
| 查看文件结构和符号列表 | `lsp documentSymbol file=<path>` | 一页看到所有导出符号 |

### 红线规则

- **同一文件 `read` 不超过 2 次**。如果需要更多，说明前面没用 LSP。
- **禁止 `read offset/limit` 分段读同一文件**。这是最常见的 LoopGuard 触发原因。改为先 `lsp workspaceSymbol` 找到目标符号，再用一次 `read symbol=X`。
- **先 LSP 后 read**。在任何 `read` 调用之前，先问自己："我用 LSP 能找吗？"
- **Batch 原则**。如果确实需要看多个不连续区域，先全部用 `lsp workspaceSymbol` 定位，再一次性批量 `read`。

---

## Development Workflow

### Adding a New Tool

1. Create file in `tools/builtin/<name>.go`
2. Implement `Tool` interface (Spec + Run)
3. Register via `registry.Register()` in `cmd/run.go`
4. Add tests in `tools/builtin/<name>_test.go`
5. Update tool schema in system prompt template

### Adding a New Guard

1. Define detection logic in `policy/`
2. Integrate into engine loop at appropriate stage
3. Add configuration toggle in `config/schema.go`
4. Write tests with positive/negative cases
5. Document trigger conditions

### Adding a New Model Feature

1. Implement in `llm/` package
2. Expose via `ModelClient` interface
3. Update router if routing logic changes
4. Test with recorded API responses

---

## Review Checklist

Before merging any PR:

- [ ] Follows layer dependency rules
- [ ] No direct imports across layer boundaries (only through interfaces)
- [ ] Error handling: all errors wrapped with context
- [ ] Tests added for new functionality
- [ ] No hardcoded values that should be configurable
- [ ] No secrets or sensitive data in code/tests
- [ ] Cross-platform: uses `filepath` not `path`, no hardcoded separators
- [ ] Documentation updated if interface changed
- [ ] `golangci-lint` passes
