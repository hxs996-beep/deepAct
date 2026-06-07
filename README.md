# DeepAct — DeepSeek 原生适配的 AI 编码助手

> **DeepSeek 专用** · 指令遵循增强 · 三闸防护 · 多层压缩与缓存优化

DeepAct 是一个为 **DeepSeek 模型深度定制** 的终端 AI 编码代理。它利用 DeepSeek 的推理能力，同时通过三层防护机制确保代码修改精确、安全、高效。

---

## 核心特性

- **原生 DeepSeek 适配** — 提示词工程、缓存优化、温度分级调度全部针对 DeepSeek API 特性调优。`reasoning_content` 回显、前缀缓存优化、流式实时输出，开箱即用。
- **三闸防护系统** — 模糊请求拦截、设计反模式审查、操作范围守卫。每次破坏性操作都经过三道闸门检查。
- **多层压缩与缓存覆盖** — 系统提示词稳定区 + 累积历史前缀缓存 + 仅 volatile tail 缺失。缓存命中率约 98%，大幅降低 API 开销。
- **Methodology Skills** — 内置方法学技能库（brainstorming、test-driven-development、systematic-debugging）。技能可链式自动激活，引导模型遵循最佳实践。
- **子代理系统** — 复杂任务可委派给专用子代理并行执行，结果汇聚回主循环。
- **Session 分叉与回退** — 所有操作按 JSONL 逐条记录，永不篡改。可回退到任意步骤，或分叉出新分支尝试不同方案。
- **内容寻址存储** — 工具输出按 SHA256 去重存储，自动脱敏（API key、密码等）。

---

## 快速开始

### 前提

