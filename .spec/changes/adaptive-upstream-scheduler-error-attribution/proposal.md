# adaptive-upstream-scheduler-error-attribution

## 问题

当前网关已经存在 `GatewayFailureStage`、`GatewayFailureScope`、账号健康反馈、TTFT 反馈和 failover 错误，但这些信息由不同调用路径分别生成，错误责任并不稳定地落到同一层级。相同的上游异常可能被当作账号问题、模型问题、endpoint 问题或普通请求失败，导致后续冷却、重试和质量统计缺少一致依据。

本 change 依赖前置 fork foundation，通过底座窄端口定义并旁路接入统一的 request/attempt/outcome 错误归因事件及公共事实，供后续重试预算、故障域健康和 shadow 调度 child 使用。公共事件与计费改写前的 UsageFacts 仍由本归因 child 拥有，不迁入底座。真实选号、优先池、重试次数、熔断阈值和用户计费行为在本 change 中保持不变。

## 目标

- 定义稳定的 request、attempt、outcome 三层内部事件及其字段边界。
- 将错误责任统一归类为：
  - `client_request`
  - `credential_account`
  - `model_capability`
  - `endpoint_protocol`
  - `shared_site_provider`
  - `transport`
  - `unknown`
- 区分上游返回错误、网关本地错误、传输失败、客户端取消和协议流内失败。
- 保留 HTTP 状态码、provider code、Retry-After、失败阶段和响应提交状态等可用于后续决策的安全证据。
- 通过底座兼容 bridge 旁路复制现有账号健康和调度反馈的安全事实，不替换原输入，保持原调用参数、次数、状态变化和实际账号选择结果。
- 对无法安全归因的结果采用 `unknown` 和不追加新处罚语义，不根据错误文本猜测账号失效；不能因此跳过或取消旧健康、阻断及重试处理。

## 非目标

- 不修改真实选号排序、评分权重、优先账号池或会话粘性。
- 不在本 change 中新增统一重试预算、故障域熔断、TPM/cache admission、TTFT 分桶或灰度开关。
- 不改变公开 API 的错误格式、HTTP 状态码、SSE 事件或客户端可见语义。
- 不新增数据库迁移，不持久化 API Key、Token、Cookie、请求正文或完整 provider 错误原文。
- 不根据模型名称、分组名称或供应商名称直接猜测错误责任。
- 不把 cache hit/miss、缓存写入失败或缓存率变化单独视为上游失败；缓存容量由后续 child 负责。
- 不执行生产配置写入、VM Gate、部署或线上调度策略切换。

## 成功标准

- 同一请求的多个 attempt 能通过稳定 `request_id` 关联，并且每个 attempt 只有一个终态 outcome。
- 结果能够区分客户端请求错误、账号/凭据错误、模型能力错误、endpoint/协议错误、共享站点错误、传输错误和未知错误；无法确认时新扩展不追加账号处罚，原反馈仍按原输入执行。
- `401/403`、`429/529`、`5xx`、超时、连接重置、客户端取消、HTTP 200 后的协议流内失败均有可测试的归因路径；状态码本身不足以判断责任时保留 `unknown` 或更高层候选。
- 现有 `RecordUpstreamTrafficFailure`、`ReportOpenAIAccountScheduleResult` 和 failover 反馈仍可运行，现有调度选择结果在回归测试中保持一致。
- 归因事件只保留字段白名单、截断后的 provider code/category 和错误指纹，不包含明文凭据或请求正文。
- 归因模块不可用、字段缺失或解析失败时，调用方继续使用旧兼容路径；不会阻断请求、计费、usage 写入或上游响应。
- 定向单测、服务层回归测试、完整 Go 测试和 `git diff --check` 均通过；验证报告能够展示事件字段、责任类别、兼容回退和选号不变证据。

## 影响范围

- `backend/internal/service`：新增或扩展内部归因模型、分类器和结果适配边界；复用现有健康与调度反馈入口。
- `backend/internal/handler`：在请求生命周期和上游 attempt 边界提供必要的上下文，不改变 handler 的公开响应行为。
- `backend/internal/service/gateway_service.go`：兼容已有 `GatewayFailureStage`、`GatewayFailureScope` 和 `UpstreamFailoverError`，不替换公开或跨层既有合同。
- `backend/internal/service/upstream_health_evidence.go`：通过底座兼容 bridge 旁路复制安全健康证据，统一归因不替换或去重原反馈；未知责任不触发新处罚，也不静默取消官方阻断。
- `backend/internal/service` 测试：覆盖分类、生命周期、取消、协议流内失败、兼容回退和无选号回归。
- 观测输出：仅增加内部聚合/调试字段，不开放原始错误文本和敏感请求信息。

## 交付形态

single-change

这是 parent `adaptive-upstream-scheduler` 的第 2 个 child，依赖 `adaptive-upstream-scheduler-fork-foundation`。新增底座在先，原六个策略 child 的 ID 和相对顺序不变，合计七个 child；本归因 child 仍先于重试预算、故障域健康、缓存容量、TTFT 隔离和灰度发布。它只交付公共事件/事实、统一错误归因合同和旁路兼容接入，可以单独 review、验证和回滚；后续 child 消费本 change 的稳定事件，不另建分类器。前置底座默认执行 legacy，不以 Noop 关闭现有 fork 功能；本 proposal 不授权把旧健康改为由新归因替代处理。

## 风险

- 错误分类过于激进会把共享站点或客户端问题错误处罚到账号，造成可用容量下降；默认必须 fail-safe 到 `unknown`。
- handler、failover 和 streaming 路径存在多个 attempt 边界，漏记或重复记账会影响后续 retry amplification 统计。
- HTTP 200 后的流内失败、首 token 前后失败和客户端取消的语义不同，若只看 HTTP 状态码会产生错误反馈。
- provider 错误文本格式不稳定，直接保存或依赖全文匹配会带来隐私和兼容风险。
- 多实例运行时若只依赖进程内状态，后续 child 可能观察到不一致；本 change 只定义事件，不承诺分布式状态同步。

## 参考资料

- 父级调研：`.spec/changes/adaptive-upstream-scheduler/research/scope-and-delivery.md`
  - 目标落点：本 child 的问题边界、依赖顺序、现有入口和生产证据限制
  - 采用方式：rewrite of internal contract, with existing symbols retained for compatibility
- `backend/internal/service/gateway_service.go`
  - 目标落点：复用 `GatewayFailureStage`、`GatewayFailureScope` 和 `UpstreamFailoverError`
  - 采用方式：direct compatibility boundary, not replacement
- `backend/internal/service/upstream_health_evidence.go`
  - 目标落点：保留账号健康原反馈，通过底座窄端口旁路复制安全事实
  - 采用方式：additive observation through compatibility bridge; no replacement of legacy feedback
- `backend/internal/service/openai_account_scheduler.go`
  - 目标落点：保持 `ReportOpenAIAccountScheduleResult` 的真实选号和反馈兼容
  - 采用方式：inspiration and additive adapter only
- Envoy outlier detection / retry host predicate
  - 目标落点：本地错误、上游错误、连续失败和已尝试 host 的分层思想
  - 采用方式：inspiration only
- Portkey Gateway retry handler
  - 目标落点：保留 Retry-After 和失败来源作为后续预算输入
  - 采用方式：inspiration only
