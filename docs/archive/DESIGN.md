# DeepAct - Architecture Design Document

> CLI-based code agent built for DeepSeek V4 (Flash + Pro dual-model routing)
> Target: Cross-platform (macOS + Windows), single binary distribution

---

## 1. Design Philosophy

**Core Principle: "Flash-first, Pro-on-risk"**

Routing is not "which model is smarter" — it's "how expensive is a mistake right now."
DeepSeek V4 is powerful but needs discipline. The architecture compensates for its weaknesses
(over-implementation, lazy design, repetition, tool loops) through structural guards.

**Key Differences from Claude Code / Codex CLI:**

| Aspect | Claude Code | Codex CLI | This Project |
|--------|-------------|-----------|--------------|
| Agent Loop | while(tool_call) simple loop | Queue-based state machine | **Staged guarded loop (5 stages)** |
| Model | Single model (Claude) | Single model (GPT) | **Dual model routing (Flash/Pro)** |
| Weakness Mitigation | Minimal (model is disciplined) | Sandboxing focus | **Ambiguity Gate + Design Guard + Loop Guard** |
| Context | 5-layer compaction | /responses/compact API | **Bookend layout + TaskState + CAS** |
| Philosophy | Trust the model | Isolate the model | **Guide the model** |

---

## 2. System Architecture

```
┌─────────────────────────────────────────────────────────────────────┐
│                         USER INTERFACE                               │
│   ┌──────────┐    ┌──────────┐    ┌─────────────┐                  │
│   │  TUI     │    │  Exec    │    │  JSON-RPC   │                  │
│   │(Interactive)│  │(CI/Headless)│ │  (IDE)      │                  │
│   └────┬─────┘    └────┬─────┘    └──────┬──────┘                  │
└────────┼───────────────┼─────────────────┼──────────────────────────┘
         └───────────────┼─────────────────┘
                         ▼
┌─────────────────────────────────────────────────────────────────────┐
│                      CORE ENGINE (State Machine)                     │
│                                                                     │
│  ┌──────────┐  ┌──────────┐  ┌──────────┐  ┌───────┐  ┌────────┐ │
│  │Ambiguity │→ │  Plan    │→ │ Design   │→ │  Act  │→ │Verify  │ │
│  │Gate      │  │(Lightweight)│ │ Guard   │  │(Tools)│  │+Compact│ │
│  └──────────┘  └──────────┘  └──────────┘  └───────┘  └────────┘ │
│                                                                     │
│  Guards: Loop Guard | Tool Dedupe | Scope Guard | Design Sanity     │
└───────────────────────────────────────┬─────────────────────────────┘
                                        │
┌───────────────────────────────────────┼─────────────────────────────┐
│                    MODEL ROUTER       │                              │
│  ┌─────────────────────────────────────────────┐                    │
│  │ RiskScore(0~1) → ModelChoice                │                    │
│  │  < 0.55 → V4 Flash (non-thinking / high)   │                    │
│  │  ≥ 0.55 → V4 Pro  (high / max)             │                    │
│  └─────────────────────────────────────────────┘                    │
└──────────────────────────────┬─────────────────────────────┘
                                        │
┌───────────────────────────────────────┼─────────────────────────────┐
│                CONTEXT & MEMORY       │                              │
│  ┌──────────────┐  ┌──────────────┐  ┌─────────────┐              │
│  │ TaskState    │  │ Artifact     │  │ Retrieval   │              │
│  │ (JSON)       │  │ Store (CAS)  │  │ (LSP+grep)  │              │
│  └──────────────┘  └──────────────┘  └─────────────┘              │
└───────────────────────────────────────┬─────────────────────────────┘
                                        │
┌───────────────────────────────────────┼─────────────────────────────┐
│                TOOLS + LLM CLIENT     │                              │
│  ┌──────────────┐  ┌──────────────┐  ┌─────────────┐              │
│  │ DeepSeek API │  │ Tool Registry│  │ Sandbox     │              │
│  │ (OAI compat) │  │ (Builtin+MCP)│  │ (Optional)  │              │
│  └──────────────┘  └──────────────┘  └─────────────┘              │
└─────────────────────────────────────────────────────────────────────┘
```

---

## 3. Dual-Model Routing Strategy

### 3.1 Model Characteristics

| Metric | V4 Flash | V4 Pro |
|--------|----------|--------|
| Active Params | 13B | 49B |
| Cost (output/1M) | $0.28 | $0.87 (promo) / $3.48 (list) |
| SWE-Bench | 79.0% | 80.6% |
| Terminal-Bench | 56.9% | 67.9% |
| LiveCodeBench | 91.6% | 93.5% |
| SimpleQA | 34.1% | 57.9% |
| Context Window | 1M tokens | 1M tokens |
| Best For | High-volume, routine | Complex agentic, multi-step |

### 3.2 Routing Rules

**Use V4 Flash (default):**
- Clarification questions, summaries, single-file edits
- Retrieval steps (grep/glob/read)
- Context compaction (non-thinking mode)
- FIM completion drafts
- Simple tool orchestration (< 5 tool calls expected)

**Escalate to V4 Pro when:**
- Multi-step agentic tasks (8+ tool calls expected)
- Repeated failures (same tool error 2+ times)
- Security-sensitive code (auth, crypto, shell exec, deserialization)
- Large diffs / multi-file changes (3+ files)
- Model must choose between multiple plausible approaches
- Factual recall needed (interpreting logs, CI failures)

### 3.3 Reasoning Mode Selection

| Mode | Usage | Cost |
|------|-------|------|
| `non-thinking` | FIM, small patches, compaction/summarization | Lowest |
| `high` | Normal planning/diagnosis/approach selection | Medium |
| `max` | Stuck states (>=2 failed iterations), high-impact security decisions | Highest (requires 384K+ context budget) |

### 3.4 RiskScore Computation

```
RiskScore = weighted_sum(
    ambiguity_signal      * 0.25,  // Missing inputs, conflicting instructions
    tool_failure_count    * 0.20,  // Previous failures in this turn
    edit_scope_estimate   * 0.20,  // Files touched, diff LOC estimate
    security_signal       * 0.20,  // Risky sinks (exec, eval, SQL, auth)
    grounding_score_inv   * 0.15,  // No citations for APIs/framework usage
)

if RiskScore >= 0.55 → Use Pro
else → Use Flash
```

### 3.5 reasoning_content Echo Protocol (Critical)

DeepSeek's thinking-mode API requires `reasoning_content` to be echoed verbatim in multi-turn.

- Store as opaque blob: `TurnRecord.reasoning_echo`
- Never trim, reformat, or summarize
- Include in next request exactly as received
- If missing → 400 error from API

---

## 4. Agent Loop (Staged Guarded Loop)

### 4.1 Five Stages

