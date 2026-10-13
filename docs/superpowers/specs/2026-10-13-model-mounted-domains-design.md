# 领域层由模型自主挂载（Design）

> **状态：已回退（2026-10-13）。** 该方案建立在“引擎应零判定任务领域、领域层由模型自主挂载”的前提上，而外部参照表明：主流做法是**默认 coding agent 预设 + 靠模型自己识别 / 用户外部 prompt**，而非引擎发布领域目录让模型挂载。此文档仅作历史记录，`load_domain` 通道与 `# Available Domains` 目录已移除。

日期：2026-10-13
状态：已回退

## 背景

上一轮把提示词拆成"核心层 + 可挂载编码层"，但**挂载判据放在了引擎**：`CodingLayer(projectRoot)` 用 `DetectLanguages(projectRoot)` 自动挂载。它回答的是"工作区是不是代码仓"，**不是"这个任务是不是编码任务"**。

后果（用户提出的三个例子）：

| 任务 | 在代码仓里 | 在非代码目录 |
|---|---|---|
| 编码 | 挂编码层 ✅ | 不挂 ⚠️ |
| 操作/执行 | 挂编码层 ⚠️（无害但无关） | 不挂 ✅ |
| **写毕业论文** | **挂编码层 ❌**（编码规则 + LSP 优先 + 编码范例 + 语言包，且无写作能力） | 不挂 ⚠️（也无写作能力） |

引擎里没有任务级判定，而且是**刻意**的：`2026-09-08` 移除 `IntentClassifier`、`2026-09-11` 移除全部关键词启发式判定，改为写进提示词的纪律（符合 AGENTS.md「能力优先/泛化性优先」）。

## 方案：引擎零判定，领域层由模型经 `load_domain` 自主挂载

- **引擎不再判定任务领域**：删除按工作区自动挂载的逻辑。引擎只做两件事——把可用领域目录发布给模型，并在模型要求时返回该层内容。
- **核心层不变**：领域无关的行为规则始终挂载。
- **通道**：新增引擎侧工具 `load_domain`（与 `load_skill` 同款通道）。模型判断当前任务属于某领域时调用它，层内容作为**工具结果**进入 history——写一次、整个会话持续有效（不同于 per-turn pinned 消息）。
- **目录注入**：`# Available Domains` 块注入系统消息（与 `# Available Skills` 同位置、同为稳定前缀），写明机制与各领域的触发条件。
- **工作区检测降级为"内容参数"**：`DetectLanguages` 仍用于决定**编码层里带哪些语言包**（描述工作区事实），但不再决定挂不挂载。

### 组件

| 位置 | 职责 |
|---|---|
| `context/domains.go` | `Domain{Name, WhenToUse, Content}`、`Domains`（`Names`/`WhenToUse`/`Content`/`Block()`）、`AvailableDomains(projectRoot)`。**拥有领域目录与内容组装**（编码规则 + 检测到的语言包 + 范例）。 |
| `engine/domains.go` | `DomainSource` 接口、`LoadDomainToolName`、`loadDomainToolSpec(names)`（enum 由 source 生成）、`processLoadDomainCalls`（返回层内容 / 报错并列出可用领域）。 |
| `engine/intercept.go` | 注册 `load_domain` 为拦截工具（表尾，保持 tool 消息顺序契约）。 |
| `engine/turn.go` | `toolSpecsWithHandoff` 在 `Domains != nil` 时暴露该工具。 |
| `context/builder.go` | `SetDomainsBlock` + 注入系统消息 + 计入 `InjectedBlocks`。 |
| `cmd/run.go` | `domains := context.AvailableDomains(workDir)`；`config.Domains = domains`；`assembler.SetDomainsBlock(domains.Block())`。 |

依赖方向：`context` 实现 `engine.DomainSource`，通过 `EngineConfig.Domains` 注入，`engine` 不 import `context`（沿用 `partitionURL` 同款倒置）。

## 取舍

- **子代理不挂领域层**：子代理没有 `load_skill`/`load_domain` 通道（工具集受限）。父代理通过 `goal/context/constraints` 传达所需规则；`core/sub_agent.md` 自带的搜索方法论（含 LSP 提示）仍在。若将来需要"父代理挂载的领域自动下发给子代理"，再加一条传播通道。
- **模型可能忘记挂载**：编码任务若模型不调 `load_domain`，就只有核心层纪律。目录块里已写明"若任务属于下列领域，动手前先挂载"；这与"技能优先"同属"靠提示词纪律驱动模型行为"的既有做法。
- **未新增其他领域**（写作/数据等），结构已支持按同法添加（`AvailableDomains` 里加一项）。

## 验证

```
go build ./... / go vet ./... / go test -count=1 ./...   → 全绿
```

新增测试：
- `context/domains_test.go`：编码层可挂载且含规则+语言包；未知领域返回空；目录块内容与空目录渲染。
- `engine/domains_test.go`：工具 schema 的 enum 来自 source；`load_domain` 已注册为拦截工具；挂载内容作为工具结果注入；未知/空名称各自报错。
- `context/builder_test.go`：`SetDomainsBlock` 的目录块落在系统消息里。
