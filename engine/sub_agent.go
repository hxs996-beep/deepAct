package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/deepact/deepact/context/promptset"
)

const (
	defaultSubAgentContext = 1_048_576 // ~1M — match main engine context window
)

// subAgentUniverseNames is the closed set of tools a sub-agent may ever be
// offered or run: sub-agents are read-only, and all modification work
// (bash/write/edit/revert, skill_install, MCP tools) belongs to the main
// agent, whose bash calls still pass the danger guard. Channels
// (handoff_to_agent, ask_user, submit_result) are injected separately by
// filterTools and are not listed here. The set is closed: tools absent from
// it — new built-ins, every MCP "<server>_<tool>" name — are excluded from
// sub-agents; unknown never means allowed.
var subAgentUniverseNames = []string{"fetch", "glob", "grep", "lsp", "read", "read_multi", "web_search"}

var subAgentUniverse = func() map[string]bool {
	m := make(map[string]bool, len(subAgentUniverseNames))
	for _, n := range subAgentUniverseNames {
		m[n] = true
	}
	return m
}()

// isSubAgentChannelTool reports whether name is an always-injected delegation
// channel, exempt from the universe intersection.
func isSubAgentChannelTool(name string) bool {
	return name == HandoffToolName || name == AskUserToolName || name == SubmitResultToolName
}

// universeViolations returns the subset of names outside the read-only
// universe. Channels are always allowed.
func universeViolations(names []string) []string {
	var bad []string
	for _, n := range names {
		if isSubAgentChannelTool(n) || subAgentUniverse[n] {
			continue
		}
		bad = append(bad, n)
	}
	return bad
}

// ValidateSubAgentTools checks names against the sub-agent read-only universe.
// The error is user- and model-facing guidance: it names the violations, the
// universe, and where modification work belongs, so both a startup config
// mistake and a delegating model's tool override can be self-corrected.
func ValidateSubAgentTools(names []string) error {
	bad := universeViolations(names)
	if len(bad) == 0 {
		return nil
	}
	return fmt.Errorf("tools not available to sub-agents: [%s]. Sub-agents are read-only; available tools: [%s]. Remove the write-class tools (bash/write/edit/revert, skill_install, MCP tools) — modification work belongs to the main agent",
		strings.Join(bad, ", "), strings.Join(subAgentUniverseNames, ", "))
}

// SubAgentRunner runs the generic sub-agent loop.
// It is shared by all agent types; specialists inject extra system prompt content.
type SubAgentRunner struct {
	workDir          string // project root for tool execution (resolves relative paths)
	sessionID        string // session identifier for tool context
	model            ModelClient
	tools            ToolExecutor
	registry         *AgentRegistry
	modelName        string // default (Pro) model
	flashModelName   string // Flash model for cheaper agents
	maxContextTokens int    // context window limit; 0 = use defaultSubAgentContext
	maxOutputTokens  int    // per-turn completion cap; 0 = use DefaultMaxOutputTokens
	reasoningEffort  string // DeepSeek thinking effort for sub-agent calls; "" = engine default (high)
	onProgress       ProgressFunc
	compressor       *CompressionOrchestrator
	// register hands a suspended run back to the engine's job table (see
	// SetSuspendedRegistrar). nil = suspensions are not registered.
	register func(*SuspendedRun, AgentID, string) string
	// partitionURL derives a per-sub-agent prefix-cache partition URL from a
	// partition name. Injected by cmd/run.go (which can import llm) so engine stays
	// llm-free. nil = sub-agents share the main agent's endpoint (no isolation).
	partitionURL func(partition string) string
	// partitionSeq is a session-global atomic counter giving each sub-agent run a
	// unique partition suffix, so parallel sub-agents get distinct prefix-cache
	// partitions instead of overwriting each other's cached system prefix.
	partitionSeq atomic.Int64
	langPackZh   string // Chinese language pack (Go/Python rules in zh)
	langPackEn   string // English language pack (Go/Python rules in en)
	maxDepth     int    // absolute delegation-depth cap; 0 = default 2
	// tokenBudget caps a run's billable tokens (cache-miss + completion).
	// Three-state: 0 = default (2× the context window), -1 = explicit
	// unlimited, >0 = explicit cap. Set from [context].sub_agent_token_budget.
	tokenBudget int
}

// NewSubAgentRunner creates a runner with the given LLM client, tool executor, and agent registry.
func NewSubAgentRunner(model ModelClient, tools ToolExecutor, registry *AgentRegistry, modelName string) *SubAgentRunner {
	return &SubAgentRunner{
		model:     model,
		tools:     tools,
		registry:  registry,
		modelName: modelName,
	}
}

// SetFlashModel sets the Flash model name for agents that should use a cheaper model.
func (r *SubAgentRunner) SetFlashModel(name string) {
	r.flashModelName = name
}

// SetRegistry sets the agent registry on the runner after creation.
// Used to break circular dependencies during initialization.
func (r *SubAgentRunner) SetRegistry(reg *AgentRegistry) {
	r.registry = reg
}

// SetOnProgress sets the progress callback for sub-agent execution visibility.
func (r *SubAgentRunner) SetOnProgress(fn ProgressFunc) {
	r.onProgress = fn
}

// SetWorkDir sets the project root directory for tool execution.
func (r *SubAgentRunner) SetWorkDir(dir string) {
	r.workDir = dir
}

// SetSessionID sets the session identifier for tool execution context.
func (r *SubAgentRunner) SetSessionID(id string) {
	r.sessionID = id
}

// SetMaxContextTokens overrides the default context window limit for this runner.
func (r *SubAgentRunner) SetMaxContextTokens(tokens int) {
	r.maxContextTokens = tokens
}

// SetMaxOutputTokens overrides the per-turn completion cap for sub-agents.
func (r *SubAgentRunner) SetMaxOutputTokens(tokens int) {
	r.maxOutputTokens = tokens
}

// SetReasoningEffort sets the DeepSeek thinking effort for sub-agent calls.
// Empty string = engine default (high).
func (r *SubAgentRunner) SetReasoningEffort(effort string) {
	r.reasoningEffort = effort
}

func (r *SubAgentRunner) outputTokenCap() int {
	if r.maxOutputTokens > 0 {
		return r.maxOutputTokens
	}
	return DefaultMaxOutputTokens
}

// SetCompressor sets the CompressionOrchestrator for layered compression (same as main agent).
// When set, replaces the simple compressSubHistory with the full 4-layer strategy.
func (r *SubAgentRunner) SetCompressor(c *CompressionOrchestrator) {
	r.compressor = c
}

