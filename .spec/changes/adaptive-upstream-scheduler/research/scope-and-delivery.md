# 自适应上游账号调度调研记录

phase: exploration
service-boundary: scope-and-delivery-shape

## 调研问题

- 如何在不重写现有 gateway/service 调度器的前提下，统一账号、模型、endpoint、共享站点和客户端错误的责任归因。
- 如何把请求级重试预算、缓存率、上下文长度、推理预算和 TTFT 纳入调度，而不把用户计费与上游容量混淆。
- 哪些结果具有独立验收边界，适合拆成 parent/child multi-change。

## 证据

| 来源 | 观察事实 | 可信度 | 对当前 change 的影响 |
| --- | --- | --- | --- |
| `backend/internal/service/openai_account_scheduler.go:70-140,1700-2217` | 高级 OpenAI 调度已有 sticky、preferred、负载、队列、错误率、TTFT、reset、quota headroom 和 cost 评分，但接口仍以账号选择和账号结果反馈为中心。 | high | 新能力应作为可插拔信号/过滤器接入，不复制选择器。 |
| `backend/internal/service/openai_account_scheduler.go:203-300` | scheduler 的错误率与 TTFT 运行时统计以 account 为主，事件触发时更新，缺少统一的模型、协议和站点时间衰减维度。 | high | 错误归因、TTFT 分桶和共享状态必须独立拆 child。 |
| `backend/internal/service/openai_ttft_guard.go:79-140,533-623` | TTFT Guard 按账号加规范化模型运行，带 TTL，但主要是进程内状态；评分 TTFT 与硬 Guard 是两套状态。 | high | 先定义统一结果事件，再处理分布式健康与软评分。 |
| `backend/internal/service/failover_loop.go:125-267` | 同账号重试、账号切换、错误专属上限和 deadline 是多个局部预算，当前没有全链路 total attempts 账本。 | high | 请求级预算必须作为独立契约，避免重试放大。 |
| `backend/internal/service/gateway_service.go:657-733` | 已有 `GatewayFailureStage`、`GatewayFailureScope` 和 `UpstreamFailoverError`，但普通错误的 scope 并不总是完整。 | high | child 1 优先扩展已有失败模型，而不是替换现有错误处理。 |
| `backend/internal/service/ratelimit_service.go:325-575,982-1077,1137-1295,1909-1947` | 已按 401/403、429、5xx、529 和 transport 做部分冷却与失败处理，但共享站点与 endpoint 的关联不完整。 | high | child 3 需要复用现有账号冷却，同时补充上层故障域。 |
| `backend/internal/service/gateway_handler_responses.go:255`、`backend/internal/handler/openai_gateway_handler.go:788-878` | Responses/流式链路在首 token 和语义输出后存在不同 failover 边界，客户端取消也不能继续换号。 | high | child 2 必须定义 HTTP committed、协议事件 committed、语义事件 committed 三态。 |
| `backend/internal/service/concurrency_service.go:338-343` | 账号并发与等待队列已有统一负载读取；共享上游有并发槽位，但没有通用 token admission。 | high | child 4 不替换并发槽位，而是在其前面增加 token/capacity 预留。 |
| `backend/internal/service/account_quality.go`、`backend/internal/service/upstream_dashboard.go` | 已有真实流量质量、TTFT、错误、usage 和成本聚合，但 probe、ops error、failed request 与最终请求结果仍需明确口径。 | high | child 6 必须使用 counterfactual/shadow 指标和明确去重口径。 |
| `.tmp/production_error_scheduler_audit_20260913_partial.md` | 2026-09-13 生产应用健康、health 200、Nginx 正常；最近 7 天有 27,394 条非空 `upstream_errors` 日志行，但本轮没有完成状态码和重试链聚合。 | high | 生产错误比例暂不能作为权重调参依据；先补只读聚合和证据口径。 |
| Envoy outlier detection / retry host predicate 设计 | 开源实现将本地错误、上游 5xx、连续失败、最大摘除比例、恢复退避和已尝试 host 分开处理。 | medium | 借鉴错误分层、ejection 和 retry host 排除，不复制完整代理层。 |
| Netflix concurrency-limits | Vegas/Gradient2 用延迟趋势和排队变化调节并发，而不是只等错误发生。 | medium | child 5 可使用排队/TTFT趋势作为软信号，保留应用硬上限。 |
| LiteLLM Router / Portkey Gateway / NewAPI 源码研究 | 可借鉴 RPM/TPM admission、TTFT 选路、deployment cooldown、Retry-After 预算、priority/weight 分层；各项目的 fail-open 和固定重试策略不应直接照搬。 | medium | 缓存率和容量准入独立建模，重试预算与 deadline 统一。 |

