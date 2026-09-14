# adaptive-upstream-scheduler-fault-domain-health

## 问题

当前上游健康主要围绕账号或 API Key 记录。`RecordUpstreamTrafficFailure` 仍以有限的 HTTP 状态码类别更新账号健康，OpenAI 运行时阻断也主要按账号、账号加模型保存在进程内。这样会产生两类问题：

- 同一站点、endpoint 或共享代理发生故障时，多个账号分别被处罚，故障扩散且恢复不一致。
- 单个账号或模型故障时，缺少稳定的跨实例状态、半开探测和退避恢复，多个网关实例可能同时继续打到已知故障域。

本 child 需要在不复制账号选择器、不改写现有计费和协议处理的前提下，为调度提供可插拔的故障域健康层。故障域健康只能作为候选过滤和软降级信号；现有账号健康仍负责账号级持久状态，最终选择仍由现有 scheduler 完成。

## 目标

- 建立账号、账号加模型、endpoint/protocol、upstream config、共享站点/provider、代理等故障域的稳定分层与安全 fingerprint。
- 将 child `adaptive-upstream-scheduler-error-attribution` 产生的结构化失败事件投影到对应故障域，避免把请求级、账号级和共享故障混为一谈。
- 提供跨实例状态存取、并发 CAS/租约、ejection、half-open、指数退避和渐进恢复能力。
- 在现有调度入口前提供窄接口的故障域过滤器，不新增第二套账号选择器。
- 对故障域摘除比例、最小样本量、冷却上限、恢复成功率和缓存不可用状态提供可观察指标与审计事件。
- 保持无配置、旧缓存不可用或未接入新策略时的现有调度行为。

## 非目标

- 不替换 `defaultOpenAIAccountScheduler`，不复制或重写 `SelectAccountWithScheduler` 的候选排序逻辑。
- 不改变 API Key 鉴权、用户计费、usage 写入、协议转换、模型路由和账号绑定。
- 不新增数据库迁移，不把 API Key、Token、Cookie、请求正文、完整 URL 查询参数写入 fingerprint 或日志。
- 新健康层不把所有 4xx/5xx 自动归因到账号，不依据主动探针单独清除真实流量造成的故障状态；这是新增层的约束，不表示 legacy 来源已完全隔离，也不授权顺带修复旧状态机。
- 不在本 child 实现 cache-read/cache-write 预测、TPM admission、TTFT 分桶或 shadow/灰度发布。
- 不执行生产配置写入、VM Gate 或生产部署。

## 成功标准

- 相同规范化故障域在不同网关实例上生成相同 fingerprint；不同 scope 的域不会发生 key 冲突或互相覆盖。
- 账号级、账号加模型级、endpoint/protocol 级和共享站点级故障可以分别记录、读取、摘除与恢复；一个域的状态变化不会直接修改其它域。
- 连续达到配置阈值且满足最小样本量的域进入 ejection；未达到阈值、请求级错误或客户端取消不会摘除域。
- ejection 到期后只有一个实例取得 half-open 租约；成功按恢复策略逐步放量，失败按递增退避重新摘除。
- 达到最大摘除比例后仍保留最低可用容量，避免共享站点误判造成全池不可用；没有可用候选时交由现有调度和 failover 语义返回原有错误。
- 分布式存储不可用时不因缓存读取失败而全局封禁候选；系统记录降级指标，并按明确的本地 advisory 规则保持可恢复。
- 新层未注入或关闭时，现有账号健康、调度顺序、usage 和上游请求行为保持不变。
- 进入 `enforced` 前验证 probe/traffic 跨来源仲裁及旧阻断保留；不得用新归因或 `unknown` 静默取消官方阻断。需要改变 legacy 行为时先另行确认修复授权。
- 相关 service/handler/cache seam 测试、现有健康和 failover 测试、完整后端测试及 `git diff --check` 能提供成功证据；不需要生产部署作为本 child 的验收条件。

## 影响范围