// SetSuspendedRegistrar injects the engine's suspension registry, so nested
// runs (depth >= 1) can hand their suspended state back to the job table —
// SubAgentRunner deliberately holds no *Engine reference. nil = nested runs do
// not suspend (they behave exactly as they do today).
func (r *SubAgentRunner) SetSuspendedRegistrar(fn func(*SuspendedRun, AgentID, string) string) {
	r.register = fn
}

// SetSubAgentPartitionURL injects a function that derives a per-sub-agent prefix-cache
// partition URL from a partition name (e.g. "sub-0-3"). The injected function closes over
// the base URL and API key; it lives in cmd/run.go so engine stays free of the llm package.
// Passing nil disables isolation (sub-agents share the main agent's endpoint).
func (r *SubAgentRunner) SetSubAgentPartitionURL(fn func(partition string) string) {
	r.partitionURL = fn
}

// SetMaxDepth caps how deep sub-agent nesting may go. 0 resets to the default (2).
func (r *SubAgentRunner) SetMaxDepth(d int) {
	if d <= 0 {
		d = 2
	}
	r.maxDepth = d
}

// SetSubAgentTokenBudget sets the runner-level token budget for sub-agent
// runs. Three-state: 0 = default (2× the effective context window), -1 =
// unlimited, >0 = explicit cap. A Handoff/AgentSpec value takes precedence
// over this runner default (see tokenBudgetFor).
func (r *SubAgentRunner) SetSubAgentTokenBudget(b int) {
	r.tokenBudget = b
}

// MaxDepth returns the current nesting cap.
func (r *SubAgentRunner) MaxDepth() int {
	if r.maxDepth <= 0 {
		return 2
	}
	return r.maxDepth
}

// SetLangPacks sets both language variants of the language-specific rules.
// Called once at startup from cmd/run.go after language detection.
func (r *SubAgentRunner) SetLangPacks(zh, en string) {
	r.langPackZh = zh
	r.langPackEn = en
}

// contextLimit returns the effective context window limit.
func (r *SubAgentRunner) contextLimit() int {
	if r.maxContextTokens > 0 {
		return r.maxContextTokens
	}
	return defaultSubAgentContext
}

// Run executes a generic sub-agent with the given handoff.
// MaxIterations <= 0 means no turn cap — the loop runs until a normal
// completion or a built-in guard (stalled narration, loop detection, etc.).
func (r *SubAgentRunner) Run(ctx context.Context, input Handoff) (*HandoffResult, error) {
	// The role's stable persona (codex-style developer instructions) is
	// injected as part of the stable system prefix, not the volatile goal,
	// so it stays constant across turns and keeps the prefix cache hot.
	return r.runLoop(ctx, input, input.Persona, input.MaxIterations, input.ModelOverride)
}

// runLoop is the core sub-agent execution loop.
// extraPrompt is additional system-level instructions injected for specialist agents.
// maxIterations caps the number of LLM turns for this agent; 0 = no cap.
// modelOverride, if non-empty, overrides the runner's default model for this run.
// subAgentStreamer guards sub-agent stream_delta emission. Sub-agents use
// non-streaming Complete, so resp.Message.Content is the full response text.
// Emitting it as a stream_delta on every runLoop iteration causes the UI to
// accumulate duplicate blocks (m.streaming += ...), producing repeated text
// and blank-line gaps. maybeEmit emits only the first non-empty content; the
// final answer is still surfaced by the main engine's Summary at run end.
type subAgentStreamer struct {
	streamed bool
}

// maybeEmit emits content as a stream_delta the first time it is called with
// non-empty content and a non-nil onProgress; subsequent calls are no-ops.
func (s *subAgentStreamer) maybeEmit(onProgress ProgressFunc, agentName, content string) {
	if s.streamed || content == "" || onProgress == nil {
		return
	}
	onProgress(ProgressEvent{Type: "stream_delta", Name: agentName, Detail: content})
	s.streamed = true
}

// runLoop is the fresh-run entry point: it builds the run's state from scratch.
func (r *SubAgentRunner) runLoop(ctx context.Context, input Handoff, extraPrompt string, maxIterations int, modelOverride ...string) (*HandoffResult, error) {
	return r.runLoopFrom(ctx, input, extraPrompt, maxIterations, nil, modelOverride...)
}

// runLoopResume continues a SUSPENDED run from its breakpoint (agent_resume).
// `answer` fills the pending tool call: the user's answer for a leaf, or the
// child's result digest for an intermediate layer. The pending response is
// written BEFORE the loop body runs — the body injects nudges at the top of an
// iteration, so an unfilled response would leave them sitting between
// assistant(tool_calls) and its tool response (an API-contract violation).
func (r *SubAgentRunner) runLoopResume(ctx context.Context, s *SuspendedRun, answer string) (*HandoffResult, error) {
	rs := *s
	rs.History = fillPendingToolResponse(s, answer)
	return r.runLoopFrom(ctx, rs.Input, rs.Input.Persona, rs.Input.MaxIterations, &rs)
}

// RunSuspended continues a suspended run on behalf of the engine (which reaches
// it through the role that owns the run). See runLoopResume.
func (r *SubAgentRunner) RunSuspended(ctx context.Context, s *SuspendedRun, answer string) (*HandoffResult, error) {
	return r.runLoopResume(ctx, s, answer)
}

