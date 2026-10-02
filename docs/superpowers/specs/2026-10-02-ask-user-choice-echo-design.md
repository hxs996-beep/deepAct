# ask_user 选择回显设计（选择记录）

- 日期：2026-10-02
- 状态：设计已批准（方案 A）
- 范围：`ui/` 纯展示改动，零引擎改动

## 背景与问题

带 options 的 ask_user 会在 UI 弹出候选框。用户回车确定后，弹窗消失，且 UI **不追加任何消息**：`ui/model.go:1476 submitConfirm` 只发送内部 `/confirm N`，不写 `DisplayMessage`。相比之下，自由输入路径会在 `submitInput`（`ui/model.go:1440`）追加一条 `Role:"user"` 消息。

结果：用户事后无法在对话流里回看自己选了哪个方案，更无从反悔改选。

## 目标

用户确定选择后，在对话流追加一条不可变的“选择记录”，展示全部候选与所选项。

## 非目标

- 不回显问题文本（已确认不需要，当前 UI 展示已足够清晰）。
- 不跨 `/resume` 持久化：引擎 history 仍保留“用户选择了：X，请按该方案执行修改。”一行，本次不额外持久化问答记录。
- 不覆盖 Tab 路径：Tab 仅把编号填入输入框，随后作为自由输入提交，不是确定性选择路径。
- 不从候选中剔除引擎追加的“输入你的意见”（`engine/loop.go:731`）——避免以字符串内容为键的脆弱判断，按弹窗原样展示。

## 触发与数据来源

- 触发点：`ui/model.go:1198-1211` 的 Enter 分支。选中非末项 → 调用 `submitConfirm(n)`。
- 候选来源：该分支在调用 `submitConfirm` **之前**已把 `m.activeOptions` 置空（`ui/model.go:1204`），因此必须在清空前取出候选并传入。
- 该分支要求 `len(m.activeOptions) > 0`（`ui/model.go:1188`）；`n == total` 时走“自由输入”提前返回，故进入 `submitConfirm` 时必有 `2 <= total` 且 `1 <= n <= len(opts)-1`。

## 展示格式（方案 A：专用角色 + 紧凑两行）

```
✔ 已选择 [2] 改用 MySQL
候选：[1] 用 Redis 缓存  [2] 改用 MySQL  [3] 输入你的意见
```

- 第 1 行：勾选标记 + 所选项的 1-based 序号与文本。
- 第 2 行：全部候选，`[i] 文本`，以两个空格分隔。
- 新增专用角色 `"choice"`，与 `"user"` / `"system"` / `"narration"` / `"toolsummary"` 并列。

## 改动点

1. `ui/model.go` Enter 分支：清空前 `opts := m.activeOptions`，改为 `m.submitConfirm(n, opts)`。
2. `ui/model.go` `submitConfirm(n int, opts []string)`：在设置 `m.runStartMsgIdx` **之前** append 选择记录消息，使记录不落在本次 Run 的消息区间内（不参与 `removeNarrationDuplicate`）。
3. `ui/model.go` `renderMessage`：新增 `case "choice"`，首行用新样式，候选行复用 `DimStyle`，经 `wrapLines` 按宽度换行。
4. `ui/styles.go`：新增 `ChoiceMsgStyle`（暗色/亮色各一处）。

## 记录构造

- `DisplayMessage{Role: "choice", Content: <两行文本>}`。
- 越界防护：`n < 1 || n > len(opts)` 时只输出 `✔ 已选择 [n]`，不索引候选、不 panic。

## 样式

- `ChoiceMsgStyle`：确认色（暗色 `108` / 亮色 `28`）+ Bold，仅作用于首行。
- 候选行复用现有 `DimStyle`。

## 测试

- 扩展 `ui/confirm_test.go`：选中第 1 项后断言消息流新增一条 `Role:"choice"` 记录，内容含 `✔ 已选择 [1]` 与全部候选文本。
- 末项（自由输入）路径不产生该记录。
- `renderMessage` 对 `Role:"choice"` 的渲染不 panic，且输出行数正确。

## 验收

- `go build ./...` 通过。
- `go test ./ui/...` 通过。