```
Stage 1: INTAKE → AMBIGUITY GATE
  - Detect missing requirements
  - If ambiguous: ask 1-3 clarification questions + present plan
  - If clear: proceed

Stage 2: PLAN (Lightweight)
  - Generate short plan + "edit intent" (files + constraints)
  - Plan is NOT implementation - it's a proposal

Stage 3: DESIGN SANITY GUARD
  - Self-challenge: fragility test, semantic correctness, edge cases
  - Anti-pattern detection (text-as-key, position-dependent, etc.)
  - If blocking issue found → request redesign (loop back to Stage 2)

Stage 4: ACT
  - Execute tools + edits
  - Loop Guard enforced (max N iterations)
  - Tool Dedupe enforced (no identical consecutive calls)
  - Scope Guard enforced (edits only if confirmed_scope=true)

Stage 5: VERIFY + COMPACT
  - Run tests/linters/compile checks
  - Rewrite TaskState (via Flash non-thinking)
  - Store large outputs as artifacts
  - Replace history with TaskState + recent 2 turns
```

### 4.2 Guard Mechanisms

**Loop Guard:**
- Max 15 tool iterations per user turn (configurable)
- If exceeded: force "diagnose + ask user" response
- Prevents runaway tool calling

**Tool Dedupe:**
- Hash tool_name + args for each call
- If identical call appears consecutively without new evidence → block
- Force model to explain what changed

**Scope Guard:**
- Before any edit tool: check `TaskState.confirmed_scope`
- If false AND not in auto mode → force plan presentation first
- Prevents "helpful but unwanted" implementations

