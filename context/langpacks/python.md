# Python Language Pack

This pack enforces typed, explicit, and modern Python practices. Prefer clarity and correctness over shortcuts.

## WRONG/RIGHT Example 1 — Bare Except and Missing Types

**WRONG**
```py
def load_user(user_id):
    try:
        return repo.get_user(user_id)
    except:
        return None
```

**RIGHT**
```py
from typing import Optional

def load_user(user_id: str) -> Optional[User]:
    try:
        return repo.get_user(user_id)
    except RepoError as exc:
        raise RuntimeError(f"load user {user_id}: {exc}") from exc
```

Explicit exceptions make failures diagnosable. Type hints make contracts clear and improve tooling. Avoid swallowing errors.

## WRONG/RIGHT Example 2 — Async for IO

**WRONG**
```py
def fetch_profile(user_id: str) -> dict:
    resp = requests.get(f"{BASE_URL}/users/{user_id}")
    resp.raise_for_status()
    return resp.json()
```

**RIGHT**
```py
import httpx

async def fetch_profile(user_id: str) -> dict:
    async with httpx.AsyncClient() as client:
        resp = await client.get(f"{BASE_URL}/users/{user_id}")
        resp.raise_for_status()
        return resp.json()
```

IO-bound work should be async when the project supports it. Sync IO in an async system causes slowdowns and blocking.

## Python Rules

- Type hints are mandatory for all public functions and methods.
- Use async/await for IO-bound work; keep sync code CPU-bound.
- Use Pydantic v2 patterns (`BaseModel`, `model_validate`, `model_dump`).
- No bare `except:`; always catch specific exceptions.
- Use `pathlib.Path` instead of `os.path` for filesystem paths.
- Avoid mutable default arguments.
- Validate external data before use.
- Keep side effects behind explicit function calls.

## Guidance

Prefer dataclasses or Pydantic models to untyped dicts for structured data. Keep functions small and deterministic. If a function returns optional data, be explicit and handle the `None` case at the boundary. Use f-strings for errors to include contextual data, and avoid hidden global state.
