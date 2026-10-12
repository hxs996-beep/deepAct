# 子代理委派树不变量 实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。
>
> **执行环境约束（deepact）：** 本仓库的 `handoff_to_agent` 子代理是**只读**的（`read/read_multi/grep/glob/lsp/web_search/fetch`，见 `engine/sub_agent.go` 的只读宇宙），既不能写文件也不能跑 `go test`。因此"每个任务派一个子代理去实现"在本仓库里不成立：子代理最多能产出代码文本（`proposer`/`sub` 角色），落盘与验证必须由主代理完成。**推荐内联执行**；若要走子代理，只把它们当作"代码草拟/评审"环节，落盘与 `make test` 仍由主代理执行。

**目标：** 给子代理委派树加三条确定性不变量——在跑宽度（默认 6）、嵌套深度（默认 2，可配置）、单次 Run 的 token 聚合上限（默认 20× 子代理上下文窗口）——三条共用一个准入点和一份计数状态，不引入任何"跑飞判定"启发式。

**架构：** 同步 depth 0、异步、嵌套、续跑四条委派路径全部汇经 `SubAgentRunner.runLoopFrom`（`engine/sub_agent.go:284`），且 registry 中每个 `specSubAgent` 持有同一个 `*SubAgentRunner` 实例（`cmd/run.go:254,289`）。因此宽度计数与 token 聚合都放在 runner 上：入口 `admitRun` 占位/校验，出口 `defer` 释放并记账；Engine 只在 Run 开始时清零聚合、在接近上限时注入一条 pinned 提示。超限一律 fail-loud 拒绝新委派（不排队、不中断在跑的 run）。

**技术栈：** Go（标准库 `sync/atomic`）、现有 engine/config/tools 包结构、`go test -race`。

**规格：** `docs/superpowers/specs/2026-10-12-subagent-fanout-invariants-design.md`

---

## 文件结构

| 文件 | 职责 | 操作 |
|---|---|---|
| `engine/types.go` | `EngineConfig` 新增三个上限字段 | 修改（:104-117 附近） |
| `engine/agent.go` | 新增两个"拒绝"类 `FinishReason` + 标题 | 修改（:35-52、:646-679） |
| `engine/sub_agent.go` | 计数状态、setter、准入/释放、记账、拒绝文案 | 修改（:91-101、:284-364） |
| `engine/loop.go` | `SubAgentBudget` 接口、`EngineDeps`/`Engine` 字段、Run 开始重置 | 修改（:31-47、:49-61、:500-501） |
| `engine/turn.go` | 80% pinned 提示，与 `injectBackgroundJobsSummary` 并列 | 修改（:93、:1848 之后） |
| `config/config.go` | 三个 TOML 键 + 启动期校验 | 修改（:137-150、:415-431） |
| `cmd/run.go` | 把配置接到 runner，把 runner 接到 `EngineDeps` | 修改（:254-290、:395-415） |
| `tools/subagent.go` | `handoff_to_agent` 描述补一句总量语义 | 修改（:69-89） |
| `engine/sub_agent_limits_test.go` | 宽度/深度/token 的全部单测 | 创建 |
| `config/config_test.go` | 三个键的校验测试 | 修改（追加） |

---

## 任务 1：配置键与启动期校验

**文件：**
- 修改：`engine/types.go:104-117`
- 修改：`config/config.go:137-150`（结构体）、`config/config.go:415-431`（Apply 校验）
- 测试：`config/config_test.go`（追加到文件末尾）

- [ ] **步骤 1：编写失败的测试**

追加到 `config/config_test.go` 末尾：

```go
// TestApply_DelegationTreeLimits: the three delegation-tree caps are applied
// when set and left at their engine defaults (0 = default) when omitted.
func TestApply_DelegationTreeLimits(t *testing.T) {
	cfg := &engine.EngineConfig{}
	err := Apply(cfg, &File{Context: contextConfig{
		MaxConcurrentSubAgents: 4,
		MaxSubAgentDepth:       3,
		RunSubAgentTokenBudget: 2000000,
	}})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if cfg.MaxConcurrentSubAgents != 4 {
		t.Errorf("MaxConcurrentSubAgents = %d, want 4", cfg.MaxConcurrentSubAgents)
	}
	if cfg.MaxSubAgentDepth != 3 {
		t.Errorf("MaxSubAgentDepth = %d, want 3", cfg.MaxSubAgentDepth)
	}
	if cfg.RunSubAgentTokenBudget != 2000000 {
		t.Errorf("RunSubAgentTokenBudget = %d, want 2000000", cfg.RunSubAgentTokenBudget)
	}
}

// TestApply_DelegationTreeLimits_Omitted: 0 means "use the engine default", so
// omitting the keys must not write anything into the config.
func TestApply_DelegationTreeLimits_Omitted(t *testing.T) {
	cfg := &engine.EngineConfig{}
	if err := Apply(cfg, &File{}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if cfg.MaxConcurrentSubAgents != 0 || cfg.MaxSubAgentDepth != 0 || cfg.RunSubAgentTokenBudget != 0 {
		t.Errorf("omitted keys must stay 0, got %d/%d/%d",
			cfg.MaxConcurrentSubAgents, cfg.MaxSubAgentDepth, cfg.RunSubAgentTokenBudget)
	}
}

// TestApply_DelegationTreeLimits_Invalid: a zero/negative width or depth and a
// token budget below -1 are typos — fail loud at startup, naming the key and
// the legal values (the form every other [context] key uses).
func TestApply_DelegationTreeLimits_Invalid(t *testing.T) {
	cases := []struct {
		name string
		f    *File
		want string
	}{
		{"negative width", &File{Context: contextConfig{MaxConcurrentSubAgents: -1}}, "max_concurrent_subagents"},
		{"negative depth", &File{Context: contextConfig{MaxSubAgentDepth: -2}}, "max_subagent_depth"},
		{"token below -1", &File{Context: contextConfig{RunSubAgentTokenBudget: -3}}, "run_sub_agent_token_budget"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := Apply(&engine.EngineConfig{}, tc.f)
			if err == nil {
				t.Fatalf("expected a startup error naming %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error must name the key %q, got %q", tc.want, err)
			}
		})
	}
}

// TestApply_DelegationTreeLimits_Unlimited: -1 is the explicit opt-out for the
// Run-level token cap and must survive Apply unchanged (never mistaken for
// "unset, use the default").
func TestApply_DelegationTreeLimits_Unlimited(t *testing.T) {
	cfg := &engine.EngineConfig{}
	if err := Apply(cfg, &File{Context: contextConfig{RunSubAgentTokenBudget: -1}}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if cfg.RunSubAgentTokenBudget != -1 {
		t.Errorf("RunSubAgentTokenBudget = %d, want -1", cfg.RunSubAgentTokenBudget)
	}
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./config/ -run 'TestApply_DelegationTreeLimits' -v -count=1`
预期：编译失败，报 `unknown field 'MaxConcurrentSubAgents' in struct literal of type contextConfig`（以及另外两个字段）。