// runLoopFrom is the shared loop. resume == nil builds a fresh run; otherwise
// the run continues from the breakpoint, and everything derivable from it is
// reused instead of re-derived (see the prologue).
func (r *SubAgentRunner) runLoopFrom(ctx context.Context, input Handoff, extraPrompt string, maxIterations int, resume *SuspendedRun, modelOverride ...string) (*HandoffResult, error) {
	if input.Depth > r.MaxDepth() {
		return &HandoffResult{
			Summary:      fmt.Sprintf("Max agent nesting depth (%d) exceeded. Cannot delegate further.", r.MaxDepth()),
			Blocked:      true,
			BlockedBy:    "max_depth",
			FinishReason: HandoffReasonMaxDepth,
		}, nil
	}

	// ---- Prologue: per-RUN state, derived exactly once ----------------------
	// A resumed run takes it from the breakpoint instead: re-deriving the model
	// and partition would throw away the prefix cache (and bump partitionSeq),
	// rebuilding the history would drop everything the run had established, and
	// zeroing the counters would hand back budget that was already spent.
	structured := input.StructuredResult
	model := r.model
	partitionName := ""
	var history []ModelMessage
	var totalUsage ModelUsage
	modelName := r.modelName
	iter := 0
	budgetNudgedTokens := false
	if resume != nil {
		model = resume.Model
		partitionName = resume.Partition
		history = resume.History
		totalUsage = resume.Spent
		modelName = resume.ModelName
		iter = resume.Iter
		budgetNudgedTokens = resume.BudgetNudgedTokens
	} else {
		// Fork model client for an isolated client instance (no shared state with
		// the parent agent).
		if f, ok := r.model.(interface{ Fork() ModelClient }); ok {
			model = f.Fork()
		}
		// Derive a per-run prefix-cache partition so this sub-agent's calls get their
		// own DeepSeek cache partition keyed by the URL query param. The partition name
		// combines the agent name, nesting depth, and a unique session-global sequence
		// number — parallel sub-agents therefore never overwrite each other's cached
		// system prefix (their volatile prompts differ, but their shared system prefix
		// stays hot within each partition). No partitionURL = no isolation.
		if r.partitionURL != nil {
			seq := r.partitionSeq.Add(1)
			pName := string(input.Agent)
			if pName == "" {
				pName = "sub"
			}
			partitionName = fmt.Sprintf("%s-%d-%d", pName, input.Depth, seq)
			if f, ok := model.(interface{ ForkWithBaseURL(string) ModelClient }); ok {
				model = f.ForkWithBaseURL(r.partitionURL(partitionName))
			}
		}

		// Stable system message — identical across all sub-agent calls → prefix cache hit
		// Role persona (extraPrompt) — appended to the system prefix so it stays
		// constant across the sub-agent's turns → prefix cache hit per role.
		// Volatile content (goal/context/constraints) — changes per call → cache miss (unavoidable)
		system := r.stableSystemPrompt(input.UserLanguage)
		if extraPrompt != "" {
			system += "\n\n" + extraPrompt
		}
		history = []ModelMessage{
			{Role: "system", Content: system},
		}
		if volatileContent := r.buildVolatilePrompt(input); volatileContent != "" {
			history = append(history, ModelMessage{Role: "user", Content: volatileContent})
		}
		// Structured run: attach the scoped submit_result tool and its requirement
		// as the trailing (highest-recency) instruction. From here on the loop
		// only completes through a valid submission — termination never depends
		// on an LLM judgment call (mirrors the harness structured_output).
		if structured {
			history = append(history, ModelMessage{Role: "user", Content: submitResultInstruction(zhFromLang(input.UserLanguage))})
		}
	}

	// Model resolution. A fresh run derives it from the role's override; a
	// resumed run keeps the effective model it had at the breakpoint (it may
	// already have escalated flash→Pro), and re-derives only isFlashAgent from
	// that model so the escalation path still knows the run started on flash.
	isFlashAgent := false // 标记 agent 是否被分配为 Flash（用于失败升级回退）
	if resume == nil {
		if len(modelOverride) > 0 && modelOverride[0] != "" {
			if modelOverride[0] == "flash" && r.flashModelName != "" {
				modelName = r.flashModelName
				isFlashAgent = true
			} else {
				modelName = modelOverride[0]
			}
		}
	} else {
		isFlashAgent = r.flashModelName != "" && modelName == r.flashModelName
	}

	// Deterministic completion (C5): no LLM ConclusionClassifier probe.
	// A text-only reply never completes a sub-agent run through an LLM
	// judgment call — completion is deterministic: submit_result (structured),
	// the critic VERDICT line, NoNudge, or tool calls that lead to them.
	// Narration is 3-strike nudged below and ends with stalled_narration.

	filteredTools := r.filterTools(input.Tools, input.UserLanguage)

	if structured {
		filteredTools = append(filteredTools, submitResultToolSpec(zhFromLang(input.UserLanguage)))
	}

	// Execution gate set: the shared registry resolves any registered name by
	// lookup, so an out-of-set call — hallucinated or injected — must be
	// refused against this set at dispatch time. Visible-set filtering alone is
	// not a security boundary.
	effectiveSet := make(map[string]bool, len(filteredTools))
	effectiveNames := make([]string, 0, len(filteredTools))
	for _, spec := range filteredTools {
		effectiveSet[spec.Function.Name] = true
		effectiveNames = append(effectiveNames, spec.Function.Name)
	}

	agentName := string(input.Agent)
	if agentName == "" {
		agentName = "sub"
	}
	limit := r.contextLimit()
	compressThreshold := limit * 95 / 100
	// Reset on resume (deliberate): the narration/truncation/blocked streaks and
	// the per-run streamer are not part of the breakpoint — a resumed run may
	// take up to 3 fresh strikes and may re-emit one stream_delta.
	consecutiveIntermediate := 0
	consecutiveTruncation := 0
	// consecutiveBlocked counts gate-refused calls with no real dispatch in
	// between; 3 in a row ends the run with loop_detected. A real dispatch
	// anywhere resets it. Blocked turns still count toward MaxIterations.
	consecutiveBlocked := 0
	streamer := subAgentStreamer{}
	budgetNudged := false // 预算尾段收尾提示只注入一次（见下方循环内）
	// tokenNudgePending 标记 80% 已越线，提示延迟到下一轮迭代顶部注入：累加
	// 点位于 assistant(tool_calls) 与其 tool 结果之间，在那里插入 user 消息会
	// 破坏"tool 响应紧跟其请求消息"的相邻性。
	tokenNudgePending := false
	// 0 = no turn cap (default); >0 = explicit cap set by the delegating agent.
	// A resumed run continues from the breakpoint's iteration count, so a capped
	// run does NOT get a fresh MaxIterations budget.
	for ; maxIterations <= 0 || iter < maxIterations; iter++ {
		select {
		case <-ctx.Done():
			return &HandoffResult{
				Summary:      "(cancelled)",
				Blocked:      true,
				BlockedBy:    "cancelled",
				FinishReason: HandoffReasonCancelled,
				Usage:        &totalUsage,
			}, nil
		default:
		}
		// Compress history using layered strategy (same as main agent) when compressor is set.
		// Falls back to simple truncation if compressor is nil.
		if r.compressor != nil {
			tokens := r.compressor.EstimateTokens(history)
			if tokens > 0 {
				layer, should := r.compressor.ShouldCompress(tokens, limit)
				if should {
					if compressed, err := r.compressor.CompressModelMessages(ctx, layer, input.Goal, history); err == nil {
						history = compressed
					}
				}
			}
		} else if estimatedTokens(history) > compressThreshold {
			history = compressSubHistory(history)
		}

		if r.onProgress != nil {
			r.onProgress(ProgressEvent{Type: "thinking", Name: agentName, Detail: fmt.Sprintf("%s: turn %d", agentName, iter)})
		}

		// Budget-tail wrap-up nudge: when the run has a finite iteration cap, a
		// research-type sub-agent that keeps exploring with tools will otherwise
		// exhaust the budget into a fallback "(analysis timed out, partial
		// result)" / "子代理未产出最终结论" summary with no real conclusion.
		// Near the end of the budget, tell it to stop exploring and wrap up so
		// it converges before the hard cap. Injected at most once; the message
		// stays in history for the remaining turns. The structured variant
		// directs the model to submit_result (plain text never completes a
		// structured run).
		if maxIterations > 0 && iter >= maxIterations-2 && !budgetNudged {
			history = append(history, ModelMessage{
				Role:    "user",
				Content: budgetTailNudge(zhFromLang(input.UserLanguage), maxIterations-iter, structured),
			})
			budgetNudged = true
		}

		// Token-budget wrap-up nudge: armed at the 80% crossing below and
		// injected here, at the top of the next iteration. It must NOT be
		// appended at the accumulation point — that sits between an
		// assistant(tool_calls) message and its tool results, and the API
		// requires tool responses to follow the message that requested them.
		// Injected at most once.
		if tokenNudgePending {
			budget := tokenBudgetFor(input, r)
			spent := totalUsage.CacheMissTokens + totalUsage.CompletionTokens
			history = append(history, ModelMessage{
				Role:    "user",
				Content: tokenBudgetNudge(zhFromLang(input.UserLanguage), budget-spent, structured),
			})
			budgetNudgedTokens = true
			tokenNudgePending = false
		}

		req := ModelRequest{
			Model:           modelName,
			Messages:        history,
			Tools:           filteredTools,
			MaxTokens:       r.outputTokenCap(),
			ReasoningEffort: r.reasoningEffort, // thinking effort, configured via [model].reasoning_effort; "" = engine default high
		}

		// Heartbeat — emit periodic progress during the blocking LLM call so the UI
		// doesn't appear frozen. Stops automatically when Complete returns.
		heartbeatDone := make(chan struct{})
		if r.onProgress != nil {
			go func() {
				ticker := time.NewTicker(2 * time.Second)
				defer ticker.Stop()
				for {
					select {
					case <-ticker.C:
						r.onProgress(ProgressEvent{Type: "thinking", Name: agentName, Detail: fmt.Sprintf("%s: thinking...", agentName)})
					case <-heartbeatDone:
						return
					}
				}
			}()
		}

		// No synthetic per-call deadline: Complete is streaming, and a
		// TOTAL-duration limit mis-kills normal slow streams (large context,
		// long generation) as "(sub-agent error: context deadline exceeded)" —
		// a stream that keeps returning content can still exceed 120s total.
		// Real hangs are caught by the client's own guards: SSE idle timeout
		// (DefaultIdleTimeout=60s, aborts only when no data line arrives),
		// ResponseHeaderTimeout, and DialTimeout.
		resp, err := model.Complete(ctx, req)
		close(heartbeatDone)
		if err != nil {
			// Don't crash the parent session — return a graceful degradation.
			summary := r.summarizeHistory(history, input.Goal)
			return &HandoffResult{
				Summary:      "(sub-agent error: " + err.Error() + ") \n" + summary,
				Blocked:      true,
				BlockedBy:    "sub_agent_error",
				FinishReason: HandoffReasonError,
				Usage:        &totalUsage,
			}, nil
		}

		// Update progress with what the agent is actually working on
		if r.onProgress != nil {
			if len(resp.Message.ToolCalls) > 0 {
				toolNames := make([]string, 0, len(resp.Message.ToolCalls))
				for _, tc := range resp.Message.ToolCalls {
					toolNames = append(toolNames, tc.Function.Name)
				}
				r.onProgress(ProgressEvent{Type: "thinking", Name: agentName, Detail: fmt.Sprintf("%s: %s", agentName, strings.Join(toolNames, ", "))})
			} else if resp.Message.Content != "" {
				preview := firstLine(resp.Message.Content, 60)
				r.onProgress(ProgressEvent{Type: "thinking", Name: agentName, Detail: fmt.Sprintf("%s: %s", agentName, preview)})
				// Stream full content for progressive display -- but only once
				// per runLoop. Subsequent text-only rounds re-emit the same
				// full body; without this guard the UI accumulates duplicates.
				streamer.maybeEmit(r.onProgress, agentName, resp.Message.Content)
			}
		}
		totalUsage.PromptTokens += resp.Usage.PromptTokens
		totalUsage.CompletionTokens += resp.Usage.CompletionTokens
		totalUsage.TotalTokens += resp.Usage.TotalTokens
		totalUsage.CacheHitTokens += resp.Usage.CacheHitTokens
		totalUsage.CacheMissTokens += resp.Usage.CacheMissTokens

		msg := resp.Message

		// Output-cap truncation (finish_reason == "length"): the response was
		// cut off, so it may end with a partially streamed tool call whose
		// arguments never closed. The partial text is NOT a conclusion — never
		// classify it, and never execute the (possibly incomplete) calls. The
		// run resumes with "继续" and gives up with reason="max_tokens" only
		// after repeated truncations.
		truncated := resp.FinishReason == "length"
		if truncated {
			msg.ToolCalls = nil
		}

		history = append(history, msg)

		// Token budget: checked every turn right after usage accumulation,
		// before any branch — a run can neither spend nor loop past it. The
		// check is soft at the call boundary (at most one LLM call of
		// overshoot); the partial history is already in place, so the
		// summarizeHistory below carries the run's findings so far.
		if budget := tokenBudgetFor(input, r); budget > 0 {
			spent := totalUsage.CacheMissTokens + totalUsage.CompletionTokens
			if spent >= budget {
				return &HandoffResult{
					Summary:      r.summarizeHistory(history, input.Goal),
					FinishReason: HandoffReasonBudgetExceeded,
					Usage:        &totalUsage,
				}, nil
			}
			if !budgetNudgedTokens && spent >= budget*80/100 {
				// Arm only; the message is injected at the top of the next
				// iteration so tool responses stay adjacent to their request.
				tokenNudgePending = true
			}
		}

		// No tool calls → agent may be done
		if len(msg.ToolCalls) == 0 {
			if truncated {
				consecutiveTruncation++
				if consecutiveTruncation >= 3 {
					result := r.buildResult(msg.Content, input.Goal)
					result.FinishReason = HandoffReasonMaxTokens
					result.Usage = &totalUsage
					return result, nil
				}
				history = append(history, ModelMessage{
					Role:    "user",
					Content: pickPrompt(zhFromLang(input.UserLanguage), "Continue.", "继续。"),
				})
				continue
			}
			// Structured run: text alone never completes. Nudge toward
			// submit_result — no classifier probe, no ambiguity. This MUST
			// take precedence over NoNudge: harness roles (collab/ratd) pass
			// NoNudge:true while the generic sub-agent forces
			// StructuredResult:true. A lenient "any text is the answer" mode
			// must not bypass the deterministic structured contract ("only
			// submit_result counts") — otherwise a plain-text narration
			// becomes the final summary and the harness parse fails.
			if structured {
				consecutiveIntermediate++
				if consecutiveIntermediate >= 3 {
					result := r.buildResult(msg.Content, input.Goal)
					result.FinishReason = HandoffReasonNoResult
					result.Usage = &totalUsage
					return result, nil
				}
				content := getSubmitResultNudge(zhFromLang(input.UserLanguage))
				history = append(history, ModelMessage{
					Role:    "user",
					Content: content,
				})
				continue
			}
			if input.NoNudge {
				result := r.buildResult(msg.Content, input.Goal)
				result.Usage = &totalUsage
				return result, nil
			}
			// Deterministic completion (C5): a text-only reply NEVER completes
			// through an LLM judgment call. Text-only output is narration →
			// 3-strike nudge → stalled_narration.
			consecutiveIntermediate++
			if consecutiveIntermediate >= 3 {
				// Break — model keeps producing text without acting
				result := r.buildResult(msg.Content, input.Goal)
				result.FinishReason = HandoffReasonStalledNarration
				result.Usage = &totalUsage
				return result, nil
			}
			// 失败回退升级：Flash agent 连续输出文本无 tool call → 升级到 Pro 重试
			if consecutiveIntermediate >= 2 && isFlashAgent && r.modelName != "" && modelName != r.modelName {
				modelName = r.modelName // 升级到 Pro
				history = append(history, ModelMessage{
					Role:    "user",
					Content: "The Flash model is having difficulty producing structured output. Escalating to Pro model. Please complete the task now.",
				})
				consecutiveIntermediate = 0
				continue
			}
			// Give one more chance with a nudge
			content := getNudgeMessage(input.Goal)
			history = append(history, ModelMessage{
				Role:    "user",
				Content: content,
			})
			continue
		}
		// dispatchedAny tracks whether this turn dispatched at least one tool
		// call through the execution gate; only then is the narration streak
		// reset below — a gate-blocked call is not progress.
		dispatchedAny := false

		// Process tool calls
		calls := make([]ToolCallRequest, 0, len(msg.ToolCalls))
		for _, tc := range msg.ToolCalls {
			if tc.Function.Name == "" {
				continue
			}
			calls = append(calls, ToolCallRequest{
				ID:    tc.ID,
				Name:  tc.Function.Name,
				Input: json.RawMessage(tc.Function.Arguments),
			})
		}

		if len(calls) == 0 {
			result := r.buildResult(msg.Content, input.Goal)
			result.Usage = &totalUsage
			return result, nil
		}

		// Structured-run terminal gate: submit_result is the ONLY way a
		// structured run completes. A valid submission ends the run
		// immediately with the submitted summary; an invalid one (missing or
		// empty summary) is refused as a tool error so the model retries.
		// Sibling calls in the same message are skipped either way (mirrors
		// the parent's task_complete interception and the harness
		// structured_output monotonic guard).
		if structured {
			if idx := submitCallIndex(calls); idx >= 0 {
				call := calls[idx]
				var params SubmitResultParams
				if err := json.Unmarshal(call.Input, &params); err != nil || strings.TrimSpace(params.Summary) == "" {
					// msg is already appended to history above; close every
					// tool_call_id so the next request is well-formed.
					for _, c := range calls {
						content := "Blocked: submit_result requires a non-empty summary string. Other calls were skipped."
						if c.ID == call.ID {
							content = "Blocked: submit_result failed parameter validation — retry with a non-empty summary."
						}
						history = append(history, ModelMessage{
							Role:       "tool",
							ToolCallID: c.ID,
							Content:    content,
						})
					}
					continue
				}
				for _, c := range calls {
					content := "Skipped: submit_result was called."
					if c.ID == call.ID {
						content = "Result submitted."
					}
					history = append(history, ModelMessage{
						Role:       "tool",
						ToolCallID: c.ID,
						Content:    content,
					})
				}
				result := r.buildResult(params.Summary, input.Goal)
				if len(params.Conclusions) > 0 {
					result.Conclusions = params.Conclusions
				}
				result.Usage = &totalUsage
				return result, nil
			}
		}

		// Per-file repetition is no longer blocked here: the engine reports the
		// repeat count to the model as a fact (see Engine.annotateRepeats) and
		// the model decides how to proceed.

		for _, call := range calls {
			// Execution gate: refuse calls outside this run's offered set. The
			// registry would resolve them anyway (it never checks the offered
			// list), so this is the only deterministic refusal point for a
			// hallucinated or injected out-of-set call.
			if !effectiveSet[call.Name] {
				consecutiveBlocked++
				history = append(history, ModelMessage{
					Role:       "tool",
					ToolCallID: call.ID,
					Content:    blockedToolMessage(call.Name, effectiveNames, zhFromLang(input.UserLanguage)),
				})
				if consecutiveBlocked >= 3 {
					return &HandoffResult{
						Summary:      r.summarizeHistory(history, input.Goal),
						FinishReason: HandoffReasonLoopDetected,
						Usage:        &totalUsage,
					}, nil
				}
				continue
			}
			consecutiveBlocked = 0
			dispatchedAny = true
			if r.onProgress != nil {
				r.onProgress(ProgressEvent{Type: "tool_start", Name: call.Name, Detail: summarizeArgs(call.Name, call.Input, r.workDir)})
			}
			// ask_user: the sub-agent needs user input. Validate, record a tool
			// response, then end the run with awaiting_user so the questions
			// bubble to the parent engine.
			if call.Name == AskUserToolName {
				q, ok := parseAskUserInput(call.Input)
				if !ok {
					history = append(history, ModelMessage{
						Role: "tool", ToolCallID: call.ID,
						Content: "Error: ask_user requires a non-empty question (options 2-6 when provided).",
					})
					continue
				}
				history = append(history, ModelMessage{
					Role: "tool", ToolCallID: call.ID,
					Content: "✓ 已记录问题，等待用户回答。",
				})
				// Carry what the run established so far alongside the question:
				// nothing persists this run's history, and the parent
				// re-delegates after the user answers — without the findings it
				// would restart from zero.
				return &HandoffResult{
					Summary:      awaitingUserSummary(zhFromLang(input.UserLanguage), q.Question, lastAssistantText(history)),
					Questions:    []string{q.Question},
					FinishReason: HandoffReasonAwaitingUser,
					Usage:        &totalUsage,
					// Leaf path: the ask_user placeholder is already in history, so
					// resume REPLACES its content with the user's answer.
					Suspended: &SuspendedRun{
						History:            history,
						Question:           q.Question,
						PendingToolID:      call.ID,
						PendingToolMissing: false,
						Input:              input,
						Partition:          partitionName,
						Model:              model,
						Spent:              totalUsage,
						Iter:               iter,
						BudgetNudgedTokens: budgetNudgedTokens,
						ModelName:          modelName,
					},
				}, nil
			}
			env := ToolExecContext{WorkDir: r.workDir, SessionID: r.sessionID, Ctx: ctx, Depth: input.Depth + 1, UserLang: input.UserLanguage}
			results := r.tools.Execute(env, []ToolCallRequest{call})
			if len(results) > 0 {
				res := results[0]
				if r.onProgress != nil {
					r.onProgress(ProgressEvent{Type: "tool_done", Name: res.ToolName, Detail: briefDigest(res.Digest), FullDetail: res.Digest})
				}
				// Nested bubble: a child's handoff result carried questions →
				// stop and bubble them up.
				if len(res.Questions) > 0 {
					// Intermediate path: this run is waiting for its CHILD, not
					// for the user. The tool response for the handoff call was
					// never written (we return above the append below), so resume
					// APPENDS it once the child delivers. Only the leaf may be
					// answered by the user — see SuspendedRun and I8.
					return &HandoffResult{
						Summary:      res.Digest,
						Questions:    res.Questions,
						FinishReason: HandoffReasonAwaitingUser,
						Usage:        &totalUsage,
						Suspended: &SuspendedRun{
							History:            history,
							PendingToolID:      res.ToolCallID,
							PendingToolMissing: true,
							ChildRunID:         res.RunID,
							Input:              input,
							Partition:          partitionName,
							Model:              model,
							Spent:              totalUsage,
							Iter:               iter,
							BudgetNudgedTokens: budgetNudgedTokens,
							ModelName:          modelName,
						},
					}, nil
				}
				// cancelled results are not written to history: the run is
				// unwinding (ctx cancelled) and no further LLM call will follow.
				if res.Status != "cancelled" {
					history = append(history, ModelMessage{
						Role: "tool", ToolCallID: res.ToolCallID, Content: res.Digest,
					})
				}
			}
		}
		if dispatchedAny {
			consecutiveIntermediate = 0
		}
	}

	// Max iterations reached — extract whatever findings the agent accumulated
	summary := r.summarizeHistory(history, input.Goal)
	reason := HandoffReasonMaxIterations
	if structured {
		// A structured run that burned its whole budget without submitting a
		// result delivered nothing — report that explicitly.
		reason = HandoffReasonNoResult
	}
	return &HandoffResult{
		Summary:      summary,
		TimedOut:     true,
		FinishReason: reason,
		Usage:        &totalUsage,
	}, nil
}

