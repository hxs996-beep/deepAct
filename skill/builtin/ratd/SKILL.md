---
name: ratd
description: 反向测试驱动开发（Red-Team-Then-Drive）：由红队写对抗测试，再驱动实现。需要严格验证实现、防止过度自信时使用。
when_to_use: 用户要求"先写测试再实现"、需要对抗性验证、或明确使用 /ratd 时
argument-hint: "<需求>"
---

# 反向测试驱动（/ratd）

## 角色与编排（模型自主按此剧本编排，非硬流程）

你是编排者，用 `handoff_to_agent`（agent=sub）逐角色委派，每角色一次委派、轮数上限 3 轮：

### 1. Proposer（提议者）
把下面角色提示原文放入 handoff goal，tools 白名单 `read/grep/glob/lsp`：

> 你是「提议者」——资深工程师。基于需求产出实现代码。只输出 CodePayload JSON：
> ```json
> {"language":"go","files":[{"path":"相对路径","content":"完整代码"}],"design_notes":"设计说明"}
> ```
> 约束：单文件优先；不得写测试；代码必须自洽可编译。

### 2. RedTeam（红队）
把下面角色提示原文放入 handoff goal，tools 白名单 `read/grep/glob/lsp`：

> 你是「红队」——对抗性测试工程师。针对实现产出对抗测试。只输出 TestPayload JSON：
> ```json
> {"tests":[{"path":"相对路径","content":"测试代码"}],"no_issues":false}
> ```
> 若无问题则输出 `{"tests":[],"no_issues":true}`。

### 3. Sandbox（引擎侧执行）
- 由你（编排者）用 `bash` 执行测试命令，语言映射：go→`go test -timeout 60s ./...`。
- 失败时收集输出。

### 4. Arbitrator（仲裁员）
把下面角色提示原文放入 handoff goal，tools 白名单 `read/grep/glob/lsp`：

> 你是「仲裁员」。基于失败测试与沙箱输出判定：`{"decision":"ACCEPT","feedback":"重构方向"}` 或 `{"decision":"REJECT","feedback":"红队测试缺陷"}`。
> ACCEPT=实现有问题，回到 Proposer 重构；REJECT=测试本身有问题，删除该测试文件回到 RedTeam。

## 完成判据
- 沙箱通过且红队无问题（no_issues=true）→ `task_complete`，交付摘要含：改动文件清单 + 最终测试结果。
- 循环由你按纪律控制（≤3 轮），`LoopTracker` 兜底防角色循环。

## 注意
- 会话语言为中文时使用上面中文角色提示；英文时译为英文。
- 两语言变体语义必须一致。
