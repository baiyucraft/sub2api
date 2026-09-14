# adaptive-upstream-scheduler-attempt-budget

## 问题

当前网关已经分别存在用户排队、账号槽位等待、同账号重试、账号切换、`Retry-After`、请求取消、流式提交和 usage 结算逻辑，但这些逻辑没有共享一个“单请求预算”。`FailoverState` 主要记录切换次数、失败账号和同账号重试次数，账号槽位等待由 `ConcurrencyHelper` 管理，usage 结算又由各协议 handler 在转发成功后异步提交。结果是同一请求可能在多个局部上限下叠加等待和尝试，难以证明不会重试放大、重复计费或在客户端已经收到语义内容后重复执行。

受影响的读者包括网关 handler、调度 service、并发/容量实现、usage 记录和运维诊断维护者。这个 child 需要先定义一套与具体 provider、Redis 和协议 handler 解耦的请求级账本，供后续故障域、缓存容量和灰度策略复用。

## 目标

- 为单个逻辑请求统一记录排队等待、用户/账号/共享上游槽位等待、上游尝试、同账号重试、账号切换、`Retry-After` 等待和总 deadline。
- 明确定义请求取消、HTTP 头提交、SSE 心跳、协议语义事件、工具调用和终态事件之间的可切换边界。
- 确保每个账号尝试具有稳定的 `attempt_id`，同一尝试的结果、槽位释放、容量预留结算和 usage 结算可以幂等处理。
- 将“是否允许继续尝试”变为可解释的预算决策：剩余尝试次数、剩余切换次数、剩余等待时间、已尝试账号/故障域和提交状态都可诊断。
- 与现有 `FailoverState`、并发服务、`UpstreamFailoverError`、`RecordUsage` 和缓存计费兼容，默认保持现有行为，并支持按配置关闭或回退到旧逻辑。
- 为后续缓存率感知容量 child 提供结算边界：重试不重复扣减逻辑请求的缓存读、缓存写、输入和输出预算；每次上游尝试只引用容量预留，不拥有独立的用户计费权。

## 非目标

- 本 child 不重新设计账号选择评分、优先池、TTFT 模型、故障域熔断或 provider 错误分类；这些属于 parent 的其他 child。
- 不直接改变现有 API 对外响应格式、鉴权、分组准入、用户计费规则或数据库 schema。
- 不在本 child 中引入新的 Redis key、分布式故障域状态或 token admission 算法；只定义与这些能力交互的内部接口和幂等边界。
- 不允许首个协议语义事件之后透明切换账号重放请求；工具调用、`previous_response_id` 和非幂等请求的续接策略不在本 child 中扩展为自动重放。
- 不把主动探针成功、HTTP 头已写出或客户端收到 SSE 心跳误认为业务成功，也不把客户端取消当成上游账号失败。

## 成功标准

- 同一请求的所有排队、槽位等待、上游尝试、同账号重试和账号切换都能在一个账本中按 `request_id`、`attempt_id` 和状态转换重建，且存在明确的总尝试数、总等待时间和 deadline 上限。
- 同账号重试和账号切换在预算耗尽、`Retry-After` 超过 deadline、客户端取消或提交状态达到不可重放边界时立即停止，并返回与现有协议兼容的终态。
- 已产生 HTTP 200/SSE 心跳但尚未产生语义事件时，系统能够区分“可写终态”与“不可透明重放”；一旦产生语义事件或工具调用，禁止新账号透明重试。
- 重试、切号、取消、超时和重复回调不会造成账号/上游槽位泄漏、等待队列计数泄漏、容量预留重复结算、usage 重复写入或重复扣费。
- `Retry-After` 仅在归一化结果允许重试且仍有预算时被采用；等待可被 context 取消，且不会延长原始 deadline。
- 默认配置下未启用新预算时，现有 handler 和 `FailoverState` 行为保持兼容；启用后可以通过结构化日志和指标观察新旧决策差异并回退。
- 对 cache-read、cache-write、未命中输入、输出和 reasoning 预算，账本只保存引用、预估和最终结算状态，不把一次 failover 误算成多次用户用量。

## 影响范围

