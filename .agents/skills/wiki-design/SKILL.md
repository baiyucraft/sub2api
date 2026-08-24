---
name: wiki-design
description: 为已接受的 proposal 设计 Wiki、Skill、CLI 或工作流改动的接口、ownership、失败回滚与验证边界。
---

# Wiki Design

设计必须让后续测试可以验证，但不要提前生成 system tests、tasks 或实现。`.wiki` 是长期知识面，不引入索引、知识图谱、缓存或投影层。

## 前置条件

- `proposal.md` 已接受且 strict validate 无 proposal blocker。
- change 是 standalone/child `single-change`，不是 parent。
- 当前 stage 为 `proposal`、`delivery` 或 `design`。

## 输入

- proposal、metadata、已有 research。
- 相关 Wiki、源码、测试、配置、CLI/Skill 合同和 SSOT owner。
- 必要时使用 `references/research-template.md` 记录只影响设计的证据。

## 工作流

1. 读取 `references/design-template.md`。
2. 定义目标页面/资产、导航、frontmatter、ownership、接口、数据/文件流和稳定机器字段。
3. 明确 path safety、无效输入、并发/重复执行、部分失败、原子性、rollback 和用户内容保护。
4. 设计验证入口、可观察输出、测试 seam 和成功标准映射，但不写具体 ST/UT case。
5. 记录外部来源的 source、target landing area、adoption mode；区分直接迁移、改写或仅借鉴。
6. 保留未知 metadata 字段，写 `design.md`，设置 `stage: design` 并 strict validate。

## 边界

- 不改变 proposal 的 goals/non-goals/success criteria；需要改变时返回 `wiki-propose`。
- 不编辑最终 Wiki 页面或产品代码。
- 不把偶然代码结构固化为公共合同，除非它决定安全、兼容或测试边界。

## 输出

- `.spec/changes/<change-id>/design.md`
- stage design、design present 的 metadata
- strict validate 成功证据

## 暂停条件

- SSOT ownership 冲突未解决。
- proposal 缺少影响接口、安全、回滚或验证的产品决定。
- 设计需要改变 delivery shape 或 parent/child 边界。

## 下一阶段

使用 `wiki-plan` 生成 normal/failure/boundary cases 与 TDD tasks。
## CodeGraph 阶段动作（必须）

1. 读取同一 change 的 `research/codegraph.md`。
2. 对拟修改符号调用 `codegraph_impact`，用 `codegraph_callers` / `codegraph_callees` 确认 ownership 与接口边界。
3. 跨层流程调用 `codegraph_trace`；目标文件已知时调用 `codegraph_affected` 找测试范围。
4. 将入口/路径、依赖边界、影响半径、测试、回滚、图证据与源码核验差异写入 `design.md` 的 CodeGraph-derived design constraints 章节。

MCP 不可用时使用 CLI 或定向源码核验，并记录 fallback 和残余风险；不要粘贴大段原始输出。
