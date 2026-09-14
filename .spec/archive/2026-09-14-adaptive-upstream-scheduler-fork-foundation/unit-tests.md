# adaptive-upstream-scheduler-fork-foundation TDD 单元测试

## UT-01 并发契约和 legacy target

- Test：`backend/internal/forkscheduling/contracts_test.go`、`backend/internal/forkscheduling/legacy/concurrency_test.go`
- Modify：`backend/internal/forkscheduling/contracts.go`、`backend/internal/forkscheduling/legacy/concurrency.go`、`backend/internal/service/upstream_scheduler_concurrency.go`
- 映射：ST-002 / SC-05
- Red：原代码没有独立 `forkscheduling` 契约；首次接入测试阶段出现 `extraValue` 和字段缺失编译红，说明兼容边界尚未闭合
- Green：增加 `ConcurrencyTarget`、`ConcurrencyAccountView` 和容量解析的纯值实现，service 方法保留兼容委托
- Refactor：保留 service 常量、类型别名和原 target 归一化语义，现有 service 测试继续通过

## UT-02 优先池纯比较

- Test：`backend/internal/forkscheduling/legacy/pool_test.go`、`backend/internal/service/group_preferred_account_pool_test.go`
- Modify：`backend/internal/forkscheduling/legacy/pool.go`、`backend/internal/service/account.go`、`backend/internal/service/preferred_account_pool.go`
- 映射：ST-003 / SC-02、SC-08
- Red：原优先池比较直接读取 `Account`，无法在无 service 依赖的契约层验证
- Green：使用 bounded `CandidateView` 委托 Priority、倍率、优先池和 Unix 秒级 LastUsedAt 分组规则
- Refactor：仅在 adapter 中复制安全字段，保留 compact tier、OAuth、负载和随机调用位置

## UT-03 Pool retry policy

- Test：`backend/internal/forkscheduling/legacy/retry_test.go`、`backend/internal/service/fork_scheduling_bridge_test.go`、现有 pool retry tests
- Modify：`backend/internal/forkscheduling/legacy/retry.go`、`backend/internal/service/upstream_config.go`、`backend/internal/service/account.go`
- 映射：ST-003 / SC-06
- Red：独立 retry contract 及 legacy 归一化实现不存在
- Green：增加状态码归一化和 `RetryPolicyReader` bridge，保持全局配置/账号凭据/默认值顺序
- Refactor：不把 FailoverState、sleep、ForceCacheBilling 或 handler 重试循环移入底座

## UT-04 TTFT bridge

- Test：`backend/internal/service/fork_scheduling_bridge_test.go`、`backend/internal/service/openai_ttft_guard_test.go`
- Modify：`backend/internal/forkscheduling/contracts.go`、`backend/internal/service/fork_scheduling_bridge.go`、`backend/internal/service/openai_ttft_guard.go`
- 映射：ST-004 / SC-03
- Red：TTFT 状态只能通过 service 私有类型访问，Observer、Excluder、Reader 没有统一窄合同
- Green：bridge 映射同一个 Guard，拆分三类接口并保持旧状态机调用次数和副作用
- Refactor：管理 Reader 仍只读视图，Excluder 明确保留试放副作用，官方 scheduler feedback 不迁移

## UT-05 健康与 runtime 装配

- Test：`backend/internal/service/fork_scheduling_bridge_test.go`、现有 `upstream_health_test.go`、`upstream_health_service_test.go`
- Modify：`backend/internal/service/fork_scheduling_bridge.go`、`backend/internal/service/upstream_health_scheduling.go`
- 映射：ST-005、ST-006 / SC-04、SC-07
- Red：健康调度包装直接读取全局 Registry，不能通过 provider-neutral Reader 验证
- Green：增加 HealthReader/Evidence/Lifecycle 适配及单 runtime 聚合视图，原全局入口保留兼容外观
- Refactor：持久化、锁、失败回滚和提交后生命周期仍留在 UpstreamConfigService/Repository owner

## 覆盖边界

- 使用内存 fixture 和现有 mock，不连接生产 Redis、数据库或上游。
- 不以契约类型测试替代官方 selector、handler retry loop、计费或协议测试；这些由现有回归入口守护。
- `GetAccountsLoadBatch` 的 Redis 清理副作用、TTFT exclusions 的 probe 序列推进和 lease 恰好一次释放必须在 service/handler 回归中继续验证。