- [ ] **步骤 3：编写 EngineConfig 字段**

在 `engine/types.go` 的 `MaxSuspendedSubAgents int`（:117）之后插入：

```go
	// MaxConcurrentSubAgents caps how many sub-agent runs may be in flight at
	// once across the WHOLE delegation tree (sync depth 0, async, nested and
	// resumed runs share one counter). 0 = default 6. Wired via
	// [context].max_concurrent_subagents.
	MaxConcurrentSubAgents int
	// MaxSubAgentDepth caps sub-agent nesting. 0 = default 2. Wired via
	// [context].max_subagent_depth.
	MaxSubAgentDepth int
	// RunSubAgentTokenBudget caps the billable tokens (cache-miss + completion;
	// cache hits are free) of every sub-agent run in ONE Run, nested children
	// included. Three-state: 0 = default (20× the sub-agent context window),
	// -1 = unlimited, >0 = explicit cap. Wired via
	// [context].run_sub_agent_token_budget.
	RunSubAgentTokenBudget int
```

- [ ] **步骤 4：编写 TOML 字段**

在 `config/config.go` 的 `MaxSuspendedSubAgents int \`toml:"max_suspended_subagents"\``（:149）之后插入：

```go
	// MaxConcurrentSubAgents caps how many sub-agent runs may be in flight at
	// once across the whole delegation tree. 0 = default 6.
	MaxConcurrentSubAgents int `toml:"max_concurrent_subagents"`
	// MaxSubAgentDepth caps sub-agent nesting. 0 = default 2.
	MaxSubAgentDepth int `toml:"max_subagent_depth"`
	// RunSubAgentTokenBudget caps the billable tokens of all sub-agent runs in
	// one Run (the whole delegation tree). 0 = default (20× the sub-agent
	// context window), -1 = unlimited, >0 = explicit cap.
	RunSubAgentTokenBudget int `toml:"run_sub_agent_token_budget"`
```

- [ ] **步骤 5：编写 Apply 校验**

在 `config/config.go` 的 `MaxSuspendedSubAgents` 校验块（:426-431）之后插入：

```go
	// Delegation-tree caps. 0 means "engine default"; a negative value is a
	// typo — fail loud rather than silently fall back.
	if f.Context.MaxConcurrentSubAgents != 0 {
		if f.Context.MaxConcurrentSubAgents < 1 {
			return fmt.Errorf("[context] max_concurrent_subagents must be positive, got %d", f.Context.MaxConcurrentSubAgents)
		}
		cfg.MaxConcurrentSubAgents = f.Context.MaxConcurrentSubAgents
	}
	if f.Context.MaxSubAgentDepth != 0 {
		if f.Context.MaxSubAgentDepth < 1 {
			return fmt.Errorf("[context] max_subagent_depth must be positive, got %d", f.Context.MaxSubAgentDepth)
		}
		cfg.MaxSubAgentDepth = f.Context.MaxSubAgentDepth
	}
	if f.Context.RunSubAgentTokenBudget != 0 {
		if f.Context.RunSubAgentTokenBudget < -1 {
			return fmt.Errorf("[context] run_sub_agent_token_budget must be -1 (unlimited), 0 (default), or positive, got %d", f.Context.RunSubAgentTokenBudget)
		}
		cfg.RunSubAgentTokenBudget = f.Context.RunSubAgentTokenBudget
	}
```

- [ ] **步骤 6：运行测试验证通过**

运行：`go test ./config/ -run 'TestApply_DelegationTreeLimits' -v -count=1`
预期：4 个测试全部 PASS。

- [ ] **步骤 7：Commit**

```bash
git add engine/types.go config/config.go config/config_test.go
git commit -m "feat(config): add the three delegation-tree caps"
```

---

## 任务 2：两个"拒绝"类 FinishReason

**文件：**
- 修改：`engine/agent.go:35-52`（常量）、`engine/agent.go:646-679`（标题）
- 测试：`engine/sub_agent_limits_test.go`（创建）

- [ ] **步骤 1：编写失败的测试**

创建 `engine/sub_agent_limits_test.go`：

```go
package engine

import (
	"strings"
	"testing"
)

// A refused delegation is NOT a partial result: nothing ran. These two reasons
// carry their own headings so the parent reads "refused, continue" rather than
// the "partial result" wording every run-that-stopped-halfway reason uses.
func TestHandoffReasonHeading_RefusalReasons(t *testing.T) {
	cases := []struct {
		reason string
		want   string
	}{
		{HandoffReasonFanoutLimit, "refused"},
		{HandoffReasonTreeBudgetExceeded, "refused"},
	}
	for _, tc := range cases {
		t.Run(tc.reason, func(t *testing.T) {
			got := handoffReasonHeading(tc.reason, "", false)
			if !strings.Contains(strings.ToLower(got), tc.want) {
				t.Errorf("heading for %q must say %q, got %q", tc.reason, tc.want, got)
			}
			// A refusal must never claim a partial result: that wording tells
			// the parent to salvage an outcome that does not exist.
			if strings.Contains(strings.ToLower(got), "partial") {
				t.Errorf("heading for %q must not say \"partial\", got %q", tc.reason, got)
			}
		})
	}
}

// A refused delegation needs the parent to continue, exactly like a run that
// hit its turn cap — so both reasons must be follow-up reasons.
func TestIsHandoffFollowUpReason_Refusals(t *testing.T) {
	if !isHandoffFollowUpReason(HandoffReasonFanoutLimit) {
		t.Error("fanout_limit must be a follow-up reason")
	}
	if !isHandoffFollowUpReason(HandoffReasonTreeBudgetExceeded) {
		t.Error("tree_budget_exceeded must be a follow-up reason")
	}
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./engine/ -run 'TestHandoffReasonHeading_RefusalReasons|TestIsHandoffFollowUpReason_Refusals' -v -count=1`
预期：编译失败，报 `undefined: HandoffReasonFanoutLimit`（以及 `HandoffReasonTreeBudgetExceeded`）。

