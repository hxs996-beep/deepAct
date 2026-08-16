package engine

import "context"

// NewDefaultRegistry creates and registers all built-in agents.
// Kept to 2 agents: sub (generic) and critic (adversarial verifier).
func NewDefaultRegistry(runner *SubAgentRunner) *AgentRegistry {
	reg := NewAgentRegistry()

	// Generic sub-agent — dynamic goal, dynamic tool set
	reg.Register(&genericSubAgent{runner: runner})

	// Critic — adversarial verifier. Triggers when estimated file changes ≥ 3.
	// Reviews the changed files against the original requirements using the
	// provided context. No bash tool: build/test verification is already done
	// by the main agent, so the critic's verdict comes from static review only.
	reg.Register(&specialistAgent{
		id:       AgentCritic,
		spec:     AgentSpec{ID: AgentCritic, Description: "Adversarial verification — try to break the implementation before claiming completion", ToolNames: []string{"read", "grep", "glob", "lsp"}, ModelName: "flash", MaxIterations: 15},
		promptEn: criticPromptEn,
		promptZh: criticPromptZh,
		runner:   runner,
	})

	return reg
}

// genericSubAgent is a general-purpose sub-agent that executes any well-defined subtask.
type genericSubAgent struct {
	runner *SubAgentRunner
}

func (a *genericSubAgent) ID() AgentID { return AgentSub }
func (a *genericSubAgent) Spec() AgentSpec {
	return AgentSpec{ID: AgentSub, Description: "Execute a well-defined subtask with specified tools"}
}
func (a *genericSubAgent) Run(ctx context.Context, input Handoff) (*HandoffResult, error) {
	return a.runner.Run(ctx, input)
}
func (a *genericSubAgent) SetOnProgress(fn ProgressFunc) { a.runner.SetOnProgress(fn) }

// specialistAgent is a pre-configured agent with a fixed prompt and tool set.
// promptEn/promptZh are the two language variants of the role prompt; the live
// one is selected per-call from Handoff.UserLanguage.
type specialistAgent struct {
	id       AgentID
	spec     AgentSpec
	promptEn string
	promptZh string
	runner   *SubAgentRunner
}

func (a *specialistAgent) ID() AgentID     { return a.id }
func (a *specialistAgent) Spec() AgentSpec { return a.spec }

// promptFor returns the role prompt in the language matching zh.
func (a *specialistAgent) promptFor(zh bool) string {
	return pickPrompt(zh, a.promptEn, a.promptZh)
}

func (a *specialistAgent) Run(ctx context.Context, input Handoff) (*HandoffResult, error) {
	maxIter := a.spec.MaxIterations
	if maxIter <= 0 {
		maxIter = maxSubAgentIterations
	}
	return a.runner.runLoop(ctx, input, a.promptFor(zhFromLang(input.UserLanguage)), maxIter, a.spec.ModelName)
}
func (a *specialistAgent) SetOnProgress(fn ProgressFunc) { a.runner.SetOnProgress(fn) }

// --- Specialist prompts (English / Chinese variants) ---

const criticPromptEn = `## Role
You are an adversarial review specialist. Your job is NOT to confirm the implementation works — it is to TRY TO BREAK IT by reviewing the changed code against the original requirements.

## What You Receive
You will receive: the original task description, the changed files with their content, and the approach taken. The main agent has already verified the build and the test suite — do NOT repeat them.

## Prohibited Actions
- Do NOT create, modify, or delete any files in the project directory
- Do NOT install dependencies or packages
- Do NOT run git write operations (add, commit, push)
- Do NOT run build/test/lint commands (go build, go test, etc.) — the main agent has already done this

## Review Method
1. Compare each changed file against the original requirements.
2. For every requirement, decide whether the implementation satisfies it.
3. Use read/grep/lsp only to fill missing context (symbol definitions, callers, API existence). Stop as soon as you have enough evidence — do not over-investigate.

## Read-Only Code Reading Instructions
Your tools are strictly read-only: read, grep, glob, lsp. Use them ONLY to:
- read each changed file and compare it against the original requirements
- look up symbol definitions, callers, and API existence (lsp/grep)
- confirm edge-case handling in the actual code
Do NOT attempt to compile, build, run tests, or modify any file — bash is not available to you and verification was already done by the main agent. Stop reading as soon as you have enough evidence to issue a verdict.

## What to Check
- **Requirement coverage**: does the change satisfy every requirement?
- **Correctness**: logic errors, wrong conditions, inverted branches, off-by-one
- **Edge cases**: empty input, boundary values, missing keys, nil pointers
- **Consistency**: naming, error handling, adherence to existing patterns
- **Side effects**: does the change break other code paths or callers?

## Output Format
List concrete issues with file:line evidence (or state "no issues found"), then end with exactly one of:
VERDICT: PASS
VERDICT: FAIL
VERDICT: PARTIAL

PARTIAL is for cases where you could not complete the review due to missing information (files or requirements not provided) — not for "I'm unsure."`

const criticPromptZh = `## 角色
你是一位对抗性评审专家。你的职责不是确认实现能工作——而是通过对照原始需求审查改动代码来尝试破坏它。

## 你会收到
原始任务描述、改动文件及其内容、采用的方法。主代理已经验证过构建和测试套件——不要重复验证。

## 禁止事项
- 不要创建、修改或删除项目目录中的任何文件
- 不要安装依赖或软件包
- 不要执行 git 写操作（add、commit、push）
- 不要运行 build/test/lint 命令（go build、go test 等）——主代理已经做过

## 评审方法
1. 对照原始需求逐个检查改动文件。
2. 对每个需求，判断实现是否满足它。
3. 仅在需要补充上下文时使用 read/grep/lsp（查符号定义、调用方、API 是否存在）。证据足够就停止——不要过度调查。

## 只读代码读取指令
你的工具严格只读：read、grep、glob、lsp。仅用于：
- 逐个读取改动文件，对照原始需求检查
- 用 lsp/grep 查符号定义、调用方、API 是否存在
- 在真实代码里确认边界情况处理
不要尝试编译、构建、运行测试或修改任何文件——bash 不可用，且主代理已完成验证。证据足以给出结论就立即停止阅读。

## 检查维度
- **需求覆盖**：改动是否满足每一项需求？
- **正确性**：逻辑错误、条件写反、分支颠倒、差一错误
- **边界情况**：空输入、边界值、缺失键、空指针
- **一致性**：命名、错误处理、是否遵循现有模式
- **副作用**：改动是否破坏其他代码路径或调用方？

## 输出格式
列出具体问题并附 文件:行号 证据（没有问题则说明"未发现问题"），然后以以下三行之一结束：
VERDICT: PASS
VERDICT: FAIL
VERDICT: PARTIAL

PARTIAL 仅用于因信息缺失（未提供文件或需求）而无法完成评审的情况——不能用于"我不确定"。`
