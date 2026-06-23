# AGENTS.md - DeepAct 开发指南

> 本文定义了本项目面向所有贡献者（人类和 AI）的开发规范、编码标准和架构约束。

---

## 项目标识

- **名称**: deepact（CLI 二进制: `deepact`）
- **语言**: Go 1.24+
- **架构**: 分阶段守卫式 Agent 循环，双模型路由
- **目标**: 跨平台 CLI（macOS、Windows、Linux）

---

## 代码规范

### Go 风格

- 遵循标准 `gofmt` 格式化（CI 强制检查）
- 使用 `golangci-lint`，配置见 `.golangci.yml`
- 错误处理：始终用 `fmt.Errorf("doing X: %w", err)` 包装上下文
- 库代码中禁止 `panic()`；仅 `main()` 中可用于不可恢复的启动失败
- 接口：定义在消费者侧，而非提供者侧
- 包命名：简短、小写、单数（例如 `engine`、`router`、`policy`）

### 文件组织

- 每个文件一个主要类型（例如 `scorer.go` 包含 `Scorer`）
- 测试文件：`*_test.go` 放同一包内

### 命名

- 导出类型：`PascalCase`
- 非导出：`camelCase`
- 接口：动词-名词或 "-er" 后缀（`Router`、`ContextBuilder`、`AmbiguityDetector`）
- 配置结构体：`XxxConfig` 后缀
- 选项：复杂构造器使用函数式选项模式（functional options pattern）

---

## 架构规则（必须遵守）

### 层依赖规则

```
cmd/ → engine/, router/, policy/, context/
                 ↓
              tools/, llm/
                 ↓
              session/, artifact/
                 ↓
              config/（共享层，无向上依赖）
```

**禁止违反：**
- `engine/` 不得导入 `ui/` 或 `cmd/`
- `tools/` 不得导入 `engine/`（通过接口通信）
- `llm/` 保持独立（不含项目特定逻辑）
- `policy/` 读取状态但不直接修改
- `ui/` 仅消费 engine 的事件（观察者模式）

### 接口边界

跨层调用必须通过已定义的接口：
- Engine ↔ Tools：通过 `Tool` 接口
- Engine ↔ LLM：通过 `ModelClient` 接口
- Engine ↔ Policy：通过 `PolicyChecker` 接口
- UI ↔ Engine：通过事件通道（禁止直接方法调用）

### 状态管理

- **TaskState** 是当前任务的唯一真相来源
- TaskState 在一次 turn 内不可变；仅 Compactor 可重写
- 工具结果存储在 Artifact Store 中，通过 SHA256 引用
- 会话事件是追加写入的（JSONL）；禁止修改历史事件

---

## 设计原则

### 1. Guard Before Act（先守卫，后执行）

每个破坏性操作（文件编辑、shell 命令）必须经过：
1. Ambiguity Gate（语义是否清晰？）
2. Scope Guard（是否在已确认范围内？）
3. Design Guard（方案是否健壮？）
4. Loop Guard（是否陷入循环？）

### 2. Structured Over Verbose（结构化优于冗长）

- 优先使用结构化 JSON（TaskState）而非自然语言描述
- 回复格式：`{summary, changes, next_step, questions}`
- 绝不重复 TaskState.decisions 中已有的信息

### 3. Bookend Context Layout（两端式上下文布局）

- 最重要的信息放在 Prompt 的**顶部**和**底部**
- 中间部分有界且精简
- 绝不倾倒整个文件内容；使用精准代码片段

### 4. Verify Before Trust（先验证，后信任）

- 每个 API/符号引用必须通过 LSP 或 grep 验证
- 每个计划必须通过设计反模式检查
- 每次编辑后必须验证（lint/编译/测试）

### 5. Fail Loud, Recover Gracefully（响亮失败，优雅恢复）

- 绝不静默吞掉错误
- 失败时：记录日志、递增失败计数、必要时升级模型
- 连续 3 次失败后：停止、诊断、询问用户

---

## DeepSeek 特定规则

### 模型交互

1. **reasoning_content**: 视为不透明数据。原样存储。在下一轮请求中原样回传。
2. **工具结果**: 始终包含完整的 ToolResultEnvelope，使用匹配的 tool_call_id。
3. **系统提示词**: 跨 turn 保持稳定（启用缓存命中 → 可节省 98% 成本）。
4. **温度**: 代码生成 0.0，规划 0.6，头脑风暴 1.0。

### 已知失败模式（代码必须防范）

| 失败模式 | 守卫机制 | 代码位置 |
|---|---|---|
| 过度实现（Over-implementation） | Ambiguity Gate | `policy/ambiguity.go` |
| 偷懒/愚蠢设计 | Design Guard | `policy/design_guard.go` |
| 工具调用循环 | Loop Guard + Tool Dedupe | `engine/guards.go` |
| 啰嗦重复 | TaskState 去重 + 强制 rebase | `context/compactor.go` |
| 幻觉 API | 验证要求（Grounding） | `policy/design_guard.go` |
| 上下文退化 | Bookend 布局 + 简短 Prompt | `context/builder.go` |

### Prompt 工程规则

- 始终包含停止条件："信息不足时询问用户，不要擅自实现。"
- 始终在 system prompt 中包含反模式示例
- 计划生成后强制自我质疑
- 包含 TaskState.decisions 并注明"不要重复以下内容"

---

## 测试要求

### 单元测试

- 每个导出函数必须有测试
- 优先使用表驱动测试（table-driven tests）
- 模拟外部依赖（LLM、文件系统、LSP）
- 核心包覆盖目标：>50%（engine/、router/、policy/）

### 测试模式

