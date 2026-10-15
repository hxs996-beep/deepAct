# 用户中断标记写入对话历史（Design）

日期：2026-10-15
状态：已实施
前置：无（独立改动）

## 背景与目标

用户按 Esc 中断一轮运行后，引擎当前把"正在流式生成但没写完"的内容丢弃（`engine/turn.go:271-299` 因 ctx 取消提前 return，`contentBuilder` 不入 history），而**已经进入 history 的部分全部保留**。问题在于：这条链路上**没有任何"这一轮被用户主动中断"的信号**。后果是下一轮模型只看到"用户消息 + 一串工具结果"，容易把被打断的半截计划当成已确认，或者忽略"被中断的工具可能只执行了一半"。

目标：取消发生时，往对话历史追加一条显式的中断记录，让后续请求的模型知道上一轮是用户主动中断的；该记录随既有落盘路径写入 session jsonl，`/resume` 后仍在。语义对齐 codex 的 `<turn_aborted>` 标记（`codex-rs/core/src/tasks/mod.rs:920-995` 写标记并 flush、`codex-rs/core/src/context/turn_aborted.rs:10-11` 文本）。

## 现状（证据）

1. **取消来源单一**：全仓非测试代码里对 runner 的 `.Cancel()` 调用点只有 `ui/model.go:1082`（Esc，`stateRunning` 分支 `ui/model.go:1079-1103`）→ `ui/runner.go:229-236` 取消本次 Run 的 ctx。因此"`ctx.Err() != nil`"与"用户按了 Esc"等价。
2. **取消时的既有处置**：流式中 → `engine/turn.go:271-299` 返回 `Blocked/stream_error`，半截文本丢弃；工具执行中 → `assistant(tool_calls)` 与每条 tool 结果都已入 history（`engine/turn.go:616`、`:746`、`:764`）；轮间 → `engine/loop.go:594-595` 返回 `Blocked/cancelled`。三种形态都不写任何中断信号。
3. **落盘路径**：`defer e.persistHistory()`（`engine/loop.go:469`）覆盖 `Run` 的所有退出路径，把本 Run 新增的 history 写成 message 事件（`engine/loop.go:885-905`）；`/resume` 经 `RebuildHistory`（`engine/resume.go:24-42`）保留 user/assistant 正文，丢弃 tool 消息与 reasoning。
4. **提示词英文单语约定**：`docs/superpowers/specs/2026-10-13-prompt-english-single-language-design.md`（commit `76f90f2`）确立模型可见文本统一英文，故标记文本用英文（codex 原文亦为英文）。

## 方案

### 1. 触发判据：Run 退出时 ctx 已取消

`Engine.Run` 的 defer 组里新增一条：

```go
defer e.persistHistory()
defer e.cancelBackgroundTasks()
// 注册在 persistHistory 之后：defer 是 LIFO，这样标记先入 history、再落盘
defer e.noteUserInterrupt(ctx)
```

判据写在 `noteUserInterrupt` 内部：`if ctx.Err() == nil { return }`。用 ctx 判定，一条规则覆盖"流式中 / 工具执行中 / 轮间"三种取消形态，不按场景写分支——与 codex 同粒度（codex 也是"只要有正在跑的 turn 被 abort 就写标记"，`core/src/tasks/mod.rs:549-577`）。

**顺序是这条改动唯一容易错的地方**：`defer` 是 LIFO，`noteUserInterrupt` 只有注册在 `persistHistory` **之后**才会先执行；否则标记既不进下一次请求，也不进 session。守卫测试 4 专门锁这条。

### 2. 记录形态

```go
func buildUserInterruptMarker() string {
	return "<turn_aborted>\nThe user interrupted the previous turn on purpose. " +
		"Any running tools may still be running in the background. " +
		"If any tools/commands were aborted, they may have partially executed.\n</turn_aborted>"
}
```

