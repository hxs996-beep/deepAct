# TypeScript Language Pack

This pack enforces strict, type-safe TypeScript and framework conventions. Prefer explicit types at boundaries and avoid unsafe shortcuts.

## WRONG/RIGHT Example 1 — `any` and Ignoring Types

**WRONG**
```ts
// @ts-ignore
export function readUser(id: any): any {
  return fetch(`/api/users/${id}`).then((r) => r.json());
}
```

**RIGHT**
```ts
export interface User {
  id: string;
  name: string;
}

export async function readUser(id: string): Promise<User> {
  const res = await fetch(`/api/users/${id}`);
  if (!res.ok) throw new Error(`read user failed: ${res.status}`);
  return (await res.json()) as User;
}
```

Using `any` erases guarantees and makes downstream code fragile. Keep type safety intact, especially at API boundaries. If data is untrusted, validate before casting.

## WRONG/RIGHT Example 2 — Named Exports + Server Components Default

**WRONG**
```tsx
export default function Profile(props: { id: string }) {
  return <div>{props.id}</div>;
}
```

**RIGHT**
```tsx
export function Profile(props: { id: string }) {
  return <div>{props.id}</div>;
}
```

Named exports improve refactoring and tooling consistency. Server components are the default; only add client directives if you truly need client-side state or effects.

## TypeScript Rules

- Never use `any`; use `unknown` and narrow explicitly.
- No `@ts-ignore` or `@ts-expect-error` in production code.
- Prefer `interface` for object shapes; use `type` for unions and primitives.
- Ensure `strict` mode is enabled; do not bypass strictness.
- Prefer named exports for components and utilities.
- Server components are default; mark client components only when needed.
- Avoid implicit `any` in callbacks; annotate or infer from typed inputs.
- Keep runtime checks aligned with types for external data.

## Guidance

When adding new code, align with existing lint rules and module conventions. Avoid creating new patterns (like a custom fetch wrapper) unless the project already uses them. Keep component props small and explicit. If a function handles external input, validate it and reflect that validation in types so downstream code stays safe.