- [ ] **步骤 3：加常量**

在 `engine/agent.go` 的 `HandoffReasonBudgetExceeded`（:46）之后插入：

```go
	// HandoffReasonFanoutLimit and HandoffReasonTreeBudgetExceeded are
	// REFUSALS, not failures: the delegation tree was already at its limit and
	// nothing was run, so the result carries no partial output. Their headings
	// must therefore never use the "partial result" wording the run-that-
	// stopped-halfway reasons use — the parent's correct move is to collect a
	// running result or do the step itself.
	HandoffReasonFanoutLimit        = "fanout_limit"         // in-flight width cap reached
	HandoffReasonTreeBudgetExceeded = "tree_budget_exceeded" // this Run's sub-agent token cap exhausted
```

- [ ] **步骤 4：加标题**

在 `engine/agent.go` 的 `handoffReasonHeading` 中，把 `HandoffReasonBudgetExceeded` 的 case（:673 附近）改为在其后追加两个 case：

```go
	case HandoffReasonFanoutLimit:
		return pickPrompt(zh, "Delegation refused: too many sub-agents are already in flight.", "委派被拒：同时在跑的子代理已达上限。")
	case HandoffReasonTreeBudgetExceeded:
		return pickPrompt(zh, "Delegation refused: this Run's sub-agent token budget is exhausted.", "委派被拒：本次 Run 的子代理 token 预算已用完。")
```

- [ ] **步骤 5：运行测试验证通过**

运行：`go test ./engine/ -run 'TestHandoffReasonHeading_RefusalReasons|TestIsHandoffFollowUpReason_Refusals' -v -count=1`
预期：PASS。（`isHandoffFollowUpReason` 的 `default:` 分支已覆盖新 reason，无需改动——该测试是防回归的锚点。）

- [ ] **步骤 6：Commit**

```bash
git add engine/agent.go engine/sub_agent_limits_test.go
git commit -m "feat(engine): add refusal-only handoff finish reasons"
```

---

## 任务 3：runner 的宽度/token 计数器与准入

**文件：**
- 修改：`engine/sub_agent.go:91-101`（结构体字段）、`engine/sub_agent.go:184-208`（setter 区）、`engine/sub_agent.go:284-364`（准入 + 记账）
- 测试：`engine/sub_agent_limits_test.go`（追加）

- [ ] **步骤 1：编写失败的测试**

追加到 `engine/sub_agent_limits_test.go`（把 `import` 块补成 `"context"`、`"strings"`、`"sync"`、`"testing"`）：

