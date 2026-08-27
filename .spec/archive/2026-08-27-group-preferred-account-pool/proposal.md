# group-preferred-account-pool

## 问题

分组目前只能通过全局账号优先级、倍率和负载信号影响选择，管理员无法表达“该分组优先使用这些已绑定账号”。复用 `account_groups.priority` 会破坏现有绑定顺序语义，也会把分组局部策略错误扩散到账号其它分组。

## 目标

- 为账号-分组关系增加独立、幂等的 `scheduler_preferred` 标记，并同步 Ent、服务模型与调度快照。
- 提供管理员读取和替换当前分组优先账号池的接口，严格校验账号属于当前分组。
- 在所有分组感知调度路径中，在硬准入和合法粘性之后优先选择立即可用的优先账号；没有可用优先账号时回退普通池并沿用现有等待/重试。
- 保存、解绑和换组时保持标记生命周期正确，并通过 scheduler outbox 刷新受影响快照。
- 在分组编辑中提供中英文搜索多选和数量标记。
- 在内部调度决策中记录 `preferred_pool_hit`、优先/普通候选数量及回退原因，便于审计实际调用链路。

## 非目标

- 不修改账号全局 `Priority`、倍率、成本权重或其它分组的关系标记。
- 不绕过模型路由、利润控制、健康、能力、并发、限额、熔断和临时保护。
- 不复制优先状态到复制出的分组或复制出的账号。
- 不自动部署生产。
- 不改变 `account_groups.priority` 的绑定顺序语义；不提供优先池内拖动排序。

## 成功标准

- 高倍率优先账号仍先于低倍率普通账号；优先池内部倍率不改变选择，但负载/健康信号仍生效。
- 优先账号满载、429/503、熔断、模型不兼容或利润不合格时正确回退；合法 session/previous response 粘性优先。
- OpenAI 高级调度、通用平台调度、复合分组、Top-K、等待和重试均不绕过优先池或模型路由。
- 未配置优先账号的分组行为保持不变。
- API 保存、清空、非法账号、解绑清理、换组保留、outbox 与快照刷新均有测试证据。
- 前端可加载、搜索、保存、清空并显示中英文提示。
- 严格校验只接受当前分组已绑定账号；保存失败不产生部分标记或 scheduler 刷新事件。

## 影响范围

`backend/migrations`、`backend/ent/schema`、`backend/internal/repository`、`backend/internal/service`、`backend/internal/handler/admin`、`backend/internal/server/routes`、`frontend/src/api/admin/groups.ts`、`frontend/src/views/admin/GroupsView.vue`、i18n、Go/Vitest 测试、`.wiki/` 稳定知识和 fork extension audit 记录。

## 交付形态

single-change，采用 TDD。

## 风险与回滚

字段默认 false 保证旧数据行为不变；优先排序只在最终实际分组和现有硬准入候选之后发生。迁移为幂等 `ALTER TABLE`，代码通过关闭优先池即可回到普通池；接口保存采用单事务替换关系标记，失败回滚且不发送刷新事件。

若快照尚未刷新，调度读取继续沿用旧快照/现有 fallback 机制，不把部分刷新视为成功；事件消费失败保留 outbox 以便重试。生产部署、迁移执行和真实流量切换不属于本 planning 阶段。
