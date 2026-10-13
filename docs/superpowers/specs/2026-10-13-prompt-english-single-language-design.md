# 提示词统一为英文单语（Design）

日期：2026-10-13
状态：已实施

## 背景与目标

DeepAct 的模型可见文本此前是多语混态：

- 系统提示词正文是中文单语（`context/promptset/zh/system.md` 等三份，`promptset.Get()` 硬编码 `zh/`）；
- 但注入块标题是英文（`# Language Pack`、`## Available Skills`、` Use when `）；
- 语言包只有 `generic`/`go` 有中文，`java/python/rust/typescript` 只有英文；
- 引擎内 model-facing 文本一部分用 `pickPrompt(zh, en, zhPrompt)` 双语，一部分只有中文、一部分只有英文；
- 角色 persona 只有英文，而内置技能文档却声称 persona 是中文/中英双语。

外部参照（`/Users/admin/gitspace/deepseek-harness`、claude-code、codex、DeepSeek-TUI、pi、oh-my-opencode）一致的做法是：**模型提示词单语（英文），不做双套；本地化只落在 UI 文案表、部署 persona 配置与文档双语对**。

目标：**把提示词统一为英文单语，删除全部中英分支**（工具 schema 保持英文）。

## 方案

选定方向为「英文单语」。据此：

1. 提示词正文由中文改写为英文（`zh/` → `en/`）。
2. 删除 `# Available Skills` 之外的混态来源：语言包只保留单份、去掉 `userLang` 选择与 `zh/` 回退。
3. 删除引擎的双语机制：`pickPrompt`、`UserLanguage`/`Lang*`/`IsChinese`/`UserLanguageFor`/`zhFromLang`、`SetUserLang`、`ToolExecContext.UserLang`、`Handoff.UserLanguage`、`ContextAssembler` 的会话语言检测与锁定、`langPackZh/En`、各 tool spec 的 `zh bool` 参数。
4. 把此前"只有中文"的模型可见文本改写为英文；"只有英文"的保持。
5. 内置技能文档（collab/ratd）改英文，并校正与代码不符的 persona 表述。

保留：`用用户的语言回复` 这条规则以英文形式保留在系统提示词里，会话语言不再影响提示词内容本身。

## 改动清单

| 区域 | 文件 | 变更 |
|---|---|---|
| 提示词正文 | `context/promptset/en/{system,examples,sub_agent}.md` | 新增（`zh/` 删除并改写为英文） |
| | `context/promptset/promptset.go` | embed 指向 `en/` |
| 语言包 | `context/langpacks/*.md` | 单一英文源；`langpacks/zh/` 删除 |
| | `context/langpacks.go` | `GetLangPacks(hits)`、`langPack(lang)`，去 `userLang` |
| 上下文 | `context/prompt.go`、`builder.go`、`langdetect.go`、`agents.go` | 去语言检测/分支；Block S 与 AGENTS.md 区块英文 |
| 引擎 | `engine/prompts_i18n.go` | 删 `pickPrompt`/`UserLanguage` 全家；`todoNudgeMessage` 单语 |
| | `engine/{agent,agent_manage,sub_agent,loop,turn,handoff,compressor,plan_methodology,approval,guards,stop_hook,suspend,types,interfaces}.go` | 去 `zh`/`userLang` 参数与中文分支；中文 model-facing 文本改英文 |
| 工具/装配 | `tools/{registry,adapter,subagent}.go`、`cmd/run.go` | 去 `UserLang` 接线；`SetLangPacks`→`SetLangPack` |
| 技能 | `skill/builtin/{collab,ratd}/SKILL.md` | 改英文；校正 persona 描述 |

## 不自洽点与取舍

- **不改代码注释**：注释不是模型可见文本，翻译它们属无关改动。
- **不改 UI 文案**：`ui/` 与 `cmd/eval.go` 的报告是用户可见输出（CLI 界面语言为中文），不属于提示词范围。
- **引擎产出的用户可见文案统一英文**：危险命令确认框（`engine/approval.go`）与同屏的确认/取消选项（`engine/loop.go`）一并英文化，一个弹窗内不混用两种语言；`engine/loop.go` 的 `输入你的意见`（ask_user 弹窗的自由输入项，与模型给出的用户语言选项同屏）仍为中文。
- **模型可见文本中的中文标记一并英文化**：AGENTS.md 偏好小节标题 `## 用户偏好` → `## User Preferences`（`tools/builtin/remember_preference.go`，含旧标题原位改名迁移）；`read` 工具「未变更」提示里重复的中文句删除（`tools/builtin/read.go`）。
- **删除语言检测**：提示词不再随会话语言变化，`detectUserLanguage`/`classifyTextLanguage`/`msgIsChinese` 成为死代码，随机制一并删除。项目语言检测（`DetectLanguages`）保留。
- **公开签名变更**：`engine.UserLanguage`、`Compressor.SetUserLang`、`Handoff.UserLanguage`、`ToolExecContext.UserLang`、`tools.SubAgentBackend` 的 `userLang` 参数被移除——这是"英文单语"的直接后果。

## 验证

```
go build ./...        # PASS
go vet ./...          # PASS
go test -count=1 ./... # 全部包 ok
```

另以脚本扫描生产代码中的字符串字面量，确认模型可见文本已无中文残留：系统提示词、语言包、技能文档、工具 description 与工具返回文本、引擎内 nudge/注入块零中文；剩余中文仅在代码注释、UI 文案（`ui/`、`cmd/eval.go`）、`memory.md` 渲染（`memory/render.go`，只写盘不回读）与 `engine/dsml.go` 的全角括号兼容正则，以及 `tools/builtin/remember_preference.go` 的旧标题迁移常量（仅用于匹配历史 AGENTS.md，不进提示词）。

## 复验修正（2026-10-13）

复验发现三处未覆盖，已在本设计实施后补修：

| 位置 | 问题 | 修正 |
|---|---|---|
| `tools/builtin/read.go` `unchangedReadHint` | 「未变更」提示是英文句 + 中文句重复，且前缀在 `read` 工具返回文本上（模型可见） | 删除中文句，只留英文 |
| `tools/builtin/remember_preference.go` `preferenceSectionHeading` | `## 用户偏好` 既进入工具 description，又作为 AGENTS.md 小节标题被注入稳定提示词 | 改为 `## User Preferences`；用 `legacyPreferenceHeadings` 保留旧标题并原位改名，避免旧 AGENTS.md 分裂出两节（含 2 个迁移测试） |
| `engine/loop.go` 危险命令弹窗选项 | 确认框正文已英文，选项 `确认执行`/`取消` 仍中文，同屏混排 | 改为 `Confirm`/`Cancel` |