```go
// --- test scaffolding -------------------------------------------------------

// gateModel blocks inside Complete until released, so a run holds its in-flight
// slot deterministically instead of racing on scheduling.
type gateModel struct {
	release chan struct{}
	entered chan struct{} // one token per Complete entry
}

func (m *gateModel) Stream(context.Context, ModelRequest) (<-chan ModelChunk, error) {
	return nil, nil
}

func (m *gateModel) Complete(ctx context.Context, _ ModelRequest) (*ModelResponse, error) {
	m.entered <- struct{}{}
	select {
	case <-m.release:
		return &ModelResponse{Message: ModelMessage{Role: "assistant", Content: "已给出结论。"}, FinishReason: "stop"}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// --- width ------------------------------------------------------------------

// TestSubAgentWidth_RefusesBeyondLimit: with the limit held by two runs parked
// inside the model call, a third delegation is REFUSED (not queued) with the
// refusal reason and an educational summary naming the limit.
func TestSubAgentWidth_RefusesBeyondLimit(t *testing.T) {
	model := &gateModel{release: make(chan struct{}), entered: make(chan struct{}, 4)}
	runner := &SubAgentRunner{model: model, tools: readSpecExec(), modelName: "test"}
	runner.SetMaxConcurrentSubAgents(2)

	var wg sync.WaitGroup
	admitted := make([]*HandoffResult, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			res, err := runner.Run(context.Background(), Handoff{Agent: AgentSub, Goal: "g"})
			if err != nil {
				t.Errorf("run %d: %v", i, err)
				return
			}
			admitted[i] = res
		}(i)
	}
	// Both admitted runs are now INSIDE Complete, i.e. holding their slots.
	<-model.entered
	<-model.entered

	refused, err := runner.Run(context.Background(), Handoff{Agent: AgentSub, Goal: "third"})
	if err != nil {
		t.Fatalf("refused run returned an error instead of a result: %v", err)
	}
	if refused.FinishReason != HandoffReasonFanoutLimit {
		t.Fatalf("expected %q, got %q", HandoffReasonFanoutLimit, refused.FinishReason)
	}
	if !strings.Contains(refused.Summary, "2") {
		t.Errorf("refusal must name the limit, got %q", refused.Summary)
	}

	close(model.release)
	wg.Wait()
	for i, res := range admitted {
		if res.FinishReason != HandoffReasonCompleted {
			t.Errorf("admitted run %d: got %q, want completed", i, res.FinishReason)
		}
	}
}

// TestSubAgentWidth_SlotReleasedAfterRun: a finished run frees its slot, so the
// next delegation is admitted. Without the deferred release this hangs on the
// second call (the counter would stay at the limit forever).
func TestSubAgentWidth_SlotReleasedAfterRun(t *testing.T) {
	model := &stubSeqModel{responses: []ModelResponse{
		{Message: ModelMessage{Role: "assistant", Content: "结论一"}, FinishReason: "stop"},
	}}
	runner := &SubAgentRunner{model: model, tools: readSpecExec(), modelName: "test"}
	runner.SetMaxConcurrentSubAgents(1)

	for i := 0; i < 3; i++ {
		res, err := runner.Run(context.Background(), Handoff{Agent: AgentSub, Goal: "g"})
		if err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
		if res.FinishReason != HandoffReasonCompleted {
			t.Fatalf("run %d: got %q, want completed (slot not released?)", i, res.FinishReason)
		}
	}
}

// --- depth ------------------------------------------------------------------

// TestSubAgentDepth_Configurable: the nesting cap is configurable and 0 keeps
// the historical default (2), so existing behavior is unchanged unless the user
// opts in.
func TestSubAgentDepth_Configurable(t *testing.T) {
	// An explicit text-only "stop" response, so the admitted run's outcome does
	// not depend on what an empty response happens to do in the loop.
	model := &stubSeqModel{responses: []ModelResponse{
		{Message: ModelMessage{Role: "assistant", Content: "结论"}, FinishReason: "stop"},
	}}
	runner := &SubAgentRunner{model: model, tools: readSpecExec(), modelName: "test"}
	if got := runner.MaxDepth(); got != 2 {
		t.Fatalf("default MaxDepth = %d, want 2", got)
	}
	runner.SetMaxDepth(1)
	res, err := runner.Run(context.Background(), Handoff{Agent: AgentSub, Goal: "g", Depth: 2})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.FinishReason != HandoffReasonMaxDepth {
		t.Fatalf("expected %q at depth 2 with max 1, got %q", HandoffReasonMaxDepth, res.FinishReason)
	}
	runner.SetMaxDepth(3)
	res, err = runner.Run(context.Background(), Handoff{Agent: AgentSub, Goal: "g", Depth: 2})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.FinishReason == HandoffReasonMaxDepth {
		t.Fatalf("depth 2 must be allowed when max is 3, got %q", res.FinishReason)
	}
}

// --- Run-level token aggregate ---------------------------------------------

// TestSubAgentTreeBudget_RefusesAfterSpent: the aggregate spans runs, not just
// one run — after the tree has spent its Run budget, the next delegation is
// refused with the refusal reason, and the spend is reported on the runner.
func TestSubAgentTreeBudget_RefusesAfterSpent(t *testing.T) {
	model := &stubSeqModel{responses: []ModelResponse{{
		Message:      ModelMessage{Role: "assistant", Content: "结论"},
		FinishReason: "stop",
		Usage:        ModelUsage{CacheMissTokens: 600, TotalTokens: 600},
	}}}
	runner := &SubAgentRunner{model: model, tools: readSpecExec(), modelName: "test"}
	runner.SetRunTokenBudget(1000)
	runner.StartRun()

	first, err := runner.Run(context.Background(), Handoff{Agent: AgentSub, Goal: "a"})
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	if first.FinishReason != HandoffReasonCompleted {
		t.Fatalf("first run: got %q, want completed", first.FinishReason)
	}
	if got := runner.Spent(); got != 600 {
		t.Fatalf("Spent after one 600-token run = %d, want 600", got)
	}

	second, err := runner.Run(context.Background(), Handoff{Agent: AgentSub, Goal: "b"})
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if second.FinishReason != HandoffReasonCompleted {
		t.Fatalf("second run must still be admitted (600 < 1000): got %q", second.FinishReason)
	}

	third, err := runner.Run(context.Background(), Handoff{Agent: AgentSub, Goal: "c"})
	if err != nil {
		t.Fatalf("third run: %v", err)
	}
	if third.FinishReason != HandoffReasonTreeBudgetExceeded {
		t.Fatalf("third run: got %q, want %q", third.FinishReason, HandoffReasonTreeBudgetExceeded)
	}
	if third.Summary == "" {
		t.Error("refusal must explain itself")
	}
}

// TestSubAgentTreeBudget_StartRunResets: the aggregate is per-Run, so a new Run
// gets a fresh budget (otherwise the cap would silently become a session cap).
func TestSubAgentTreeBudget_StartRunResets(t *testing.T) {
	model := &stubSeqModel{responses: []ModelResponse{{
		Message:      ModelMessage{Role: "assistant", Content: "结论"},
		FinishReason: "stop",
		Usage:        ModelUsage{CacheMissTokens: 1500, TotalTokens: 1500},
	}}}
	runner := &SubAgentRunner{model: model, tools: readSpecExec(), modelName: "test"}
	runner.SetRunTokenBudget(1000)

	runner.StartRun()
	if _, err := runner.Run(context.Background(), Handoff{Agent: AgentSub, Goal: "a"}); err != nil {
		t.Fatal(err)
	}
	if res, _ := runner.Run(context.Background(), Handoff{Agent: AgentSub, Goal: "b"}); res.FinishReason != HandoffReasonTreeBudgetExceeded {
		t.Fatalf("expected the cap to bite in Run 1, got %q", res.FinishReason)
	}

	runner.StartRun()
	if res, _ := runner.Run(context.Background(), Handoff{Agent: AgentSub, Goal: "c"}); res.FinishReason != HandoffReasonCompleted {
		t.Fatalf("a new Run must start with a fresh budget, got %q", res.FinishReason)
	}
}

// TestSubAgentTreeBudget_Unlimited: -1 is an explicit opt-out and must not be
// replaced by the default.
func TestSubAgentTreeBudget_Unlimited(t *testing.T) {
	model := &stubSeqModel{responses: []ModelResponse{{
		Message:      ModelMessage{Role: "assistant", Content: "结论"},
		FinishReason: "stop",
		Usage:        ModelUsage{CacheMissTokens: 9_000_000, TotalTokens: 9_000_000},
	}}}
	runner := &SubAgentRunner{model: model, tools: readSpecExec(), modelName: "test"}
	runner.SetRunTokenBudget(-1)
	runner.StartRun()
	for i := 0; i < 3; i++ {
		res, err := runner.Run(context.Background(), Handoff{Agent: AgentSub, Goal: "g"})
		if err != nil {
			t.Fatal(err)
		}
		if res.FinishReason != HandoffReasonCompleted {
			t.Fatalf("run %d: unlimited budget must never refuse, got %q", i, res.FinishReason)
		}
	}
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./engine/ -run 'TestSubAgentWidth_|TestSubAgentDepth_Configurable|TestSubAgentTreeBudget_' -count=1`
预期：编译失败，报 `runner.SetMaxConcurrentSubAgents undefined`、`runner.SetRunTokenBudget undefined`、`runner.StartRun undefined`、`runner.Spent undefined`。

- [ ] **步骤 3：加计数状态与默认值**

在 `engine/sub_agent.go` 的 `SubAgentRunner` 结构体末尾（`tokenBudget int`，:101 之后）插入：

```go
	// --- Delegation-tree invariants --------------------------------------
	// Every delegation path (sync depth 0, async, nested, resumed) runs
	// through this ONE runner instance, so one counter bounds them all.
	//
	// inFlight counts runs currently executing. A run parked on ask_user
	// leaves runLoopFrom and therefore holds no slot — the same rule the async
	// outstanding cap uses (see dispatchAsync).
	inFlight atomic.Int64
	// treeSpent is the billable token total (cache-miss + completion) of every
	// sub-agent run in the CURRENT Run, nested children included. Reset by the
	// Engine at Run start via StartRun; never model-settable.
	treeSpent atomic.Int64
	// treeBudget caps treeSpent: 0 = default, -1 = unlimited, >0 = explicit.
	treeBudget atomic.Int64
	// maxConcurrent caps inFlight: 0 = defaultMaxConcurrentSubAgents.
	maxConcurrent atomic.Int64
	// treeNudged marks the one-shot near-limit notice for the current Run.
	treeNudged atomic.Bool
```

