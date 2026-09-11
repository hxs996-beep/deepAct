# 移除关键词启发式 + 统一循环守卫 — 设计规格

> **日期：** 2026-09-11
> **状态：** 已批准（评审结论：主要违反发生在"引擎写死了本应由模型自主决定的判定"与"一类问题被拆成多个场景补丁"）

## 目标

对照 AGENTS.md 的两条设计原则（泛化性优先、能力优先），移除引擎中依赖关键词枚举的模型行为判定，并把四个各自为政的循环守卫收敛为统一计数机制：

1. **能力优先**：`isIntermediateText`（意图/计划句识别）与 `detectNegativeFeedback`（负面情绪识别）都是用场景枚举替模型做语义判断——删除，改为 prompt 纪律把判断交还模型。
2. **泛化性优先**：LoopGuard / ReadLoopState / ErrorLoopState / ProgressLoopState 是同一类问题（"同一可观察键重复计数"）被拆成的四个补丁——收敛为一个 `LoopTracker` 核心 + 配置，切断"被绕过→补新守卫"的演进路径。

## 背景

### 违反点 1：isIntermediateText（engine/classifier.go:30-62）

- 6 个关键词前缀（`Let me`/`让我`/`我来`/`我要先`/`接下来`/`我先`）判断"这句是中间意图还是结论"，命中即把 content 置空。
- 作用点：`engine/turn.go:226`（主 agent 有工具调用时剥离）、`engine/sub_agent.go:375`（子 agent 同）、`engine/sub_agent.go:685-702` `isPlanStatement`（collab 报告倒退选摘要时跳过计划句）。
- 症状：`classifier_test.go:92` 已为"让我读取文件后，发现 bug 在第 42 行"这种真实结论打反例补丁——典型场景枚举。

### 违反点 2：detectNegativeFeedback（engine/feedback.go）

- 30+ 中文词、7 个英文短语、2 个防误伤正则判断"用户负面情绪"，命中后 `applyNegativeFeedbackRewrite`（`loop.go:610` 调用）把 history 最后一条 user 消息**改写**成"用户对当前进展给出了负面反馈，请暂停反思重新规划"。
- 双重问题：枚举追不上表达方式；且改写文案预设了负面情绪与"模型路径有错"（用户仅轻微补充时可能引导模型假性自我否定）。

### 违反点 3：四个循环守卫（engine/guards.go）

| 守卫 | 计数键 | 阈值 |
|---|---|---|
| LoopGuard | `tool:path:contentHash`（精确重复） | 4 → block |
| ReadLoopState | `read:path::scope`（同 scope 重复读） | 3 nudge / 4 block |
| ErrorLoopState | `tool:path`（同目标反复失败，成功清零） | 3 → block |
| ProgressLoopState | 全局无进展轮次 | 4 nudge / 6 block |

- 演进史即"被绕过→补守卫"（`guards.go:523-524` 注释自认 ProgressLoopState 针对 "the loop form that bypassed all prior guards"）。
- 规律：全部是"同一可观察键重复计数"，差别只在键粒度（精确→scope→粗）与权重（失败使粗键累积）。

### 违反点 4：MadeProgress 枚举（engine/turn.go:620-652）

- 引擎枚举哪些工具算进展：edit/write/revert/bash 成功、novel read、handoff 算；grep/glob/lsp/todo_write、重复读、ask_user、叙述不算。
- 保守估计会误判"大量 grep/glob 的有效探索期"为无进展。决策：**新 scope 的 grep/glob 也算进展**（获取新信息＝推进理解）。

## 范围

**做：**
1. 删除 `isIntermediateText` 与 `isPlanStatement`（含两处清洗调用、相关测试）；`summarizeHistory` 诚实化。
2. 删除 `feedback.go`（含 `loop.go:610` 调用、`feedback_test.go`）。
3. system prompt（`context/promptset/zh/system.md`）新增两条纪律：输出纪律（调用工具回合不输出计划文字）+ 用户消息最高优先级纪律（中性措辞，不预设负面情绪、不预设模型有错）。
4. `guards.go` 重构：四个守卫收敛为 `LoopTracker` 统一计数核心 + 四个配置实例；key 构造收敛。
5. `turn.go` MadeProgress 扩展：新 scope 的 grep/glob 算进展（统一 `progressKeys` 机制）。
6. 适配/删除受影响测试。

**不做：**
- 不改变循环检测的确定性（harness 必要，模型不识别就空耗，不交判断权——AGENTS.md 确定性保护例外）。
- 不强行统一四个守卫的阈值数字（沿用现状，避免行为回归）。
- 不删除 `REMEMBER` 标记机制（显式记忆通道，合规）。
- 不删除危险命令黑名单（`guards.go:310-353`，AGENTS.md 明确豁免的安全确定性保护）。
- 不删除语言检测启发式（`langdetect.go`，配置类低风险）。

## 设计

### 变更 1：删除意图/计划句关键词判断

**删除：**
- `engine/classifier.go:30-62` `isIntermediateText`（保留 `extractRememberMarkers`）
- `engine/turn.go:226` 清洗调用（Layer 3）
- `engine/sub_agent.go:375` 清洗调用
- `engine/sub_agent.go:680-702` `isPlanStatement`
- `engine/classifier_test.go:73-101`、`engine/sub_agent_summarize_test.go:88-113`