`noteUserInterrupt` 往 `e.history` 追加一条 `Message{Role: "user", Content: buildUserInterruptMarker(), Timestamp: time.Now()}`。

- **角色必须是 user**：system 只在会话开头注入、tool 必须挂 tool_call_id，user 是唯一可直接追加的通道（codex 的 `ContextualUserFragment::role()` 同样返回 `"user"`，`core/src/context/turn_aborted.rs:27-29`）。
- **标签与 codex 同名**（`<turn_aborted>`），便于两边对照排查。
- **放置位置**：`engine/loop.go` 中与 `buildProgressNudge` / `buildProgressBlockMsg`（`loop.go:1305-1315`）同区，沿用"引擎注入的模型可见文本 = 包级函数"的既有约定。
- **不触发内容守卫**：`turn_contentless_assistant_test.go` 锁定的不变量是"assistant 消息必须有 content 或 tool_calls"，本标记是 user 消息，不涉及。

### 3. 不做的事（YAGNI）

- 不加配置开关（codex 的 `agents.interrupt_message` 默认 true；deepact 无此需求）。
- 不改 UI 显示：`已中断`（`ui/model.go:1093`）已有，display 与 history 是两套，本设计只碰模型上下文。
- 不引入 `pendingPinnedMessages` 通道（一次性注入，且不落盘；已选定持久记录方案）。
- 不改被取消工具自身的文案（已带取消/失败信息）。
- 不做"剔除上下文"的回滚分支。

## 边界与取舍

- Run 极早期被取消（尚未发出任何请求）也会写一条标记：与 codex 同粒度，代价是多一条无害提示。若要收窄为"仅当本轮确有产出"，就引入场景判断，违反泛化优先，不做。
- 连续两次 Esc 不会产生两条标记：第一次 Esc 后 UI 进入 `stateReady`，第二次 Esc 走清输入分支（`ui/model.go:1094-1101`），不会再启动 Run。跨 Run 的两次独立中断各写一条，语义正确。
- steer 中断（`engine/turn.go:203-241`）不取消 Run，不产生标记。
- 标记作为普通 user 消息参与后续压缩，不做特殊保护。

## 守卫测试（TDD 顺序，先红后绿）

新增 `engine/interrupt_marker_test.go`：

1. `TestRun_InterruptAppendsTurnAbortedMarker`：ctx 预先取消 → `Run` 返回 `BlockedBy == "cancelled"`；`e.history` 末尾是一条 `Role == "user"` 且含 `<turn_aborted>` 的消息，且整条 history 里该标记只出现一次。
2. `TestRun_InterruptMidStreamAppendsMarker`：ctx 感知的阻塞 model（`Stream` 内 `<-ctx.Done()` 后送出 `ModelChunk{Err: ctx.Err()}`），Run 进行中取消 → 同样留下标记。锁"流式中取消"这一真实 Esc 形态；`engine/test_stubs_test.go` 的 `stubStreamModel` 不感知 ctx，故在本文件内另建 stub。
3. `TestRun_CompletedHasNoInterruptMarker`：正常跑到 Done 的 Run，history 中不得出现 `<turn_aborted>`（防止污染每一次 Run；这条比第 1 条更要紧）。
4. `TestRun_InterruptMarkerPersisted`：Engine 挂 `mockSessionStore`（`engine/loop_persist_test.go`）＋已取消的 ctx → 断言存在 payload 含 `<turn_aborted>` 的 message 事件（锁 defer 顺序）。
5. `TestRebuildHistory_KeepsInterruptMarker`（可选）：把标记事件喂给 `RebuildHistory`，断言仍在。

## 影响面

- `engine/loop.go`：`Run` 增 1 行 defer；新增 `noteUserInterrupt`（约 10 行）与 `buildUserInterruptMarker`（约 5 行）。
- `engine/interrupt_marker_test.go`：新增。
- 不改对外接口、不改 UI、不改配置、不改既有测试。
