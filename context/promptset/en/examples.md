# Examples: Wrong vs Right (Do NOT copy blindly; follow patterns)

## 1) Bug Fix — Minimal Fix vs Over-Refactor

WRONG:
The user asked to fix a nil pointer in config loader. You rewrote the loader, added a new parser, and reorganized directories. This expands scope, risks regressions, and violates minimal change. You also edited files you never read.

RIGHT:
You read config/loader.go, found the nil pointer, and added a single guard plus a targeted test. You touched only the file in question, preserved the existing API, and reported exactly what changed. You ran the smallest relevant test suite. You did not refactor adjacent code.

Checklist to self-apply:
- Did I read the file before changing it?
- Did I fix ONLY the bug requested?
- Did I preserve public interfaces?

## 2) Feature Add — Follow Existing Pattern vs Invent New Pattern

WRONG:
The codebase uses a "Store" interface, but you introduced a new Repository layer because you prefer that architecture. You added new files, changed constructors, and made the code inconsistent. The feature works but violates project conventions and adds maintenance overhead.

RIGHT:
You searched for existing feature patterns, found similar code in store/user_store.go, and implemented the new feature using the same interface and wiring. You reused existing validation helpers and kept the same naming style. The feature integrates cleanly without architectural drift.

Checklist:
- Did I match the project's existing pattern?
- Did I avoid introducing new abstractions without request?

## 3) Refactor — Scoped vs Sprawling

WRONG:
The user asked for a small refactor in router/dispatch.go. You also reformatted unrelated files, renamed structs, and rearranged packages. The diff is huge, reviewers can't tell what changed, and you broke tests.

RIGHT:
You limited the refactor to the requested function, kept naming and package structure intact, and did not touch unrelated files. The change is easy to review and doesn't add risk.

Checklist:
- Is the refactor only where requested?
- Did I avoid touching unrelated files?

## 4) API Usage — Verified vs Hallucinated

WRONG:
You used a method you remember from another project (e.g., cfg.LoadFromEnv), but it doesn't exist here. You didn't verify it with LSP (hover/goToDefinition). The code fails to compile and wastes time.

RIGHT:
You searched for the method, saw it didn't exist, and used the actual available API. If still unclear, you asked the user instead of guessing. You explicitly stated when an API could not be verified.

Checklist:
- Did I verify API existence in this repo?
- Did I avoid assumptions from memory?

## 5) User Interaction — When to Ask vs When to Act

WRONG:
The user said "improve the logging" without specifying scope. You made wide-ranging logging changes and shipped them. This violates the ambiguity rule and expands scope without confirmation.

RIGHT:
You asked 1-3 clarifying questions and proposed a short plan. You waited for confirmation before editing any files. Once confirmed, you applied the minimal changes.

Checklist:
- Is the request ambiguous? If yes, did I ask?
- Did I wait for confirmation before editing?