// submitCallIndex returns the index of the submit_result call in calls, or -1.
func submitCallIndex(calls []ToolCallRequest) int {
	for i, c := range calls {
		if c.Name == SubmitResultToolName {
			return i
		}
	}
	return -1
}

// getSubmitResultNudge directs a structured run's text-only turn to the
// terminal tool instead of the generic "use tools" nudge: the tool call is
// the only recognized completion for this run.
func getSubmitResultNudge(zh bool) string {
	if zh {
		return "请立即调用 submit_result 提交你的最终结论（summary 必填）。不要继续输出纯文本或调用其他工具。"
	}
	return "Call submit_result now to report your final result (summary is required). Do not continue with plain text or further tool calls."
}

func firstLine(s string, max int) string {
	if idx := strings.IndexByte(s, '\n'); idx > 0 {
		s = s[:idx]
	}
	r := []rune(s)
	if len(r) > max {
		return string(r[:max]) + "…"
	}
	return s
}

func (r *SubAgentRunner) summarizeHistory(history []ModelMessage, goal string) string {
	// On an interruption (per-call timeout, loop guard, or iteration cap) the
	// model's last real text is its output, so it is returned verbatim as the
	// partial result — no length or shape heuristic decides what counts as a
	// conclusion.
	if text := lastAssistantText(history); text != "" {
		return "(analysis timed out, partial result)\n" + text
	}
	// Fallback: no substantive assistant text was produced (per-call timeout,
	// loop guard, or iteration cap). Return a concise readable failure instead
	// of dumping the first line of every raw tool result — those file paths
	// and code lines flooded /collab's stage outputs and summary, making them
	// unreadable.
	var count int
	for _, msg := range history {
		if msg.Role == "tool" && msg.Content != "" {
			count++
		}
	}
	if msgIsChinese(goal) {
		return fmt.Sprintf("子代理未产出最终结论（已执行 %d 次工具调用后中断）。请重试或缩小任务范围。", count)
	}
	return fmt.Sprintf("Sub-agent produced no final conclusion (interrupted after %d tool calls). Retry or narrow the task.", count)
}

