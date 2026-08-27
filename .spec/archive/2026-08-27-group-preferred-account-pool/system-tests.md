# group-preferred-account-pool 系统测试

## 测试环境

- runtime/platform：本地 Go 1.x、Node/pnpm/Vitest、Ent generated code、PostgreSQL fixture 或 repository test harness；最终后端/迁移/前后端混合变更进入 `sub2api-dev` VM Gate。
- fixture/data：可重复创建的分组、账号、账号-分组关系、模型路由、健康/限额/并发/429/503/熔断/利润控制状态；前端使用 mock API。
- 外部依赖：不连接生产数据库、生产 Redis 或真实 provider；scheduler/outbox 使用本地 DB/fixture 或 mock。

## ST-001 管理 API 读取、替换与清空优先账号池

- 类型：normal / boundary
- 前置：分组 G 绑定账号 A、B、C，其中 B 已为优先；账号 D 未绑定 G。
- 操作：调用 `GET /admin/groups/:id/preferred-accounts`，再 `PUT {"account_ids":[A,C,A]}`，最后 `PUT {"account_ids":[]}`。
- 断言：GET 只返回当前分组已绑定账号并标记优先状态；PUT 去重后仅 A/C 为优先；空数组清空；`account_groups.priority` 绑定顺序不变化。
- 证据：Go handler/service/repository 测试；API contract 测试；数据库行断言。

## ST-002 非法账号与事务失败 fail-closed

- 类型：failure
- 前置：分组 G 绑定账号 A，账号 D 存在但未绑定 G；数据库可观察 outbox。
- 操作：`PUT {"account_ids":[A,D]}` 或包含非法 ID。
- 断言：返回 400/既有错误格式；A 不会被部分置为优先；不发送 scheduler outbox；错误不暴露内部 SQL。
- 证据：Go service/repository 测试；事务回滚和 outbox 计数断言。

## ST-003 关系生命周期：解绑、换组、复制隔离

- 类型：normal / boundary
- 前置：账号 A/B 绑定 G 且 A 为优先；目标分组 H 存在；复制分组/复制账号功能可用。
- 操作：解绑 A；保存账号分组从 G 切换到 H 或追加 H；执行“从其他分组复制账号”和复制账号本身。
- 断言：解绑自然删除优先标记；仍保留在 G 的关系维持原标记；新增 H 关系默认非优先；复制 group/account 不复制优先状态。
- 证据：Go repository/admin duplicate 测试；数据库关系行断言。

## ST-004 outbox 与 scheduler snapshot 刷新

- 类型：normal / failure
- 前置：分组 G 已配置优先账号 A；scheduler snapshot/cache 可序列化 account groups。
- 操作：保存优先池并触发 outbox 消费；同时验证旧 snapshot 缺字段反序列化。
- 断言：提交后 outbox 包含对应 group/account 影响范围；重建后快照中 `SchedulerPreferred=true`；旧数据缺字段按 false；提交失败不产生刷新事件。
- 证据：Go scheduler snapshot/cache 测试；outbox 消费路径测试。

## ST-005 通用网关优先池选择与普通池基线

- 类型：normal / regression
- 前置：分组 G 同时有高倍率优先账号 P 和低倍率普通账号 O；另有未配置优先池的分组 G0。
- 操作：通过通用平台调度选择账号。
- 断言：P 先于 O；优先池内部不使用倍率/上游成本权重，但保留账号全局 Priority、负载、错误率、TTFT、额度与重置时间等非倍率信号；G0 调度结果与基线一致。
- 证据：Go scheduler unit/integration 测试；决策审计字段断言。

## ST-006 硬准入失败时即时回退普通池

- 类型：failure / boundary
- 前置：优先账号存在但分别处于满并发、429/503 临时保护、熔断、模型/协议/能力不兼容、利润不合格或不可调度状态；普通账号可用。
- 操作：执行通用网关和 OpenAI 高级调度。
- 断言：每类硬准入失败都不绕过现有限制；不会额外等待满载优先账号；继续尝试其它优先账号，再回退普通池；两池都无立即容量时才进入现有等待策略。
- 证据：Go scheduler tests；wait-plan/retry-path 断言。

## ST-007 粘性、模型路由与复合分组边界

- 类型：normal / boundary
- 前置：存在合法 session/previous_response 粘性账号 S、模型路由候选 R、复合分组最终解析到 G；另有优先账号 P 不在某模型路由候选中。
- 操作：分别触发粘性有效、粘性失效、模型路由收窄、复合分组解析后的调度。
- 断言：合法粘性优先于优先池；粘性失效后才进入最终实际分组 G 的优先池；优先池不能扩大模型路由候选；复合分组使用最终分组而非父组合定义。
- 证据：Go scheduler tests；decision audit 断言。

## ST-008 OpenAI 高级调度、Top-K、等待与重试共用语义

- 类型：normal / regression
- 前置：OpenAI 高级调度启用 Top-K、成本权重、负载计划与等待/重试路径；分组 G 有优先和普通候选。
- 操作：执行 `defaultOpenAIAccountScheduler.Select` 及排除/重试场景。
- 断言：优先池整体排在普通池前；普通池算法保持原有倍率/成本/Top-K/负载；优先池内部不受倍率压低；审计记录 `preferred_pool_hit`、候选数量、回退原因。
- 证据：Go OpenAI scheduler tests；现有回归测试。

## ST-009 前端分组编辑优先池体验

- 类型：normal / failure
- 前置：GroupsView 加载分组列表和已绑定账号；mock API 支持搜索、保存、清空和非法账号错误。
- 操作：打开编辑弹窗，搜索账号，多选保存，清空，切换 zh/en 文案，模拟保存 400。
- 断言：仅展示当前分组已绑定账号；选项显示名称、平台、倍率、状态、上游来源；列表显示优先账号数量；提示说明局部生效、不受倍率压低、不绕过硬准入、自动回退；非法账号错误可见且不关闭弹窗。
- 证据：Vitest component/API tests；typecheck/lint/build。

## 成功标准映射

| 成功标准 | ST | 证据 |
| --- | --- | --- |
| 高倍率优先账号仍先于低倍率普通账号；优先池内部倍率不改变选择 | ST-005, ST-008 | Go scheduler tests |
| 硬准入失败、满载、429/503、熔断、模型/利润不合格时回退 | ST-006 | Go scheduler tests |
| 合法粘性优先，模型路由和复合分组不被绕过 | ST-007 | Go scheduler tests |
| 未配置优先池行为保持不变 | ST-005 | baseline regression tests |
| API 保存、清空、非法账号、解绑/换组/复制、outbox 与快照刷新 | ST-001..004 | Go repository/service/handler/snapshot tests |
| 前端加载、搜索、保存、清空、非法错误和中英文提示 | ST-009 | Vitest/typecheck/lint/build |
| 完整交付不自动部署生产 | ST-001..009 | local gates + VM Gate report + final note |
