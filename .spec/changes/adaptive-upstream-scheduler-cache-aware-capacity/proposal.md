# adaptive-upstream-scheduler-cache-aware-capacity

## 问题

当前系统已经记录 `input_tokens`、`output_tokens`、`cache_creation_tokens` 和 `cache_read_tokens`，并在质量与用量页面展示缓存率；但这些数据尚未形成独立的上游容量准入合同。调度器容易把缓存率、计费价格、并发槽位和 RPM 混成一个评分信号，无法回答一个请求在不同 provider 上实际会占用多少输入、缓存创建、输出和 reasoning 容量。

本 change 需要为后续自适应调度提供一个与现有计费解耦的内部容量层。它应能在请求进入上游前基于可用证据估算需求，在 Redis 或等价状态存储中原子预留，在请求结束后按实际 usage 结算并退还未使用部分；当 provider 不支持缓存字段、tokenizer 不可用、usage 不完整或状态存储异常时，必须明确标记 unknown/partial，并保持现有调度链路可回退。

## 目标

- 建立 provider-neutral 的缓存感知容量需求模型，至少区分：cache-read 预估、cache-write/创建、未命中输入、上下文总量、最大输出和 reasoning 预算。
- 将缓存因素作为容量向量和 provider capability 的输入，而不是单一“缓存率越高越优”的奖励，也不读取或改写用户计费余额与计费结算。
- 提供内部 `CapacityReservation` 生命周期：预估、原子预留、实际结算、退款/释放、过期回收和幂等重试。
- 通过 provider adapter 隔离 token 估算、缓存语义、TPM/上下文限制和 usage 归一化；未知能力时不奖励缓存命中。
- 以可插拔 admission 接口接入现有账号/上游调度，默认 shadow-only 或 legacy-compatible，关闭时不改变当前选号、RPM、并发和计费行为。
- 为 Redis 故障、部分 usage、客户端取消、流中断、重试切换和多实例时钟差异定义可观测且可回滚的行为。

## 非目标

- 不修改用户余额、用量扣减、倍率、缓存读写价格或任何计费公式。
- 不替换现有账号并发槽位、RPM 限制、TTFT Guard、优先账号池或 failover loop；本 change 只定义可插拔容量准入边界。
- 不新增数据库迁移、公开 API、管理页面或 provider 凭据字段。
- 不根据缓存率单独提高某个账号、模型或站点的调度分数。
- 不要求首版支持所有 provider 的真实 tokenizer；缺少 adapter 证据时保留 unknown/legacy fallback。
- 不在本 change 中决定最终重试次数、熔断策略、P2C 选择算法或灰度比例。

## 成功标准

- 对至少一种支持缓存 usage 的 provider，容量需求能分别输出 cache-read、cache-write、未命中输入、最大输出和 reasoning 预算；缺失字段不会被静默当作 0。
- 同一请求的预留、重复 settle、重复 release 和过期清理具有幂等结果，不产生负计数或永久占用。
- 固定 cache-read、高 cache-write、完全未命中和未知 capability 请求会产生不同的容量记录；缓存率本身不能绕过模型、协议、RPM、并发或冷却硬约束。
- 实际 usage 已知时只结算已消耗容量并释放差额；usage 不完整时保留 `partial/unknown` 原因并由 TTL 回收，不调用计费服务补账。
- Redis/容量 adapter 故障时，默认回退现有调度行为并产生结构化诊断；关闭 feature 后不存在必须清理的数据库数据或改变后的旧调度状态。
- 现有计费、usage log、账号选择、重试和路由测试继续通过；新容量模块可用 fake store、fake adapter 和 fake clock 独立测试。
- 设计文档能够明确指出请求入口、容量模块 ownership、Redis adapter 边界、结果事件接入点和回滚开关，供后续 `wiki-plan` 生成可执行测试任务。

## 影响范围

- `backend/internal/service`：新增 provider-neutral 容量需求、预留和结算接口；以 optional admission seam 接入调度。
- `backend/internal/handler`：仅提供已归一化的请求上下文、deadline、协议和 workload 信息，不直接读取 Redis 或调用计费服务。
- `backend/internal/repository` 或 Redis adapter：实现原子预留、结算、释放、TTL 和幂等键；不改变现有 billing cache 的 ownership。
- `backend/internal/service/account_stats_pricing.go`、usage 相关模块：只作为事实字段的来源或结果事件消费者，不能成为容量模块的依赖反向入口。
- 配置/运行时：提供关闭、shadow-only 和 legacy fallback 状态；不要求数据库迁移。
- 测试：覆盖 estimator、provider capability、reservation store、部分失败、回滚和现有调度兼容性。

## 交付形态

single-change

这是 parent `adaptive-upstream-scheduler` 的第 5 个 child，首先依赖前置 `adaptive-upstream-scheduler-fork-foundation`，并保留对 `adaptive-upstream-scheduler-error-attribution` 的依赖。整体为前置底座加原六个策略 child，共七个 child；本 child 只消费底座窄端口，前置不反向依赖策略合同。本 child 只交付容量准入的内部设计边界，独立于错误归因、请求级重试预算、故障域健康和灰度发布的具体实现；它向这些 child 提供容量状态和失败原因，不拥有它们的状态机。可单独 review、测试和回滚，且关闭新增策略后仍默认执行当前 fork legacy 行为，不关闭现有 TTFT、健康或并发保护。

## 风险

- provider 对 cache-read 是否占用 TPM、cache-write 是否有独立配额的语义不一致，错误假设可能导致误拒绝或过量放行。
- 请求体 token 估算不准确，尤其是多模态、工具调用、超长上下文和 reasoning 请求，可能造成容量预留偏差。
- usage 只在流结束或 provider 特定事件中出现，客户端取消或中途断流可能留下暂时性过预留。
- Redis 故障、TTL 过短或跨实例时间不一致可能造成双重预留、容量泄漏或短期过量放行。
- 如果把容量代价直接接入现有 billing/pricing 类型，会形成计费与调度的隐式耦合，增加回滚难度。
- 预留接入位置错误可能在首个语义事件后影响 failover 或重复工具调用；必须服从 parent 的 commit-state 和请求级预算设计。

## 参考资料

- 来源：`../adaptive-upstream-scheduler/research/scope-and-delivery.md`
  - 目标落点：本 child 的问题边界、缓存维度、默认 shadow/legacy fallback 和跨 child 契约。
  - 采用方式：rewrite
- 来源：`../adaptive-upstream-scheduler/split.md`
  - 目标落点：`CapacityReservation`、长上下文/高 cache-read/首次 cache-write 混合验收和 parent 依赖顺序。
  - 采用方式：direct migration
- 来源：`backend/internal/service/account_stats_pricing.go`、`backend/internal/service/account_usage_service.go`
  - 目标落点：已存在的 cache usage 字段仅作为事实输入，不复用计费计算作为容量算法。
  - 采用方式：inspiration only
- 来源：`backend/resources/model-pricing/model_prices_and_context_window.json` 与现有 channel pricing 字段
  - 目标落点：确认模型上下文、reasoning 和 cache write/read 数据存在，但禁止直接把价格字段当容量权重。
  - 采用方式：inspiration only
- 来源：Netflix `concurrency-limits`、LiteLLM Router、Portkey Gateway、Envoy outlier/retry 设计
  - 目标落点：容量/延迟趋势、TPM admission、Retry-After 和失败回退的边界。
  - 采用方式：inspiration only