在同文件的常量区（`defaultSubAgentContext`，:15-17 附近）追加：

```go
const (
	// defaultMaxConcurrentSubAgents bounds how many sub-agent runs may be in
	// flight at once across the whole delegation tree. Six matches the 2-6
	// directions a /collab round fans out, so normal parallel research is
	// unaffected; how that budget is split between width and nesting is the
	// model's call.
	defaultMaxConcurrentSubAgents = 6
	// defaultTreeBudgetWindows is the Run-level token cap expressed in
	// sub-agent context windows (~20M with a 1M window). Its job is to bound a
	// runaway Run, not to be a daily budget.
	defaultTreeBudgetWindows = 20
)
```

- [ ] **步骤 4：加 setter 与查询**

在 `engine/sub_agent.go` 的 `SetMaxDepth` 附近（:184-208）插入：

```go
// SetMaxConcurrentSubAgents caps how many sub-agent runs may be in flight at
// once across the whole delegation tree. 0 resets to the default (6).
func (r *SubAgentRunner) SetMaxConcurrentSubAgents(n int) {
	r.maxConcurrent.Store(int64(n))
}

func (r *SubAgentRunner) maxConcurrentSubAgents() int {
	if n := r.maxConcurrent.Load(); n > 0 {
		return int(n)
	}
	return defaultMaxConcurrentSubAgents
}

// SetRunTokenBudget caps the billable tokens of every sub-agent run in one Run
// (the whole delegation tree). Three-state: 0 = default (20 context windows),
// -1 = explicit unlimited, >0 = explicit cap. The model can neither set nor
// widen it — it only ever sees the remaining amount in the near-limit notice.
func (r *SubAgentRunner) SetRunTokenBudget(n int) {
	r.treeBudget.Store(int64(n))
}

// Budget resolves the effective Run-level cap; 0 means unlimited.
func (r *SubAgentRunner) Budget() int64 {
	switch n := r.treeBudget.Load(); {
	case n > 0:
		return n
	case n < 0:
		return 0
	default:
		return int64(defaultTreeBudgetWindows) * int64(r.contextLimit())
	}
}

// Spent reports the billable tokens the current Run's delegation tree has used.
func (r *SubAgentRunner) Spent() int64 { return r.treeSpent.Load() }

// StartRun resets the Run-scoped aggregate and its one-shot notice. Called by
// the Engine at Run start so the cap covers exactly one Run.
func (r *SubAgentRunner) StartRun() {
	r.treeSpent.Store(0)
	r.treeNudged.Store(false)
}

// ClaimBudgetNotice reports whether the near-limit notice still has to be
// emitted this Run, and consumes it. One-shot by design: the notice is
// information the model acts on, not a per-turn banner.
func (r *SubAgentRunner) ClaimBudgetNotice() bool { return r.treeNudged.CompareAndSwap(false, true) }

// admitRun reserves an in-flight slot and checks the Run-level token budget.
// nil = admitted, and the caller MUST defer releaseRunSlot. A non-nil result is
// the fail-loud refusal handed back to the delegating model: nothing ran, so it
// carries no partial output and the summary says how to proceed.
func (r *SubAgentRunner) admitRun(input Handoff) *HandoffResult {
	zh := zhFromLang(input.UserLanguage)
	limit := r.maxConcurrentSubAgents()
	for {
		cur := r.inFlight.Load()
		if int(cur) >= limit {
			return &HandoffResult{
				Summary:      fanoutRefusalText(zh, limit),
				Blocked:      true,
				BlockedBy:    HandoffReasonFanoutLimit,
				FinishReason: HandoffReasonFanoutLimit,
			}
		}
		if r.inFlight.CompareAndSwap(cur, cur+1) {
			break
		}
	}
	// Checked AFTER taking the slot so the refusal cannot be raced past by two
	// runs entering together; the slot is released before returning.
	if budget := r.Budget(); budget > 0 && r.treeSpent.Load() >= budget {
		r.inFlight.Add(-1)
		return &HandoffResult{
			Summary:      treeBudgetRefusalText(zh, r.treeSpent.Load(), budget),
			Blocked:      true,
			BlockedBy:    HandoffReasonTreeBudgetExceeded,
			FinishReason: HandoffReasonTreeBudgetExceeded,
		}
	}
	return nil
}

func (r *SubAgentRunner) releaseRunSlot() { r.inFlight.Add(-1) }

// fanoutRefusalText explains a width refusal. It names the limit and the ways
// forward, because the refusal is not a dead end: the parent can collect a
// running result or do the step itself, then delegate again.
func fanoutRefusalText(zh bool, limit int) string {
	if zh {
		return fmt.Sprintf("委派被拒：整个委派树已有 %d 个子代理在跑（上限 %d）。先取一个结果（list_agents / wait_agent / agent_poll），或你自己完成这一步，然后再委派。", limit, limit)
	}
	return fmt.Sprintf("Delegation refused: %d sub-agents are already in flight across the delegation tree (limit %d). Collect a result first (list_agents / wait_agent / agent_poll) or do this step yourself, then delegate again.", limit, limit)
}

// treeBudgetRefusalText explains a Run-budget refusal and, crucially, what is
// still available: tokens already spent cannot be reclaimed, but results of
// runs that are still working can.
func treeBudgetRefusalText(zh bool, spent, budget int64) string {
	if zh {
		return fmt.Sprintf("委派被拒：本次 Run 的子代理 token 预算已用完（已用 %d / %d）。请基于已有结果继续；仍在运行的子代理结果仍可用 agent_poll 收取。", spent, budget)
	}
	return fmt.Sprintf("Delegation refused: this Run's sub-agent token budget is exhausted (%d of %d spent). Continue from the results you already have; runs still in flight can still be collected with agent_poll.", spent, budget)
}
```

- [ ] **步骤 5：接上准入与记账**

在 `engine/sub_agent.go` 的 `runLoopFrom` 里做两处插入。

第一处：紧接在既有的深度检查块（:285-292）之后：

```go
	// Delegation-tree admission: width, then the Run-level token aggregate.
	// The refusal is returned BEFORE any per-run state is built (no history, no
	// model fork, no partition), so a refused delegation is cheap and
	// deterministic. Resumed runs take a slot too — they are in flight again.
	if refused := r.admitRun(input); refused != nil {
		return refused, nil
	}
	defer r.releaseRunSlot()
```

