package engine

// planMethodology is the built-in deep-planning methodology injected when the
// model calls plan_task. The prompt is English-only, so there is a single
// variant.
const planMethodology = `# Deep-Planning Methodology (plan_task)

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