- `backend/internal/service`：故障域 fingerprint、状态机、结果事件投影、候选过滤和健康接口的 ownership。
- `backend/internal/handler`：在现有失败归因和 failover 预算之后传递故障域上下文，不新增选号实现。
- `backend/internal/repository` 或缓存适配层：实现跨实例状态存取和 half-open 租约，但通过接口隔离具体 Redis 驱动。
- `backend/internal/config`：阈值、冷却、最大摘除比例和降级策略；配置更新必须原子替换。
- 管理/运维指标：只输出聚合的故障域 scope、状态、原因类别、计数和时间，不输出凭据或请求内容。
- 受影响测试：`upstream_health_service_test.go`、`openai_account_scheduler_test.go`、`failover_loop_test.go`、429/529/capacity shed、concurrency/cache 相关测试。

## 交付形态

single-change

该 change 是 parent `adaptive-upstream-scheduler` 的第 4 个 child，依赖 `adaptive-upstream-scheduler-fork-foundation`、`adaptive-upstream-scheduler-error-attribution` 和 `adaptive-upstream-scheduler-attempt-budget`。新增底座在先，原六个策略 child 的 ID 和相对顺序不变，合计七个 child；本健康 child 位于预算之后、缓存容量之前。它通过底座窄端口消费现有健康能力，只定义故障域健康与调度过滤边界，可独立 review、测试、灰度和回滚；不吸收公共事件/事实、缓存容量、TTFT 隔离或发布灰度职责。底座默认执行 legacy，不以 Noop 关闭旧功能，也不代为修复来源覆盖。

## 风险

- 归因过宽会把共享故障错误地扩散到大量账号，归因过窄则无法抑制站点级故障；必须保留 evidence、最小样本量和最大摘除比例。
- 多实例同时 half-open 可能造成恢复期流量尖峰；需要原子租约和租约过期回收。
- 分布式缓存不可用时若 fail-closed 会造成全局拒绝，若 fail-open 会暂时重复访问故障域；默认应选择可观测的 fail-open/advisory 降级。
- 已登记静态风险：现有 traffic 401 暂停后，后续默认 probe 404 可能将健康状态改为 degraded 并清空暂停来源；不能宣称旧来源完全隔离。前置底座只做行为等价收口，不修此问题；本健康 change 在 `enforced` 前必须验证跨来源仲裁，新层只叠加受约束意见，不直接重置旧健康或借新归因取消官方阻断。超出既有授权的行为修复另行确认。
- 长请求、流式首事件之后和非幂等工具调用不能由本 child 擅自触发跨账号重放；重试边界由 attempt-budget child 保持。

## 参考资料

- 来源：`adaptive-upstream-scheduler/research/scope-and-delivery.md`
  - 目标落点：现有 `RecordUpstreamTrafficFailure`、`SelectAccountWithScheduler`、账号冷却、GatewayCache 与 failover 的 ownership 和回滚边界
  - 采用方式：direct migration of constraints；不复制现有选择器
- 来源：`backend/internal/service/upstream_health_evidence.go`、`upstream_health.go`、`openai_account_runtime_block_fastpath.go`
  - 目标落点：复用账号健康状态与失败事件，增加上层故障域 overlay
  - 采用方式：rewrite behind narrow interfaces
- 来源：`backend/internal/service/gateway_service.go` 的 `GatewayCache` 与现有缓存适配边界
  - 目标落点：跨实例状态和 half-open 租约的抽象接口
  - 采用方式：direct interface pattern
- 来源：Envoy outlier detection、local/external origin error、最大摘除比例、指数退避和 retry host predicate 设计
  - 目标落点：错误分层、限摘除、恢复退避和排除已尝试故障域
  - 采用方式：inspiration only；不引入 Envoy 代理层或复制完整实现
- 来源：`adaptive-upstream-scheduler-error-attribution`、`adaptive-upstream-scheduler-attempt-budget`
  - 目标落点：结构化结果事件、attempt ledger、已尝试账号/故障域排除和请求 deadline
  - 采用方式：dependency contract

## 研究与证据边界

本 child 没有单独的 `research/**` 或 `research/codegraph.md`。proposal 复用 parent research；CodeGraph 使用当前索引的 CLI 影响分析和定向源码核验作为 fallback。由于索引报告存在待同步文件，具体实现前仍需对最终修改符号重新执行 `codegraph sync` 或等价核验。