一个 [DeepSeek API Key](https://platform.deepseek.com/)。二进制文件静态编译，无外部依赖。

### 安装（任选一种）

**macOS（Homebrew）：**
```bash
brew install hxs996-beep/homebrew-tap/deepact
```

**Linux / macOS（一键脚本）：**
```bash
curl -sSfL https://raw.githubusercontent.com/hxs996-beep/deepAct/main/install.sh | sh
```
安装到 `/usr/local/bin`。设置 `DEEPACT_INSTALL=~/.local/bin` 可自定义路径。

**Windows（PowerShell）：**
```powershell
powershell -c "irm https://raw.githubusercontent.com/hxs996-beep/deepAct/main/install.ps1 | iex"
```
安装到 `%LOCALAPPDATA%\deepact` 并自动添加 PATH。

**Go（需 Go 1.24+）：**
```bash
go install github.com/deepact/deepact@latest
```

**手动下载：** 从 [GitHub Releases](https://github.com/deepact/deepact/releases) 下载对应平台压缩包，解压后将可执行文件放入 `$PATH`。

### 配置 API Key

```bash
deepact set api-key
```

或设置环境变量：

```bash
export DEEPSEEK_API_KEY=sk-...
```

### 运行

```bash
# 交互式 TUI 模式（默认）
deepact

# 非交互 / CI 模式
deepact exec "修复连接池中的竞态条件"
```

首次启动会自动验证 API Key，避免输入半天才发现 Key 无效。

---

## 核心机制

### 三闸防护系统

DeepAct 在每次修改代码前经过三道闸门：

```
┌──────────────┐    ┌──────────────┐    ┌──────────────┐
│  模糊性闸门   │ →  │  设计审查闸   │ →  │  范围守卫闸   │
│              │    │              │    │              │
│ "需求明确吗?" │    │ "方案有反模式 │    │ "操作在范围   │
│              │    │  吗?"        │    │  内吗?"      │
└──────────────┘    └──────────────┘    └──────────────┘
        │                   │                   │
        ▼                   ▼                   ▼
   追问用户            审查并修改           阻止或确认
```

| 守卫 | 阻止什么 | 触发条件 |
|------|----------|----------|
| **模糊性闸门** | "优化一下配置处理"（太模糊） | 追问：什么配置？怎么优化？ |
| **设计审查闸** | 用显示文本做查找键、吞错误、过度实现 | 10+ 个反模式自动扫描方案 |
| **范围守卫闸** | `rm -rf /`、编辑未确认的文件 | 用户必须确认后才能执行 |

### Methodology Skills

DeepAct 包含一套方法学技能库，通过 `/<name>` 命令激活。技能可以链式自动激活：

```
/brainstorming → writing-plans → executing-plans → finishing-a-development-branch
```

可用技能包括：

| 技能 | 用途 |
|------|------|
| `brainstorming` | 探索需求、设计讨论，适合开发前使用 |
| `writing-plans` | 生成结构化实现方案 |
| `test-driven-development` | 测试驱动开发：先写测试后实现 |
| `systematic-debugging` | 结构化调试：复现 → 定位 → 修复 → 验证 |
| `code-review` | 按检查清单系统性审查代码 |
| `subagent-driven-development` | 将复杂任务分解为子任务并行执行 |
| `verification-before-completion` | 在声明完成前自动验证 |

技能链通过 NextSkills 字段定义，当前技能完成后自动激活下一个。使用 `/skills` 查看完整列表。

### 提示词缓存优化

DeepAct 的提示词构建采用分层布局：

```
[STABLE ZONE — 始终命中缓存]
  Message 1: System prompt（固定）
  Message 2: Session 环境信息（启动时检测一次）
  Message 3: 可用技能列表（启动时构建一次）
  Message 4: RepoMap（会话内稳定）

[HISTORY ZONE — 追加式，大部分可缓存]
  多轮对话历史（前序 assistant + tool 消息）

[VOLATILE TAIL — 仅尾部缺失]
  AccumulatedBlocks + TaskState + TaskReminder
```

这种布局确保每轮只有尾部约 500-1000 token 缺失缓存，其余均命中前缀缓存，大幅降低 API 成本。

### Session 持久化

所有交互记录以 JSONL 格式存储在 `~/.deepact/sessions/`。支持：

- **回退**：从任意时间点重新执行
- **分叉**：从某一步创建新分支，尝试不同方案
- **审计**：完整的历史记录，可追溯每次工具调用

### 内容寻址存储

工具输出（文件内容、搜索匹配、diff）以 SHA256 为 key 去重存储在 `~/.deepact/artifacts/`。自动脱敏扫描，防止 API key 等敏感信息被持久化。

---

## 快捷键

| 按键 | 功能 |
|------|------|
| `Ctrl+Q` | 退出 |
| `Esc` | 取消当前运行任务 |
| `Enter` | 提交输入 |
| `Tab` | 自动补全 |
| `↑/↓` | 浏览建议列表 |
| `Alt+Enter` | 插入换行 |
| `Shift+drag` | 选中文本（绕过鼠标滚动模式） |

---

## CLI 命令

| 命令 | 说明 |
|------|------|
| `deepact` | 启动交互式 TUI |
| `deepact exec <prompt>` | 非交互模式执行任务 |
| `deepact eval history` | 查看评估记录 |
| `deepact eval stats` | 查看评估统计 |
| `deepact eval compare <v1> <v2>` | 对比两个提示词版本 |
| `deepact set api-key` | 配置 DeepSeek API Key |

---

## 架构

```
cmd/          CLI 入口（Cobra）
ui/           终端 UI（Bubble Tea）
engine/       代理循环、守卫系统、子代理、评估系统
context/      提示词构建、仓库地图、语言包、分层压缩
llm/          DeepSeek API 客户端（流式、重试、限速、Echo 管理）
policy/       模糊检测、设计审查、范围守卫
tools/        内置工具（读、写、编辑、搜索、Bash、网络、回退）
session/      JSONL 会话持久化，支持分叉与回退
artifact/     内容寻址存储，自动脱敏
router/       模型路由（预留扩展）
skill/        方法学技能加载与注册（TOML 定义）
```

---

## 配置

创建 `~/.deepact/config.toml`：

```toml
[model]
default = "deepseek-v4-flash"

[context]
max_budget_tokens = 1048576

[guards]
scope_guard = true
```

---

## License

[MIT License](LICENSE)