- `backend/internal/handler/failover_loop.go`：将现有局部 failover 状态适配到请求级预算，不复制新的重试策略。
- `backend/internal/handler/gateway_handler_responses.go`、`gateway_handler_chat_completions.go`、`gateway_handler.go`：在协议入口、账号尝试、流提交、取消和 usage 结算位置发出生命周期事件。
- `backend/internal/handler/gateway_helper.go`、`backend/internal/service/concurrency_service.go`：让槽位等待和 release token 可被请求账本引用，并保持现有 fail-open/释放语义。
- `backend/internal/service/gateway_service.go`：复用 `UpstreamFailoverError`、`GatewayFailureStage`、`GatewayFailureScope` 和现有临时阻断接口作为预算输入，不改变错误分类所有权。
- `backend/internal/service` 的 usage/cache 结算边界：增加一次逻辑请求对应一次结算的约束，为缓存率容量 child 留出 adapter seam。
- 结构化日志、指标和测试：增加非敏感的预算决策、提交状态、尝试索引、终止原因和结算状态；不记录 API Key、Token、Cookie 或请求正文。

## 交付形态

single-change

这是 parent `adaptive-upstream-scheduler` 的第 3 个 child，依赖 `adaptive-upstream-scheduler-fork-foundation` 和 `adaptive-upstream-scheduler-error-attribution`。新增底座在先，原六个策略 child 的 ID 和相对顺序不变，合计七个 child；本预算 child 位于归因之后、健康之前。它只交付请求级预算和生命周期消费合同，能够独立测试、shadow 运行和回滚；公共事件/事实及错误类别仍由归因 child 提供，预算经底座窄端口消费 legacy 能力，后续策略通过内部 adapter 消费预算意见。前置底座默认执行 legacy，不以 Noop 关闭现有 fork 功能；本 child 不接管旧健康或货币结算。

## 风险

- 多协议 handler 的流提交语义不同，若把现有 `streamStarted` 直接等同于“不可重试”，可能过早阻断安全的首字节前切换，或错误地允许语义事件后重放。
- 槽位释放通常使用脱离请求取消的 background context；若账本只依赖请求 context，客户端取消可能造成槽位和等待计数泄漏。
- provider 返回的 `Retry-After` 可能格式错误、过大或与本地 deadline 冲突；必须由归因 child 归一化并在本 child 校验预算，不得裁短等待后提前发送。
- usage 记录异步提交，转发成功、客户端取消和上游已产生消耗之间可能出现竞态；需要把 settlement 状态与 attempt 生命周期分开。
- 现有 `ForceCacheBilling` 是 failover 状态中的兼容字段，若简单迁移为“每次切号都强制 cache-read”，会扭曲缓存率和真实成本；必须保留显式原因和一次性结算边界。
- 账本若直接进入 handler 或 Redis 实现，会扩大依赖和回滚范围；设计要求先通过接口和 feature flag 接入，旧路径仍可工作。

## 参考资料

- 来源：`../adaptive-upstream-scheduler/split.md`
  - 目标落点：parent/child 依赖、共同内部合同和集成验收场景
  - 采用方式：direct migration
- 来源：`../adaptive-upstream-scheduler/research/scope-and-delivery.md`
  - 目标落点：现有 failover、Responses 提交、并发、usage/cache 边界和 CodeGraph 事实
  - 采用方式：direct migration
- 来源：`backend/internal/handler/failover_loop.go:124-333`
  - 目标落点：`FailoverState`、同账号重试、切号、取消和 `Retry-After`/退避适配
  - 采用方式：rewrite behind adapter
- 来源：`backend/internal/handler/gateway_handler_responses.go:23-356`
  - 目标落点：Responses 的用户槽位、账号槽位、转发、流提交、failover 和 usage 入口
  - 采用方式：rewrite behind adapter
- 来源：`backend/internal/service/gateway_service.go:657-760`
  - 目标落点：`UpstreamFailoverError`、failure stage/scope 和临时阻断的兼容输入
  - 采用方式：direct migration
- 来源：Envoy retry host predicate / outlier retry 设计
  - 目标落点：已尝试 host 排除、错误来源分离和有界重试思路
  - 采用方式：inspiration only
- 来源：Netflix `concurrency-limits`
  - 目标落点：把等待、并发和延迟预算作为独立信号，不与 RPS 或用户计费混为一谈
  - 采用方式：inspiration only
