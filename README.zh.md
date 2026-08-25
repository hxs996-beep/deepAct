# DeepAct — 轻量级终端 AI 编码代理 · 为 DeepSeek 而生

**简体中文** | [English](README.md)

<p align="center">
  <a href="https://goreportcard.com/report/github.com/deepact/deepact"><img src="https://img.shields.io/badge/go_report-A-brightgreen?style=flat-square" alt="Go Report"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-MIT-blue?style=flat-square" alt="MIT"></a>
  <a href="https://golang.org"><img src="https://img.shields.io/badge/Go-1.24+-00ADD8?style=flat-square&logo=go" alt="Go 1.24+"></a>
  <img src="https://img.shields.io/badge/platform-macOS%20%7C%20Linux%20%7C%20Windows-lightgrey?style=flat-square" alt="Platforms">
</p>

<p align="center">
  <b>⚡ 单二进制 · ~6 MB 下载 · ~25 MB 内存 · 零运行时依赖 · DeepSeek 原生</b>
</p>

**DeepAct 是一个跑在终端里的 AI coding agent（AI 编码代理）**：用 Go 编写、静态编译、开源（MIT）、为 DeepSeek API 全链路调优。

- **轻** —— 安装只需一个 ~6 MB 的压缩包，没有 Node、没有 Python、没有 Docker。
- **快** —— 提示工程、前缀缓存、温度调度、工具调用格式全部针对 DeepSeek 逐项调校。
- **准** —— 相比"通用代理换上 DeepSeek 模型"，**成本更低、响应更快、指令遵循更准**。

---

## 📖 目录

