---
name: wiki-continue
description: 根据当前状态调度 SpecWiki Lite change 的下一阶段；处理 bootstrap、单 change、parent/child、依赖和阻塞问题。
---

# Wiki Continue

这是跨阶段调度入口，不替代任何专业阶段 Skill。仅在 Codex 仓库中工作，并把 Lite CLI 与 `.spec/changes/**` 视为权威。

## 前置条件

- 仓库已执行 `spec-wiki-lite init --host codex`。
- 用户指定 change id、只有一个可安全选择的 active change，或没有 active change 但 Wiki bootstrap 尚未完成。

## 权威输入

依次执行并读取：

```text
spec-wiki-lite status --json
spec-wiki-lite show <change-id> --json
spec-wiki-lite validate <change-id> --strict --json
```

文件存在性只能辅助核验，不能绕过 metadata consistency、blocking issues、parent/child 关系或 full/pass 报告门禁。

## 选择与依赖

- 用户指定 id 时使用该 change；不得处理 `.spec/archive/**` 中的历史 change。
- 多个 active changes 无法唯一选择时暂停，让用户选择。
- parent 不进入 design/apply/review；选择最早 `order` 且 `dependsOn` 已归档的 child。
- child metadata 必须与 parent 的 children、order、dependsOn 一致。
- stage 与 required artifact 矛盾时暂停，不自行回退或修补 stage。

## Bootstrap 路由

- 没有 active change 且 `wiki.bootstrapPending: true`：路由 `wiki-explore`，探索并创建完成正式 Wiki 首页的 change。
- 没有 active change且 bootstrap 已完成：报告无可继续 change。
- active change 始终优先按 stage 路由，bootstrap 不覆盖其 stage。

## Stage 路由

`implementation-mode` 只接受 `tdd` 或 `direct`：合法值直接进入 `wiki-apply`，缺失或非法值回到 `wiki-plan`。

| 当前状态 | 目标 Skill |
| --- | --- |
| `exploration`，scope/split/research 未完成 | `wiki-explore` |
| `exploration` standalone/child 已可提案 | `wiki-propose` |
| `proposal` / `delivery`，proposal 未完成 | `wiki-propose` |
| `proposal` / `delivery`，proposal 完成 | `wiki-design` |
| `design`，设计未完成 | `wiki-design` |
| `design` / `cases` | `wiki-plan` |
| `tasks`，缺失或非法 `implementation-mode` | `wiki-plan` |
| `tasks`，mode 合法且当前请求已授权实现 | `wiki-apply` |
| `implementation`，任务或局部验证未完成 | `wiki-apply` |
| `implementation` / `review` | `wiki-review` |
| `verification`，缺 full/pass 证据 | `wiki-review` |
| `verification`，strict validate 通过 | `wiki-archive` |
| `archive` | `wiki-archive` |

## 输出

报告 selected change、current stage、status summary、blocking issues、target Skill、reason 和 exact next action，然后按目标 Skill 继续同一个 change。

## 暂停条件

- change 无法唯一选择或 id 不安全。
- parent/child、依赖、stage 与 artifacts 不一致。
- strict validate 有未解决 blocking issue。

## 约束

- 每次只调度一个目标 Skill。
- 不直接修改 `meta.yaml.stage`，不创建阶段 artifact，不签发报告，不移动 change 目录。
- 不用主观判断替代 strict validate 或 full/pass evidence。
## CodeGraph 阶段动作

继续路由不自动调用 CodeGraph；只有需要核对代码影响时，按目标阶段 Skill 的专用流程调用。阶段路由、strict validate 和 `.spec` 状态始终以 Lite CLI 为权威。
