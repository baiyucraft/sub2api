# adaptive-upstream-scheduler-fork-foundation 系统测试

## 测试环境

- runtime/platform：Go 1.27，当前 fork 基线 `9449571f7e6b03d93c29775ba9cf9d1e892dd2c5`
- fixture/data：纯值对象 fixture、内存 TTFT Guard、内存健康 Registry；不使用生产数据
- 外部依赖：无；service 回归只使用现有测试 doubles

## ST-001 契约依赖隔离

- 类型：boundary
- 前置：构建 `internal/forkscheduling` 与 `internal/forkscheduling/legacy`
- 操作：检查 Go import 图和包依赖
- 断言：契约/legacy 不依赖 service、handler、repository、Ent、Gin、Redis 或 adaptive scheduler；服务类型只在 bridge 适配层出现
- 证据：`go list -deps ./internal/forkscheduling/...`、定向 import 检查

## ST-002 并发 target 与 RPM 边界

- 类型：normal/boundary
- 前置：分别准备普通账号、共享 upstream 账号、显式 unlimited 账号及可选 RPM cache
- 操作：解析 target、共享容量、RPM reader/limiter
- 断言：普通账号使用账号 target，共享账号使用 `upstream_config_id` target；无限语义保持；RPM 预读和最终原子扣次仍是两个能力，缺少 cache 不新增动作
- 证据：`go test ./internal/forkscheduling/... ./internal/service -run 'Concurrency|RPM' -count=1`

## ST-003 优先池、倍率与 pool retry 等价

- 类型：normal/boundary
- 前置：准备相同 Priority、不同倍率的优先账号和普通账号，以及全局/账号级 retry 配置
- 操作：执行纯比较、优先池适配和 retry policy 读取
- 断言：优先池在普通池前，优先池内不按倍率重排；普通池有效倍率排序不变；全局配置、账号旧凭据、默认状态码优先级保持
- 证据：`go test ./internal/forkscheduling/... ./internal/service -run 'Preferred|PoolModeRetry|Legacy' -count=1`

## ST-004 TTFT 同实例状态一致

- 类型：normal/failure
- 前置：一个内存 Guard runtime，配置启用，提交一条临界慢样本
- 操作：分别调用 Observer、DegradationReader 和 Excluder
- 断言：三者看到同一份 legacy 状态；Excluder 保留试放序列副作用，Reader 不推进试放；无效 TTFT 不被改成成功样本
- 证据：`TestLegacyTTFTRuntimeSharesStateAcrossObserverReaderAndExcluder` 及现有 TTFT Guard 回归

## ST-005 健康 runtime 与生命周期边界

- 类型：normal/failure
- 前置：一个内存健康 Registry 和固定时间
- 操作：通过 bridge 写入 traffic failure，读取 snapshot/exclusion，再执行生命周期恢复
- 断言：bridge 不泄露 Registry 类型；同一 Registry 的状态可读；原持久化/锁/事务提交后入口仍由 service owner 负责，底座不创建第二个写入者
- 证据：`TestLegacyHealthRuntimeDelegatesToOneRegistry`、现有 `upstream_health*` 测试

## ST-006 未注入扩展时行为不变

- 类型：regression
- 前置：不配置 adaptive scheduler，使用当前 fork legacy 默认路径
- 操作：运行 service、handler、repository 相关回归测试
- 断言：原选择、健康、TTFT、槽位释放、RPM 和 pool retry 行为通过；不新增 goroutine、文件、数据库或网络操作
- 证据：`go test ./internal/service ./internal/handler ./internal/repository` 和完整 `go test ./...`

## 成功标准映射

| 成功标准 | ST | 证据 |
| --- | --- | --- |
| SC-01、SC-08 | ST-001 | import 图、白名单审查、代码构建 |
| SC-02、SC-06 | ST-002、ST-003 | 纯规则和 service 兼容回归 |
| SC-03 | ST-004 | TTFT runtime bridge 与现有状态机测试 |
| SC-04 | ST-005 | 健康 Registry/服务持久化回归 |
| SC-05 | ST-002、ST-006 | target/lease 与 service/handler 回归 |
| SC-07 | ST-006 | 未注入 legacy 路径回归 |