**summarizeHistory 诚实化**（`sub_agent.go:636-678`）：
- 删除 `isPlanStatement` 跳过逻辑，**保留结构性过滤**（<50 字符、单行冒号结尾——客观结构，非语义猜测）。
- 行为变化：若最后一条实质消息是计划句，collab 报告直接显示它 + 前缀"（分析超时，部分结果）"，不再假装找到更实质的结论。

**prompt 新增输出纪律**（`zh/system.md`，主 agent + 子 agent 共用基底自动生效）：
> 调用工具的回合不要输出计划性文字（如"让我…""接下来…"），直接执行；结论只在最后无工具调用的回合给出。

### 变更 2：删除负面情绪关键词判断

**删除：**
- `engine/feedback.go` 整个文件
- `engine/loop.go:610` 调用
- `engine/feedback_test.go`

**prompt 新增用户消息优先级纪律**（`zh/system.md`，仅主 agent 语义、文案中性）：
> 用户的最新消息永远是最高优先级指令，可推翻此前任何计划。若最新消息包含纠正、不满或需求变更，先暂停当前执行，反思此前路径是否符合用户需求，重新规划并向用户说明新计划后再继续。

- 用户消息原样留在 history，不再被改写。
- 不添加每轮兜底注入（决策 A2：纯 prompt 纪律，零 context 开销）。

### 变更 3：统一循环守卫

`engine/guards.go` 重构为：

```go
// LoopTracker 统一计数核心（替换 LoopGuard/ReadLoopState/ErrorLoopState/ProgressLoopState）
type LoopTracker struct {
    mu             sync.Mutex
    counts         map[string]int
    nudgeAt        int // 0 = 无 nudge
    blockAt        int
    resetOnSuccess bool // error 语义：同键成功即清零
    // 全局计数（progress）用 key="" 约定，无独立字段。
}
func (t *LoopTracker) Check(key string, success bool) GuardAction // allow → diagnose(nudge) → block
func (t *LoopTracker) Reset()
```

Engine 持有四个配置实例（loop / read / error / progress），共享同一套 Check/Reset/双阈值/并发安全语义：

| 实例 | key | nudgeAt | blockAt | resetOnSuccess |
|---|---|---|---|---|
| loop | `tool:path:contentHash` | 0 | 6 | false |
| read | `read:path::scope` | 3 | 4 | false |
| error | `tool:path`（粗） | 0 | 3 | true |
| progress | 忽略 key（全局计数，调用 `Check("", progress)`） | 4 | 6 | 外部调用方在 progress 信号时传 success=true |

- **新循环变体 = 新增一个配置实例 + 新 key 构造，不再写新 struct**——切断"被绕过→补守卫"的演进路径。
- key 构造收敛：`extractToolKey` / `readMultiTargetScope` / `extractReadScope` / `contentSignature` 统一收敛到 `keyFromCall(call, mode)`，`turn.go` 分散的 key 构造点收敛。

### 变更 4：MadeProgress 扩展

`turn.go:620-652`：
- 新增 case `grep` / `glob`：以 `grep:pattern:path` / `glob:pattern:path` 为 key（pattern 为空时用 path），未见过 → `MadeProgress=true`。
- 复用现有 `readProgressKeys` 机制，扩展为统一 `progressKeys` map。
- 语义：**获取新信息（read 或 grep/glob）＝推进理解＝进展**；重复获取同一信息由变更 3 的 read/loop 守卫抓，叙述且不获取任何新信息由 progress 守卫抓。

## 影响文件

| 文件 | 变更 |
|---|---|
| `engine/classifier.go` | 删 isIntermediateText |
| `engine/turn.go` | 删清洗调用、MadeProgress 扩展、key 收敛 |
| `engine/sub_agent.go` | 删清洗调用、删 isPlanStatement、summarizeHistory 诚实化 |
| `engine/feedback.go` | 整个删除 |
| `engine/loop.go` | 删反馈调用、key/守卫接入调整 |
| `engine/guards.go` | 重构为 LoopTracker |
| `context/promptset/zh/system.md` | 加两条纪律 |
| 测试 | `classifier_test.go`、`feedback_test.go`、`sub_agent_summarize_test.go`、`guards_test.go`、`turn_test.go` 等同步 |

## 测试与验证

- 删除的测试 → 删；`LoopTracker` 新测试覆盖四个原守卫的阈值语义（含回归：原 guards_test 各场景映射到新配置）。
- `summarizeHistory` 诚实化：新增"计划句作为部分结果直接返回"测试。
- 验证：`go build ./... && go test ./engine/... ./context/...`（race 开启）。

## 已确认决策

1. 主题 1：方案 A（删除 isIntermediateText，提示纪律 + summarizeHistory 诚实化）。
2. 主题 2：方案 A2（删除 feedback.go，纯 prompt 纪律，中性措辞，无每轮注入）。
3. 主题 3：统一 RepetitionGuard（现名 LoopTracker），保持纯确定性（harness 必要，不交判断权）。
4. 主题 4：B2（新 scope 的 grep/glob 算进展）。
