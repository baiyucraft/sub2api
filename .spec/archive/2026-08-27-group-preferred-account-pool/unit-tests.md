# group-preferred-account-pool TDD 单元测试

## UT-001 关系字段、DTO 与 scheduler snapshot 投影

- Test：`backend/internal/service/group_preferred_account_pool_test.go`、`backend/internal/repository/*scheduler*_test.go` 或现有 snapshot 测试。
- Modify：`backend/ent/schema/account_group.go`、generated Ent、`service.AccountGroup`、DTO mapper、scheduler cache/snapshot repository。
- 映射：ST-003、ST-004；关系字段与旧快照兼容边界。
- Red：测试读取/序列化 `scheduler_preferred` 时字段缺失或恒为 false；旧 JSON/快照缺字段必须仍反序列化为 false。
- Green：新增字段、迁移、Ent generated code 和所有 mapper/cache 投影。
- Refactor：确认 `account_groups.priority` 仍只表示绑定顺序，旧查询排序无语义漂移。

## UT-002 优先账号池保存的严格绑定校验和原子性

- Test：`backend/internal/service/group_preferred_account_pool_test.go` 或 repository/admin service 测试。
- Modify：`backend/internal/repository/group_repo.go`、`backend/internal/service/admin_group.go`、`backend/internal/service/admin_service.go`。
- 映射：ST-001、ST-002。
- Red：未绑定账号被静默忽略或造成部分更新；重复 ID 可能错误计数；失败后 outbox 被写入。
- Green：去重、校验分组存在与全部关系存在，单事务替换标记，失败 rollback 且不发事件。
- Refactor：统一错误码和 repository/service 边界，保持可测试接口窄。

## UT-003 API handler 与 route contract

- Test：`backend/internal/handler/admin/*group*_test.go`、`backend/internal/server/api_contract_test.go`。
- Modify：`backend/internal/handler/admin/group_handler.go`、`backend/internal/server/routes/admin.go`、DTO/types。
- 映射：ST-001、ST-002。
- Red：`GET/PUT /admin/groups/:id/preferred-accounts` 404 或请求体/错误格式不符合约定。
- Green：新增 request/response DTO、handler、路由和 admin service 调用。
- Refactor：沿用现有 admin 鉴权、参数解析和错误响应模式。

## UT-004 关系生命周期不复制优先状态

- Test：`backend/internal/service/admin_service_duplicate_account_test.go`、account/group repository tests。
- Modify：`AddToGroup`、`BindGroups`、复制分组/复制账号相关 repository/service。
- 映射：ST-003。
- Red：解绑/换组/复制后优先标记泄漏到新关系或其它分组。
- Green：删除关系自然清理；保留未解绑关系标记；新增关系默认 false；复制路径显式不复制 `scheduler_preferred`。
- Refactor：将“关系局部标记”约束沉淀到 helper 或测试命名。

## UT-005 统一 preferred pool 分割 helper

- Test：`backend/internal/service/group_preferred_account_pool_test.go`。
- Modify：`backend/internal/service/gateway_scheduling.go` 或独立 scheduler helper。
- 映射：ST-005、ST-008。
- Red：优先候选没有先于普通候选；无 group 时改变旧顺序。
- Green：实现单一判断函数，按最终 groupID 切分 preferred/ordinary，普通池顺序保持不变。
- Refactor：OpenAI 和通用调度只调用同一 helper，不复制字段判断。

## UT-006 通用网关硬准入、粘性、模型路由与等待回退

- Test：现有 `gateway_scheduling` / `gateway_service` scheduler tests。
- Modify：`gateway_scheduling.go`、模型路由/复合分组调度入口的 glue code。
- 映射：ST-005、ST-006、ST-007。
- Red：优先账号绕过模型路由或硬准入；满载优先账号导致额外等待而不是先回退普通池；合法粘性被优先池覆盖。
- Green：硬准入后分池，先尝试立即可用优先账号，失败记录 fallback reason，再使用普通池旧算法；两池无容量才等待。
- Refactor：保持排除/重试路径和普通池 Top-K/负载算法不变。

## UT-007 OpenAI 高级调度审计与 Top-K 回归

- Test：`backend/internal/service/openai_account_scheduler*_test.go`。
- Modify：`openai_account_scheduler.go`、`OpenAIAccountScheduleDecision`。
- 映射：ST-006、ST-008。
- Red：OpenAI 高级调度仍按倍率/成本压低优先池或缺少审计字段；普通池 Top-K 被改变。
- Green：接入统一 helper，优先池内部用非倍率 load plan，普通池继续既有 Top-K/成本权重；填充 `preferred_pool_hit`、候选数、fallback reason。
- Refactor：共享决策审计字段命名并保留英文稳定字段。

## UT-008 前端 API client、GroupsView 与 locale

- Test：`frontend/src/api/admin/__tests__/*groups*.spec.ts`、`frontend/src/views/admin/__tests__/GroupsView*.spec.ts`。
- Modify：`frontend/src/api/admin/groups.ts`、`frontend/src/views/admin/GroupsView.vue`、`frontend/src/i18n/locales/{zh,en}/admin/*`。
- 映射：ST-009。
- Red：API method 缺失、弹窗不加载优先池、搜索/保存/清空无行为、locale key 缺失。
- Green：添加 API types/client、搜索多选、保存/清空、数量标记和双语提示。
- Refactor：沿用现有管理页组件风格；不引入拖动排序或新的全局状态。

## 覆盖边界

- 不连接生产系统，不使用真实 provider 请求。
- 调度测试优先使用 fixture/mock 明确表达硬准入和候选集，不依赖概率性负载。
- UI 测试断言文本、请求参数和错误留存，不以截图作为唯一证据。
- TDD Red 必须是目标行为缺失导致的失败；编译错误、依赖缺失或测试环境故障不算 Red。