// fillPendingToolResponse returns the breakpoint history with the pending tool
// call answered. ONE implementation for both paths, because the API contract is
// one rule: a tool response must follow the assistant(tool_calls) that
// requested it, with nothing in between.
//   - placeholder present (leaf ask_user): replace its content in place;
//   - placeholder missing (the nested-bubble path returns before writing it):
//     append at the end — where it still follows the requesting message, even
//     when the tail is a sibling's response (e.g. one assistant emitting
//     [read, handoff_to_agent]).
//
// A history that does not actually contain the pending call is returned
// unchanged: never fabricate a tool response (that would violate the contract).
func fillPendingToolResponse(s *SuspendedRun, answer string) []ModelMessage {
	hist := s.History
	if s.PendingToolID == "" {
		return hist
	}
	if !s.PendingToolMissing {
		for i := len(hist) - 1; i >= 0; i-- {
			if hist[i].Role == "tool" && hist[i].ToolCallID == s.PendingToolID {
				out := append([]ModelMessage(nil), hist...)
				out[i].Content = answer
				return out
			}
		}
		return hist
	}
	if !hasPendingCall(hist, s.PendingToolID) {
		return hist
	}
	return append(append([]ModelMessage(nil), hist...), ModelMessage{
		Role: "tool", ToolCallID: s.PendingToolID, Content: answer,
	})
}

