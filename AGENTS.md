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
- Integration tests: `*_integration_test.go` with build tags
- Mocks: in `internal/mocks/` or via `go:generate`

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
cmd/ → app/ → engine/, router/, policy/, context/, memory/
                 ↓
              tools/, llm/, retrieval/
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
- App ↔ Engine: via `Engine` interface
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
- Coverage target: >80% for core packages (engine/, router/, policy/)

### Integration Tests

- Test full agent loop with recorded API responses
- Test session persistence (save/load/fork/rewind)
- Test cross-platform paths (use `filepath` everywhere)
- Test tool execution with sandboxed environment

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

- Default limit: 128K tokens (not full 1M)
- Monitor token usage per turn
- Compact when approaching 80% of budget
- Pro escalation can expand to 256K when justified

### Streaming

- Always stream API responses (never buffer full response)
- UI updates on each streaming chunk
- Tool outputs: digest immediately, store full output async

### Startup Time

- Target: <500ms to first interactive prompt
- Lazy-load: LSP connections, MCP discovery, heavy configs
- Pre-warm: API client connection, session load

---

## Development Workflow

### Adding a New Tool

1. Create file in `tools/builtin/<name>.go`
2. Implement `Tool` interface (Spec + Run)
3. Register in `tools/registry.go`
4. Add tests in `tools/builtin/<name>_test.go`
5. Update tool schema in system prompt template
6. Document in DESIGN.md Appendix

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
5. Document API quirks in DESIGN.md Appendix B

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
- [ ] Design aligns with DESIGN.md architecture