第二处：紧接在 prologue 的 `if resume != nil { ... } else { ... }` 块结束之后（即 `// Model resolution.` 注释之前，:365 附近）：

```go
	// Tree accounting: this run's billable tokens join the Run aggregate on
	// EVERY exit path. A resumed run already contributed everything it spent
	// before parking, so only the delta since the breakpoint counts — without
	// the subtraction each resume would double-count its pre-suspension spend.
	baseSpent := 0
	if resume != nil {
		baseSpent = resume.Spent.CacheMissTokens + resume.Spent.CompletionTokens
	}
	defer func() {
		r.treeSpent.Add(int64(totalUsage.CacheMissTokens + totalUsage.CompletionTokens - baseSpent))
	}()
```

- [ ] **步骤 6：运行测试验证通过**

运行：`go test ./engine/ -run 'TestSubAgentWidth_|TestSubAgentDepth_Configurable|TestSubAgentTreeBudget_' -v -count=1`
预期：6 个测试全部 PASS。

- [ ] **步骤 7：跑既有子代理测试防回归**

运行：`go test ./engine/ -run 'TestSubAgent' -count=1 -race`
预期：PASS。既有测试用默认上限（宽度 6 / 深度 2 / token 20×），不应出现新的拒绝。

- [ ] **步骤 8：Commit**

```bash
git add engine/sub_agent.go engine/sub_agent_limits_test.go
git commit -m "feat(engine): bound sub-agent fan-out, depth and per-Run tokens"
```

---

## 任务 4：Engine 接线（Run 开始重置 + 接近上限提示）

**文件：**
- 修改：`engine/loop.go:31-47`（`EngineDeps`）、`engine/loop.go:49-61`（`Engine`）、`engine/loop.go:500-501`（Run 开始）
- 修改：`engine/turn.go:93`（调用点）、`engine/turn.go:1848` 之后（新函数）
- 测试：`engine/sub_agent_limits_test.go`（追加）

- [ ] **步骤 1：编写失败的测试**

追加到 `engine/sub_agent_limits_test.go`：

```go
// --- engine wiring ----------------------------------------------------------

// budgetStub is a scripted SubAgentBudget, so the Engine-side reset and notice
// can be tested without a live runner.
type budgetStub struct {
	spent      int64
	budget     int64
	starts     int
	noticeLeft int // how many ClaimBudgetNotice calls still return true
}

func (b *budgetStub) StartRun() { b.starts++; b.spent = 0; b.noticeLeft = 1 }
func (b *budgetStub) Spent() int64 { return b.spent }
func (b *budgetStub) Budget() int64 { return b.budget }
func (b *budgetStub) ClaimBudgetNotice() bool {
	if b.noticeLeft <= 0 {
		return false
	}
	b.noticeLeft--
	return true
}

// TestEngineRun_ResetsSubAgentBudget: every Run must start with a clean
// delegation-tree aggregate, or the cap silently becomes a session cap.
func TestEngineRun_ResetsSubAgentBudget(t *testing.T) {
	budget := &budgetStub{budget: 1000, spent: 999, noticeLeft: 0}
	e := newTestEngineWithBudget(t, budget)
	if _, err := e.Run(context.Background(), "你好"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if budget.starts == 0 {
		t.Error("Run must call SubAgentBudget.StartRun")
	}
}

// TestEngineRun_SubAgentBudgetNoticeOnce: past 80% the pinned notice fires
// exactly once, names both numbers, and does not survive into the next turn's
// history (pendingPinnedMessages semantics).
func TestEngineRun_SubAgentBudgetNoticeOnce(t *testing.T) {
	budget := &budgetStub{budget: 1000, spent: 900, noticeLeft: 1}
	e := newTestEngineWithBudget(t, budget)
	e.isChinese = true
	e.langDetected = true

	e.injectSubAgentBudgetNotice()
	if len(e.pendingPinnedMessages) != 1 {
		t.Fatalf("expected exactly one pinned notice, got %d", len(e.pendingPinnedMessages))
	}
	got := e.pendingPinnedMessages[0]
	if !strings.Contains(got, "900") || !strings.Contains(got, "1000") {
		t.Errorf("notice must name spent and budget, got %q", got)
	}
	e.pendingPinnedMessages = nil

	// Second call: already claimed this Run → no second banner.
	e.injectSubAgentBudgetNotice()
	if len(e.pendingPinnedMessages) != 0 {
		t.Errorf("the near-limit notice must be one-shot, got %d", len(e.pendingPinnedMessages))
	}
}

// TestEngineRun_SubAgentBudgetNoticeSilent: below the threshold, when the cap
// is unlimited, and when no budget surface is wired, the notice stays silent.
func TestEngineRun_SubAgentBudgetNoticeSilent(t *testing.T) {
	cases := []struct {
		name   string
		budget SubAgentBudget
	}{
		{"below threshold", &budgetStub{budget: 1000, spent: 100, noticeLeft: 1}},
		{"unlimited", &budgetStub{budget: 0, spent: 100_000, noticeLeft: 1}},
		{"not wired", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := &Engine{subBudget: tc.budget}
			e.injectSubAgentBudgetNotice()
			if len(e.pendingPinnedMessages) != 0 {
				t.Errorf("expected no notice, got %v", e.pendingPinnedMessages)
			}
		})
	}
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./engine/ -run 'TestEngineRun_(ResetsSubAgentBudget|SubAgentBudgetNotice)' -count=1`
预期：编译失败，报 `undefined: SubAgentBudget`、`unknown field subBudget in struct literal`、`undefined: newTestEngineWithBudget`。

- [ ] **步骤 3：定义接口与依赖**

在 `engine/loop.go` 的 `EngineDeps` 定义之前（:31 之前）插入：

```go
// SubAgentBudget is the delegation-tree budget surface the Engine owns across a
// Run: it clears the Run-scoped aggregate at Run start and reports the spend
// for the near-limit notice. *SubAgentRunner implements it; cmd/run.go injects
// it through EngineDeps. nil = no aggregate (tests, embeddings) and the notice
// stays silent.
type SubAgentBudget interface {
	StartRun()
	Spent() int64
	Budget() int64
	ClaimBudgetNotice() bool
}
```

在 `EngineDeps` 的 `Agents *AgentRegistry`（:38）之后插入：

```go
	// SubAgents, when set, is the shared sub-agent budget surface. Optional:
	// nil means the Engine neither resets nor reports the Run aggregate.
	SubAgents SubAgentBudget
```

