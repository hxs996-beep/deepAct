# 设计：建立"先定位后精读"协议（纯 prompt 改动）

- 日期：2026-06-28
- 状态：已批准，待实现
- 类型：行为引导 / 零代码
- 作者：brainstorming 会话产出

## 1. 背景与问题

观察到一个具体案例：用户问"API 调用失败时能否把失败和重试信息展示给用户"，agent 在探索阶段对
`llm/deepseek.go` 连续 read 全文 **8 次**，期间夹着 LSP 调用。关键细节：

- LSP 已经精确给出行号（`findReferences llm/deepseek.go:202:2`、`documentSymbol`），
  但 agent 拿到行号后仍读**全文**，而不是 `read offset=195 limit=30` 只读那段。
- 全程几乎没有 grep/glob。跨文件流程探索（错误从哪产生→重试在哪→经谁→UI 怎么显示）
  本应是 grep 主场，agent 却靠逐个读全文拼凑。
- UI 只显示 `[<>] <path>`，不显示读的 scope，连用户都无法判断是"重复读同一段"
  还是"读不同区段"。

**根因**：现有 prompt 是**符号导向**——"符号查询优先 LSP"只覆盖"按名字找单个定义"，
没有建立"**定位后再精确读**"的协议，也没有把 grep 定位成**跨文件探索主力**。模型的默认反射
是"理解某文件 = 读全文"，即便已有精确行号也照读全文。

## 2. 设计原则

- **纯 prompt，零代码**：只改 `system.md`（中英两份）与三个工具的 `Description`。
  不改 read 工具行为、不加引擎 nudge、不动循环守卫、不加 UI scope 显示。
- **YAGNI**：先以零成本方式验证软引导对 DeepSeek 是否有效；若无效再升级到结构强制。
- **不削弱现有规则**：现有的"符号查询优先 LSP""禁止一轮一个只读工具"等规则保留，
  只在其上补一条定位协议。
- **中英对等**：zh / en 两份 promptset 同步修改，措辞对等。

## 3. 改动清单

### 3.1 system.md「工具使用策略」新增定位协议

**位置**：zh 在第 65 行（"搜索代码：先用 LSP workspaceSymbol…"）之后；
en 在第 65 行（"SEARCH CODE: Use LSP workspaceSymbol FIRST…"）之后。

**zh 新增（2 条）**：
```
- 先定位后精读：要理解某文件里的特定代码（一个函数、一段流程、一个错误处理）时，先用 grep（按模式）或 lsp（按符号）定位到精确行号，再用 read 的 offset/limit 或 symbol 只读那一段。不要为了找一处代码而读整个文件——尤其是已经从 lsp 拿到行号之后。
- grep 是跨文件探索的主力：找某错误/字符串/模式的所有出现位置、找某函数的所有调用点、梳理一段调用流程时，先用 grep，而不是逐个读文件。lsp 适合按符号名精确定位单个定义。
```

**en 新增（2 条）**：
```
- LOCATE THEN READ: To understand specific code within a file (a function, a flow, an error handler), first locate the exact line numbers with grep (by pattern) or lsp (by symbol), then read only that range with read's offset/limit or symbol. Do NOT read a whole file to find one piece of code — especially after lsp has already given you the line numbers.
- grep is the primary tool for cross-file exploration: finding all occurrences of a pattern/error string, all call sites of a function, or tracing a flow — grep first rather than reading files one by one. lsp is for precise single-definition lookup by symbol name.
```

### 3.2 read 工具 Description 末尾追加一句

**文件**：`tools/builtin/read.go` 第 41 行 `Description` 字符串末尾。

**追加**：
```
If you're looking for specific code within a file (a pattern, an error string, a flow), grep for it first to get exact line numbers, then read only that range with offset/limit instead of the whole file.
```

### 3.3 grep 工具 Description 补一句主力用途

**文件**：`tools/builtin/grep.go` 第 32 行 `Description`。

**保留**现有"prefer lsp for symbols"措辞，**追加**：
```
Primary tool for cross-file exploration: finding all occurrences of a pattern/error string, all call sites of a function, or tracing a flow — grep first rather than reading files one by one.
```

### 3.4 glob 工具 Description

不动。glob 的现有描述已足够（按文件名模式找文件）。

## 4. 不做的事（明确排除）

- 不改 read 工具运行时行为（不返回大纲、不加 mtime stub、不加读次数硬上限）。
- 不加引擎期中 nudge 注入（"检测到探索期连续读多文件未编辑则提示"——留作下一阶段）。
- 不改 UI 显示 read 的 scope（用户已选不做）。
- 不动已修好的循环守卫（路径归一化 / ErrorLoopState / ReadLoopState 保持作为兜底）。
- 不清理 `fileUnchangedStub` / `mtimeCache` 死代码（与本次无关，留待 stub 决策时一并处理）。

## 5. 验证方式

改完后用**同类任务**实测观察（不写自动化测试，因为是纯 prompt 行为）：

1. 跑"API 错误/重试如何展示给用户"这类跨文件流程追踪任务。
2. 看 UI：read 前是否出现 grep/lsp 定位？read 是否带 offset/limit（而非全文）？
   跨文件流程是否用 grep 而非逐个读？
3. 对照改动前的 deepseek.go 案例（8 次全文读）作为基线。

**判定标准**：同类任务里全文读次数明显下降、grep/lsp 出现在 read 之前 → 软引导有效。
若几轮任务后模型仍直接读全文，说明软引导对 DeepSeek 无效，升级到结构强制方案。

## 6. 风险

- **软引导对 DeepSeek 可能无效**：现有"符号查询优先 LSP"引导模型并未充分遵守，
  新增的定位协议可能同样被无视。这是预期内的风险——本次正是用最低成本验证这一点。
  验证方式见第 5 节。
- **措辞过长稀释注意力**：system.md 已较长，新增 2 条可能被忽略。措辞已尽量
  用大写前缀（LOCATE THEN READ）和对比句式（"不要…而是…"）提升显著性。

## 7. 涉及文件

| 文件 | 改动 |
|------|------|
| `context/promptset/zh/system.md` | 工具使用策略新增 2 条 |
| `context/promptset/en/system.md` | 工具使用策略新增 2 条 |
| `tools/builtin/read.go` | Description 末尾追加 1 句 |
| `tools/builtin/grep.go` | Description 追加 1 句主力用途 |
