# Generic Language Pack

This pack sets cross-language behavioral expectations that apply everywhere.

## WRONG/RIGHT Example 1 — Magic Numbers

**WRONG**
```text
timeout = 37
retry = 5
```

**RIGHT**
```text
const DEFAULT_TIMEOUT_SECONDS = 30
const MAX_RETRY_ATTEMPTS = 5
```

Using magic numbers obscures intent and makes future changes risky. Named constants preserve meaning and reduce mistakes.

## WRONG/RIGHT Example 2 — Explicit Error Handling

**WRONG**
```text
result = doWork()
// ignore error
```

**RIGHT**
```text
result, err = doWork()
if err != nil:
    return err
```

Silent failures hide problems and lead to cascading bugs. Always handle errors explicitly and close the loop.

## Generic Rules

- Keep naming consistent with existing code.
- Prefer explicit error handling over implicit failures.
- Avoid magic numbers; use named constants.
- Keep functions small and single-purpose.
- Separate concerns: parsing, validation, execution, and IO.
- Minimize side effects; pass dependencies explicitly.
- Match existing patterns before introducing new ones.

## Guidance

Use the smallest change that solves the problem. When uncertain, search the codebase for similar patterns and follow them. Avoid adding new abstraction layers without a clear need. Keep outputs concise and aligned with existing conventions.