// hasPendingCall reports whether an assistant message requested toolCallID and
// no tool response for it has been written yet.
func hasPendingCall(history []ModelMessage, toolCallID string) bool {
	req := -1
	for i, m := range history {
		if m.Role != "assistant" {
			continue
		}
		for _, tc := range m.ToolCalls {
			if tc.ID == toolCallID {
				req = i
			}
		}
	}
	if req < 0 {
		return false
	}
	for i := req + 1; i < len(history); i++ {
		if history[i].Role == "tool" && history[i].ToolCallID == toolCallID {
			return false
		}
	}
	return true
}

// lastAssistantText returns the model's most recent non-empty text, or "" when
// the run produced none (e.g. it stopped to ask the user as its first action).
// This is the single definition of "what the run established so far", shared by
// summarizeHistory and the awaiting-user path.
func lastAssistantText(history []ModelMessage) string {
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Role == "assistant" && history[i].Content != "" {
			return history[i].Content
		}
	}
	return ""
}

// awaitingUserSummary composes the summary of a run that stopped to ask the
// user. That channel stores no history and the parent re-delegates once the
// user answers, so whatever the run established must travel with the result —
// otherwise the re-delegated run restarts from zero. The question stays
// separate in HandoffResult.Questions (the UI presents it); this text is what
// the parent reads in the handoff digest. The findings section is omitted when
// the run had not established anything yet.
func awaitingUserSummary(zh bool, question, findings string) string {
	findings = strings.TrimSpace(findings)
	if findings == "" {
		if zh {
			return fmt.Sprintf("问题：%s", question)
		}
		return fmt.Sprintf("Question: %s", question)
	}
	if zh {
		return fmt.Sprintf("问题：%s\n\n已有发现：\n%s", question, findings)
	}
	return fmt.Sprintf("Question: %s\n\nFindings so far:\n%s", question, findings)
}