1. [它有多轻](#它有多轻)
2. [快速开始](#快速开始)
3. [日常使用](#日常使用)
4. [对接 DeepSeek](#对接-deepseek)
5. [核心能力](#核心能力)
6. [CLI 命令一览](#cli-命令一览)
7. [架构](#架构)

---

## ⚡ 它有多轻

| 指标 | 实测值 | 说明 |
|------|--------|------|
| 下载体积 | **~6 MB**（tar.gz） | Linux / macOS / Windows，amd64 + arm64 |
| 单文件大小 | **~16 MB** | 静态编译（`CGO_ENABLED=0` + `-s -w`），零外部库 |
| 启动峰值内存 | **~25 MB** | 实测 `deepact --help`（macOS arm64） |
| 启动耗时 | **~10 ms** | 同上实测 |
| 运行时依赖 | **0** | 无需 Node / Python / Docker / Electron，只要一个 DeepSeek API Key |

*上文数据实测于 release 1.0.6（macOS arm64），不同平台略有差异。*

一个 16 MB 的 Go 文件，装下完整的代理能力：四道守卫、团队协作、子代理并行、MCP 扩展、可回退会话。没有浏览器内核，没有运行时拖累——**启动即用，用完即走**，服务器 / CI / 低配笔记本都能轻松跑。

## 🚀 快速开始

> [!NOTE]
> 需要一个 [DeepSeek API Key](https://platform.deepseek.com/)（在 [platform.deepseek.com](https://platform.deepseek.com/) 注册领取）。

### 第 1 步 · 安装

```bash
# macOS / Linux 一键安装
curl -sSfL https://raw.githubusercontent.com/hxs996-beep/deepAct/main/install.sh | sh

# 或 Go
go install github.com/deepact/deepact@latest
```

Windows 用户见 [Releases](https://github.com/hxs996-beep/deepAct/releases)（PowerShell 或手动下载）。

### 第 2 步 · 配置 DeepSeek API Key

```bash
deepact set api-key          # 交互式输入，写入 ~/.deepact/config.toml（权限 0600）
```

> [!TIP]
> 项目级配置 `.deepact/config.toml` 会覆盖全局配置，可为不同仓库设置不同模型与权限。

### 第 3 步 · 开始使用

```bash
deepact                      # 启动交互式 TUI（Windows / macOS / Linux 通用）
deepact exec "修复连接池竞态"  # 非交互 / CI 模式
deepact --auto exec "..."    # 自动模式（跳过确认）
deepact --model pro "..."    # 指定模型：flash（快/省）或 pro（强/全）
```

## 🖥️ 日常使用

### TUI 快捷键

| 按键 | 作用 |
|------|------|
| `Ctrl+Q` | 退出 |
| `Esc` | 取消当前任务 |
| `Enter` | 提交 |
| `Tab` | 补全 |
| `Alt+Enter` | 换行 |

### 一句话开工（命令行直出）

```bash
deepact exec "给 LoginHandler 加超时和熔断"
deepact exec "把 user 表迁移到 Postgres 并修好所有编译错误" --auto
deepact exec "review 最近 5 个 commit 的潜在 bug" --output jsonl > review.jsonl
```

`exec` 常用参数：`--auto` 跳过确认 · `--output human|jsonl` · `--max-turns N` · `--model flash|pro` · `--verbose`。

### 多角色团队协作（/team）

```bash
deepact exec "/team 给订单模块加幂等控制"
```

主代理先出 2-3 个实现方案，架构师、安全工程师等角色**并行评审、独立打分**，输出方案×角色评分矩阵；你选定方案后代理直接落地。支持 `--members` 指定成员、`--add` 加载自定义角色（TOML）。

### 项目规范与技能（Skills）

项目规范、工作流、领域知识通过**技能**注入系统提示：技能列表渲染进稳定区，Agent 用 LLM 按 `name`/`description` 语义匹配你的消息**自动激活**最相关技能（匹配失败静默回退），也可用 `activate_skill` 工具手动切换。

技能目录按优先级加载（重名时后者覆盖）：

| 优先级 | 目录 | 说明 |
|--------|------|------|
| 1 | `~/.deepact/skills/` | DeepAct 专属 |
| 2 | `<项目>/.claude/skills/` | 项目级 |
| 3 | `~/.agent/skills/` | Agent 通用 |
| 4 | `~/.claude/skills/` | Claude Code 兼容 |

格式为 `<name>/SKILL.md`（Claude Code 布局，YAML frontmatter）：

```markdown
---
name: my-flow
description: 处理模块 X 的代码审计；包含编译检查与测试生成。
when_to_use: 用户提到 X 相关代码时
next_skills: [writing-plans]
---
# My Workflow
1. 先做 A
2. 再做 B
3. 验证 C
```

### MCP 扩展

在 `config.toml` 的 `[mcp]` 段注册外部 MCP 服务器，其工具自动并入可用工具集，无需改代码。

## 🔌 对接 DeepSeek

DeepAct 从零为 DeepSeek 构建，不为"通用模型"妥协：

- **前缀缓存分层** —— 请求稳定区全命中、仅 volatile tail 缺失，直接省 token、降延迟。
- **`reasoning_content` 回显** —— DeepSeek 推理过程结构化存入会话，可回放、可审计。
- **温度分级路由** —— 依据任务类型（分析 / 编码 / 工具调用）自动调配温度，减少幻觉与无效重试。
- **双模型路由** —— `flash`（快、省）跑工具调用与日常任务，`pro`（强）跑设计评审与疑难推理，性价比按需分配。
- **重试与限速** —— 按 DeepSeek 错误特征（限流 / 超时 / 截断）自动降级，不空转。

配置示例（`~/.deepact/config.toml` 或项目级 `.deepact/config.toml`）：

```toml
[model]
api_key = "sk-..."        # 也可以 deepact set api-key
default = "flash"         # 默认路由模型

[search]
provider    = "tavily"    # 原生 web_search 工具
api_key     = "tvly-..."
max_results = 5
```

> [!TIP]
> 完整字段见配置文件内注释。模型与路由、权限模式、上下文预算、UI、LSP、MCP 服务器均可在 TOML 中配置。

## ✨ 核心能力

### 四道守卫

每个破坏性操作（文件编辑、shell 命令）先过四关：

1. **模糊检测** —— 含糊的请求会被反问
2. **设计审查** —— 反模式方案会被打回
3. **范围守卫** —— 越界操作会被拦截
4. **循环检测** —— 原地打转会被叫停

### 子代理并行

复杂任务拆给专用子代理（searcher / planner / critic / tester）独立推进，结果汇聚回主循环——快而不乱。

### 可回退

每步操作写入不可变 JSONL：可回退到任意步骤、分叉新分支；工具输出内容寻址存储，落盘前自动脱敏密钥。

## 📦 CLI 命令一览

| 命令 | 说明 |
|------|------|
| `deepact` | 交互式 TUI |
| `deepact exec <prompt>` | 非交互 / CI 模式（`--auto`、`--output`、`--max-turns`） |
| `deepact set [key] [value]` | 配置项（如 `set api-key`） |
| `deepact eval history` / `stats` / `compare <v1> <v2>` | 提示版本评估与对比 |

## 🧱 架构

```text
cmd/      CLI 入口（Cobra）        ui/       终端 UI（Bubble Tea）
engine/   代理循环·守卫·圆桌·子代理  policy/   模糊检测·设计审查·范围守卫
context/  提示构建·目录树快照·压缩   llm/      DeepSeek 客户端（流式·重试·限速）
tools/    内置工具 + MCP 适配        router/   模型路由
session/  JSONL 会话·分叉·回退       artifact/ 内容寻址存储·自动脱敏
skill/    外部技能加载与注册         config/   共享配置
```

分层铁律：`engine/` 不依赖 `ui/`/`cmd/`；`tools/` 不依赖 `engine/`；跨层调用走接口。

---

## License

[MIT](LICENSE)