```go
func TestAmbiguityGate_DetectsVagueRequest(t *testing.T) {
    tests := []struct {
        name    string
        input   string
        state   TaskState
        wantAmb bool
    }{
        {
            name:    "模糊的改进请求",
            input:   "改进配置处理逻辑",
            state:   TaskState{},
            wantAmb: true,
        },
        {
            name:    "明确的修复请求（含文件）",
            input:   "修复 config/loader.go 第 45 行的空指针",
            state:   TaskState{},
            wantAmb: false,
        },
    }
    // ...
}
```

---

## Git 规范

### 提交信息

```
<type>(<scope>): <description>

Types: feat, fix, refactor, docs, test, chore, perf
Scope: engine, router, policy, tools, llm, context, ui, session, config
```

示例：
```
feat(engine): add staged loop with ambiguity gate
fix(llm): preserve reasoning_content echo in multi-turn
refactor(tools): extract ToolResultEnvelope to shared type
test(policy): add design guard anti-pattern detection tests
```

### 推送规则（必须遵守）

- **git push 必须先询问用户** — 获得用户确认后才能执行 push
- **代码编译成功即止** — 不需要额外验证（lint、test 等），除非用户明确要求

### 分支命名

```
feat/<简短描述>
fix/<issue 编号>-<简短描述>
refactor/<模块>-<内容>
```

---

## 安全规则

### 敏感数据

- 绝不在日志中记录 API 密钥、令牌或凭证
- 绝不在 artifacts 中存储密钥（存储前脱敏）
- 绝不在会话数据中包含 `.env` 或凭证文件
- 工具输出：在存储前扫描密钥模式（API keys、密码等）

### Shell 执行

- 维护安全命令白名单（可配置）
- 危险命令需要用户明确确认
- 默认拒绝：`rm -rf`、`git push --force`、`DROP TABLE`、`chmod 777`
- 所有 shell 执行记录到会话事件中

### 网络

- 仅连接到已配置的 API 端点
- 未经用户批准，禁止发起任意 HTTP 请求
- 支持企业环境的代理配置

---

## 性能指南

### 上下文预算

- 默认限制：100 万 Token（可通过 `max_budget_tokens` 在 config.toml 中配置）
- 统一压缩：单个 80% 阈值触发全量压缩（Flash archive）

### 流式处理

- 始终流式返回 API 响应（绝不缓存完整响应后再返回）
- UI 在每次流式块到达时更新
- 工具输出：立即生成摘要，异步存储完整内容

### 启动时间

- 目标：<500ms 到达首个交互式提示
- 懒加载：LSP 连接、MCP 发现、重型配置
- 预热：API 客户端连接、会话加载

---

## 代码读取协议（必须遵守）

**`read` 工具对同一文件有 4 次上限**（LoopGuard: `guards.go:68`，按路径去重计数）。超过即阻塞，每次读取必须精打细算。

### 优先级顺序（严格从左到右）

```
lsp workspaceSymbol → lsp hover/goToDefinition → read symbol=X → read offset/limit
```

| 你要做什么 | 用这个 | 为什么 |
|-----------|--------|--------|
| 找函数/类型定义在哪个文件 | `lsp workspaceSymbol <name>` | 精确定位，不读文件 |
| 查变量/函数返回值的类型 | `lsp hover file=<path> line=<n> char=<n>` | 返回类型签名，不读上下文 |
| 跳到定义处看实现 | `lsp goToDefinition file=<path> line=<n> char=<n>` | 直接跳到定义位置 |
| 读某个函数/类型的完整定义 | `read symbol=<name>` | AST 提取，只返回到该声明块 |
| 查所有调用者 | `lsp findReferences file=<path> line=<n> char=<n>` | 不读文件就找到所有引用 |
| 查看文件结构和符号列表 | `lsp documentSymbol file=<path>` | 一页看到所有导出符号 |

### 红线规则

- **同一文件 `read` 不超过 2 次**。如果需要更多，说明前面没用 LSP。
- **禁止 `read offset/limit` 分段读同一文件**。这是最常见的 LoopGuard 触发原因。改为先 `lsp workspaceSymbol` 找到目标符号，再用一次 `read symbol=X`。
- **先 LSP 后 read**。在任何 `read` 调用之前，先问自己："我用 LSP 能找吗？"
- **Batch 原则**。如果确实需要看多个不连续区域，先全部用 `lsp workspaceSymbol` 定位，再一次性批量 `read`。

---

## 开发工作流

### 添加新工具

1. 在 `tools/builtin/<name>.go` 创建文件
2. 实现 `Tool` 接口（Spec + Run）
3. 在 `cmd/run.go` 中通过 `registry.Register()` 注册
4. 在 `tools/builtin/<name>_test.go` 添加测试
5. 在 system prompt 模板中更新工具 schema

### 添加新守卫

1. 在 `policy/` 中定义检测逻辑
2. 在 engine 循环的适当阶段集成
3. 在 `config/schema.go` 中添加配置开关
4. 编写包含正反例的测试
5. 记录触发条件

### 添加新模型功能

1. 在 `llm/` 包中实现
2. 通过 `ModelClient` 接口暴露
3. 如果路由逻辑变化，更新 router
4. 使用录制的 API 响应进行测试

---

## 审查清单

合并 PR 前检查：

- [ ] 遵循层依赖规则
- [ ] 没有跨层直接导入（仅通过接口通信）
- [ ] 错误处理：所有错误都包装了上下文
- [ ] 新功能已添加测试
- [ ] 没有不应硬编码的值
- [ ] 代码和测试中没有密钥或敏感数据
- [ ] 跨平台：使用 `filepath` 而非 `path`，无硬编码分隔符
- [ ] 接口变更时更新了文档
- [ ] `golangci-lint` 通过
