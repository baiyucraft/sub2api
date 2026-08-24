---
name: wiki-explore
description: 探索不清晰的文档或工作流需求，收集影响范围的证据，并决定 single-change 或 parent/child 交付形态。
---

# Wiki Explore

只负责 `exploration`：澄清问题、读取必要仓库事实、形成 research，并决定 delivery shape。不要提前写 proposal、design、tests 或实现。

## 前置条件

- 需求仍缺少可验证边界，或包含多个可能独立验收的结果。
- 目标 change 不存在，或仍处于 `stage: exploration`。

## 输入

- 用户目标、约束和已确认决策。
- `.wiki/INDEX.md`、相关 Wiki 页面、代码、配置、测试和历史 archive。
- `spec-wiki-lite status --json` 与已有 `meta.yaml` / `split.md` / `research/**`。

## 工作流

1. 只询问真正阻塞 scope、ownership 或安全边界的必要问题；可安全推断的内容记录为风险或未知项。
2. 识别 problem、affected readers、SSOT owner、non-goals 和 observable acceptance boundary。
3. 按需读取仓库事实，不做无差别扫描，不建立代码索引、知识图谱或隐藏数据库。
4. 当证据会改变 scope、delivery shape、依赖或风险时，读取并使用 `references/research-template.md` 写入 `research/<topic>.md`。
5. 一个结果能独立 review/archive 时选择 `single-change`；只有多个结果具有独立验收边界或依赖时选择 `multi-change`。

## Single-change Stub

仅当 explore research 需要稳定落点时创建：

```yaml
id: <change-id>
stage: exploration
deliveryShape: single-change
artifacts:
  proposal:
    status: missing
  metadata:
    status: present
```

不得创建 `proposal.md`。后续必须交给 `wiki-propose`。

## Parent 与 Child Stub

parent 使用 `deliveryShape: multi-change`、`multiChange.role: parent`、children/order/dependsOn，并创建 `split.md`。每个 child 只创建 `stage: exploration`、`deliveryShape: single-change`、`multiChange.role: child` 的 `meta.yaml`，proposal 状态为 missing。

child id 默认使用 `<parent-id>-<topic>`；split、parent metadata 和 child metadata 必须完全一致。parent 永不进入 implementation。

## 输出

- 可选 `research/<topic>.md`。
- standalone exploration stub，或 parent `split.md` + parent metadata + child stubs。
- 推荐的最早 dependency-ready child 和下一步 `wiki-propose`。

## 暂停条件

- 存在无法安全推断的产品选择、SSOT 冲突或破坏性范围。
- child 无法给出独立验收边界。
- 现有 parent/child 的 id、order、dependsOn 相互矛盾。

## 下一阶段

single-change 或最早可执行 child 使用 `wiki-propose`；parent 保持 coordination-only。
## CodeGraph 阶段动作（必须）

1. 调用 `codegraph_status`，记录索引状态和时间。
2. 用 `codegraph_context` 建立项目区域与功能入口上下文。
3. 用 `codegraph_explore` 获取关键符号源码；已知起止点时用 `codegraph_trace` 获取完整调用链。
4. 必要时用 `codegraph_callers` / `codegraph_callees` 核对 ownership。
5. 将结构化摘要写入 `.spec/changes/<change-id>/research/codegraph.md`：目标、查询、入口/符号/调用关系、影响、测试线索、事实/推断、未知项和降级原因。

MCP 不可用时使用等价 CLI；两者都不可用时才定向读取源码和测试，并明确记录 fallback。不要粘贴大段原始输出。
