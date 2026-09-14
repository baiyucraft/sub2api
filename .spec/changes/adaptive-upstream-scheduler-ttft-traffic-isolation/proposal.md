# adaptive-upstream-scheduler-ttft-traffic-isolation

## 问题

当前上游账号调度已经有账号级负载、队列、错误率和 TTFT 信号，也有按账号与规范化模型运行的 TTFT Guard，但这些信号没有形成统一的质量分桶与流量隔离契约。短交互请求、长推理请求、工具续接、批处理、探针和 shadow 请求可能共享同一组并发与质量统计，导致以下问题：

- 不同 provider、模型、协议和流式模式的正常 TTFT 差异被混在一起，评分容易误惩罚或误奖励账号。
- 长推理或大上下文请求占满账号槽位后，交互请求排队；反过来，交互流量突发也可能饿死长任务。
- 业务请求质量统计、探针结果和 shadow 结果的语义不同，却可能影响同一套 Guard 或评分状态。
- TTFT 硬阻断与软评分缺少清晰边界；短时抖动可能造成过度摘除，持续劣化又可能没有及时降权。
- 进程内状态在多实例、蓝绿和重启期间不一致，恢复过程可能瞬间放量或误解除保护。

本 child 只定义可插拔的 TTFT/质量分桶与流量类型隔离能力，不重写现有账号选择器，不改变既有网关业务链路。

## 目标

- 按 `provider/model/protocol/stream/workload/context/reasoning` 建立稳定、可解释、可衰减的 TTFT 与质量分桶。
- 将 `interactive`、`long_reasoning`、`tool_continuation`、`batch`、`probe`、`shadow` 明确分类，并为不同类型提供独立的统计、容量和恢复边界。
- 将硬 Guard 与软评分分离：硬 Guard 只处理明确、连续且达到最小样本量的不可接受状态；普通 TTFT 劣化只作为软惩罚。
- 在不改变首 token、语义事件、usage、计费和 failover 既有兼容边界的前提下，为现有 scheduler 提供资格信号、评分信号和诊断信息。
- 考虑缓存率相关容量因素：把 cache-read、cache-write、未命中输入、上下文长度、最大输出和 reasoning 预算作为 workload/capacity 的输入，避免以单一 TTFT 或请求数比较不同成本的请求。
- 为进程内与共享状态设定清晰边界，支持逐步迁移到共享状态而不让 handler 依赖具体 Redis 实现。

## 非目标

- 不复制或替换 `openAI_account_scheduler`、failover loop 或现有 provider 路由选择器。
- 不改变 API Key 鉴权、模型准入、分组优先池、账号绑定、用户计费、usage 结算和协议公开行为。
- 不把探针成功直接当作业务成功，不用探针独立清除真实流量的 Guard 或故障状态。
- 不在本 child 实现统一错误归因、请求级 attempt budget、共享故障域熔断或 shadow 灰度；这些属于 parent 的其他 child。
- 不把缓存命中率作为无条件奖励，也不在 provider 缺少可靠 cache usage 时猜测命中；未知值必须保持 `unknown`。
- 不为 batch、probe 或 shadow 暴露新的对外 API，不记录请求正文、API Key、Token、Cookie 或完整上游错误。
- 不新增数据库迁移；共享状态的具体存储选型和生产启用属于后续设计/实施阶段。

## 成功标准

- 同一请求可稳定得到规范化的分桶键，至少包含 provider、canonical model、protocol、stream、workload、context bucket 和 reasoning bucket；缺失维度使用显式 `unknown`，不会借用不相干维度填充。
- `interactive`、`long_reasoning`、`tool_continuation`、`batch`、`probe`、`shadow` 的 TTFT、排队和容量统计互不污染；业务统计不会被 probe/shadow 样本改变。
- TTFT 统计具备最小样本量、时间衰减、异常值边界和 p50/p95/p99 或等价分位数输出；样本过少时只返回 `low_sample`，不触发硬摘除。
- 硬 Guard 只有在连续失败/超阈值样本、最小样本量和冷却条件同时满足时才阻止候选；软评分能反映 TTFT、排队趋势和质量劣化，但不会单独把账号永久判死。
- 长推理与交互请求具备可配置的并发保留、最大占比或隔离池语义；任一类型没有容量时的回退行为可解释且不会突破全局并发、RPM/TPM 或请求级预算。
- cache-read 命中、cache-write 创建、未命中输入、上下文长度、最大输出和 reasoning 预算进入容量/质量诊断；provider 不支持或数据不可信时不产生缓存奖励。
- 请求取消、首 token 后断流、工具续接和客户端取消不会被错误当成账号 TTFT 失败；已有 committed/usage/幂等边界保持不变。
- 进程内状态与共享状态的所有权、TTL、版本/generation、恢复和 fail-open 行为均有结构化诊断；状态不可用时不会静默清除硬保护。
- 相关单元测试、集成测试、类型/构建和差异检查通过；本 child 的文档可以独立 review、回滚和归档。