// stableSystemPrompt returns the full system prompt shared by all sub-agents.
// Combines the main system prompt (rules, examples, language pack) with the sub-agent role suffix.
// Identical across every sub-agent call in the session → enables prefix cache hits.
func (r *SubAgentRunner) stableSystemPrompt(userLang string) string {
	prompts := promptset.Get()
	langPack := r.langPackEn
	if userLang == "中文" {
		langPack = r.langPackZh
	}
	base := prompts.System + "\n\n" + prompts.Examples
	if langPack != "" {
		// Same header as the main system prompt (see ContextAssembler.Build) so
		// one strip rule covers both.
		base += "\n\n# Language Pack\n" + langPack
	}
	return base + "\n\n" + prompts.SubAgent
}

// buildVolatilePrompt assembles the per-call variable content (goal, context, constraints).
// This is appended as the last user message, after stable system + agent-specific prompts.
func (r *SubAgentRunner) buildVolatilePrompt(input Handoff) string {
	zh := zhFromLang(input.UserLanguage)
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("%s\n%s\n\n", pickPrompt(zh, "## Goal", "## 目标"), input.Goal))
	if input.ExpectedOutput != "" {
		sb.WriteString(fmt.Sprintf("%s\n%s\n\n", pickPrompt(zh, "## Expected Output", "## 预期输出"), input.ExpectedOutput))
	}
	if input.Context != "" {
		sb.WriteString(fmt.Sprintf("%s\n%s\n\n", pickPrompt(zh, "## Context", "## 上下文"), input.Context))
	}
	if len(input.Constraints) > 0 {
		sb.WriteString(fmt.Sprintf("%s\n- %s\n\n", pickPrompt(zh, "## Constraints", "## 约束"), strings.Join(input.Constraints, "\n- ")))
	}
	return sb.String()
}

// filterTools returns a tool spec list filtered to the allowed tools. The
// handoff_to_agent and ask_user tools are ALWAYS included — delegation and
// user-questions are core sub-agent capabilities that an allowList must not
// strip (the old code always prepended handoff; /ratd and /collab pass a
// read-only allowList but their members still need to delegate and ask).
// Non-channel specs are additionally restricted to the read-only universe:
// an allowList can only narrow, never widen. The constructed specs are used
// for both so the registry copy (if present) is not duplicated.
func (r *SubAgentRunner) filterTools(allowList []string, userLang string) []ModelTool {
	all := r.tools.Specs()
	// Prefer the registered SubAgentTool spec (dynamic role enum) so sub-agents
	// see the same roles as the main agent when they delegate further. Fall back
	// to the static handoffToolSpec if the tool is not registered.
	handoffSpec := handoffToolSpec(zhFromLang(userLang))
	for _, spec := range all {
		if spec.Function.Name == HandoffToolName {
			handoffSpec = spec
			break
		}
	}
	result := []ModelTool{handoffSpec}
	result = append(result, askUserToolSpec(zhFromLang(userLang)))

	if len(allowList) == 0 {
		for _, spec := range all {
			if spec.Function.Name == HandoffToolName || spec.Function.Name == AskUserToolName {
				continue // already added above
			}
			if !subAgentUniverse[spec.Function.Name] {
				continue // read-only universe: excluded from sub-agents
			}
			result = append(result, spec)
		}
		return result
	}

	allowSet := make(map[string]bool, len(allowList))
	for _, name := range allowList {
		allowSet[name] = true
	}
	for _, spec := range all {
		if spec.Function.Name == HandoffToolName || spec.Function.Name == AskUserToolName {
			continue // already added above
		}
		if !subAgentUniverse[spec.Function.Name] {
			continue // read-only universe: excluded from sub-agents
		}
		if allowSet[spec.Function.Name] {
			result = append(result, spec)
		}
	}
	return result
}

// blockedToolMessage composes the tool-result text for a call refused by the
// execution gate. It is educational: naming this run's effective tools stops
// the model from burning turns probing other out-of-set names.
func blockedToolMessage(name string, effective []string, zh bool) string {
	avail := strings.Join(effective, ", ")
	if zh {
		return fmt.Sprintf("Blocked: %s 对子代理不可用。子代理是只读的；本 run 可用工具：%s。修改类操作属于主代理。", name, avail)
	}
	return fmt.Sprintf("Blocked: %s is not available to sub-agents. Sub-agents are read-only; tools available in this run: %s. Modification work belongs to the main agent.", name, avail)
}

// buildResult builds the handoff result from the agent's final text response.
// Conclusions are carried only by the structured submit_result tool (see the
// params.Conclusions path in runLoop) — the free-text path has no reliable
// structural signal, so nothing is inferred from line prefixes.
func (r *SubAgentRunner) buildResult(content string, goal string) *HandoffResult {
	result := &HandoffResult{
		Summary:      content,
		Conclusions:  make([]string, 0),
		FinishReason: HandoffReasonCompleted,
	}

	// If goal is short (< 80 chars), include it as artifact reference
	if len(goal) < 80 {
		result.Artifacts = []string{fmt.Sprintf("goal: %s", goal)}
	}

	return result
}

// estimatedTokens returns a rough token count for a slice of model messages.
// Uses len/4 heuristic (no external estimator dependency).
func estimatedTokens(history []ModelMessage) int {
	total := 0
	for _, msg := range history {
		total += len(msg.Content) / 4
		total += len(msg.ReasoningContent) / 4
		for _, tc := range msg.ToolCalls {
			total += len(tc.ID) / 4
			total += len(tc.Function.Name) / 4
			total += len(tc.Function.Arguments) / 4
		}
		total += 10 // per-message overhead
	}
	return total
}

