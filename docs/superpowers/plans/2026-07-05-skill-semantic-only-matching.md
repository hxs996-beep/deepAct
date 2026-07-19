# Skill 匹配改为纯语义匹配 — 实施计划

**日期**: 2026-07-05
**目标**: 移除关键字匹配,直接用 `deepseek-v4-flash` 做语义匹配(用户输入 + 全部 skill 描述 → LLM 选一个最相关的)

---

## 背景

当前 `KeywordMatcher`(子串匹配)误触发严重:`finishing-a-development-branch` 的关键词 `"PR"` 小写为 `"pr"`,子串命中用户消息里的 `"prefix"`,导致 "分析 prefix cache 原则" 被误激活。根因是 `matcher.go` 把 `nil` 阈值默认成 1,与 `skill.go` 文档、设计文档、`run.go` 注释三方矛盾。

`SemanticMatcher`(`skill/matcher_llm.go`)已实现且签名满足 `SkillMatcher` 接口,用 `MatchFunc` 回调避开了 `skill→engine` 反向依赖,但 `cmd/run.go` 当前传 `nil` 未接线。

用户决定:关键字匹配整条删掉,直接走 flash 语义匹配。

---

## 改动文件

### 1. `skill/matcher.go` — 精简为只剩接口
- 删除 `KeywordMatcher` struct + `NewKeywordMatcher` + `Match`
- 删除 `FallbackMatcher` struct + `NewFallbackMatcher` + `Match`
- **保留** `SkillMatcher` 接口(更新注释:语义匹配专用)

### 2. `skill/matcher_llm.go` — 基本不动
- `SemanticMatcher` 已满足 `SkillMatcher` 接口,逻辑完整(system prompt + skill 列表 + JSON 解析 + 未知 name 兜底)
- timeout 保持 2s(flash 小请求通常 <2s;超时则返回 nil 不阻塞主流程)

### 3. `cmd/run.go`(L289-298)— 接线 `SemanticMatcher`
- 删除 `kwMatcher` + `NewFallbackMatcher(kwMatcher, nil)`
- 用 `MatchFunc` 闭包包装 `client.Complete`(`client` 已在 L198 创建,flash 调用模式参考 `engine/compressor.go:140-154`):
  ```go
  matchFn := func(ctx context.Context, sys, usr string) (string, error) {
      req := engine.ModelRequest{
          Model: config.FlashModelName,
          Messages: []engine.ModelMessage{
              {Role: "system", Content: sys},
              {Role: "user", Content: usr},
          },
          Temperature: 0,
          JsonMode:    true,
      }
      resp, err := client.Complete(ctx, req)
      if err != nil {
          return "", fmt.Errorf("skill semantic match: %w", err)
      }
      return resp.Message.Content, nil
  }
  skillMatcher := skill.NewSemanticMatcher(matchFn, config.FlashModelName)
  ```
- `config.FlashModelName` 默认 `"deepseek-v4-flash"`;为空时 `SemanticMatcher.Match` 返回 nil(优雅降级)
- 替换 L289-298 注释,说明改为语义优先

### 4. `engine/loop.go`(L328-346)— 删除关键字建议 fallback
- 删除 `else` 分支(L339-345)的 `MatchTopSkillsWithScores` + `showSkillSuggestions`(这是关键字建议机制,一并移除)
- 语义返回 nil 时直接不激活;模型仍可自行调用 `activate_skill`(skills block 已在 system prompt)
- 保留 `skillJustActivated` 逻辑(命中时置 true)
- 更新 L328-330 注释
- 删除 `showSkillSuggestions` 函数(L1619-1637,已无引用)

### 5. `skill/skill.go` — 删除关键字匹配死代码
- 删除 `MatchTopSkills`、`MatchTopSkillsWithScores`、`MatchByKeywords`(移除关键字路径后均无引用)
- **保留** `Skill.Keywords` / `AutoActivateThreshold` 字段(避免改动 loader + 14 个 TOML,仅作未用元数据)

### 6. 测试
- `skill/matcher_test.go`:
  - 删除 `TestKeywordMatcher_*`(3 个)、`TestFallbackMatcher_*`(3 个)、`intPtr` helper
  - **保留** `TestSemanticMatcher_*`(6 个,已 mock MatchFunc 覆盖正常/null/超时/坏JSON/未知skill/禁用)
- `skill/skill_test.go`:删除 `TestMatchByKeywords_*`(2 个,被测函数已删)

---

## 延迟权衡(已知风险)

- 原 `run.go` 注释说不接线语义匹配,是因为第三方 flash ~30s/调用会阻塞主循环
- 语义匹配仅在 `ActiveSkillName == ""` 时触发(无活动 skill)
- 常见无 skill 场景下每轮付一次 flash 延迟,用 2s 超时兜底(flash 对 ~300 input token 的小请求通常 <2s)
- 若实测延迟不可接受,后续可改异步(后台匹配、下一轮激活),本次不做

---

## 验证

1. `make build` 编译通过
2. `make test` 全绿(重点 `skill/` 包)
3. 手动:用 "分析一下 prefix cache 原则" 验证不再误激活 `finishing-a-development-branch`
