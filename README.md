# DeepAct — 专为 DeepSeek 打造的终端 AI 编码代理

<p align="center">
  <a href="https://github.com/hxs996-beep/deepAct/releases"><img src="https://img.shields.io/github/v/release/hxs996-beep/deepAct?style=flat-square" alt="Release"></a>
  <a href="https://goreportcard.com/report/github.com/deepact/deepact"><img src="https://img.shields.io/badge/go_report-A-brightgreen?style=flat-square" alt="Go Report"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-MIT-blue?style=flat-square" alt="MIT"></a>
  <a href="https://golang.org"><img src="https://img.shields.io/badge/Go-1.24+-00ADD8?style=flat-square&logo=go" alt="Go 1.24+"></a>
</p>

<p align="center">
  <b>DeepSeek 原生 · 圆桌团队做需求 · 四道守卫 · 子代理并行 · MCP 扩展</b>
</p>

---

> **DeepAct 是什么？** 一个跑在终端里的 AI 编码助手，从零为 DeepSeek 模型构建。提示工程、前缀缓存、温度调度、工具调用格式都针对 DeepSeek API 调优——相比"通用代理换上 DeepSeek 模型"，**成本更低、响应更快、指令遵循更准**。

## 为什么选 DeepAct

- **圆桌团队做需求（Roundtable）** —— 输入 `/round <需求>`，主代理先就需求生成 2-3 个实现方案，随后多位带立场的角色（架构师、安全工程师……）**并行评审、独立打分**，输出方案×角色评分矩阵；你选定方案后，代理循环直接落地实现。一个 agent 闭门造车 vs. 一桌专家对着吵，结论质量不在一个量级。
- **四道守卫** —— 每个破坏性操作经过：模糊检测 → 设计审查 → 范围守卫 → 循环检测。含糊的请求会被反问，反模式方案会被打回，越界操作会被拦截，原地打转会被叫停。
- **子代理并行** —— 复杂任务拆给专用子代理（searcher / planner / critic / tester）独立推进，结果汇聚回主循环。
- **DeepSeek 原生** —— `reasoning_content` 回显、前缀缓存分层（稳定区全命中，仅 volatile tail 缺失）、温度分级路由，全部按 DeepSeek API 特性调校。
- **MCP 扩展** —— 接入任意 MCP 服务器，工具集随需扩展。
- **可回退** —— 每步操作不可变 JSONL 记录，可回退到任意步骤、分叉新分支；工具输出内容寻址存储，落盘前自动脱敏密钥。

## 快速开始

需要一个 [DeepSeek API Key](https://platform.deepseek.com/)。二进制静态编译，零运行时依赖。

```bash
# 安装（macOS / Linux 一键）
curl -sSfL https://raw.githubusercontent.com/hxs996-beep/deepAct/main/install.sh | sh

# 或 Homebrew
brew install hxs996-beep/homebrew-tap/deepact

# 或 Go
go install github.com/deepact/deepact@latest
```

Windows PowerShell、手动下载见 [Releases](https://github.com/hxs996-beep/deepAct/releases)。

```bash
deepact set api-key          # 交互式配置 API Key
deepact                      # 启动交互式 TUI（默认）
deepact exec "修复连接池竞态"  # 非交互 / CI 模式
```

## 用户接入点

**`deepact.md`** — 放在项目根目录，定义本项目对 AI 的编码规范、架构约束、安全红线。DeepAct 启动自动加载。本项目自带的 [`deepact.md`](deepact.md) 就是完整范例。

**`.deepact/config.toml`** — 项目级（或全局 `~/.deepact/config.toml`）配置：模型与路由、权限模式、危险命令白名单、上下文预算、UI、LSP、MCP 服务器等。完整字段见文件内注释。

**外部技能** — 在 `~/.deepact/skills/` 放 TOML 技能文件，TUI 中以 `/<name>` 激活。技能可声明 `next_skills` 形成链式工作流。

```toml
# ~/.deepact/skills/my-flow.toml
name        = "my-flow"
description = "我的自定义工作流"
keywords    = ["我的项目"]
next_skills = ["writing-plans"]
content     = """
# My Workflow
1. 先做 A
2. 再做 B
3. 验证 C
"""
```

**MCP 服务器** — 在 `config.toml` 的 `[mcp]` 段注册外部 MCP 服务器，其工具自动并入可用工具集。

## CLI 命令

| 命令 | 说明 |
|------|------|
| `deepact` | 交互式 TUI |
| `deepact exec <prompt>` | 非交互 / CI 模式 |
| `deepact set [key] [value]` | 配置项（如 `set api-key`） |
| `deepact eval history` / `stats` / `compare <v1> <v2>` | 提示版本评估与对比 |

TUI 常用键：`Ctrl+Q` 退出 · `Esc` 取消任务 · `Enter` 提交 · `Tab` 补全 · `Alt+Enter` 换行。

## 架构

```
cmd/      CLI 入口（Cobra）        ui/       终端 UI（Bubble Tea）
engine/   代理循环·守卫·圆桌·子代理  policy/   模糊检测·设计审查·范围守卫
context/  提示构建·代码库映射·压缩   llm/      DeepSeek 客户端（流式·重试·限速）
tools/    内置工具 + MCP 适配        router/   模型路由
session/  JSONL 会话·分叉·回退       artifact/ 内容寻址存储·自动脱敏
skill/    外部技能加载与注册         config/   共享配置
```

分层铁律：`engine/` 不依赖 `ui/`/`cmd/`；`tools/` 不依赖 `engine/`；跨层调用走接口。

## License

[MIT](LICENSE)