**Design Sanity Guard:**
- Anti-patterns: fragile-key, string-match-on-formatted, position-dependent, implementation-shortcut
- Self-challenge questions injected into prompt
- Optional: dual-pass review (Flash reviews Flash's plan)

---

## 5. Technology Stack

### 5.1 Core Decisions

| Component | Choice | Rationale |
|-----------|--------|-----------|
| Language | Go 1.24+ | Single binary, fast compile, goroutines for streaming |
| CLI Framework | Cobra | Standard, well-tested |
| TUI | Bubble Tea + Lipgloss | Elm architecture, Charm ecosystem |
| Config | TOML (via Viper) | Codex-like, clean syntax |
| Storage | SQLite (state) + FS (artifacts) | Lightweight, reliable |
| API Client | net/http + SSE streaming | No heavy dependencies |
| LSP | Custom JSON-RPC 2.0 client | Keep it minimal |
| Git | go-git + shell fallback | Pure Go for portability |
| Distribution | GoReleaser | GitHub Releases + Homebrew + Scoop |
| Testing | go test + testcontainers | Standard + integration |

### 5.2 Why Go (not Rust, not TypeScript)

- **vs Rust**: Faster iteration, easier hiring, good enough performance for CLI agent
- **vs TypeScript**: No runtime dependency, single binary, better Windows support
- **vs Python**: No runtime dependency, faster startup, static typing
- Go's goroutines naturally handle: streaming API responses + parallel tool execution + TUI rendering

### 5.3 Distribution Strategy

```
Primary:   GitHub Releases (GoReleaser builds for all platforms)
macOS:     Homebrew tap (brew install deepact/tap/deepact)
Windows:   Scoop bucket
Universal: curl -fsSL https://install.deepact.dev | bash
```

---

## 6. Code Layering

### 6.1 Directory Structure

```
deepact/
├── cmd/                          # CLI entry points
│   ├── root.go                   # Cobra root command
│   ├── run.go                    # Interactive mode
│   ├── exec.go                   # Headless/CI mode
│   ├── plan.go                   # Plan-only mode (no execution)
│   ├── session.go                # Session management (list/resume/fork)
│   └── config.go                 # Config management
│
├── app/                          # Application layer (orchestrates use-cases)
│   ├── runner.go                 # Load session, select mode, run engine
│   └── modes/
│       ├── interactive.go        # TUI interactive mode
│       ├── headless.go           # CI/exec mode
│       └── server.go             # JSON-RPC server (IDE integration)
│
├── engine/                       # Core engine (pure logic, no I/O)
│   ├── loop.go                   # Staged state machine
│   ├── guards.go                 # Loop guard, tool dedupe, scope guard
│   ├── events.go                 # Event definitions (Submission, ToolCall, Response)
│   └── turn.go                   # Single turn management
│
├── router/                       # Model routing
│   ├── scorer.go                 # RiskScore computation
│   ├── selector.go               # Flash/Pro + reasoning mode selection
│   └── escalation.go             # Upgrade/downgrade strategy
│
├── policy/                       # Policy layer (rules & detection)
│   ├── ambiguity.go              # Ambiguity detection
│   ├── scope.go                  # Scope guard (confirmed_scope)
│   ├── permissions.go            # Operation permissions (auto/ask/deny)
│   ├── security.go               # Security keyword detection
│   └── design_guard.go           # Design sanity / anti-pattern detection
│
├── context/                      # Context assembly
│   ├── builder.go                # Bookend-style prompt assembly
│   ├── compactor.go              # TaskState rewrite + context compression
│   ├── snippets.go               # Code snippet selection + truncation
│   └── ordering.go               # Top/bottom placement (anti lost-in-middle)
│
├── memory/                       # State & memory
│   ├── taskstate.go              # TaskState JSON reducer
│   ├── decisions.go              # Decision tracking (prevent repetition)
│   └── agents_md.go              # AGENTS.md cascading loader
│
├── retrieval/                    # Code retrieval
│   ├── grep.go                   # Ripgrep-style full-text search
│   ├── glob.go                   # File pattern matching
│   ├── lsp.go                    # LSP go-to-definition / references
│   ├── symbols.go                # Symbol extraction
│   └── ranker.go                 # Candidate file ranking
│
├── llm/                          # LLM client
│   ├── client.go                 # DeepSeek API (OpenAI compatible)
│   ├── streaming.go              # SSE streaming handler
│   ├── thinking.go               # reasoning_content management (echo protocol)
│   └── retry.go                  # Exponential backoff + 429 handling
│
├── tools/                        # Tool system
│   ├── registry.go               # Tool registration center
│   ├── executor.go               # Parallel tool execution
│   ├── envelope.go               # ToolResultEnvelope (standard output format)
│   ├── builtin/
│   │   ├── bash.go               # Shell command execution
│   │   ├── read.go               # File reading
│   │   ├── edit.go               # File editing (diff-based)
│   │   ├── write.go              # File writing
│   │   ├── grep.go               # Content search
│   │   ├── glob.go               # File finding
│   │   └── lsp_tools.go          # LSP symbol operations
│   └── mcp/
│       ├── client.go             # MCP client
│       └── discovery.go          # MCP server discovery
│
├── session/                      # Session persistence
│   ├── store.go                  # JSONL event storage
│   ├── rewind.go                 # Rewind/rollback
│   ├── fork.go                   # Fork session
│   └── resume.go                 # Resume session
│
├── artifact/                     # Artifact storage (CAS)
│   ├── store.go                  # Content-addressed store
│   └── redact.go                 # Sensitive info redaction
│
├── ui/                           # TUI rendering
│   ├── model.go                  # Bubble Tea Model
│   ├── views/
│   │   ├── chat.go               # Chat view
│   │   ├── status.go             # Status bar
│   │   └── tools.go              # Tool execution display
│   └── styles.go                 # Lipgloss styles
│
├── config/                       # Configuration
│   ├── loader.go                 # Cascading load (CLI > project > user > system)
│   └── schema.go                 # Config structs
│
└── .deepact/              # Project-level config directory
    └── config.toml               # Project config template
```

### 6.2 Key Interfaces

```go
// --- Router ---
type Router interface {
    Select(ctx TurnContext) ModelChoice
}

type ModelChoice struct {
    Model     string // "deepseek-v4-flash" | "deepseek-v4-pro"
    Reasoning string // "non-thinking" | "high" | "max"
    JsonMode  bool
}

// --- Engine ---
type Engine interface {
    Step(userMsg string) (AssistantResponse, error)
    Interrupt() error
    GetState() TaskState
}

// --- Tool ---
type Tool interface {
    Spec() ToolSpec
    Run(ctx ToolContext, input json.RawMessage) (ToolResultEnvelope, error)
}

type ToolResultEnvelope struct {
    ToolCallID   string `json:"tool_call_id"`
    ToolName     string `json:"tool_name"`
    Status       string `json:"status"`       // "ok" | "error"
    Digest       string `json:"digest"`       // 1-3 line summary (inline)
    ArtifactRef  string `json:"artifact_ref"` // CAS reference for full output
    ExitCode     *int   `json:"exit_code,omitempty"`
}

// --- Policy ---
type AmbiguityDetector interface {
    Analyze(userMsg string, state TaskState) AmbiguityResult
}

type DesignGuard interface {
    Review(plan PlanOutput, context CodeContext) DesignReview
}

// --- Context ---
type ContextBuilder interface {
    Assemble(state TaskState, workingSet []Snippet, observations []Observation) Prompt
}

// --- Session ---
type SessionStore interface {
    Save(event Event) error
    Load(sessionID string) ([]Event, error)
    Fork(sessionID string) (string, error)
    Rewind(sessionID string, toEvent int) error
}
```

### 6.3 Dependency Rules (Strict)

```
cmd/ → app/ → engine/, router/, policy/, context/, memory/
                 ↓
              tools/, llm/, retrieval/
                 ↓
              session/, artifact/
                 ↓
              config/ (shared, no upward deps)

Rules:
- engine/ NEVER imports ui/ or cmd/
- tools/ NEVER imports engine/ (only interfaces)
- llm/ is standalone (no project-specific logic)
- policy/ can read state but NEVER mutates it directly
- ui/ only consumes events from engine (observer pattern)
```

---

## 7. Multi-Agent State Sharing & Handoff Protocol

### 7.1 Design Principle

**NEVER share raw transcripts between agents.** DeepSeek degrades with long repetitive context.
Always share structured TaskState + artifact references.

### 7.2 Shared State Schema (SQLite)

```sql
CREATE TABLE goals (
    id TEXT PRIMARY KEY,
    text TEXT NOT NULL,
    status TEXT DEFAULT 'active',  -- active | completed | cancelled
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE tasks (
    id TEXT PRIMARY KEY,
    goal_id TEXT REFERENCES goals(id),
    status TEXT DEFAULT 'pending',  -- pending | in_progress | completed | failed
    needs TEXT,                     -- JSON array of task IDs (dependencies)
    owner_agent TEXT,
    payload JSON,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE artifacts (
    id TEXT PRIMARY KEY,            -- sha256 hash
    type TEXT,                      -- file_content | tool_output | diff | plan
    path TEXT,
    size INTEGER,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE handoffs (
    id TEXT PRIMARY KEY,
    from_agent TEXT NOT NULL,
    to_agent TEXT NOT NULL,
    task_id TEXT REFERENCES tasks(id),
    payload JSON NOT NULL,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
```

### 7.3 Handoff Payload (Standard JSON Format)

```json
{
  "version": "1.0",
  "task_id": "T-123",
  "goal": "Fix Windows config path parsing bug",
  "confirmed_scope": true,
  "constraints": [
    "Do not change public API",
    "No new dependencies",
    "Only modify config/ directory"
  ],
  "assumptions": ["CI uses Go 1.22", "Target Windows 10+"],
  "decisions": [
    {"id": "D1", "text": "Use filepath.ToSlash for path normalization"}
  ],
  "plan": [
    {"step": 1, "text": "Find path handling in config/loader.go", "status": "done"},
    {"step": 2, "text": "Fix Windows path compatibility", "status": "in_progress"},
    {"step": 3, "text": "Add cross-platform tests", "status": "pending"}
  ],
  "working_set": {
    "files": [
      {"path": "config/loader.go", "rev": "git:abcd123", "lines": "45-82"}
    ],
    "symbols": [
      {"name": "LoadConfig", "path": "config/loader.go", "kind": "function"}
    ]
  },
  "open_questions": ["Use XDG standard or AppData for config directory?"],
  "recent_observations": [
    {
      "type": "tool_result",
      "tool": "bash",
      "artifact_ref": "artifact:sha256:e3b0c44...",
      "digest": "go test ./config/... fails on Windows: path separator mismatch"
    }
  ],
  "model_state": {
    "model": "deepseek-v4-pro",
    "reasoning_content": "<opaque-verbatim-blob-must-echo>"
  },
  "metrics": {
    "tokens_used": 12450,
    "tool_calls": 8,
    "elapsed_seconds": 45
  }
}
```

### 7.4 Orchestration Modes

| Mode | Semantics | Use Case |
|------|-----------|----------|
| **Handoff** (sync) | Transfer control, wait for completion | Sequential dependent tasks |
| **Assign** (async) | Dispatch task, continue independently | Parallel independent tasks |
| **Message** (direct) | Send info to running agent | Coordination, iterative feedback |

---

## 8. Code Context Design (Anti Lost-in-Middle)

### 8.1 Context Strategy: Aggressive 1M Utilization

**Design Decision:** Use DeepSeek V4's 1M context aggressively. This is its primary competitive
advantage over Claude. Conservative 128K budgets waste this advantage.

**Two Context Modes:**

| Mode | Budget | Trigger | Strategy |
|------|--------|---------|----------|
| **Deep Context** | 500K-800K | Task involves 3+ files, call chains, root cause analysis | Load complete files, full dependency chain |
| **Focused Context** | 128K | Single-file edits, clear scope, simple changes | Bookend + snippets (old strategy) |

**Deep Context allocation (default for non-trivial tasks):**
```
System prompt + AGENTS.md:       ~8K
Project structure (dir tree):    ~20K
Working files (FULL, not snips): ~200K (current task files, complete)
Call chain files (FULL):         ~150K (upstream/downstream dependencies)
Test files:                      ~50K (relevant tests, complete)
Conversation history:            ~100K (recent turns)
Reserve for output:              ~64K
─────────────────────────────────────
Total:                           ~592K (well within 800K safe zone)
```

**Key principle:** Don't snippet. Load complete files. DeepSeek 1M context's value is
"give full, structured code blocks" not "stuff more fragments."

### 8.2 Prompt Layout (Hierarchical Structure)

Context must have clear hierarchy so DeepSeek can distinguish primary vs secondary info:

```
╔══════════════════════════════════════╗  ← ZONE 1: DIRECTIVE (always attended)
║  1. TaskState JSON (goal, scope)     ║
║  2. Constraints + stop conditions    ║
║  3. Decisions made (DO NOT restate)  ║
╠══════════════════════════════════════╣
║  4. MODIFIED files (full content)    ║  ← ZONE 2: PRIMARY CODE (high attention)
║     [These are YOUR work products]   ║
╠══════════════════════════════════════╣
║  5. CALL CHAIN files (signatures     ║  ← ZONE 3: REFERENCE CODE (medium)
║     or full, based on collapse level)║
╠══════════════════════════════════════╣
║  6. OTHER context files              ║  ← ZONE 4: SUPPORTING (lower attention)
║     (test files, docs, configs)      ║
╠══════════════════════════════════════╣
║  7. Conversation history             ║  ← ZONE 5: HISTORY (compressed older)
╠══════════════════════════════════════╣
║  8. Recent tool results              ║  ← ZONE 6: LATEST (high attention)
║  9. Current user message             ║
╚══════════════════════════════════════╝
```

Each zone is clearly marked with headers so the model can "see the structure."

### 8.3 Retrieval Strategy: Call Chain Discovery

**Purpose:** Before loading context, trace the call chain to know WHICH files matter.

```
User goal → identify entry point (file/function)
  ├── LSP go-to-definition: find implementations
  ├── LSP find-references: find callers
  └── Recursively expand (max depth 3)

Result: ordered list of files in the call chain
  → Load them into context at appropriate zone
```

**Implementation:** `retrieval/callchain.go`
- Input: entry point symbol (from user message or grep)
- Use LSP references recursively (up to depth 3)
- Output: `[]FileRef` ordered by distance from entry point
- Files at depth 0-1: load FULL
- Files at depth 2-3: load SIGNATURES only (AST collapse)

### 8.4 Context Compaction: 4-Layer Synchronous Compression

**Trigger thresholds (based on 1M budget):**
```
Layer 1: ALWAYS (every tool call)     → Tool Output Governance
Layer 2: 60% (~600K)                  → Stale Result Eviction  
Layer 3: 80% (~800K)                  → Code-Aware Collapse (AST)
Layer 4: 90% (~900K)                  → Full Compact (Flash API call)
```

**All layers are synchronous.** Layer 1-3 are instant (<500ms). Layer 4 is rare (5-15s).

#### Layer 1: Tool Output Governance (every tool call, <10ms)

Runs after EVERY tool execution. Prevents context pollution at the source.

```
Rules:
- bash output > 10KB → store artifact, keep digest only
- grep results > 50 matches → keep top 20 + "and N more matches"
- file already in working set → don't duplicate in tool result
- Command-aware filtering:
    npm install  → strip deprecation warnings, funding
    go test      → keep only FAIL + summary, strip PASS details
    git push     → strip progress lines
    cargo build  → strip "Compiling X/Y" lines
    pytest       → keep only FAILED + summary
```

#### Layer 2: Stale Result Eviction (60% threshold, <50ms)

Removes information that has been superseded by newer information.

```
Rules:
- Tool results older than 5 turns → remove (keep digest ref)
- File content that was later re-read → remove old version
- grep results that were followed by targeted read → remove grep
- "Superseded" principle: newer info always wins
```

#### Layer 3: Code-Aware Collapse (80% threshold, <500ms)

**Our key differentiator.** Uses AST parsing to structurally fold code.

```
Implementation:
- tree-sitter parse each file in context
- Determine CollapseLevel per file:
    Modified by current task       → KEEP FULL (never collapse)
    In call chain + not modified   → COLLAPSE TO SIGNATURES
    Read in last 3 turns           → KEEP FULL
    Read in last 5 turns           → COLLAPSE TO SIGNATURES
    Older reads                    → COLLAPSE TO PATH + SUMMARY
    Test files (all passed)        → COLLAPSE TO test_name: status

Signature extraction (via AST):
  - Function/method: name + params + return type + first docstring line
  - Class/struct: name + field names + method signatures
  - Interface: name + method signatures
  - Constants/vars: name + type

Example collapse (Go):
  FULL:     40 lines of ValidateToken() implementation
  SIGNATURE: func ValidateToken(token string) (*Claims, error) { ... }
  SUMMARY:  "auth/token.go: JWT validation (ValidateToken, decodeHeader, verifySignature)"
```

**Call chain tracking via LSP:**
```go
type FileContext struct {
    Path           string
    CollapseLevel  CollapseLevel  // Full | Signature | Summary | Remove
    InCallChain    bool
    ChainDepth     int            // 0 = entry point, 1 = direct dep, etc.
    Modified       bool           // edited by current task
    TurnLastRead   int
}

// Collapse decision logic
func (f FileContext) DecideLevel(currentTurn int) CollapseLevel {
    if f.Modified                          { return CollapseFull }
    if f.InCallChain && f.ChainDepth <= 1  { return CollapseFull }
    if f.InCallChain && f.ChainDepth <= 3  { return CollapseSignature }
    if currentTurn - f.TurnLastRead <= 3   { return CollapseFull }
    if currentTurn - f.TurnLastRead <= 5   { return CollapseSignature }
    return CollapseSummary
}
```

#### Layer 4: Full Compact (90% threshold, 5-15s, 1 Flash call)

Last resort. Generates a structured handoff summary.

```
Trigger: context reaches 90% of 1M (~900K)
Model: Flash (non-thinking) — cheap and fast
Input: structured representation of current context
Output: handoff summary document (~4-8K tokens)

Summary structure:
  1. User's original goal
  2. Confirmed technical decisions
  3. Files modified (list + change summary per file)
  4. Current unresolved issues
  5. Next steps in plan
  6. Failed attempts (don't repeat)

Preserved verbatim (NOT summarized):
  - Last 3 conversation turns
  - TaskState JSON
  - Modified files (full content)
  - Active call chain signatures

Post-compact, context drops from ~900K → ~200K, allowing continued work.
```

### 8.5 AST Integration (tree-sitter)

**Dependency:** `github.com/smacker/go-tree-sitter` + language grammars

**Supported languages (Phase 1):**
- Go, TypeScript/JavaScript, Python, Rust, Java

**Used for:**
1. Signature extraction during collapse (Layer 3)
2. Function boundary detection for code loading
3. Call chain verification (supplement LSP)

**Fallback:** If no tree-sitter grammar available for a language, fall back to
line-based heuristics (find `func`/`def`/`class` lines). Less accurate but functional.

---

## 9. DeepSeek V4 Weakness Mitigation

### 9.1 Over-Implementation Without Asking (PRIMARY WEAKNESS)

**Problem**: DeepSeek proceeds to implement without clarifying ambiguous requirements.

**Architectural Solution: Ambiguity Gate + Confirmation Contract**

```
System prompt injection:
"If the user's request has ANY ambiguity about WHAT to change or HOW:
 - DO NOT write code
 - Ask 1-3 specific clarification questions
 - Present what you WOULD change (files + approach)
 - Wait for user confirmation"

Code-level enforcement:
- AmbiguityDetector scores user message (0~1)
- If score >= 0.45: block Act stage, force clarification
- Model output constrained to: questions + plan proposal
- Edit tools disabled until confirmed_scope=true
```

**Detection signals:**
- No explicit target files mentioned
- Open-ended verbs ("improve", "optimize", "fix")
- Missing acceptance criteria
- Multiple possible interpretations
- Scope larger than what user likely intends

### 9.2 Lazy/Stupid Design Choices (Design Sanity Guard)

**Problem**: DeepSeek takes shortcuts (e.g., using text content as key instead of IDs).

**Architectural Solution: Three-Layer Design Guard**

```
Layer 1: PROMPT (Prevention)
  - Self-challenge questions injected into system prompt
  - Anti-pattern catalog with BAD/GOOD comparisons
  - "Explain WHY this approach, not just WHAT"

Layer 2: PLAN-TIME REVIEW (Detection)
  - Dual-pass: Flash reviews Flash's plan before execution
  - Check for: fragile-key, position-dependent, shortcut-impl
  - If blocking → reject plan, request redesign

Layer 3: POST-IMPLEMENTATION (Backstop)
  - Static analysis / AST grep for fragile patterns
  - If detected → warn user + suggest alternative
```

**Anti-pattern catalog:**
| Pattern | Description | Detection |
|---------|-------------|-----------|
| `fragile-key` | Using display text/content as identifier when structured IDs exist | Code uses text/innerHTML/label for lookup |
| `position-dependent` | Using array index/position as stable key | Code uses [0], firstChild, nth-element |
| `string-on-formatted` | Regex on formatted output when structured API exists | Parsing CLI output/logs instead of using API |
| `implementation-shortcut` | Works only for current case, breaks for variations | Magic numbers, hardcoded values |

### 9.3 Verbose/Repetitive Multi-Turn

**Problem**: Response patterns degrade and self-reinforce over conversation length.

**Architectural Solution:**
- **Output schema enforcement**: `{summary, changes, next_step, questions}` with length caps
- **State-based dedup**: `TaskState.decisions` tracks what's been said; prompt says "DO NOT restate"
- **Forced rebase every 5 turns**: replace history with TaskState + last 2 turns
- **Fresh context for sub-agents**: sub-agents start clean, never inherit full transcript

### 9.4 Tool Call Loops

**Problem**: Model gets stuck calling the same tool repeatedly (root cause: results not passed back).

**Architectural Solution:**
- **Strict ToolResultEnvelope**: every execution produces a standardized result with matching `tool_call_id`
- **Loop Guard**: identical call (name + args hash) blocked if consecutive without new evidence
- **Forced diagnosis**: after 3 same-tool failures, switch to "diagnose mode" (explain what's wrong)
- **reasoning_content echo**: ensure thinking-mode state is preserved correctly between turns

### 9.5 Hallucinated APIs (Local-First Grounding)

**Problem**: Invents non-existent framework methods, especially for niche frameworks.

**Design Decision: Local-first verification, NO web search dependency.**

Web search results are unreliable for code grounding — they return outdated docs, wrong versions,
or generic tutorials that don't match the project's actual dependencies. The codebase itself is
the ground truth. Our grounding strategy is strictly local-first with escalation.

**Grounding Escalation Chain (local → local docs → ask user):**

```
Level 1: LSP Verification (instant, zero cost)
  ├── go-to-definition → symbol exists? ✓ proceed
  ├── find-references → used elsewhere? ✓ proceed  
  └── NOT FOUND → escalate to Level 2

Level 2: Local Documentation (fast, zero cost)
  ├── grep node_modules/<pkg>/README.md
  ├── grep vendor/<pkg>/doc.go
  ├── read .d.ts type definitions
  ├── read go.sum / requirements.txt (confirm version)
  ├── read project docs/, CHANGELOG, API.md
  └── NOT FOUND → escalate to Level 3

Level 3: Package Introspection (fast, zero cost)
  ├── bash: <tool> --help (CLI tools)
  ├── grep package source for exported symbols
  ├── AST parse for public interface
  └── NOT FOUND → escalate to Level 4

Level 4: Ask User (zero hallucination risk)
  └── "I cannot verify that <API> exists in your project's dependencies.
       Can you confirm this is correct, or point me to the right API?"
```

**Why NOT web search:**
- Web results are often outdated (wrong version of the library)
- Search engines return tutorials/blogs, not authoritative API refs
- Adds 1-3s latency per lookup, breaks agent flow
- Security risk: code snippets may leak to search engines
- The codebase + its dependencies ARE the source of truth
- Claude Code's lesson: "They threw out RAG. Agentic search (grep) beat retrieval."

**Web search is deferred to Phase 3 as an optional MCP tool** for the rare cases where:
- Working with a brand-new library not yet in the project
- Investigating error messages with no local context
- Looking up migration guides for major version upgrades

**Architectural Solution (code-level):**
- **Grounding requirement**: before generating API calls, verify symbol exists via LSP/grep
- **Citation rule**: system prompt requires citing `path:line` for any API usage
- **Uncertainty expression**: if cannot verify, must state "I'm unsure if this API exists"
- **Verification tool chain**: LSP goto-definition → local docs grep → package introspection → ask user
- **NEVER**: skip verification and assume an API exists based on model's training data

### 9.6 Context Degradation

**Problem**: Quality drops in long conversations, attention dilutes.

**Architectural Solution:**
- **Short prompts**: never feed raw transcript beyond last 1-2 turns
- **Stable state**: model operates on TaskState + Working Set + Recent Observations (regenerated each turn)
- **Pro for high-risk only**: keep Flash as steady-state executor (shorter, focused prompts)
- **Bookend layout**: critical info always at top and bottom of prompt

---

## 10. Lessons from Codex & Claude Code

### 10.1 From Claude Code

| Feature | Their Approach | Our Adaptation |
|---------|---------------|----------------|
| CLAUDE.md cascading | 5-level memory hierarchy | AGENTS.md: system > user > project > directory |
| Permission modes | 6 modes (default→bypass) | 3 modes + ambiguity gate as 4th dimension |
| TodoWrite tool | Track progress in conversation | TaskState.plan (persisted, structured) |
| 5-layer compaction | budget→snip→micro→collapse→auto | Single-pass: rewrite TaskState each turn |
| Sub-agents | Depth-1, result-only return | Handoff/Assign/Message (3 orchestration patterns) |
| Session persistence | JSONL with rewind/fork | JSONL events + SQLite state (hybrid) |
| Plan mode | Separate research from execution | Stage 1-3 (intake→plan→guard) before any execution |

### 10.2 From OpenAI Codex CLI

| Feature | Their Approach | Our Adaptation |
|---------|---------------|----------------|
| Queue-driven state machine | Submission/Event pairs | Event-driven engine with staged guards |
| Platform sandbox | Seatbelt/seccomp/Landlock | Defer to v2; MVP uses command allowlist |
| AGENTS.md cascading | global override→global→project→dir | Same pattern adopted |
| App Server mode | JSON-RPC for IDE integration | Planned for v2 (JSON-RPC server mode) |
| Approval policies | never/on-request/untrusted | auto/ask/deny + ambiguity gate |
| OpenTelemetry | Full audit logging | Metrics + artifact store for audit trail |
| Rust + Ratatui | Performance-first TUI | Go + Bubble Tea (pragmatic, faster dev) |

---

## 11. Configuration Cascading

### 11.1 AGENTS.md Loading Order (highest priority first)

```
1. CLI flags (--constraint, --model, etc.)
2. Project override:  ./.deepact/AGENTS.override.md
3. Project default:   ./.deepact/AGENTS.md  OR  ./AGENTS.md
4. Directory-specific: <subdir>/AGENTS.md (lazy-loaded when entering dir)
5. User config:       ~/.deepact/AGENTS.md
6. System default:    /etc/deepact/AGENTS.md (enterprise)
```

### 11.2 Config File (TOML)

```toml
# .deepact/config.toml

[model]
default = "deepseek-v4-flash"
escalation = "deepseek-v4-pro"
api_base = "https://api.deepseek.com"
# api_key loaded from env: DEEPSEEK_API_KEY

[routing]
risk_threshold = 0.55
max_iterations = 15
auto_escalate_on_failure = true

[context]
max_budget_tokens = 131072  # 128K default
snippet_max_lines = 200
max_snippets = 5
compact_every_n_turns = 5

[permissions]
mode = "ask"  # auto | ask | deny
allow_shell = true
allow_write = true
dangerous_commands = ["rm -rf", "git push --force", "DROP TABLE"]

[session]
storage_dir = "~/.deepact/sessions"
artifact_dir = "~/.deepact/artifacts"

[ui]
theme = "auto"  # auto | dark | light
show_thinking = false
show_tool_output = true
```

---

## 12. Concurrency Model & Multi-Agent Architecture

### 12.1 Three-Layer Concurrency Design

```
┌─────────────────────────────────────────────────────────────┐
│ Layer 1: LLM Client (底层并发控制)                           │
│                                                             │
│  Adaptive Semaphore: 控制同时发给 DeepSeek 的请求数          │
│  - Initial: 5 concurrent slots (conservative)              │
│  - On 429: halve (min 1)                                   │
│  - On 60s sustained success: +1 (max 10)                   │
│  - Token-bucket rate limiter: 10 RPS burst 5               │
│                                                             │
└─────────────────────────────────────────────────────────────┘
                              ▲
┌─────────────────────────────┼───────────────────────────────┐
│ Layer 2: Agent Pool (角色并发调度)                            │
│                                                             │
│  Dynamic role agents spawned per-task                       │
│  Each role = 1 goroutine + isolated context                │
│  Share findings via Findings Board (pub/sub)               │
│  Deliver results via Handoff format to Decision Agent      │
│                                                             │
│  ┌──────────┐  ┌──────────┐  ┌──────────┐                 │
│  │ Role A   │  │ Role B   │  │ Role C   │                 │
│  │ (Flash)  │  │ (Flash)  │  │ (Flash)  │                 │
│  └────┬─────┘  └────┬─────┘  └────┬─────┘                 │
│       │              │             │                        │
│       └── publish ───┼── read ─────┘                        │
│              ▼       ▼                                      │
│       Findings Board (shared)                               │
│              ▼                                              │
│       Decision Agent (Pro) → confirm with user if needed   │
│                                                             │
└─────────────────────────────────────────────────────────────┘
                              ▲
┌─────────────────────────────┼───────────────────────────────┐
│ Layer 3: Session Coordinator                                │
│                                                             │
│  - One active task per session at a time                   │
│  - A task can fan-out to multiple role agents              │
│  - TaskState is the coordination point (RWMutex)           │
│  - Cancellation propagates via context.Context             │
│                                                             │
└─────────────────────────────────────────────────────────────┘
```

### 12.2 Dynamic Task Decomposition

Roles are NOT predefined. The Task Decomposer (1 Pro call) analyzes the user's goal
and spawns appropriate role agents dynamically.

**Decomposition Flow:**
```
User goal → Task Decomposer (Pro, 1 call)
         → Outputs: [{role, goal, tools, model}]
         → Spawn goroutines in parallel
         → Fan-in results to Decision Agent
```

**Example decompositions:**

| User Goal | Spawned Roles | Model |
|-----------|---------------|-------|
| "Fix API 500 error" | root_cause_analyst, test_investigator, impact_assessor | All Flash |
| "Add pagination to /users" | architect, implementor, test_writer | Architect=Pro, rest=Flash |
| "Why is this slow?" | profiler, bottleneck_finder, optimization_proposer | All Flash |
| "Review this PR" | security_checker, logic_reviewer, style_checker | All Flash |

**Role selection principle:**
- Investigation/retrieval roles → Flash (cheap, fast, sufficient)
- Decision/synthesis roles (Decomposer, Decision Agent) → Pro (needs strong reasoning)
- Execution roles (actually writing code) → Flash (or Pro if complex multi-file)

### 12.3 Shared Findings Board

**Purpose:** Allow sub-agents to share discoveries that help OTHER agents make better decisions.
NOT for intermediate working notes — those go in the agent's own handoff deliverable.

**Publishing Rule:** Only publish to shared board when the finding is:
1. Related to the main goal (not just the sub-agent's own task)
2. Would change how another agent approaches their work
3. Reveals something unexpected (constraint, dependency, risk)

**Examples of SHARED checkpoints:**
- "The function being modified is called from 12 other places" → impacts all agents
- "There's a race condition in the same module" → impacts implementation approach
- "The existing test suite has 0% coverage here" → impacts verification strategy

**Examples of NOT shared (goes in own handoff):**
- "I searched 5 files before finding the right one" → intermediate work
- "The function has 3 parameters" → factual detail, not decision-changing
- "I tried grep with pattern X, no results" → dead end, own log

```go
type Checkpoint struct {
    ID         string    `json:"id"`
    Timestamp  time.Time `json:"timestamp"`
    AgentRole  string    `json:"agent_role"`
    Type       string    `json:"type"`       // finding | blocker | risk | dependency
    Summary    string    `json:"summary"`    // 1 sentence
    Relevance  string    `json:"relevance"`  // WHY this matters to other agents
    Evidence   []Evidence `json:"evidence"`
}

type FindingsBoard struct {
    mu          sync.RWMutex
    checkpoints []Checkpoint
    subscribers []chan Checkpoint
}

// Publish: non-blocking notify to all subscribers
func (b *FindingsBoard) Publish(cp Checkpoint) {
    b.mu.Lock()
    b.checkpoints = append(b.checkpoints, cp)
    b.mu.Unlock()
    for _, sub := range b.subscribers {
        select {
        case sub <- cp:
        default: // non-blocking, skip slow consumers
        }
    }
}

// ReadAll: agents can read all shared findings at any time
func (b *FindingsBoard) ReadAll() []Checkpoint {
    b.mu.RLock()
    defer b.mu.RUnlock()
    return slices.Clone(b.checkpoints)
}
```

### 12.4 Sub-Agent Deliverable (Handoff Format)

Each sub-agent, upon completion, delivers a structured handoff to the Decision Agent:

```json
{
  "agent_role": "root_cause_analyst",
  "goal": "定位500的触发路径和根因",
  "status": "completed",
  "confidence": 0.85,
  "findings": [
    {
      "type": "root_cause",
      "summary": "token 过期判断用了 <= 应该用 <",
      "evidence": [{"type": "code_ref", "path": "auth/middleware.go", "line": 67}]
    }
  ],
  "proposals": [
    {
      "id": "P1",
      "description": "最小修复: 改 <= 为 <",
      "files": ["auth/middleware.go"],
      "effort": "low",
      "risk": "low"
    },
    {
      "id": "P2",
      "description": "加固: 修复 + 添加边界日志",
      "files": ["auth/middleware.go", "auth/logger.go"],
      "effort": "medium",
      "risk": "low"
    }
  ],
  "own_checkpoints": [
    "searched 5 files before locating root cause",
    "eliminated 2 other hypotheses via test reproduction"
  ]
}
```

---

## 13. Autonomy Boundary & Confirmation Protocol

### 13.1 Four Autonomy Levels

| Level | Name | Agent Can Auto-Do | Agent Must Confirm |
|-------|------|-------------------|--------------------|
| **L0** | Information Gathering | Read files, grep, LSP, run --help | Nothing |
| **L1** | Analysis & Diagnosis | Form hypotheses, eliminate candidates, locate root cause | Nothing |
| **L2** | Proposal & Planning | Generate options, evaluate tradeoffs | **Which approach to take** |
| **L3** | Code Execution | Write code, run tests (after plan confirmed) | **Scope changes, new blockers, plan deviation** |

### 13.2 Must-Confirm Rules (L2 Boundary)

The Decision Agent MUST confirm with user when:

```yaml
must_confirm:
  multiple_valid_approaches:
    trigger: "2+ viable options with different effort/risk/structure"
    action: "Present options with tradeoffs, recommend one, ask user"
    example: "Fix at call-site (5 lines) vs refactor middleware (3 files)"

  scope_expansion:
    trigger: "Fix requires modifying files/modules user didn't mention"
    action: "Explain why scope must expand, ask permission"
    example: "Fixing this requires also changing auth/middleware.go"

  interface_change:
    trigger: "Modify public API, change data structures, add dependencies"
    action: "Show what changes and downstream impact, ask user"
    example: "I need to add a parameter to UserService.GetUser()"

  uncertainty:
    trigger: "Root cause unclear, multiple hypotheses remain"
    action: "Present hypotheses with confidence levels, ask which to pursue"
    example: "Could be a race condition (60%) or a cache issue (40%)"

  tradeoff_decision:
    trigger: "Performance vs readability, quick-fix vs thorough fix"
    action: "Present tradeoff explicitly, ask preference"
    example: "Patch now (10min) vs refactor properly (1hr)"
```

**Auto-decide (never ask):**
- Which tool to use for searching
- Which file to read first
- Search keyword selection
- Test command construction
- Two equivalent code styles (follow project convention)
- Import ordering
- Variable naming (follow existing patterns)

### 13.3 Confirmation Presentation Format

When Decision Agent needs user input:

```
## 分析完成，需要确认

### 问题定位
[1-2 sentence summary of root cause with evidence]

### 方案选项

**A: [name]** (推荐)
- 改动: [files + scope]
- 风险: [low/medium/high]
- 理由: [why recommended]

**B: [name]**
- 改动: [files + scope]
- 风险: [low/medium/high]

### 需要你确认
→ 选哪个方案？（或说说你的想法）
```

### 13.4 Progressive Trust (渐进式信任)

The system learns which decisions the user trusts it to make automatically:

**Mechanism:**
```
Default: Conservative (all L2 decisions require confirmation)
    ↓
User accepts same type of decision 3 times consecutively
    ↓
Auto-trust: That decision type becomes auto-decide
    ↓
User says "别自己决定这个" / rejects a decision
    ↓
Revoke trust: Back to must-confirm for that type
```

**Persisted in:** `~/.deepact/preferences.toml`

```toml
[autonomy]
# Auto-learned: decisions user consistently accepts
trusted_decisions = [
    "single_file_fix_under_10_lines",
    "test_file_addition",
    "style_fix_matching_existing_pattern",
]

# Explicitly revoked: user said "always ask me about this"
always_confirm = [
    "public_api_change",
    "new_dependency",
    "database_schema_change",
]

# Trust counters (internal tracking)
[autonomy.counters]
"single_file_fix_under_10_lines" = 5  # accepted 5 times, threshold is 3
"multi_file_refactor" = 1             # not yet trusted
```

### 13.5 Re-confirmation Triggers During Execution (L3)

Even after a plan is confirmed, the executing agent must pause and re-confirm if:

1. **Plan deviation**: "I confirmed approach A, but it's not working. Need to try B."
2. **Scope creep**: "The fix also requires changing another module I didn't mention."
3. **New blocker**: "Discovered a pre-existing bug that blocks this fix."
4. **Risk escalation**: "This is more complex than initially assessed."

---

## 14. LLM Client Design

### 14.1 Interface

```go
type Client interface {
    Stream(ctx context.Context, req Request) (<-chan Chunk, error)
    Complete(ctx context.Context, req Request) (*Response, error)
}
```

### 14.2 Adaptive Rate Limiter

```go
type AdaptiveLimiter struct {
    sem             *semaphore.Weighted  // Concurrency control
    rateLimiter     *rate.Limiter        // RPS control
    currentLimit    int                  // Dynamic, adjusts on 429/success
    consecutive429s int
}

// Behavior:
// - Initial concurrency: 5
// - On 429 (3 consecutive): halve concurrency (min 1)
// - On 60s sustained success: +1 concurrency (max 10)
// - Rate limit: 10 RPS with burst of 5
// - Retry: exponential backoff with jitter (1s base, 30s cap)
```

### 14.3 Token Estimation

```go
type TokenEstimator struct {
    calibrationFactor float64  // Adjusted based on actual vs estimated
}

// Pre-send estimation (heuristic):
//   Chinese: ~1.5 tokens/char
//   English: ~1.3 tokens/word
//   Code:    ~1 token/3 chars
//   Default: len(text) / 3
//
// Post-response calibration:
//   actual = response.Usage.PromptTokens
//   estimated = our pre-send estimate
//   error_rate = |actual - estimated| / actual
//   calibrationFactor adjusted toward actual over time
//
// Display: "~12.3K / 128K" (estimated) → "12.8K / 128K" (actual, after response)
```

### 14.4 Streaming Architecture

```
All API calls are streaming by default.
Complete() is implemented as: collect all chunks from Stream().

Stream flow:
  1. Acquire semaphore slot
  2. Open SSE connection to DeepSeek API
  3. Yield Chunk{} on each SSE event
  4. On final chunk: include Usage stats
  5. Release semaphore slot
  6. Calibrate token estimator with actual usage

Chunk types:
  - Content delta (text)
  - Reasoning delta (thinking, if enabled)
  - Tool call delta (function name + args building up)
  - Done signal (finish_reason + usage)
```

### 14.5 reasoning_content Protocol

```
On RECEIVE (from API):
  - Store reasoning_content as opaque string in TurnRecord
  - Never parse, truncate, or summarize

On SEND (to API, next turn):
  - IF previous turn had tool_calls + reasoning_content:
    → MUST include reasoning_content in the assistant message
  - IF previous turn had NO tool_calls:
    → reasoning_content can be omitted (API ignores it)
  - IF missing when required → API returns 400

Implementation:
  - TurnRecord.ReasoningEcho stores the blob
  - ContextBuilder checks: if last turn has tool_calls, include echo
  - Validation: before sending, assert echo is present when needed
```

---

## 15. MVP Scope (Updated)

### Phase 1 (MVP - 1-2 weeks)
- [ ] Basic agent loop with Flash model
- [ ] Core tools: bash, read, edit, write, grep, glob
- [ ] Ambiguity Gate (prompt-based)
- [ ] Simple TUI (chat + tool output display)
- [ ] Session persistence (JSONL)
- [ ] AGENTS.md loading (project + user level)
- [ ] Local grounding: LSP go-to-definition for symbol verification
- [ ] LLM Client: streaming, retry, adaptive semaphore
- [ ] Token estimation + display in status bar

### Phase 2 (Core Features - 2-4 weeks)
- [ ] Dual-model routing (Flash + Pro)
- [ ] Design Sanity Guard
- [ ] Loop Guard + Tool Dedupe
- [ ] Context compaction (TaskState rewrite)
- [ ] Full LSP integration (go-to-definition, references, symbols)
- [ ] Local docs grounding (read package READMEs, .d.ts, --help)
- [ ] Artifact store (CAS)
- [ ] Session fork/rewind
- [ ] Multi-agent: dynamic task decomposition + parallel execution
- [ ] Findings Board (shared checkpoint layer)
- [ ] Decision Agent + confirmation protocol
- [ ] Progressive trust mechanism

### Phase 3 (Advanced - 4+ weeks)
- [ ] Multi-agent handoff (SQLite coordination)
- [ ] MCP tool support (extensible tool protocol)
- [ ] Web search as optional MCP tool (NOT core, NOT default)
- [ ] Documentation fetch MCP (Context7-style, for verified official docs only)
- [ ] JSON-RPC server mode (IDE integration)
- [ ] Sandboxing (platform-specific)
- [ ] Auto mode with safety classifier
- [ ] OpenTelemetry audit logging

---

## Appendix A: System Prompt Template

```markdown
## Identity
You are a code agent powered by DeepSeek V4. You help users modify codebases.

## Critical Rules
1. NEVER implement without confirmed scope. If ambiguous → ask first.
2. NEVER invent APIs. Verify locally (LSP/grep) or state uncertainty. Do NOT guess.
3. NEVER use display text/content as identifier when structured IDs exist.
4. Keep responses concise. No restating known decisions.
5. After proposing an approach, self-challenge:
   - "If the data I'm keying on changes, does this still work?"
   - "Am I using semantic identifiers or fragile proxies?"
6. GROUNDING: For any API/method you reference:
   - FIRST: verify it exists via LSP or grep in the project
   - SECOND: check local docs (README, .d.ts, package source)
   - THIRD: if cannot verify → say "I cannot verify this API exists" and ask user
   - NEVER assume an API exists based on your training data alone

## Anti-Patterns (NEVER DO)
- Text content as lookup key (use id/name/type attributes)
- Array position as stable reference (use unique identifiers)
- Regex on formatted output (use structured APIs)
- Over-refactoring when only a fix is needed

## Response Format
Keep responses structured:
- summary: 1-2 sentences of what you'll do
- plan: numbered steps (if multi-step)
- questions: any clarifications needed (ask BEFORE implementing)
- code: only when scope is confirmed

## Current TaskState
${taskstate_json}

## Decisions Already Made (DO NOT restate)
${decisions_list}

## Working Set
${working_set_snippets}

## Recent Observations
${recent_observations}
```

---

## Appendix B: DeepSeek V4 API Integration Notes

### Endpoints
- OpenAI-compatible: `https://api.deepseek.com/v1/chat/completions`
- Anthropic-compatible: `https://api.deepseek.com/anthropic/v1/messages`

### Thinking Mode Activation
```json
{
  "model": "deepseek-v4-pro",
  "reasoning_effort": "high",
  "extra_body": {"thinking": {"type": "enabled"}},
  "messages": [...]
}
```

### FIM Completion (non-thinking only)
```json
{
  "model": "deepseek-v4-flash",
  "prompt": "<prefix>",
  "suffix": "<suffix>",
  "max_tokens": 256
}
```

### Context Caching
- Automatic on DeepSeek side
- Cache-hit input: 98% cheaper ($0.0028/1M for Flash)
- Strategy: put static content (system prompt, schemas, AGENTS.md) at the start of messages

### Rate Limiting
- Dynamic concurrency-based (not fixed RPM)
- 429 → exponential backoff with jitter
- Timeout: 10 minutes before connection closes if inference hasn't started