## 影响范围

- `backend/internal/service/openai_ttft_guard.go`：复用并扩展 TTFT 统计与 Guard 的抽象边界，硬 Guard 与软评分分离。
- `backend/internal/service/openai_account_scheduler.go`：消费分桶质量、工作负载隔离和容量信号，不复制候选选择逻辑。
- `backend/internal/service/account_quality.go`、`upstream_dashboard.go`：复用真实流量质量口径，补充分桶诊断和低样本语义。
- `backend/internal/service/concurrency_service.go` 及 gateway scheduling：承载按 workload 的容量/队列信号，保持现有全局并发与释放语义。
- `backend/internal/service` 的 usage/cache 适配边界：提供 cache-read/cache-write、上下文和输出预算的可信输入；不改变用户计费。
- Responses、Chat Completions、Messages 等现有 handler：只通过通用 scheduling context/outcome 接口提供或消费信号，保持流式提交和取消行为。
- 配置与管理诊断：增加内部可观测字段、默认关闭/影子模式和安全的统计聚合，不新增敏感输出。
- 测试：`openai_ttft_guard_test.go`、`openai_account_scheduler_test.go`、`account_quality`、并发/队列、usage/cache 和相关 gateway handler 测试。

## 交付形态

single-change

这是 parent `adaptive-upstream-scheduler` 的第 6 个 child，首先依赖前置 `adaptive-upstream-scheduler-fork-foundation`，并保留对请求预算、故障域健康和缓存容量 child 的依赖。整体为前置底座加原六个策略 child，共七个 child；本 child 只消费底座窄端口，前置不反向依赖策略合同。它交付 TTFT/质量分桶、workload 隔离、硬/软边界及冻结 Guard owner 的新接管，可独立测试和回滚；前置仅保持现有 legacy 实现，不提前承担 shared Report helper/guardDispatch。真实流量灰度与自动回滚由灰度发布 child（交付顺序 7）负责；新增策略 disabled 时仍执行当前 fork legacy 行为，不关闭原 TTFT 或健康保护。

## 风险

- 分桶维度过细会造成低样本和噪声，维度过粗又会把不同 provider 或工作负载混淆；必须限制 bucket cardinality 并提供未知/折叠策略。
- provider 的流式、reasoning 和 cache usage 语义不一致，错误映射可能使高成本请求获得错误容量或评分。
- workload 分类错误会造成错误隔离，尤其是工具续接、Responses continuation 和 long reasoning。
- 硬 Guard 阈值过低会摘除正常账号，阈值过高会放行持续劣化；必须要求最小样本、连续性和冷却退避。
- 多实例间共享状态延迟或版本冲突可能导致过度摘除或恢复放量；必须明确本地状态的可接受局部性。
- 引入按 workload 的容量池可能降低整体利用率；需要保底容量、受控借用和可观测的饿死率。
- TTFT 或 cache 信号接入评分后可能改变账号选择顺序；默认必须 shadow-only，并由后续 rollout child 决定是否启用。
- 统计和诊断若携带原始模型上下文、请求标签或错误正文，可能扩大敏感信息暴露面；只保留白名单字段和聚合结果。

## 参考资料

- 来源：parent `.spec/changes/adaptive-upstream-scheduler/research/scope-and-delivery.md`
  - 目标落点：现有调度入口、TTFT Guard、并发、缓存、failover 边界和 multi-change 依赖
  - 采用方式：direct migration of constraints
- 来源：`backend/internal/service/openai_ttft_guard.go` 与 `openai_account_scheduler.go`
  - 目标落点：现有 TTFT Guard、账号+模型状态和 scheduler 信号消费边界
  - 采用方式：rewrite behind existing service interfaces
- 来源：`backend/internal/service/account_quality.go`、`concurrency_service.go` 与 gateway scheduling
  - 目标落点：质量分位数、队列/并发测量和 workload capacity seam
  - 采用方式：direct migration of ownership, narrow extension
- 来源：Netflix `concurrency-limits`
  - 目标落点：以排队和延迟趋势调节容量，而不是只等待错误
  - 采用方式：inspiration only
- 来源：Envoy outlier detection、retry host predicate 与 weighted least request
  - 目标落点：最小样本、连续失败、恢复退避、候选排除和负载选择约束
  - 采用方式：inspiration only
- 来源：LiteLLM Router latency/cache-aware routing 研究
  - 目标落点：TTFT、RPM/TPM、cache usage 与 deployment capacity 的区分
  - 采用方式：inspiration only；不直接复制 provider 实现或固定重试策略
- 来源：child 独立 research/codegraph 资料
  - 目标落点：本 child 未提供独立资料，使用 parent research 和现有源码入口 fallback
  - 采用方式：fallback only，后续 implementation 前需重新执行定向 CodeGraph 与源码核验
