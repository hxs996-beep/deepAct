# DeepAct — DeepSeek 原生适配的 AI 编码助手

> **DeepSeek 专用** · 指令遵循增强 · 三闸防护防止蛮干 · 双模型路由降低成本

DeepAct 是一个为 **DeepSeek 模型深度定制** 的终端 AI 编码代理。它不是通用 Agent 套壳，而是专门针对 DeepSeek 的特点（强推理、但容易过实现、指令遵循不稳定）做了三层防护和引导，让 DeepSeek 在代码修改中**更听话、更安全、更省钱**。

---

## 为什么用 DeepAct？

大多数 AI 编码助手只是一个循环：听 → 做 → 重复。DeepAct 不一样：

- **🎯 原生 DeepSeek 适配** — 不是通用 wrapper。提示词工程、路由策略、回退机制全都针对 DeepSeek 的推理模式和 API 特性调优。`reasoning_content` 的回显、缓存命中优化、温度分级调度，全部开箱即用。
- **🛡️ 三闸防护，防止蛮干** — DeepSeek 推理能力强，但也更容易"自作主张"改多余的东西。DeepAct 的三个守卫门拦截每一次破坏性操作：模糊请求？拦住问清楚。设计有反模式？审查并否决。超出范围？拒绝执行。
- **💰 双模型路由，节省 80% 成本** — 简单操作（读文件、搜索代码）用 **DeepSeek V4 Flash**（¥1/M tokens），复杂任务（规划、设计审查）自动升级到 **DeepSeek V4 Pro**（¥3/M tokens）。不浪费一次推理。
- **📋 Session 分叉 / 回退** — 所有操作按 JSONL 逐条记录，永不篡改。走偏了回退到任意步骤，或者分叉出新分支尝试不同方案。

---

## 快速开始

### 前提

一个 [DeepSeek API Key](https://platform.deepseek.com/)。除此之外无任何依赖——二进制文件是静态编译的。

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

**手动下载：** 从 [GitHub Releases](https://github.com/deepact/deepact/releases) 下载对应平台的压缩包，解压后将 `deepact`（Linux/macOS）或 `deepact.exe`（Windows）放到 `$PATH` 目录。

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

### 会议管线（复杂任务自动触发）

对于复杂需求，DeepAct 执行结构化管线：

| 阶段 | 做什么 |
|------|--------|
| `/plan <目标>` | 探索代码库 → 输出方案 → 自我挑战 → 你选一个 |
| `/implement <目标>` | 按步骤执行，范围守卫全程监视 |
| `/review` | 评分卡对比实现 vs 方案，标记偏差 |

也可以直接描述需求——DeepAct 自动判断复杂度，必要时进入管线。

### 双模型路由

不是每一轮都需要大模型。DeepAct 按场景分配：

| 模型 | 适用场景 | 成本 |
|------|----------|------|
| **Flash** (`deepseek-v4-flash`) | 读文件、搜索代码、简单操作 | ~¥1/M 输入 tokens |
| **Pro** (`deepseek-v4-pro`) | 规划、设计审查、复杂编辑 | ~¥3/M 输入 tokens |

路由根据模糊度、编辑范围、失败历史自动判断，无需手动切换。

---

## Slash 命令

| 命令 | 说明 |
|------|------|
| `/plan <目标>` | 探索代码库并给出方案建议 |
| `/implement <目标>` | 直接执行实现 |
| `/review` | 对比实现与方案的一致性 |

## 快捷键

| 按键 | 功能 |
|------|------|
| `Enter` | 提交 |
| `Ctrl+C` | 取消当前运行 / 退出 |
| `Alt+Enter` | 插入换行 |
| `Alt+drag` | 选中复制文本 |
| `↑↓` | 滚动查看输出历史 |
| `Tab` | 自动补全 slash 命令 |

---

## 架构

```
cmd/          CLI 入口（Cobra）
ui/           终端 UI（Bubble Tea）
engine/       代理循环、守卫系统、会议管线、子代理
context/      提示词构建、仓库地图、语言包、压缩
llm/          DeepSeek API 客户端（流式、重试、限速、Echo 管理）
policy/       模糊检测、设计审查、范围守卫
tools/        内置工具（读、写、编辑、搜索、Bash、网络、回退）
session/      JSONL 会话持久化，支持分叉/回退
artifact/     内容寻址存储，自动脱敏
router/       双模型路由（Flash ↔ Pro）
```

## 配置

创建 `~/.deepact/config.toml`：

```toml
[model]
default = "deepseek-v4-flash"    # 快速模型用于简单操作
escalation = "deepseek-v4-pro"   # 强模型用于规划和审查

[context]
max_budget_tokens = 1048576

[guards]
scope_guard = true               # 阻止越界操作

[conference]
enabled = true
```

---

> **DeepAct 正在持续进化。** 如果你有功能需求或遇到问题，欢迎提交 Issue。

[MIT License](LICENSE)

---

## 🇬🇧 English

### DeepAct — DeepSeek-Native AI Coding Agent

A terminal-native AI coding agent **built specifically for DeepSeek models**. DeepAct enhances DeepSeek's instruction-following, prevents reckless behavior through three guard gates, and reduces cost with dual-model routing.

**Key differences from generic AI coding agents:**

- **DeepSeek-native** — Prompt engineering, routing, fallback, and `reasoning_content` echo are all tuned for DeepSeek's API and inference patterns. Not a generic wrapper.
- **Three guard gates** — Ambiguity → Design → Scope. Every destructive action is checked before execution.
- **Dual-model routing** — Flash model for simple tasks, Pro model for complex planning. ~80% cost savings.
- **Session fork/rewind** — Branch from any point, roll back without data loss.

**Install:**
```bash
# macOS (Homebrew)
brew install hxs996-beep/homebrew-tap/deepact

# Linux / macOS
curl -sSfL https://raw.githubusercontent.com/hxs996-beep/deepAct/main/install.sh | sh

# Windows (PowerShell)
powershell -c "irm https://raw.githubusercontent.com/hxs996-beep/deepAct/main/install.ps1 | iex"

# Go
go install github.com/deepact/deepact@latest
```

**Quick start:**
```bash
deepact set api-key   # Configure DeepSeek API key
deepact               # Start interactive TUI
```

[MIT License](LICENSE)
