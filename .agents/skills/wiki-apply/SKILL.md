---
name: wiki-apply
description: 执行已授权且 implementation-mode 合法的 SpecWiki Lite 任务计划，按选定模式更新证据并处理 scope drift 与失败回退。
---

# Wiki Apply

只实现已经确认的 tasks。可以直接编辑源码、测试、package assets 和最终 `.wiki` 页面，但不建立索引、知识图谱或隐藏中间层。

## 前置条件

- change 是 standalone/child `single-change`，stage 为 `tasks` 或 `implementation`。
- `tasks.md` 完整且 `implementation-mode` 为 `tdd` 或 `direct`。
- 用户已授权实施；strict validate 无 planning blocker。

## 输入

- proposal、design、system tests、unit tests、tasks、metadata。
- 受影响源码、Wiki、tests、assets 和仓库约束。
- tasks 中声明的 focused/aggregate commands。

## 授权与范围检查

- 只执行已授权 task，不把“完成”解释为新的外部发布、破坏性迁移或未声明产品扩展。
- 实现需要改变 goals/non-goals/success criteria 时回到 `wiki-propose`；改变关键设计时回 `wiki-design`；只需改 cases/tasks 时回 `wiki-plan`。
- path safety、数据丢失或外部状态风险不在授权内时暂停。

## 实现模式

1. 设置 `stage: implementation`，保留未知 metadata 字段。
2. `tdd`：先运行相关失败测试，确认它因目标行为缺失而失败；基础设施故障不算 Red；再执行 Green，最后 Refactor。
3. `direct`：直接实现最小完整行为，执行 Verify，再做 Refactor；不要求预先 Red 证据。
4. 每项证据通过后立即勾选 tasks 并记录命令/结果；失败 task 保持未完成。
5. 两种模式都必须完成 declared focused、aggregate、lint、typecheck、build、pack 与 diff gates。

## 失败回退

- 文件同步或迁移失败时验证原子 rollback 和用户文件保护。
- 测试失败先定位原因，修复后重跑；无法运行且无可信替代证据时不得声明完成。
- scope drift 需要更新前序 artifact 并重新 strict validate，不能只改实现掩盖漂移。

## 输出

- 完成的代码、资产、Wiki 和自动化测试。
- 实时更新的 `tasks.md` 与证据。
- stage 保持 implementation，准备 final review。

## 暂停条件

- 新需求超出 proposal/design/tasks。
- 未覆盖的安全、路径、数据损失或外部状态风险。
- required tests 无法运行且没有可信替代证据。

## 下一阶段

所有 task 与局部/聚合检查完成后使用 `wiki-review`；本阶段不签发正式报告。
## CodeGraph 阶段动作

编辑目标符号前执行 `codegraph_impact`，确认没有超出 design 记录的影响半径；编辑后用 `codegraph_affected` 选择测试并运行项目测试命令。若影响跨越设计边界，暂停实现并返回 `wiki-design`。CodeGraph 只辅助定位，不能替代安全检查或测试结论。
