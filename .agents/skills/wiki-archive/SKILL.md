---
name: wiki-archive
description: 在 strict/full-pass 门禁通过后用 Lite CLI 单次归档 change，并核验 parent/child 与长期 Wiki 沉淀状态。
---

# Wiki Archive

让 `spec-wiki-lite archive` 独占目录移动和 parent/child 同步。不得手工移动 change、编辑 archive marker 或伪造归档时间。

## 前置条件

- ordinary/child change 位于 `verification` 或 `archive`，tasks 完成。
- `review-report.md` 为 `review-result: pass` / `scope: full`。
- `test-report.md` 为 `verification-result: pass` / `scope: full`。
- parent 只有在所有 child 已物理归档且 metadata/split marker 一致时才可归档。

## 工作流

1. 执行 `spec-wiki-lite status --json`。
2. 执行 `spec-wiki-lite validate <change-id> --strict --json`；任何 blocker 都停止。
3. 检查 `.spec/archive/YYYY-MM-DD-<change-id>` 不存在。
4. 确认长期 Wiki 沉淀状态：
   - `wiki-updates-made`：本 change 已更新 current authority 页面；
   - `wiki-updates-required`：仍缺必要沉淀，返回 apply/review，不归档；
   - `wiki-updates-not-needed`：给出为何无长期知识变化的证据。
5. 只执行一次 `spec-wiki-lite archive <change-id>`。
6. 核验 active 目录消失、dated archive 存在且证据未改写。
7. child 归档时核验 active parent 收到 `archiveStatus`、`archivedAt`、`archivedTo` 与 split marker。
8. 再次运行 status，记录 archive path 和下一个 dependency-ready child。

## 输出

- dated archive path。
- post-archive status、parent/child 同步结果和真实 Wiki update 状态。
- ordinary/child 不再位于 active changes。

## 暂停条件

- strict validate 失败、报告 partial/non-pass、tasks 未完成。
- archive target 冲突或 parent/child metadata 不一致。
- `wiki-updates-required` 尚未解决。

## 约束

- archive CLI 只调用一次；不在失败后盲目重试。
- 不批量改写历史 `.spec/archive/**`。
- 归档不是实现或 Wiki 沉淀的替代品。
## CodeGraph 阶段动作

归档不自动执行 CodeGraph。只有需要核对长期沉淀或影响范围时按需调用，并把摘要写入既有 review/verification 证据；归档门禁仍完全由 Lite CLI 和 strict validate 决定。