- [ ] **步骤 4：加 Engine 字段**

在 `engine/loop.go` 的 `Engine` 结构体 `agents *AgentRegistry`（:56）之后插入：

```go
	// subBudget is the shared delegation-tree budget (nil when not wired).
	subBudget SubAgentBudget
```

在 `NewEngine` 的结构体字面量里（`agents: deps.Agents,`，:385）之后插入：

```go
		subBudget:    deps.SubAgents,
```

- [ ] **步骤 5：Run 开始重置**

在 `engine/loop.go` 的 `Run` 中，紧接 `e.runUsageAccum = ModelUsage{}`（:501）之后插入：

```go
	// The delegation tree's token aggregate is per-Run, exactly like the
	// background tasks that feed it: a new Run starts with a fresh budget.
	if e.subBudget != nil {
		e.subBudget.StartRun()
	}
```

- [ ] **步骤 6：加提示函数与调用点**

在 `engine/turn.go` 的 `injectBackgroundJobsSummary` 之后（:1886 之后）插入：

```go
// injectSubAgentBudgetNotice appends a one-shot pinned notice when this Run's
// delegation tree has spent 80% of its token budget, so the model knows the
// remaining total BEFORE it delegates again. This is information, not a guard:
// the same principle as the per-run tokenBudgetNudge — the model decides how to
// converge, the engine only reports. No-op when no budget surface is wired,
// when the cap is unlimited, when the threshold is not reached, or when the
// notice already fired this Run.
func (e *Engine) injectSubAgentBudgetNotice() {
	if e.subBudget == nil {
		return
	}
	budget := e.subBudget.Budget()
	if budget <= 0 {
		return
	}
	spent := e.subBudget.Spent()
	if spent*5 < budget*4 { // below 80%
		return
	}
	if !e.subBudget.ClaimBudgetNotice() {
		return
	}
	remaining := budget - spent
	if remaining < 0 {
		remaining = 0
	}
	if e.isChinese {
		e.pendingPinnedMessages = append(e.pendingPinnedMessages, fmt.Sprintf(
			"注意：本次 Run 的子代理 token 总量已用 %d / %d（剩余约 %d）。请基于已有结果收敛，或用你自己的工具完成剩余工作。", spent, budget, remaining))
		return
	}
	e.pendingPinnedMessages = append(e.pendingPinnedMessages, fmt.Sprintf(
		"Note: this Run's sub-agent token total is at %d / %d (~%d left). Converge on the results you have, or do the remaining work with your own tools.", spent, budget, remaining))
}
```

在 `engine/turn.go:93` 的 `e.injectBackgroundJobsSummary()` 之后插入：

```go
	e.injectSubAgentBudgetNotice()
```

- [ ] **步骤 7：加测试辅助**

把测试辅助追加到 `engine/sub_agent_limits_test.go`（`newTestEngine` 的既有形态见 `engine/ask_user_test.go:143`）：

```go
// newTestEngineWithBudget builds a minimal Engine whose single turn answers with
// plain text, with the given delegation-budget surface wired. The field set
// mirrors the other Engine literals in this package (there is no shared helper):
// Run dereferences context, state, config and guards, so all four are required.
func newTestEngineWithBudget(t *testing.T, budget SubAgentBudget) *Engine {
	t.Helper()
	return &Engine{
		model: &stubStreamModel{chunks: []ModelChunk{
			{Delta: "任务已完成。", FinishReason: "stop"},
		}},
		tools:     stubToolExecutor{},
		context:   steerContextBuilder{},
		state:     &TaskState{TaskID: "test"},
		config:    EngineConfig{ModelName: "test-model"},
		isChinese: true,
		guards:    &GuardSystem{scope: NewScopeGuard()},
		subBudget: budget,
	}
}
```

- [ ] **步骤 8：运行测试验证通过**

运行：`go test ./engine/ -run 'TestEngineRun_(ResetsSubAgentBudget|SubAgentBudgetNotice)' -v -count=1`
预期：3 个测试全部 PASS。

- [ ] **步骤 9：Commit**

```bash
git add engine/loop.go engine/turn.go engine/sub_agent_limits_test.go
git commit -m "feat(engine): reset and report the per-Run sub-agent budget"
```

---

## 任务 5：cmd/run.go 接线

**文件：**
- 修改：`cmd/run.go:254-290`（runner 配置）、`cmd/run.go:390-415`（`EngineDeps` 字面量）

- [ ] **步骤 1：接上 runner 的三个配置**

在 `cmd/run.go` 的 `runner.SetSubAgentTokenBudget(config.SubAgentTokenBudget)`（:268）之后插入：

```go
	runner.SetMaxConcurrentSubAgents(config.MaxConcurrentSubAgents)
	runner.SetMaxDepth(config.MaxSubAgentDepth)
	runner.SetRunTokenBudget(config.RunSubAgentTokenBudget)
```

- [ ] **步骤 2：把 runner 交给 Engine**

在构建 `EngineDeps` 的字面量里，`Agents: agentReg,` 之后插入：

```go
		SubAgents: runner,
```

（`runner` 即 `NewSubAgentRunner` 的返回值，`*SubAgentRunner` 已实现 `SubAgentBudget`。）

- [ ] **步骤 3：编译验证**

运行：`go build ./...`
预期：无输出（成功）。若报 `cannot use runner (variable of type *SubAgentRunner) as engine.SubAgentBudget value`，说明任务 3 的四个方法名与接口不一致，按接口签名修正 runner 的方法名。

- [ ] **步骤 4：端到端冒烟**

运行：`go test ./engine/ ./config/ -count=1 -race`
预期：PASS。

- [ ] **步骤 5：Commit**

```bash
git add cmd/run.go
git commit -m "feat(cmd): wire the delegation-tree caps from config"
```

---

## 任务 6：模型可见面（handoff_to_agent 描述）

**文件：**
- 修改：`tools/subagent.go:69-89`（`Spec()` 的 Description）
- 测试：`tools/subagent_test.go`（追加）

- [ ] **步骤 1：编写失败的测试**

追加到 `tools/subagent_test.go`：

```go
// TestSubAgentToolSpec_DeclaresTreeLimit: the model plans around a bounded
// delegation tree only if the tool description says the bound exists. The exact
// number is deliberately not embedded (it is configurable, and the refusal
// digest names it when it matters).
func TestSubAgentToolSpec_DeclaresTreeLimit(t *testing.T) {
	spec := NewSubAgentTool(nil, nil, nil, 2).Spec()
	if !strings.Contains(spec.Description, "in flight at once") {
		t.Errorf("description must state that a bounded number of sub-agents run at once, got %q", spec.Description)
	}
	if !strings.Contains(spec.Description, "nesting") {
		t.Errorf("description must say the budget covers nesting too, got %q", spec.Description)
	}
}
```

