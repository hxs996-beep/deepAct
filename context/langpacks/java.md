# Java Language Pack

This pack enforces safe, explicit Java conventions. Prefer immutability and clear dependency injection.

## WRONG/RIGHT Example 1 — Optional Instead of Null

**WRONG**
```java
public User findUser(String id) {
    if (id == null) {
        return null;
    }
    return repo.find(id);
}
```

**RIGHT**
```java
public Optional<User> findUser(String id) {
    if (id == null || id.isBlank()) {
        return Optional.empty();
    }
    return repo.find(id);
}
```

Returning `null` forces every caller to remember to check for it. `Optional` makes absence explicit and safer.

## WRONG/RIGHT Example 2 — Constructor Injection

**WRONG**
```java
public class UserService {
    @Autowired
    private UserRepository repo;
}
```

**RIGHT**
```java
public final class UserService {
    private final UserRepository repo;

    public UserService(UserRepository repo) {
        this.repo = repo;
    }
}
```

Constructor injection enables immutability and makes dependencies obvious. It is easier to test and reason about.

## Java Rules

- Do not return `null`; use `Optional` for absent values.
- Use `final` by default on classes, fields, and variables.
- Prefer immutable DTOs; make fields final and set in constructor.
- Use constructor injection, not field injection.
- Avoid static state in services and controllers.
- Validate inputs at boundaries; fail fast with descriptive exceptions.
- Use interfaces only when there is more than one implementation.
- Keep methods small and single-purpose.

## Guidance

Align with existing frameworks and annotations in the codebase. Avoid introducing new dependency injection containers or annotation styles. Use builder patterns only if they already exist in the project. Keep error messages precise and avoid swallowing exceptions.
