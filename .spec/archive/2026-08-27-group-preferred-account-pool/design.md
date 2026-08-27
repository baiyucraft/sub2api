# group-preferred-account-pool 设计方案

## Ownership 与数据流

`account_groups.scheduler_preferred` 是关系 SSOT，定义为 `boolean NOT NULL DEFAULT false`；`service.AccountGroup.SchedulerPreferred` 是服务模型；scheduler cache/snapshot 只序列化该字段。管理员 service 负责当前分组绑定校验和原子替换，repository 负责关系更新及 `account_groups_changed` outbox。调度层统一使用 `AccountGroup` 判断，OpenAI 高级调度和通用网关不各自实现规则。

`account_groups.priority` 继续表示绑定顺序，仅用于既有展示/查询排序；优先池是无序集合，不把该字段重载为调度优先级。

## 调度契约

```text
合法 session/previous_response 粘性
  -> 既有模型/平台/能力/健康/利润/容量硬准入
  -> final group 的 scheduler_preferred=true 且立即可用
  -> 优先池内部沿用非倍率信号
  -> 普通池现有排序/Top-K/等待/重试
```

优先池只对最终分组、模型路由候选交集生效；优先账号没有立即容量时不等待，继续尝试其它优先账号，再回普通池。普通池不配置时走旧路径。决策结构增加 `preferred_pool_hit`、候选数量和回退原因，日志字段保持内部英文稳定。

硬准入必须先于分池：健康、启用、可调度、平台/协议/模型/能力、并发/限额、熔断、429/503 临时保护和分组利润控制均不可绕过。合法 `previous_response` 或 session 粘性继续优先于优先池；粘性失效后才进入优先池。模型路由先收窄候选，只有路由候选交集中的优先账号进入优先池。

优先池内部忽略上游倍率和成本权重，但继续使用账号全局 `Priority`、负载、排队、错误率、TTFT、额度和重置时间等非倍率信号。普通池保留现有倍率、成本权重、Top-K、负载、等待和重试算法。优先账号槽位满时立即尝试其它优先账号并回退普通池；两池都无立即容量时才生成既有等待计划。

## 管理接口

- `GET /admin/groups/:id/preferred-accounts` 返回当前分组已绑定账号的可展示元数据和 `scheduler_preferred` 状态。
- `PUT /admin/groups/:id/preferred-accounts` 接收 `{account_ids:number[]}`，去重后校验全部关系存在；空数组清空。
- 返回项至少包含账号名称、平台、倍率、状态、上游来源、账号 ID 和 `scheduler_preferred`，仅列当前分组绑定关系。
- 事务只更新该分组关系；解绑自然删除标记，换组保留仍存在的关系标记、新关系默认 false；复制 group/account 不携带标记。
- 非法账号、重复/非法 ID、未知分组或关系竞态均 fail-closed；事务失败不发送 outbox。

## 快照与失败边界

关系更新提交后发送已有 scheduler outbox 事件，使对应分组桶及受影响账号快照刷新，无需重启。事件 payload 必须包含当前分组和受影响账号范围；事务失败不得发送事件。旧快照缺字段按 false 反序列化。请求非法账号返回 400，未知分组沿用现有 not found。

## UI

GroupsView 编辑弹窗调用独立 API 加载已绑定账号，多选搜索展示名称、平台、倍率、状态、上游来源；保存和清空独立于复制账号操作，不提供拖动排序。说明文案明确当前分组生效、不受倍率排序压低、不绕过利润/健康/能力/并发限制、无可用优先账号自动回退。列表新增简短优先数量列/标记，文案加入 zh/en。

## CodeGraph 与验证边界

已通过 CodeGraph 定位 `service.AccountGroup`、`queryAccountsByGroup`、`scheduler_snapshot_service.go`、`defaultOpenAIAccountScheduler`、`gateway_scheduling.go`、`GroupsView.vue` 及 admin group 路由。源码核验要求覆盖 account group CRUD、快照投影、OpenAI `buildOpenAIAccountLoadPlan`/Top-K、通用 `listSchedulableAccounts`/`selectAccountWithLoadAwarenessCore`、复合分组最终解析、outbox `handleAccountEvent`/`handleGroupEvent` 和 GroupHandler/AdminService。CodeGraph 只辅助影响面，不替代 Go/Vitest/API 断言；完整摘要见 `research/codegraph.md`。