（若 `tools/subagent_test.go` 未导入 `strings`，在本步骤一并补上。）

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./tools/ -run 'TestSubAgentToolSpec_DeclaresTreeLimit' -v -count=1`
预期：FAIL，报 `description must state that a bounded number of sub-agents run at once, got "Delegate a sub-task to a specialized agent (role). ..."`。

- [ ] **步骤 3：补描述**

把 `tools/subagent.go` 的 `Spec()` 里 `Description` 字段改为：

```go
		Description: "Delegate a sub-task to a specialized agent (role). Sub-agents are read-only: they research code, brainstorm solutions, and critically review decisions, but cannot modify files or run commands; the main agent applies changes itself. " +
			"A bounded number of sub-agents run in flight at once across the whole delegation tree, and nesting counts against the same budget: plan the split (parallel directions vs. nested delegation) before you delegate, and collect results before exceeding it.",
```

- [ ] **步骤 4：运行测试验证通过**

运行：`go test ./tools/ -run 'TestSubAgentToolSpec' -v -count=1`
预期：PASS。

- [ ] **步骤 5：Commit**

```bash
git add tools/subagent.go tools/subagent_test.go
git commit -m "feat(tools): tell the model the delegation tree is bounded"
```

---

## 任务 7：回归与全量验证

**文件：**
- 测试：`engine/sub_agent_limits_test.go`（追加回归用例）

- [ ] **步骤 1：编写 6 路扇出回归测试**

追加到 `engine/sub_agent_limits_test.go`：

```go
// TestSubAgentWidth_SixWayFanoutStillFits: the default width (6) is chosen to
// match the 2-6 directions a /collab round fans out, so a six-way parallel
// delegation must complete with NO refusal. This is the regression that keeps
// the invariant from breaking the built-in research skill.
func TestSubAgentWidth_SixWayFanoutStillFits(t *testing.T) {
	model := &gateModel{release: make(chan struct{}), entered: make(chan struct{}, 8)}
	runner := &SubAgentRunner{model: model, tools: readSpecExec(), modelName: "test"}

	var wg sync.WaitGroup
	results := make([]*HandoffResult, 6)
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			res, err := runner.Run(context.Background(), Handoff{Agent: AgentResearcher, Goal: "方向"})
			if err != nil {
				t.Errorf("run %d: %v", i, err)
				return
			}
			results[i] = res
		}(i)
	}
	for i := 0; i < 6; i++ {
		<-model.entered
	}
	close(model.release)
	wg.Wait()
	for i, res := range results {
		if res == nil || res.FinishReason != HandoffReasonCompleted {
			t.Fatalf("six-way fan-out must fit the default width; run %d: %+v", i, res)
		}
	}
}
```

- [ ] **步骤 2：运行验证通过**

运行：`go test ./engine/ -run 'TestSubAgentWidth_SixWayFanoutStillFits' -v -count=1`
预期：PASS。

- [ ] **步骤 3：全量测试（含 race）**

运行：`make test`
预期：`ok` 全部通过，无 race 报告。若出现失败，先判断是否为本次改动引入（本次只新增拒绝路径与计数，不应改变任何被允许的既有行为）。

- [ ] **步骤 4：人工冒烟（可选但推荐）**

在真实会话里跑 `/collab <一个真实目标>`，确认：6 个方向仍在一轮内并行发出并全部完成；UI 步骤树正常；无 `fanout_limit` 出现。

- [ ] **步骤 5：Commit**

```bash
git add engine/sub_agent_limits_test.go
git commit -m "test(engine): pin the six-way fan-out regression"
```

---

## 自检

**1. 规格覆盖度**

| 规格条目 | 对应任务 |
|---|---|
| 宽度不变量（统一计数、fail-loud、挂起不占位、默认 6） | 任务 3（准入/释放/文案）、任务 7（6 路回归） |
| 新增 `fanout_limit` reason + 标题 | 任务 2 |
| 深度不变量（可配置、默认 2） | 任务 1（配置）、任务 3（setter 测试）、任务 5（接线） |
| token 聚合（Run 级、runner 侧计数、resume 不重复计） | 任务 3（记账 defer）、任务 4（Run 开始重置） |
| 新增 `tree_budget_exceeded` reason + 标题 | 任务 2 |
| 80% pinned 提示（信息而非刀、一次性） | 任务 4 |
| 三个 TOML 键 + 启动期校验 | 任务 1 |
| 模型可见面（工具描述一句总量语义） | 任务 6 |
| 破坏性变更：无（默认值保持既有行为） | 任务 3 步骤 7（既有 TestSubAgent* 回归）、任务 7 步骤 3 |

**2. 占位符扫描**：无 TODO / 待定 / "类似任务 N"；每个改代码的步骤都带完整代码块、精确命令与预期输出。

**3. 类型与命名一致性**：`SubAgentBudget` 的四个方法 `StartRun / Spent / Budget / ClaimBudgetNotice` 在任务 3（实现）、任务 4（接口 + 测试 stub `budgetStub`）中逐字一致；`HandoffReasonFanoutLimit` / `HandoffReasonTreeBudgetExceeded` 在任务 2 定义后，任务 3 与测试中同名使用；`SetMaxConcurrentSubAgents / SetRunTokenBudget / SetMaxDepth` 三个 setter 在任务 3 定义、任务 5 调用，签名一致。

**4. 已知边界（写下来而不是留空）**：异步委派的拒绝发生在后台 goroutine 内，因此模型是在 `agent_poll` 时读到拒绝摘要，而非在派发当下。这是"单一收口点"设计的自觉代价——若在 `dispatchAsync` 再预占一次槽位，就会出现第二处计数，正是本方案要避免的。默认 async 在飞上限（8）高于树宽度上限（6），所以正常用法不会撞上这一点。

**5. 既有缺口（不在本次范围）**：`Engine.runUsageAccum` 只累计 depth-0 委派返回的用量，嵌套子 run 的用量在 `runHandoff` 的 nested 分支被丢弃（`engine/handoff.go:424`）。本方案的 Run 聚合在 runner 侧独立计数，因此不受影响；修正 eval 统计的嵌套归集是独立的后续项。
