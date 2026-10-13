# 通用核心层 + 可挂载编码领域层（Design）

> **状态：已回退（2026-10-13）。** 该方案建立在“DeepAct 应做成领域无关底座”的前提上，而外部参照（harness / pi / codex / claude-code）表明：它们**默认就是 coding agent 预设**，不追求通用底座。此文档仅作历史记录，代码已还原为单一编码向提示词（见 `2026-10-13-prompt-english-single-language-design.md`）。

日期：2026-10-13
状态：已回退

## 背景

`system.md` 是一份编码语境下的固定长文（拆分前 21.9KB ≈ 5.5k token / 277 行）。非编码业务任务上，其中编码专用内容（代码质量规则、编码范例、语言包、LSP 优先、bug 修复约束）成为噪声，并把模型往"改代码"路径上带。与外部对照：harness 的内置 prompt 几乎不含行为元规则（identity 3 行），codex 整份 prompt 约 1.6k token。

## 目标

- 核心层只保留与领域无关的行为规则，始终挂载。
- 编码相关内容（工具纪律/质量规则/语言包/范例）拆为**可挂载**的领域层，仅在编码工作区挂载。

## 方案

### 分层

| 层 | 文件 | 内容 |
|---|---|---|
| Core（始终挂载） | `context/promptset/core/system.md` | 身份（通用 agent，非"编码助手"）、技能优先、消息优先级、输入解析、意图分类、改动前先读、Never/Always、回复格式、回合纪律、通用工具纪律（去 LSP 专用项）、反过度实现/反懒惰/反冗长/反幻觉/反锚定、目标驱动、步骤上报、边界与停止条件、完成前自验、通用验收清单 |
| Core | `context/promptset/core/sub_agent.md` | 子代理输出契约（本就与领域无关） |
| Coding（可挂载） | `context/promptset/coding/system.md` | LSP 优先与符号查找优先级链、改代码规则（只修 bug/按模式/类型抑制/SQL-eval/孤儿清理）、跑测试与代码验收项 |
| Coding（可挂载） | `context/promptset/coding/examples.md` | 5 个编码范例 |
| Coding（可挂载） | `context/langpacks/*.md` | 各语言规则包（含 generic） |

### 挂载规则

`context.CodingLayer(projectRoot)`：复用已有的项目语言检测（`DetectLanguages`）。检测到任一语言 → 返回 `Coding` + `# Language Pack` + 语言包 + `CodingExamples`；未检测到 → 返回 `""`（只有核心层）。

主 agent（`ContextAssembler`）与子代理（`SubAgentRunner.SetCodingLayer`）挂载**同一份**编码层，由 `cmd/run.go` 在启动时计算一次并注入，避免两侧漂移。

`promptset.Get()` 改为返回 `{Core, Coding, CodingExamples, SubAgent}` 四层；挂载决策交给调用方。

## 效果（估算 token，英文 ≈ 4 字符/token）

| | 之前 | 之后 |
|---|---|---|
| 非编码工作区 system prompt | ~7.3k | **~5.0k**（仅核心层） |
| 编码工作区 system prompt | ~7.3k | ~7.3k（核心 + 编码层，编码部分占 31%） |

即：非编码场景的固定开销下降约 31%，且不再被注入代码规则/编码范例/语言包；编码场景行为基本不变（内容重组而非删减）。

## 取舍与未做

- **未精简核心层本身**：核心层仍有 ~5.0k token 的行为元规则，且存在既有重叠（"技能优先"在核心层与 skills 目录块各写一遍；`边界/Never/Stop conditions` 三处重叠）。这属于"把提示词做薄/去重"的范畴，是独立的一步，本次未做。
- **挂载判据只按项目语言检测**：未新增配置开关。一个非编码任务若在代码仓内运行，仍会挂编码层（可接受：工作区本身是编码仓）。若需要显式开关（`[context].prompt_domains`），可在 `CodingLayer` 这一处扩展。
- 未新增其他领域层（写作/数据等），但分层结构已允许按同法添加。

## 验证

```
go build ./...          # PASS
go vet ./...            # PASS
go test -count=1 ./...  # 全部包 ok
```

新增测试锁定挂载规则：`TestBuild_NonCodingWorkspaceGetsCorePromptOnly`（非编码工作区不含编码层）、`TestBuild_CodingLayerMountedWhenProjectLanguageDetected`（含 `go.mod` 的工作区挂载编码层）。
