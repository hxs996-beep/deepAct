# Go 语言包

本包为系统提示补充 Go 特定的规则和示例。偏好简单、地道的 Go 代码，避免花哨。

## 错误/正确示例 1 — 错误包装与上下文

**错误**
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

**正确**
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

在 Go 中，错误上下文很重要。调用者需要看到失败发生的位置，尤其是在多个文件读取和 JSON 解码的场景中。包装错误保留了根因同时提供了有用的上下文。

## 错误/正确示例 2 — context.Context 作为第一个参数

**错误**
```go
func FetchUser(id string, ctx context.Context) (*User, error) {
    req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/users/"+id, nil)
    if err != nil {
        return nil, err
    }
    return doRequest(req)
}
```

**正确**
```go
func FetchUser(ctx context.Context, id string) (*User, error) {
    req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/users/"+id, nil)
    if err != nil {
        return nil, fmt.Errorf("build request: %w", err)
    }
    return doRequest(req)
}
```

将 context 放在首位使 API 在整个项目中保持一致，并与标准库惯例对齐。它还使得链式传递 context 更清晰。

## Go 规则

- 始终使用 `fmt.Errorf("doing X: %w", err)` 包装错误并附带上下文。
- 在获取资源后立即使用 `defer`；避免隐藏的 defer。
- 接口属于消费者包，而不是提供者包。
- 避免复杂的 `init()` 逻辑；偏好显式构造函数。
- 对重复用例偏好表格驱动测试。
- `context.Context` 是任何请求范围函数的第一个参数。
- 保持导出的表面最小化；能导出就导出。
- 偏好显式错误返回而不是全局状态或 panic。

## 指导

Go 偏好清晰而非聪明。如果一个辅助函数只使用一次，就内联它。如果函数变得太大，只有在拆分能提高可读性和可复用性时才拆分。尽可能使用标准库原语。保持控制流易于理解，避免难以追踪的副作用。只有在项目已经使用时才使用结构化日志；不要引入新的日志框架。