## 结论

- 本需求不是一个可安全一次性改完的单 change，而是六个具有独立验收和回滚边界的 child。
- 最早可执行的是错误归因 child；没有统一责任类别，就无法判断某个评分信号是否应该惩罚账号、模型还是站点。
- 缓存率必须进入容量模型，但不能直接作为“命中率越高就越优”的单一奖励。至少要区分：预计 cache-read 命中、cache-write/创建成本、未命中输入成本、上下文长度、最大输出和 reasoning 预算。
- 新调度默认 shadow-only，生产行为保持现状，直到新旧策略的可用性、TTFT、重试放大、成本和缓存预测误差通过门禁。

## Delivery Shape 影响

- 推荐：multi-change。
- 原因：错误归因、请求预算、故障域、缓存容量、TTFT隔离和灰度发布分别具有独立的接口、测试、运维风险和回滚边界，并且存在明确依赖顺序。

## 未知项与风险

- 生产 24h/7d 状态码、类别、owner、attempt 去重和共享站点关联尚未完成，只能作为后续 child 的前置观测任务，不能直接调权重。
- 不同 provider 对 cache read/write 的 usage 字段、计费和语义不一致，必须先定义 provider capability 与 unknown 状态，未知时不能奖励缓存率。
- 多实例部署下进程内 TTFT、sticky 和临时阻断状态的一致性尚未解决，child 3/5 需要明确 Redis 共享或实例局部性的边界。
- 工具调用、Responses continuation 和非幂等请求不能因为网络失败就自动跨账号重放。

## Artifact 影响

- 创建 parent `meta.yaml`、`split.md` 和 research。
- 创建六个 child exploration stub；每个 child 后续由 `wiki-propose` 单独生成 proposal。
- 不创建 parent proposal、design、tests 或 implementation artifact。

## CodeGraph Evidence

- index: available, up-to-date on 2026-09-13
- analysis goal: 确认现有调度入口、失败反馈、并发、缓存接口和 failover 边界，决定 multi-change 的 ownership 与依赖。

### Queries

| tool | query | result |
| --- | --- | --- |
| `codegraph status` | 当前项目索引状态 | 3,908 files, 117,869 nodes, 420,515 edges；index up to date |
| `codegraph context` | gateway scheduling, upstream account selection, failover, TTFT, RPM, usage, cache tokens | 定位到 scheduler、GatewayCache、并发负载和现有服务边界；未将新逻辑绑定到具体实现 |
| `codegraph explore` | `openAIAccountScheduler SelectAccountWithScheduler selectByLoadBalance ReportResult TTFTGuard failover retry budget` | 确认高级调度、ReportResult、OpenAIAccountScheduleRequest、failover 和 TTFT 运行时状态 |

### Confirmed Facts

- `OpenAIGatewayService.SelectAccountWithScheduler` 是高级 OpenAI 调度入口，`defaultOpenAIAccountScheduler.Select` 负责候选决策。
- `ReportOpenAIAccountScheduleResult` 同时反馈 API Key health、TTFT Guard、模型临时状态和 scheduler 统计。
- `failover_loop.go` 管理同账号重试和账号切换，但没有统一 total-attempt ledger。
- `concurrency_service.go` 提供账号负载和等待队列读取；token admission 不是现有通用接口。
- `GatewayCache` 已抽象 session sticky 等缓存能力，新增分布式状态应通过接口而非在 handler 中直接依赖 Redis。

### Impact And Test Leads

- impact: `backend/internal/service` 为主，必要时触及 gateway handler、配置、Redis/cache adapter、dashboard 和管理端只读指标；不改认证、计费和协议公开契约。
- affected tests: `openai_account_scheduler_test.go`、`failover_loop_test.go`、`openai_ttft_guard_test.go`、transport/429/529/capacity shed tests、concurrency tests、usage/cache tests。

### Facts vs Inferences

- confirmed: 上述 CodeGraph 符号、调用入口和现有状态粒度。
- inferred: 用六个 child 替代一次性重写能降低回归和回滚风险；需在 proposal/design 阶段以测试和 shadow 指标验证。

### Unknowns And Fallback

- unavailable tool: SpecWiki CLI 未在当前 PowerShell PATH 中发现；本次先按仓库内 skill 规定创建结构化 exploration artifacts，strict validate 需在 CLI 可用环境补跑。
- fallback: 使用 `codegraph status/context/explore` 和定向 Wiki/生产部分报告读取。
- residual risk: 本次未修改运行时代码，未验证真实生产错误分布，也未证明任何新策略已启用。