// compressSubHistory reduces history size when approaching context limits.
// Keeps system (index 0) and first user message (index 1) intact.
// For older turns (beyond the latest 3 assistant+tool groups), tool result
// content is truncated to a short summary.
func compressSubHistory(history []ModelMessage) []ModelMessage {
	if len(history) <= 4 {
		return history
	}

	// Reserve indices 0 (system) and 1 (first user) — always keep intact
	stable := history[:2]
	rest := history[2:]

	// Group rest into turns: [assistant, tool...] pairs. Walk backward from the
	// end so turns[0] is the MOST RECENT turn (kept fresh) and later indices are
	// progressively older.
	type turn struct {
		start int
		end   int
	}
	var turns []turn
	for i := len(rest) - 1; i >= 0; i-- {
		if rest[i].Role != "assistant" {
			continue
		}
		// assistant marks the start of a turn; all tool messages after it
		// belong to this turn.
		end := i + 1
		for end < len(rest) && rest[end].Role == "tool" {
			end++
		}
		turns = append(turns, turn{start: i, end: end})
	}

	// Keep the most recent 20 turns verbatim; older turns have their tool
	// results truncated (assistant messages and tool_call_ids stay intact).
	keepTurns := 20
	if keepTurns > len(turns) {
		keepTurns = len(turns)
	}
	fresh := turns[:keepTurns]

	// Build result: stable + compressed old turns + fresh turns.
	// Map fresh turn indices to actual ranges.
	freshRange := make(map[int]bool)
	for _, ft := range fresh {
		for j := ft.start; j < ft.end; j++ {
			freshRange[j] = true
		}
	}

	result := make([]ModelMessage, 0, len(stable)+len(rest))
	result = append(result, stable...)
	for idx := 0; idx < len(rest); idx++ {
		if freshRange[idx] {
			result = append(result, rest[idx])
		} else if rest[idx].Role == "tool" {
			// Stale tool result: keep the message (role + tool_call_id) so the
			// DeepSeek assistant(tool_calls) → tool response contract holds, but
			// cut the digest body to a compact preview.
			result = append(result, truncateToolResult(rest[idx]))
		} else {
			result = append(result, rest[idx])
		}
	}
	return result
}

// subAgentToolResultCap bounds each stale turn's tool-result body kept verbatim
// after compressSubHistory compresses it (~512 chars ≈ 128 tokens, down from up
// to 25k tokens for a full read). The message itself is preserved — only the
// digest is truncated.
const subAgentToolResultCap = 512

// truncateToolResult shortens a stale tool-result message to a compact preview.
func truncateToolResult(msg ModelMessage) ModelMessage {
	if len(msg.Content) <= subAgentToolResultCap {
		return msg
	}
	msg.Content = msg.Content[:subAgentToolResultCap] + "\n…[truncated: full result was in earlier context]"
	return msg
}

// budgetTailNudge returns a user message telling a research sub-agent to stop
// exploring and wrap up with its findings when the iteration budget is about
// to run out. Without it, a finite-budget research agent burns all its turns
// on tools and falls into the "(analysis timed out, partial result)" fallback
// with no real conclusion. The structured variant directs the model to the
// submit_result terminal tool (plain text never completes a structured run).
func budgetTailNudge(zh bool, remaining int, structured bool) string {
	if zh {
		if structured {
			return fmt.Sprintf("你的子代理迭代预算只剩 %d 轮。请立即停止新的探索，基于已有发现总结最终结论，并调用 submit_result 提交（summary 必填）。", remaining)
		}
		return fmt.Sprintf("你的子代理迭代预算只剩 %d 轮。请立即停止新的探索，基于已有发现直接产出最终结论。", remaining)
	}
	if structured {
		return fmt.Sprintf("You have only %d iterations left. Stop exploring now, summarize your final findings, and call submit_result to report them (summary is required).", remaining)
	}
	return fmt.Sprintf("You have only %d iterations left. Stop exploring now and produce your final conclusion from what you have found.", remaining)
}

// tokenBudgetFor resolves the run's token budget: cache-miss + completion
// tokens. Three-state at every level — 0 = inherit from the next level down,
// -1 = explicit unlimited, >0 = explicit cap — so an explicit unlimited
// (-1) can never silently fall through to a default (the fail-loud intent
// behind "giving up the protection must be written down deliberately").
// Returns 0 for unlimited; the caller treats 0 as "no check".
func tokenBudgetFor(input Handoff, r *SubAgentRunner) int {
	if input.TokenBudget != 0 {
		if input.TokenBudget < 0 {
			return 0
		}
		return input.TokenBudget
	}
	switch {
	case r.tokenBudget < 0:
		return 0
	case r.tokenBudget > 0:
		return r.tokenBudget
	default:
		return 2 * r.contextLimit()
	}
}

// tokenBudgetNudge tells the model how much budget remains and to converge —
// information the model can act on, mirroring budgetTailNudge for iterations.
// The structured variant directs the model to submit_result: a plain text reply
// never completes a structured run, so a nudge that only says "deliver your
// conclusion" would cost the run a turn it may not have.
func tokenBudgetNudge(zh bool, remaining int, structured bool) string {
	if zh {
		if structured {
			return fmt.Sprintf("注意：本 run 的 token 预算只剩约 %d。请立即停止新的探索，基于已有发现总结最终结论，并调用 submit_result 提交（summary 必填）。", remaining)
		}
		return fmt.Sprintf("注意：本 run 的 token 预算只剩约 %d。请停止新的探索，基于已有发现直接产出最终结论并交付。", remaining)
	}
	if structured {
		return fmt.Sprintf("Note: only about %d tokens remain in this run's budget. Stop exploring now, summarize your final findings, and call submit_result to report them (summary is required).", remaining)
	}
	return fmt.Sprintf("Note: only about %d tokens remain in this run's budget. Stop exploring, produce your final conclusion from what you have found, and deliver it now.", remaining)
}

// getNudgeMessage returns a language-appropriate nudge when the sub-agent
// keeps producing text without tool calls.
func getNudgeMessage(goal string) string {
	if msgIsChinese(goal) {
		return "请直接使用工具执行下一步，完成目标后给出最终结论。不要只描述计划。"
	}
	return "Use tools to take the next action. Complete the goal and give your final conclusions. Do not just describe a plan."
}

// parseAskUserInput validates an ask_user call input and returns the question.
// Options are accepted for validation but bubbled as nil (YAGNI: the parent
// presents via the awaiting_user free-input path).
func parseAskUserInput(input json.RawMessage) (struct{ Question string }, bool) {
	var p struct {
		Question string   `json:"question"`
		Options  []string `json:"options"`
	}
	if err := json.Unmarshal(input, &p); err != nil || strings.TrimSpace(p.Question) == "" {
		return struct{ Question string }{}, false
	}
	if len(p.Options) > 0 && (len(p.Options) < 2 || len(p.Options) > 6) {
		return struct{ Question string }{}, false
	}
	for _, o := range p.Options {
		if strings.TrimSpace(o) == "" {
			return struct{ Question string }{}, false
		}
	}
	return struct{ Question string }{Question: p.Question}, true
}
