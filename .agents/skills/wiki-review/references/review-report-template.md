review-result: pending
scope: partial

# <change-id> Review Report

## Review 范围

- artifacts：<proposal/design/tests/tasks/meta>
- implementation diff：<commit/range/path>
- selected standards：general / frontend / go / java / python
- exclusions：<明确排除及原因>

## Findings

| 优先级 | 位置 | 问题 | 影响 | 修复/回退阶段 |
| --- | --- | --- | --- | --- |
| P0/P1/P2 | `<path:line>` | <可复现问题> | <风险> | apply/design/plan |

## Artifact 一致性

| Artifact / success criterion | 实现与证据 | 结果 |
| --- | --- | --- |
| <item> | <path/command> | pass / fail / skipped |

## 安全、Ownership 与回滚

- path/input safety：<结果>
- 用户内容保护：<结果>
- 失败原子性/rollback：<结果>

## 残余风险

- <风险；无则写“无”>

## 结论

- scope 为 full 且无 blocking finding 时，首行改为 `review-result: pass`、`scope: full`。
- 否则保持 fail/pending/partial，并写明下一步。
