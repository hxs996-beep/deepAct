---
name: ratd
description: 反向测试驱动开发（Red-Team-Then-Drive）：由红队写对抗测试，再驱动实现。需要严格验证实现、防止过度自信时使用。
when_to_use: 用户要求"先写测试再实现"、需要对抗性验证、或明确使用 /ratd 时
argument-hint: "<需求>"
---

# 反向测试驱动（/ratd）

## 角色与编排（模型自主按此剧本编排，非硬流程）

你是编排者，用 `handoff_to_agent` 逐角色委派，每角色一次委派、轮数上限 3 轮。三个角色均为引擎内置具名角色，角色 persona 与只读工具白名单已内置，goal 只放任务输入：

### 1. Proposer（提议者）
`handoff_to_agent`（**agent="proposer"**），goal 放需求描述（含约束、验收标准）。角色输出 CodePayload JSON：
```json
{"language":"go","files":[{"path":"相对路径","content":"完整代码"}],"design_notes":"设计说明"}
```
拿到 JSON 后由你（编排者）用 edit/write 把 `files` 落盘到对应路径。

### 2. RedTeam（红队）
`handoff_to_agent`（**agent="redteam"**），goal 放实现文件清单 + 落盘路径，请其针对性产出对抗测试。角色输出 TestPayload JSON：
```json
{"tests":[{"path":"相对路径","content":"测试代码"}],"no_issues":false}
```
若无问题则输出 `{"tests":[],"no_issues":true}`。拿到 JSON 后由你把 `tests` 落盘。

### 3. Sandbox（引擎侧执行）
- 由你（编排者）用 `bash` 执行测试命令，语言映射：go→`go test -timeout 60s ./...`。
- 失败时收集输出。

### 4. Arbitrator（仲裁员）
`handoff_to_agent`（**agent="arbitrator"**），goal 放失败测试输出 + 实现/测试文件路径，请其判定。角色输出 decision JSON：
```json
{"decision":"ACCEPT","feedback":"重构方向"} 或 {"decision":"REJECT","feedback":"红队测试缺陷"}
```
ACCEPT=实现有问题，回到 Proposer 重构；REJECT=测试本身有问题，删除该测试文件回到 RedTeam。

## 完成判据
- 沙箱通过且红队无问题（no_issues=true）→ `task_complete`，交付摘要含：改动文件清单 + 最终测试结果。
- 循环由你按纪律控制（≤3 轮），`LoopTracker` 兜底防角色循环。

## 注意
- 会话语言为中文时，goal 用中文描述任务输入；英文时译为英文。
- 角色 persona 已内置中英双语 JSON 契约，编排者无需重复角色提示。
