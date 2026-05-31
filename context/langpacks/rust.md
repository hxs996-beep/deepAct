# Rust Language Pack

This pack enforces safe, explicit Rust with production-friendly patterns. Avoid panic paths and keep errors meaningful.

## WRONG/RIGHT Example 1 — No `unwrap()` in Production

**WRONG**
```rust
pub fn load_config(path: &str) -> Config {
    let data = std::fs::read_to_string(path).unwrap();
    serde_json::from_str(&data).unwrap()
}
```

**RIGHT**
```rust
pub fn load_config(path: &str) -> Result<Config, ConfigError> {
    let data = std::fs::read_to_string(path)
        .map_err(|e| ConfigError::Read { path: path.to_string(), source: e })?;
    let cfg = serde_json::from_str(&data)
        .map_err(|e| ConfigError::Parse { path: path.to_string(), source: e })?;
    Ok(cfg)
}
```

Using `unwrap` hides failure modes and crashes the process. Explicit error types let callers handle errors gracefully.

## WRONG/RIGHT Example 2 — Explicit Lifetimes When Needed

**WRONG**
```rust
pub fn first_line(input: &str) -> &str {
    input.lines().next().unwrap_or("")
}
```

**RIGHT**
```rust
pub fn first_line<'a>(input: &'a str) -> &'a str {
    input.lines().next().unwrap_or("")
}
```

When returning references, be explicit about lifetimes when it improves clarity or the compiler cannot infer them.

## Rust Rules

- Never use `unwrap()` or `expect()` in production paths.
- Use explicit error types; avoid `Box<dyn Error>` unless required.
- Make lifetimes explicit when returning references.
- Keep `clippy` clean; treat warnings as issues to fix.
- Prefer `Result<T, E>` for fallible operations.
- Use `?` for propagation with contextful error mapping.
- Avoid unnecessary `clone()`; pass references when possible.
- Keep public APIs minimal and stable.

## Guidance

Prefer explicit enums for error types and derive `thiserror::Error` when allowed by project conventions. Keep ownership and borrowing clear in function signatures. Avoid overly generic lifetimes when a concrete lifetime suffices. Use iterators for clarity but do not sacrifice readability. When in doubt, choose clarity over micro-optimizations.
