# CodeGraph 影响面记录

## 查询

- `account_groups scheduler_preferred account_group.go AccountGroup admin group preferred accounts scheduler_snapshot_service gateway_scheduling openai_account_scheduler GroupsView preferred-accounts route handler`

## 关键发现

- `service.AccountGroup`、generated `ent.AccountGroup`、DTO mapper 和 scheduler cache 都是 `scheduler_preferred` 投影必须同步的边界。
- `accountRepository.loadAccountGroups` 是账号调度快照读取关系字段的重要入口；新增字段必须随 account groups 一起进入 `Account.AccountGroups`。
- `groupRepository` 已有 group/account 关系管理、复制、排序和计数逻辑，优先池保存应只更新当前 group 的关系标记，不复用 `account_groups.priority`。
- `SchedulerSnapshotService` 的 account/group outbox 消费、bucket rebuild 和 account rebuild 是刷新对应分组桶及受影响账号快照的验证重点。
- 通用网关路径集中在 `gateway_scheduling.go` 的模型/能力/利润/容量过滤和选择函数；优先池必须在硬准入后、普通池算法前接入。
- OpenAI 高级调度集中在 `defaultOpenAIAccountScheduler.Select`、load plan、Top-K 和 weighted selection；普通池成本权重和 Top-K 需要保持回归。
- `GroupsView.vue` 已承载分组编辑、复制账号、模型路由和多个配置区；优先账号池 UI 应复用现有弹窗风格，避免引入新的拖动排序。

## 测试线索

- 后端：新增 `group_preferred_account_pool_test.go`，并扩展 admin group/repository、scheduler snapshot、gateway scheduling、OpenAI scheduler 既有测试。
- 前端：扩展 `frontend/src/api/admin/groups.ts` 对应 tests 与 `GroupsView` 组件测试；保留既有 `GroupsView.duplicate.spec.ts` 行为。
- 门禁：本变更触及后端、migration、前端 API/UI，分类为 `dev-gated`；本地测试通过后需要当前 commit 对应 VM Gate，VM Gate 不能代替生产部署声明。
