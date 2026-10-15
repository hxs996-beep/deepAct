# DeepAct — 轻量级终端 AI 编码代理

**简体中文** | [English](README.md)

<p align="center">
  <a href="https://goreportcard.com/report/github.com/hxs996-beep/deepAct"><img src="https://img.shields.io/badge/go_report-A-brightgreen?style=flat-square" alt="Go Report"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-MIT-blue?style=flat-square" alt="MIT"></a>
  <a href="https://golang.org"><img src="https://img.shields.io/badge/Go-1.24+-00ADD8?style=flat-square&logo=go" alt="Go 1.24+"></a>
  <img src="https://img.shields.io/badge/platform-macOS%20%7C%20Linux%20%7C%20Windows-lightgrey?style=flat-square" alt="Platforms">
</p>

<p align="center">
  <b>⚡ 单二进制 · 零运行时依赖</b>
</p>

**DeepAct 是一个跑在终端里的 AI coding agent（AI 编码代理）**：用 Go 编写、静态编译、开源（MIT）。

---

## 📖 目录

1. [快速开始](#快速开始)
2. [日常使用](#日常使用)
3. [对接模型](#对接模型)
4. [核心能力](#核心能力)
5. [CLI 命令一览](#cli-命令一览)
6. [架构](#架构)

---

## 🚀 快速开始

> [!NOTE]
> 需要一个 [DeepSeek API Key](https://platform.deepseek.com/)。首次启动时 TUI 会交互式提示输入，并自动持久化到 `~/.deepact/config.toml`——无需手动配置步骤。

### 安装

```bash
# macOS / Linux 一键安装
curl -sSfL https://raw.githubusercontent.com/hxs996-beep/deepAct/main/install.sh | sh

# 或从源码构建
git clone https://github.com/hxs996-beep/deepAct && cd deepAct && make install
```

Windows 用户见 [Releases](https://github.com/hxs996-beep/deepAct/releases)（PowerShell 或手动下载）。

### 开始使用

```bash
deepact                      # 启动交互式 TUI（Windows / macOS / Linux 通用）
```

环境变量 `DEEPSEEK_API_KEY` 优先级最高；项目级配置 `.deepact/config.toml` 覆盖全局配置。

## 🖥️ 日常使用

### TUI 快捷键

| 按键 | 作用 |
|------|------|
| `Ctrl+Q` | 退出 |
| `Esc` | 取消当前任务 / 清空输入 |
| `Enter` | 提交 |
| `Shift+Enter` | 换行 |
| `Tab` | 补全 |
| `↑` | 召回上次提交的内容（输入框为空时） |

### 并行研究（/collab）

在 TUI 里输入 `/collab 加一个缓存层`。`/collab` 是内置协作技能，由 `handoff_to_agent` 编排：主 agent 自己把目标拆成 2~6 个研究方向，然后**并行**把每个方向委派给 `researcher` 子代理（只读工具），再把各方向发现合并成一份结构化报告——需要快速摸清广度时，比串行调研更快。

### 独立复核（/review）

在 TUI 里输入 `/review 当前改动`，也可以缩小范围，如 `/review 对比 main 分支`。`/review` 是**你主动调用**的内置技能——它被排除在模型可见的技能目录之外，agent 不会自己发起复核。命令后面的文字就是复核范围，且**必须给**：裸 `/review` 会让引擎拿不到任务，只加载技能、不启动任何运行。范围会被映射成事实（`git status` + `git diff HEAD`、某个 commit、某个分支、或某个文档），diff 加一份评审要求交给**独立**的 `sub` 子代理——它有自己的一份上下文，而不是作者那段对话——然后每条发现都要先对着代码核验再报告。发现带优先级（P0–P3）、置信度与 `file:line` 证据；复核者只读，未经你同意不改任何代码。

### 项目规范与技能（Skills）

项目规范、工作流、领域知识通过**技能**注入系统提示：技能列表渲染进稳定区，Agent 在任务明显匹配某技能描述时调用 `load_skill` 工具加载其全文再遵循，也可用 `/<name>` 直接加载。

内置技能（`skill/builtin/`，go:embed：`ratd` / `collab` / `review`）最先注册、优先级最低；用户技能从常见 agent 技能目录加载（`~/.claude/skills/`、`~/.agent/skills/`、`<项目>/.claude/skills/`、`~/.deepact/skills/`），同名冲突时 `~/.deepact/skills/` 覆盖。也可用 `skill_install` 工具从社区仓库安装技能。

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

在项目目录下的 `.deepact/mcp.json` 注册外部 MCP 服务器——**你运行 DeepAct 的那个目录里的这份优先**，该文件不存在时才回落到 `~/.deepact/mcp.json`。其工具自动并入可用工具集，无需改代码；两份文件不会合并，只用找到的第一份。

`env` 块用于传非敏感值，会原样交给 server（不做 `${VAR}` 展开）；而 server 本就继承你的 shell 环境，所以 token 改为在 shell 里 export，不要写进文件：

```json
{
  "servers": [
    { "name": "github", "command": "npx", "args": ["-y", "@modelcontextprotocol/server-github"] }
  ]
}
```

## 🔌 对接模型

默认端点是 DeepSeek；`[model].base_url` 可把客户端指向任意 OpenAI 兼容端点，没有 provider 映射。以下是客户端为这个默认端点实现的 DeepSeek 专属优化：

- **前缀缓存分层** —— 请求稳定区全命中、仅 volatile tail 缺失，直接省 token、降延迟。
- **`reasoning_content` 回显** —— DeepSeek 推理过程结构化存入会话，可回放、可审计。
- **温度分级路由** —— 依据任务类型（分析 / 编码 / 工具调用）自动调配温度，减少幻觉与无效重试。
- **双模型路由** —— 主循环的 `selectModel()` 固定返回主模型（稳定的 model 字段保 DeepSeek per-model 前缀缓存命中）；`flash` 仅用于子代理与压缩路径，性价比按需分配。
- **重试与限速** —— 按 DeepSeek 错误特征（限流 / 超时 / 截断）自动降级，不空转。

配置示例（`~/.deepact/config.toml`）。key 只放这个用户级文件：项目级 `.deepact/config.toml` 是**取代**它而非与其合并，所以仓库里若保留项目级配置，请改用 `DEEPSEEK_API_KEY` 环境变量传 key：

```toml
[model]
api_key = "sk-..."        # 或：DEEPSEEK_API_KEY 环境变量 / 首次启动 TUI 提示
default = "deepseek-v4-flash"  # 子代理与压缩使用的模型
escalation = "<主循环模型>"     # 主循环使用的模型（可选；不填则用 flash 模型）
reasoning_effort = "high" # 思考强度：none | low | high | max（可选，默认 high）

[search]
provider    = "tavily"    # 原生 web_search 工具
api_key     = "tvly-..."
max_results = 5

[approval]
mode = "on-request"       # 或 "never"：项目级危险命令不再询问（system 级仍硬拦）

[[approval.rules]]        # 声明式命令规则，优先于内置判定
program  = "terraform"
args     = ["destroy"]    # token 前缀；"*" 匹配任意单个 token（留空 = 该程序的所有调用）
decision = "forbidden"    # allow | prompt | forbidden
reason   = "生产环境禁地"
```

> [!NOTE]
> 危险命令分四层判定，越靠前越权威：① 系统级不可逆操作（硬拦，规则无法放行）② `decision = "forbidden"` 规则 ③ `decision = "allow"` 规则与你在会话中确认过的命令 ④ 项目级危险与 `decision = "prompt"` 规则（按 `mode` 决定是否询问）。你在弹窗里确认过的命令会被**原样记住**并跨会话生效（存于 `~/.deepact/approvals.toml`）；要放行一整类命令，请显式声明 `[[approval.rules]]`，而不是指望确认一次就自动放宽。

> [!TIP]
> 所有字段都可通过 TOML 配置：模型、上下文预算、UI、LSP、MCP 服务器、自定义子代理角色；权威列表是 `config/config.go` 里 `File` 结构体的 `toml` tag。

## ✨ 核心能力

### 子代理并行

`handoff_to_agent` 委派给不同内置角色的子代理——`sub`（通用只读分析）、`researcher`（只读调研），以及 `ratd` 流水线角色 `proposer` / `redteam` / `arbitrator`。同一轮发出的多个 handoff **并行**执行（`tools/registry.go` 每个工具调用一个 goroutine），结果汇聚回主循环——快而不乱。代码复核不是角色：它是用户主动调用的 `/review` 技能（见上），所以你要求时才付一次子代理运行的成本。

子代理是**只读的**：可以读、搜、分析（`read`、`read_multi`、`grep`、`glob`、`lsp`、`web_search`、`fetch`），不能修改文件或运行命令——`bash`/`write`/`edit`/`revert`、`skill_install` 与 MCP 工具在任何委派深度都被排除，per-call 的 `tools` 覆盖只能收窄角色工具集、不能放宽。修改属于主代理（其 bash 仍经过危险命令守卫）。`config.toml` 里 `[agents]` 角色声明写类工具会在启动时报错并给出迁移指引。

子代理 run 同时受**花费护栏**约束：每个 run 的计费 token（cache-miss + completion，cache hit 免费）默认上限为 2× 上下文窗（`[context].sub_agent_token_budget`，`-1` 表示显式关闭），80% 时注入收尾提示、100% 时带部分结果终止。后台（`async`）handoff 同时最多 8 个未取回任务——用 `agent_poll` 收结果释放名额。

子代理遇到需要你决定的问题时会**停下提问**，而不是自行猜：问题会呈现给你，你回答后模型可调用 `agent_resume` 让**同一个 run** 带着已经建立的上下文继续——原 history、前缀缓存分区与预算都延续，不会把已经读过的内容重读一遍。挂起 run 上限 4 个（`[context].max_suspended_subagents`）、30 分钟后过期；挂起态只在进程内，退出 TUI 后回落为普通重新委派。

### 会话记录

每步操作写入不可变 JSONL，`/resume` 可重新打开早前的会话；工具输出内容寻址存储，落盘前自动脱敏密钥。

## 📦 CLI 命令一览

| 命令 | 说明 |
|------|------|
| `deepact` | 交互式 TUI（首次启动提示输入 API Key） |
| `deepact set api-key <key>` | 将 API Key 写入 `~/.deepact/config.toml` |

## 🧱 架构

```text
cmd/      CLI 入口（Cobra）        ui/       终端 UI（Bubble Tea）
engine/   代理循环·子代理·共享类型中枢
context/  提示构建·目录树快照·压缩   llm/      DeepSeek 客户端（流式·重试·限速）
tools/    内置工具（builtin/）+ MCP（mcp/）
session/  JSONL 会话日志            artifact/ 内容寻址存储·自动脱敏
skill/    内置（ratd/collab/review）+ 外部技能加载    config/   共享配置
memory/   跨会话持久记忆
```

分层：`engine/` 是共享类型/接口中枢（hub-and-spoke）。核心 `llm/`、`tools/` 文件零项目依赖，由小型 `adapter.go` 桥接文件对接 engine 类型；`engine/` 不依赖 `ui/`/`cmd/`；跨层调用走接口。

---

## License

[MIT](LICENSE)
