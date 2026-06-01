package skill

// RegisterBuiltinSkills registers the built-in methodology skills.
func RegisterBuiltinSkills(r *Registry) {
	r.Register(DebuggingSkill())
	r.Register(BrainstormingSkill())
	r.Register(VerificationSkill())
}

// DebuggingSkill returns the systematic debugging methodology skill.
func DebuggingSkill() *Skill {
	return &Skill{
		Name:        "debugging",
		Description: "Systematic debugging methodology with root cause investigation before fixes",
		Keywords:    []string{"bug", "修复", "问题", "错误", "崩溃", "失败", "异常", "不对", "坏了", "不工作", "出错", "debug", "失败", "不通过", "报错", "出问题"},
		Content: `## 系统化调试流程

在尝试任何修复之前，必须先完成根因调查。症状修复等于失败。

### 阶段 1：根因调查

1. **仔细阅读错误信息** — 栈跟踪、行号、错误码
2. **稳定复现** — 能否稳定触发？精确步骤是什么？
3. **检查最近改动** — git diff、最近提交、新依赖
4. **追踪数据流** — 从错误点反向追溯：坏值从哪来？谁传进来的？一直追溯到源头
5. **在多层系统中添加诊断探针** — 在每个组件边界打印进出数据，运行一次收集证据，然后分析

### 阶段 2：模式分析

1. **找类似的工作代码** — 代码库中什么和这个类似且正常工作？
2. **对比差异** — 工作代码和故障代码的每个差异，无论多小
3. **理解依赖** — 需要哪些组件、配置、环境假设

### 阶段 3：假设验证（科学方法）

1. **提出单一假设** — "我认为 X 是根因，因为 Y"
2. **最小改动验证** — 一次只改变一个变量
3. **验证后再继续** — 工作了吗？是→阶段4，否→新假设
4. **不懂就说"我不理解 X"** — 不要假装懂

### 阶段 4：修复实施

1. **先写失败测试** — 最简单的复现
2. **单一修复** — 一次性改动，不做"顺便改进"
3. **验证修复** — 测试通过？其他测试没破坏？
4. **如果修复不工作** — 计数。>=3次 → 停下来质疑架构

### 防循环机制

如果连续 3 次修复失败，停止猜测试试。每次修复都暴露出新位置的耦合/共享状态问题 → 这可能是架构问题。和用户讨论后再继续。

### 红旗 — 立即 STOP

- "先快速修一下，后续再调查"
- "试一下改 X 看看行不行"
- "应该没问题了"
- 同时改多个东西再跑测试
- 列出修复方案却没有做根因调查
- 每次修复都暴露出不同位置的新问题`,
	}
}

// BrainstormingSkill returns the brainstorming methodology skill.
func BrainstormingSkill() *Skill {
	return &Skill{
		Name:        "brainstorming",
		Description: "Explore user intent, requirements and design before implementation",
		Keywords:    []string{"设计", "实现", "开发", "创建", "添加", "增加", "搭建", "重构", "改造", "设计", "方案", "build", "create", "add", "implement", "design"},
		Content: `## 设计方案探索流程

在写任何代码之前，必须完成设计方案探索并获得用户认可。

### 流程

1. **探索项目上下文** — 检查文件、文档、最近提交
2. **逐一提问澄清** — 一次一个问题，了解目的/约束/成功标准
3. **提出 2-3 种方案** — 含权衡分析和你的推荐
4. **展示设计方案** — 按节展示，每节征得用户认可
5. **写设计文档** — 保存到 docs/ 目录并提交

### 关键原则

- **一次一个问题** — 不要用多个问题淹没用户
- **宁可多选** — 比开放式问题更容易回答
- **严格 YAGNI** — 从所有设计中删除不必要的功能
- **探索替代方案** — 在确定前提出 2-3 种方法
- **渐进验证** — 展示设计，获得批准后再继续

### 硬门禁

在展示设计方案并获得用户批准之前，不得调用任何实现技能、编写任何代码、搭建任何项目。

### 红旗

- "这个太简单了不需要设计" → 简单项目是隐藏假设最多的地方
- "我先看看代码再开始" → 先设计方案，再看代码
- "我先快速搭个架子" → 必须先设计`,
	}
}

// VerificationSkill returns the verification-before-completion methodology skill.
func VerificationSkill() *Skill {
	return &Skill{
		Name:        "verification",
		Description: "Evidence before claims: verify before claiming work is complete",
		Keywords:    []string{"完成", "好了", "搞定", "修复了", "通过了", "解决了", "做好", "改好了", "done", "fixed", "completed", "finished", "pass"},
		Content: `## 完成前验证 — 铁律

没有验证证据就声称完成，是欺骗，不是高效。

### 铁律

在声称任何状态或表达满意之前：
1. **识别** — 什么命令能证明这个声明？
2. **运行** — 执行完整的命令（全新、完整运行）
3. **读取** — 完整输出，检查退出码，数失败数
4. **验证** — 输出确认声明了吗？
   - 否 → 陈述实际状态（带证据）
   - 是 → 陈述声明（带证据）
5. **然后才** — 做声明

跳过任何一步 = 说谎，不是验证。

### 常见失败模式

- "应该能工作了" → 运行验证命令
- "我很确信" → 信心 ≠ 证据
- "就这一次" → 没有例外
- "lint 通过了" → lint ≠ 编译器
- "Agent 说成功了" → 独立验证
- "我有点累了" → 累不是借口

### 什么时候必须应用

始终在以下场景前应用：
- 任何成功/完成声明
- 任何表示满意的表达
- 提交、创建 PR、任务完成
- 移动到下一个任务
- 委托子代理`,
	}
}
