---
name: review
description: Independent code review — an adversarial review of a chosen change range, returning findings with priority, confidence and file:line evidence, filtered by "would the author fix this if they knew?".
when_to_use: the user explicitly invokes /review with a range (e.g. /review 当前改动), or explicitly asks to review a specific change range
disable-model-invocation: true
argument-hint: "<range>"
---

# Independent code review (/review)

This is a review the **user asks for**. Do not start one on your own; the skill is reachable only through `/review`.

The range is carried by the command's task text — `/review 当前改动`, `/review 对比 main`, `/review only the prompt changes`. A bare `/review` has no task text, so the engine only loads the skill and starts no run; if you ever find yourself invoked with no range, ask the user for one instead of assuming a default.

## Step 1 — Take the range from the command argument (no delegation)

The text the user typed after `/review` is the range. Map it to facts with `bash`:

- “the current changes” / “未提交改动” → the uncommitted change:

```bash
git status --porcelain
git diff HEAD
```

`git diff HEAD` covers tracked changes only: a brand-new file is a `??` line in the porcelain output and appears in no diff. For each `??` path, hand the reviewer the file itself (its path to read, or its content) — otherwise new files are reviewed as if they did not exist.

- one commit → `git show <sha>`
- against a branch → `git diff <base>...HEAD`
- a plan or a document → read that file directly

When the diff is large, do not paste it whole: give `git diff HEAD --stat` plus the list of changed files (tracked and untracked) and let the reviewer read those files itself.

## Step 2 — Delegate to an independent reviewer (the only delegation)

Delegate with `handoff_to_agent`. **`agent` must be `"sub"`** (the general read-only analyst). Do not use another role, and do not state your own conclusions first.

- `goal`: `Independently review the changes below and report only the problems the author would certainly fix.`
- `context` must carry three parts:
  1. the **Review requirements** below, verbatim — do not paraphrase or condense them
  2. the range facts from step 1 (the diff, or stat plus the changed-file list)
  3. any extra requirement the user attached to the range
- `expected_output`: `Findings list: for each, title / priority P0-P3 / confidence 0-1 / file:line / why it is a problem / the conditions that trigger it. When nothing qualifies, state "no findings" and list what you examined.`

The reviewer is a structured run: its result exists only once it calls `submit_result`, so a reply that carries no submission is not a result. If that happens, re-delegate once — do not read the empty digest as "no findings".

The reviewer must be an **independent context**: it does not have the author's plan or reasoning, which is exactly what makes the review worth running. Do not put your design intent, or conclusions such as "I already verified this", into `context`. The engine may append its own "Main Agent Context (Review Starting Point)" block (working set, memory markers, modified files) and frames it as "re-examine them from your own perspective"; that block is not yours to suppress, so the part you control is simply not stacking *your* conclusions on top of it.

### Review requirements (put verbatim into `context`)

- Report only three classes of problem: **correctness, security, maintainability**. Each finding must be discrete and actionable, not a general comment about the codebase.
- Every finding must name the code that is **provably affected** (file:line). Speculation that "this might affect something else" is not allowed — either point at the exact affected location or drop the finding.
- Do not report: pure style, formatting or typos; pre-existing problems unrelated to this change; a change that is **visibly deliberate in the diff**; problems whose fix demands more rigor than the rest of the repository shows. Judge deliberateness from the diff itself, never from assumed intent.
- **Prefer no findings over a padded list.** If nothing is a problem the author would certainly fix, report "no findings". Returning zero findings is a valid outcome; do not invent findings to look thorough — but do state the files and line ranges you examined, so "no findings" is distinguishable from "did not look".
- Priority: P0 blocks release or usage (independent of any assumption about inputs); P1 must be fixed in the next cycle; P2 normal, to be fixed eventually; P3 optional.
- Confidence must discriminate: high for established facts, low for inference.

## Step 3 — Verify and present (no delegation)

Once the reviewer's findings are back:

1. **Verify each finding**: read the cited lines yourself and confirm it holds. Downgrade or drop anything whose evidence does not hold up — do not relay it as-is.
2. Deduplicate and merge findings that point at the same place.
3. Give the overall verdict: whether the change is correct — that requires zero P0/P1 findings.
4. **Report only; do not change code.** Fix it if the user asks.
5. Finish with `task_complete`, delivering: the overall verdict, the findings by priority, and any assumptions still unverified.

## Caveats

- The command needs task text: `/review` alone only loads the skill and starts no run. Invoke it with a range — `/review 当前改动`.
- Write `goal` / `context` and the final report in the user's language.
- The reviewer is read-only and cannot run git — you supply the range facts in step 1.
- One review costs one sub-agent run; do not run it repeatedly unless the user asks.
