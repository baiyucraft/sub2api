---
name: wiki-propose
description: 为一个可独立验收的 SpecWiki Lite change 创建 proposal.md 和一致的 metadata，不提前进入设计或实现。
---

# Wiki Propose

只负责具体 standalone/child change 的 `proposal` 与 `delivery` 阶段。范围仍需拆分时返回 `wiki-explore`。

## 前置条件

- change id 是 canonical kebab-case，且 `.spec/changes` / `.spec/archive` 无冲突。
- delivery shape 已确定为 `single-change`。
- child 的 parent、order、dependsOn 与 parent metadata / split 一致。

## 输入

- 用户已确认的问题、目标、非目标和约束。
- 已有 exploration stub、parent split、`research/**`、相关 Wiki 和仓库证据。
- `spec-wiki-lite show <change-id> --json` 与 strict validate 结果。

## 工作流

1. 复用 explore research；只有 proposal 信息不足时才做定向补充，并使用 `references/research-template.md`。
2. 读取 `references/proposal-template.md`，写清 problem、goals、non-goals、success criteria、impact scope、delivery shape、risks 和 references。
3. 成功标准必须可观察、可测试，并能在 review/verification 阶段产生证据。
4. 外部或 upstream 资料记录 source、target landing area 和 adoption mode。
5. 保留未知 metadata 字段；写完 proposal 后设置 `stage: proposal` 和 artifact present。
6. 执行 `spec-wiki-lite validate <change-id> --strict --json`。

## Parent/Child 一致性

- parent 不是可执行 proposal；只对 child 或 standalone change 操作。
- child proposal 不能扩大 parent split 的目标或改变依赖。
- 新证据需要改 delivery shape、children 或 order 时返回 `wiki-explore`，不在 proposal 内偷改。

## 输出

- `.spec/changes/<change-id>/proposal.md`
- 更新后的 `meta.yaml`，proposal present，stage proposal
- strict validate 成功证据

## 暂停条件

- change 实际是 parent。
- success criteria 依赖未解决产品决策。
- scope 已无法保持 single-change 或 parent/child 关系不一致。

## 下一阶段

proposal 被接受后使用 `wiki-design`。本阶段不改最终 Wiki 页面或实现。
## CodeGraph 阶段动作

复用 `research/codegraph.md`。只有 proposal 引入新入口、跨模块边界或安全边界时，补充一次 `codegraph_context` 或 `codegraph_impact`，并在 proposal 中引用具体入口、符号、文件/行号和验证边界。不要重复全量扫描，也不保存原始输出。
