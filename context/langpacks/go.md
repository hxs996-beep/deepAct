# Go Language Pack

This pack augments the system prompt with Go-specific rules and examples. Prefer simple, idiomatic Go; avoid cleverness.

## WRONG/RIGHT Example 1 — Error Wrapping and Context

**WRONG**
```go
func loadConfig(path string) (*Config, error) {
    data, err := os.ReadFile(path)
    if err != nil {
        return nil, err
    }
    var cfg Config
    if err := json.Unmarshal(data, &cfg); err != nil {
        return nil, err
    }
    return &cfg, nil
}
```

**RIGHT**
```go
func loadConfig(path string) (*Config, error) {
    data, err := os.ReadFile(path)
    if err != nil {
        return nil, fmt.Errorf("read config %s: %w", path, err)
    }
    var cfg Config
    if err := json.Unmarshal(data, &cfg); err != nil {
        return nil, fmt.Errorf("decode config %s: %w", path, err)
    }
    return &cfg, nil
}
```

In Go, error context matters. Callers need to see where a failure happened, especially when multiple file reads and JSON decodes occur. Wrapping errors preserves the root cause while giving helpful context.

## WRONG/RIGHT Example 2 — context.Context First Param

**WRONG**
```go
func FetchUser(id string, ctx context.Context) (*User, error) {
    req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/users/"+id, nil)
    if err != nil {
        return nil, err
    }
    return doRequest(req)
}
```

**RIGHT**
```go
func FetchUser(ctx context.Context, id string) (*User, error) {
    req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/users/"+id, nil)
    if err != nil {
        return nil, fmt.Errorf("build request: %w", err)
    }
    return doRequest(req)
}
```

Placing context first makes APIs consistent across the project and aligns with standard library conventions. It also makes it easier to chain or pass through context without confusion.

## Go Rules

- Always wrap errors with context using `fmt.Errorf("doing X: %w", err)`.
- Use `defer` immediately after acquiring a resource; avoid hidden defers later.
- Interfaces belong at the consumer package, not the provider.
- Avoid complex `init()` logic; prefer explicit constructors.
- Prefer table-driven tests for repeated cases.
- `context.Context` is the first parameter for any request-scoped function.
- Keep exported surface minimal; unexport when possible.
- Prefer explicit error returns over global state or panics.

## Guidance

Go favors clarity over cleverness. If you need a helper that is only used once, inline it. If a function grows too large, split it only when the split improves readability and reusability. Use standard library primitives when possible. Keep control flow easy to follow, and avoid side effects that are hard to trace. Use structured logging only if the project already uses it; do not introduce new logging frameworks.
