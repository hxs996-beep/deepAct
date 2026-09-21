package engine

// planMethodologyZh is the built-in deep-planning methodology injected when
// the model calls plan_task in a Chinese session.
const planMethodologyZh = `# 深度分析方法论（plan_task）

你已进入深度规划模式。在动手修改代码前，先按以下框架完成分析、拆解与计划，然后严格按计划执行。

## 何时用
- 复杂、多步骤、需要先深度分析再动手的任务（建模、重构、跨模块、多方案权衡）。
- 简单任务不需要本框架，直接用 todo_write 记录步骤即可。

## 分析流程
1. **吃透背景**：先读相关文件、目录树、AGENTS.md、现有代码模式——先理解再规划，不要凭空假设。
2. **拆解**：把目标拆成可独立验证的子问题。每个子问题自包含、可单独验证。
3. **多假设**：列出 2-3 个可能方向或方案，不要锚定第一个直觉。说明每个方向的依据与代价。
4. **验证优先**：用只读工具取证（grep / read / lsp / glob）。用证据支撑结论；无法验证的假设显式标注"未验证"。
5. **产出计划**：明确执行步骤、每步的验证点、边界与失败模式。
6. **执行纪律**：按计划走，每步验证；发现计划偏差时更新计划，而不是闷头改。

## 协作
- 若目标需要多方向并行调研 → 使用 /collab：把已形成的全局框架作为 context 传给各子代理，避免子代理从零开始。
- 需要用户决策或权衡取舍 → 用 ask_user 提供候选方案；能自行验证的信息不要问。

## 收敛纪律
- 规划不是目的，执行才是。完成计划后立即开始执行，不要停留在分析阶段。
- 若发现任务其实简单 → 放弃本框架，直接用 todo_write 推进。`

// planMethodologyEn is the English counterpart of planMethodologyZh.
const planMethodologyEn = `# Deep-Planning Methodology (plan_task)

You are in deep-planning mode. Before modifying code, complete the analysis
and plan below, then execute strictly by the plan.

## When to use
- Complex, multi-step tasks that need deep analysis before acting (modeling,
  refactoring, cross-module work, tradeoff analysis).
- For simple tasks, skip this framework and use todo_write directly.

## Analysis flow
1. **Understand the background**: read relevant files, the directory tree,
   AGENTS.md, and existing code patterns before planning. Never assume.
2. **Decompose**: split the goal into independently verifiable sub-problems.
3. **Multiple hypotheses**: list 2-3 candidate directions or approaches. Do
   not anchor on your first intuition. State the rationale and cost of each.
4. **Verify first**: gather evidence with read-only tools (grep / read / lsp /
   glob). Support conclusions with evidence; explicitly mark unverified
   assumptions.
5. **Produce a plan**: list concrete steps, each step's verification point,
   boundaries, and failure modes.
6. **Execution discipline**: follow the plan and verify each step; when the
   plan drifts, update the plan instead of coding blindly.

## Collaboration
- If the goal needs multi-direction parallel research → use /collab: pass the
  global framework as context to each sub-agent so they do not start from zero.
- When the user must decide a tradeoff → use ask_user with candidate options;
  never ask for information you can verify yourself.

## Convergence discipline
- Planning is not the goal; executing is. Start executing as soon as the plan
  is complete. Do not stay in analysis.
- If the task turns out simple → drop this framework and proceed with todo_write.`

// planMethodology returns the methodology text for the session language.
func planMethodology(zh bool) string {
	if zh {
		return planMethodologyZh
	}
	return planMethodologyEn
}
