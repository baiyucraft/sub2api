# adaptive-upstream-scheduler-error-attribution 设计方案

## 方案概述

新增一个只负责“结果归因”的内部边界，将请求生命周期、单次上游 attempt 和最终/中间 outcome 分开建模。分类器只读取结构化 HTTP、传输、协议和请求上下文，输出稳定枚举、证据来源、责任 scope 和安全诊断摘要。

归因结果只进入新增的有界旁路观察，不替代现有健康反馈和调度反馈的输入。原路径按原参数、原次数处理真实响应和错误；本 child 不改变候选过滤、评分、重试、计费或公开响应。任何分类失败都回退到 `unknown`，它只表示新扩展不追加动作，不能省略或取消原有处理。后续 child 通过该事件消费归因，不在 handler 或各 provider adapter 中重复判断。

共同包、官方接入白名单、ID、CommitState 与版本合同以 [parent split](../adaptive-upstream-scheduler/split.md#官方兼容与共同合同) 为准。本 child 实现独立 `backend/internal/adaptivescheduler/` 内的公共事件/事实、纯分类器和归因视图；proposal 的 service 影响范围由 fork bridge 承担，不要求核心与官方服务同 package。本 child 调整为第 2 个交付，直接依赖 fork foundation；新增底座后合计七个 child，原六个策略 child 的 ID、相对顺序和既有策略依赖保留，不改变现有失败类型定义。

本设计不把缓存率纳入本 child 的决策。缓存命中、cache-read、cache-write 和上下文长度可作为后续容量 child 的独立输入；本 child 明确禁止把缓存字段缺失或缓存命中失败自动归因成账号/上游失败。

### 前置底座消费边界

- 依赖 [fork foundation 设计](../adaptive-upstream-scheduler-fork-foundation/design.md) 完成行为等价的 legacy 收口后，再接入本归因旁路。
- `backend/internal/forkscheduling/` 只放底座值对象和窄契约，旧状态及纯规则位于其 `legacy/` 子包；两者均不 import service、handler、repository、Ent、Gin、Redis 或 adaptivescheduler。只允许 `adaptivescheduler -> forkscheduling` 契约包，禁止引用 `forkscheduling/legacy` 具体实现或反向依赖。
- 本 child 只消费底座窄端口，不直接访问 `GlobalUpstreamHealthRegistry`、全局 recorder 或具体 legacy Service；只有底座兼容 bridge 可映射原类型并委托原 owner，不能把可变官方对象交给核心。
- 底座不定义 `Outcome`、`UsageFacts`、`mode`、`epoch`。公共事件/事实仍由本归因 child 按 parent 共同合同拥有和实现，包括 `SchedulingOutcomeEvent` 与计费改写前 `UsageFacts`；模式与版本规格继续由 parent 协调，不迁入底座，不由容量 child 重定义事实。
- 底座 default 为 legacy，保留旧健康、probe guard 和 global retry 行为，不以 Noop 全关；本 child 的 disabled/旁路失败只停新增观测。proposal 旧措辞不构成替换健康输入的授权，`unknown` 不跳过旧健康，不借归因修复来源覆盖或取消官方阻断。

## 接口与稳定合同

以下是 parent 共同对象的不可变归因视图，不是第二套请求/attempt/outcome SSOT；ID、提交状态和终态事件均使用同一来源。具体 Go 名称可按现有约定落地：

```text
GatewayRequestAttribution
  schema_version          string
  request_id             string
  mode                    disabled/shadow/enforced, frozen at request start
  control_epoch           monotonic control epoch
  policy_revision         immutable policy reference
  registry_revision       immutable registry reference
  raw_provider_identity   bounded provider/upstream/protocol identity
  protocol                enum
  endpoint_class          enum
  requested_model         string, normalized and bounded
  canonical_model         string, optional
  started_at              time
  deadline_at             time, optional
  client_state            active/cancelled/deadline_exceeded

GatewayAttemptAttribution
  request_id              string
  attempt_id              string
  attempt_index           int
  account_id              int64, optional
  upstream_config_id      int64, optional
  provider_family         registered namespace/unknown
  base_url_fingerprint    string, bounded fingerprint only
  protocol                enum
  started_at              time
  ended_at                time
  commit_state            parent CommitState
  send_ids                bounded references; transport sends have independent IDs
  first_token_observed    bool

GatewayOutcomeAttribution
  event_id                common event identity
  request_id              string
  attempt_id              string
  send_id                 string, optional; absent when coverage unavailable
  control_epoch           request's frozen epoch
  phase                   enum
  origin                  enum
  responsibility          enum
  scope                   enum
  http_status             int, optional
  provider_code           string, allowlisted and bounded
  error_kind              enum
  retry_after_ms          int64, optional and bounded
  transport_kind          enum
  protocol_event_kind     enum
  error_fingerprint       string, non-reversible digest
  confidence              enum
  selection_effect        enum
  retryability            advisory evidence only; never an execution command
  usage_facts_ref          immutable pre-billing facts, optional
```

稳定枚举至少包括：

- `origin`: `client`, `gateway_local`, `upstream_http`, `upstream_stream`, `transport`, `cancel`, `unknown`
- `responsibility`: `client_request`, `credential_account`, `model_capability`, `endpoint_protocol`, `shared_site_provider`, `transport`, `unknown`
- `scope`: `request`, `account`, `account_model`, `endpoint`, `provider_site`, `none`, `unknown`
- `phase`: `admission`, `connect`, `headers`, `first_token`, `stream`, `completion`, `cancel`, `unknown`
- `commit_state`: 使用 parent 的 `uncommitted`、`http_committed`、`heartbeat_only`、`semantic_committed`、`tool_continuation_committed`、`terminal_committed`；取消是独立 client_state，不能覆盖已有提交事实
- `confidence`: `explicit`, `structured`, `inferred`, `unknown`
- `selection_effect`: `none`、`observe_only`、`unknown`；本 child 无权返回调用旧健康/熔断/重试执行器的指令，后续 child 的策略输出独立于分类结果

字段约束：

- `request_id` 与 `attempt_id` 由共同生命周期 bridge 产生且不能携带凭据，分类器不自行重建。旧入口缺失时只由该 bridge 生成一次并标记来源；send_id 由真实 transport send 边界产生，未覆盖的旧入口标记不可用，不能猜测发送次数。
- `requested_model`、`canonical_model`、`provider_code` 和 `error_kind` 均需长度上限和字符约束；保留结构化值，不保存完整错误正文。
- `retry_after_ms` 只接受解析成功且位于合理上限内的值；无效值置空，不由文本猜测。
- `scope` 是责任影响范围，不是“本次实际是否已经熔断”的承诺。
- 未注入或 mode=disabled 时不生成新事件、不遍历候选、不创建 writer/队列/存储请求。显式启用旁路后 `selection_effect=observe_only`；即便其他 child 处于 enforced，本分类器也不执行副作用。
- raw_provider_identity 在官方兼容平台规范化之前提取；registry 未注册时保留 unknown，不能利用 NormalizeOpenAICompatiblePlatform 的默认 OpenAI 结果推导共享故障域。

兼容规则：

- 原调用方继续按原逻辑填充已有 `GatewayFailureStage`、`GatewayFailureScope` 和 `UpstreamFailoverError`；桥接只读取并映射，不为新分类增加这些官方类型的必需字段或覆盖其内容。
- 旧调用方只提供 HTTP 状态码时，通过保守映射生成归因；无法区分账号、模型或站点时使用 `unknown`。
- 无论新字段是否完整、分类是否成功，原账号健康/调度反馈都照常运行；不能只在分类异常时才恢复旧反馈，也不能把一次旧反馈与一次新归因各写一次真实健康状态。
- 不改变 API 响应 JSON、SSE 事件、usage 记录、计费扣减和现有管理端合同。

## Ownership 与数据/文件流

```text
gateway handler request context
  -> original forward / failover / health / scheduler / billing path
         |
         +--> fork bridge: immutable safe facts copy
                  -> pure classifier / attribution views
                  -> bounded new observation / downstream child input
```

Ownership：

- request attribution builder：fork bridge 使用 gateway/service 的真实生命周期创建共同 request 上下文，不让核心持有 Gin context 或官方 Account 指针。
- attempt attribution：由原账号转发边界的 bridge 开始、结束和递增 attempt_index；同一 attempt 内 send 单独计数。预算 child 启用时复用同一身份与账本接口，不能重建另一套 lifecycle。
- classifier：`backend/internal/adaptivescheduler/` 拥有公共事件/事实、纯分类与 versioned registry，通过底座值对象和窄端口接入 legacy；service/handler bridge 负责适配，核心不 import service、handler、repository、Ent、Gin、Redis 实现。
- existing health adapter：`upstream_health_evidence` 原写入经底座兼容 bridge 保留原调用，新归因只消费窄端口的旁路观察视图，不能再次触发真实状态写入。健康 child 后续消费独立证据，其状态执行职责不属于本 child。
- scheduler feedback adapter：`ReportOpenAIAccountScheduleResult` 的账号健康、模型瞬态清除、scheduler ReportResult 等调用不变；已有 fork TTFT 也保持原逻辑，待 TTFT 隔离 child 通过底座单一 Guard 端口接管其 fork 部分，而非跳过整个函数。
- metrics/dashboard projection：只保存聚合计数、延迟、枚举和指纹；不保存原始正文。

不允许的依赖：

- handler 不直接依赖 Redis、数据库表或前端模型来决定责任类别；其新增调用只通过 parent 白名单的 fork bridge。
- classifier 不调用 scheduler 选择账号，不执行 cooldown，不写 usage，不修改计费。
- dashboard 不反向成为调度状态源。

## 正常流程

1. 新旁路启用时，入口 bridge 从共同 SchedulingRequestContext 创建归因视图，保留官方规范化前的 provider 身份、协议、endpoint class、模型、冻结版本、deadline 和取消信号；disabled 不生成视图。
2. 原账号转发边界使用共同 attempt_id 创建归因视图，transport 边界产生 send_id；记录账号/上游安全 ID、provider/site 指纹和覆盖状态，不改原调用顺序。
3. 收到 headers、首 token、协议事件、完整响应或 transport error 时，按阶段生成一次或多次内部观测；attempt 结束时归并为唯一终态 outcome。
4. 分类器优先使用结构化证据：请求解析结果、HTTP 状态、Retry-After、provider code、协议事件、transport error 类型和客户端取消信号。
5. 只有证据足够明确时才落到账号、模型、endpoint 或站点责任；否则使用 `unknown`，并将 `selection_effect` 保持为 `observe_only`。
6. 原健康、调度和计费调用独立完成；安全事实副本经分类后只进入新增内部聚合或 shadow 输出。原调用不等候分类成功、不消费新 unknown 覆盖原错误，新事件的去重不能去重掉官方反馈。
7. request 结束时释放上下文，不读取请求体之外的额外敏感内容，不创建临时文件，不产生新的持久化记录。

错误映射原则：

下表的“处理”仅限定新归因消费者可追加的意见，不重定义官方旧路径。尤其是“不处罚/不重试”，表示本 child 不新增相应动作；并不抑制本方案接入前就存在的账号健康或重试逻辑。

| 证据 | 默认责任 | 允许的 scope | 处理 |
| --- | --- | --- | --- |
| 请求 schema、参数或模型输入明确非法 | `client_request` | `request` | 不处罚账号，不触发上游 failover |
| 明确 credential invalid/expired | `credential_account` | `account` | 保留旧账号反馈，后续 child 决定冷却 |
| 模型不存在或账号无该模型权限 | `model_capability` | `account_model` 或 `request` | 不把整个站点判为故障 |
| provider/endpoint 明确不支持协议或路径 | `endpoint_protocol` | `endpoint` | 不把同站点其他协议一起封禁 |
| 站点维护、网关过载或多个账号共享同一结构化站点错误 | `shared_site_provider` | `provider_site` | 本 child 只记录，不改变当前选号 |
| DNS、connect、TLS、read timeout、reset 等 | `transport` | `endpoint` 或 `provider_site` | 依赖错误证据，未知时不处罚账号 |
| 客户端取消或 deadline 已由客户端触发 | `cancel`/`unknown` | `none` | 不计作账号失败，不重放请求 |
| HTTP 200 后协议流内 `failed` | 按结构化 failure code | `account_model`/`endpoint`/`provider_site`/`unknown` | 必须保留 `upstream_stream` origin |

状态码只作为证据之一：`401/403`、`429/529` 和 `5xx` 不允许单独决定最终责任；需要结合 provider code、Retry-After、共享故障证据和请求阶段。尤其不能把所有 `502` 直接归为账号失效，也不能把所有 `429` 直接归为 Key 失效。

## 失败、边界与回滚

- 无效输入：字段缺失、状态码非法、provider code 超长、Retry-After 无法解析时，保留可用字段并将相关枚举置为 `unknown`；不因分类失败阻断请求。
- 重复/并发：共同 attempt_id 的新增终态只提交一次，send 子事件按 send_id 独立去重；重复新增终态只增加有界计数。不得用旁路去重表改变原健康函数的调用次数。
- 部分失败：若 headers 已提交但流内分类失败，仍结束 attempt 并输出最保守 outcome；不得为了补齐归因而重读请求体或重放上游请求。
- 客户端取消：停止新归因计算和排队，允许幂等的最小取消终态；本 child 不额外发送账号失败反馈，原协议取消与资源释放路径照常执行。
- 首 token/语义提交：commit_state 必须由协议 owner 设置；分类器不能根据耗时猜测提交状态。语义提交后失败只记录新事件，不改变原 failover 或已提交响应。
- 用户内容：禁止保存请求正文、响应正文、Authorization、Cookie、API Key、OAuth token、完整 provider 错误文本；错误指纹必须不可逆且不能由原文直接还原。
- path safety：本 change 不新增文件输出路径。若后续 adapter 写入调试聚合，只能通过现有固定安全输出边界，不允许错误文本参与路径拼接。
- 兼容回滚：普通停用由新 control_epoch 影响新请求，已开启的旁路按冻结 registry_revision 有界结束或丢弃非必要观测；旧 GatewayFailureStage、健康反馈和调度反馈始终运行，不需要“恢复”此前被替代的函数。旧 outcome 可标记历史版本，不能改变新 epoch 的健康/放量控制面。
- 分类器异常：panic、超时或版本不兼容由边界捕获，返回 `unknown` 并记录有界内部计数；不得让观测错误变成网关 5xx。

## 验证设计

- classifier seam：给定结构化 request/attempt/outcome 输入，验证责任枚举、scope、origin、phase、confidence 和 `selection_effect` 的确定性输出。
- lifecycle seam：验证 request/attempt/send 关联、一次 attempt 终态与独立 send 计数，取消和 deadline 不由新分类器额外产生账号失败反馈。
- protocol seam：覆盖非流式、SSE、Responses 流内 failure、首 token 前失败、首 token 后失败和 HTTP 200 协议错误。
- transport seam：覆盖 DNS/connect/TLS/read timeout/reset，并确认不把所有传输错误归为 credential/account。
- status/code seam：覆盖 400、401、403、408、429、500、502、503、504、529，并验证只有结构化证据足够时才提升责任粒度。
- compatibility seam：以接入前当前 fork 为基线，比对 GatewayFailureStage/GatewayFailureScope/UpstreamFailoverError 以及 RecordUpstreamTrafficFailure、ReportOpenAIAccountScheduleResult 的原参数、次数和状态变化；分类成功、unknown、失败和 disabled/shadow 均保持一致。
- privacy seam：验证敏感头、请求正文、响应正文和完整 provider 错误不会出现在事件、日志和聚合输出中。
- no-selection-change seam：对同一候选集回放旧路径与新旁路，验证选中账号、账号顺序、重试次数和公开响应完全一致。
- ownership seam：核心 import 检查、parent 官方调用点白名单、无新健康写入，以及原模型瞬态清除、API Key 健康恢复和 fork TTFT 反馈未被跳过。
- foundation seam：验证公共事件/UsageFacts 的归因 owner、单向包依赖和底座窄端口调用；default legacy 与新增旁路 disabled 均不以 Noop 关闭旧反馈或绕过官方阻断。
- facts seam：验证 raw_provider_identity 不被官方默认 OpenAI 规范化污染，UsageFacts 在 ForceCacheBilling/TTL 改写前复制；未知字段不推断 cache 命中或额外健康处罚。
- operational seam：分类器故障、未知字段、重复终态和旁路关闭均应 fail-open 到旧路径。
- 推荐验证入口：`go test ./backend/internal/service`、相关 handler/failover 测试、`go test ./...`、`git diff --check`；不得使用生产写入作为验收证据。

## Wiki 与长期合同落点

- `.wiki/` 长期页面：后续实现完成并 review 通过后，新增“上游调度/错误归因合同”页面，记录稳定枚举、责任 scope、脱敏规则和兼容边界。
- 本 change 的 `.spec/changes/.../proposal.md` 与 `design.md`：只作为本次 change 的提案和设计依据，不作为运行时配置源。
- 后续 child：attempt budget 消费共同 request_id/attempt_id/send_id/commit_state；fault-domain health 消费 responsibility/scope/error_kind；shadow rollout 消费聚合结果。后续 child 不得重新解析原始 provider 文本，不重复生产公共终态。

## 参考边界

- 来源：`.spec/changes/adaptive-upstream-scheduler/research/scope-and-delivery.md`
  - 目标落点：本设计的入口、依赖和已知生产证据边界
  - 采用方式：direct project evidence
- 来源：`backend/internal/service/gateway_service.go`
  - 目标落点：旧失败阶段、scope 和 failover 错误兼容
  - 采用方式：direct compatibility contract
- 来源：`backend/internal/service/upstream_health_evidence.go`
  - 目标落点：保留账号健康写入，fork bridge 旁路复制安全证据
  - 采用方式：additive observation; no replacement of legacy feedback
- 来源：Envoy outlier detection / retry host predicate
  - 目标落点：错误来源和故障层级的分离
  - 采用方式：inspiration only
- 来源：Portkey Gateway retry handler
  - 目标落点：Retry-After 和错误来源作为后续预算证据
  - 采用方式：inspiration only

## 回滚

关闭归因旁路后，原状态码映射、健康反馈和调度反馈无需重建或重新执行。控制面使用新 control_epoch 引用旧 policy/registry 内容，旧请求的观察不能写新控制面；不回退 epoch 数字，不中断原 billing/usage/资源清理。回滚只停用新增事件生产/消费，不删除历史数据，不需要数据迁移或线上配置清理。

## CodeGraph-derived design constraints

- entry points and call paths: 2026-09-13 定向查询 ReportOpenAIAccountScheduleResult 的 impact(depth=1)、callers、callees；callees 确认账号健康 success/failure、reportOpenAITTFTGuard、模型瞬态清除和 scheduler ReportResult。当前源码 2842-2877 行核实上述均为真实副作用，不能替换为只执行新分类。ReportUpstreamTrafficFailure/RecordUpstreamTrafficFailure 的 fork 入口也只做旁路接入。
- ownership and dependency boundaries: 公共事件/事实与核心分类由本 child 在 backend/internal/adaptivescheduler 拥有，legacy 值对象/窄契约/状态纯规则由前置底座的 backend/internal/forkscheduling 拥有；本 child 经窄端口消费，兼容 bridge 映射原类型，官方反馈、协议与计费仍拥有原执行职责。本 child 不扩张官方失败模型、不调用 scheduler 或 cooldown。统一归因先提供可消费事实，并不意味着先替换官方反馈算法。
- impact radius: parent 白名单内的结果/生命周期薄接入、fork bridge、独立核心和其测试；不改 GatewayFailureStage/Scope、UpstreamFailoverError、HTTPUpstream、ConcurrencyCache、UsageBillingCommand 的字段合同。
- affected tests: 定向 impact 返回 19 个节点/17 条边，包含模型瞬态恢复、scheduler metrics/default wrappers、TTFT disabled/绑定/fail-open 等测试；补以独立 classifier/bridge 兼容验证。图返回范围不等于所有运行时协议调用覆盖，不据此宣称全 provider 已验证。
- rollback boundary: 新 control_epoch 停用旁路；旧官方/fork 反馈从未被替换，不重新上报健康或重置账本；无数据库或 usage 回滚。
- graph evidence vs source verification: 以官方 98d86915becae9fe9491a91ffc6defd5235c8d2b 和 fork 9449571f7e6b03d93c29775ba9cf9d1e892dd2c5 的已核实归属为准。图曾有陈旧/未完成的全图查询，本次使用低深度定向结果并核实当前源码，不重建索引、不把没有返回的动态调用视为已覆盖。
- unresolved items: 生产错误分布和各 provider 的结构化字段仍需独立脱敏证据；未知/未覆盖只记录 unknown/unavailable，不改变官方响应或扩大新处罚。具体 fixture 和调用点测试留给 wiki-plan，不在本阶段生成用例或代码。
- extensibility boundary: 新 provider/protocol/reason 通过 registry 与 bridge 扩展；保持公共事件、大类和 ID 稳定，不把平台 switch 或具体 SDK 类型塞入核心。

## 独立 CodeGraph research fallback

本 child 没有独立 research/codegraph.md，沿用 parent research，并将本次实际返回的 impact/callers/callees 与源码核验结论记录在上一节。CLI 没有可直接替代全部动态流程的完整 trace 证据，因此从调用关系逐段核实官方/fork owner；实现阶段仍需对最终接入函数做影响与兼容检查，不能仅复用历史图行号。此次修订没有生成新的系统测试、任务、源码或索引。

## 可扩展性设计

错误分类不是封闭枚举。系统将“稳定责任大类”与“可扩展原因”分离：大类用于跨 provider 的健康、重试和看板合同，原因码用于表达 provider、协议或业务错误的具体证据。新增原因不得迫使现有调用方理解新值，也不得通过修改旧事件字段来承载新语义。

### 稳定大类与 versioned reason code

- 稳定大类继续使用 `client_request`、`credential_account`、`model_capability`、`endpoint_protocol`、`shared_site_provider`、`transport`、`unknown`。这些值是跨 provider 的决策边界，不应随某个供应商的错误码变化而重命名。
- 具体原因使用可注册的 `reason_code`，格式至少包含命名空间与版本，例如 `newapi.auth.invalid_credential.v1`、`openai.responses.stream_failed.v1`、`transport.read_timeout.v1`。版本升级表示语义或证据合同发生变化，不复用旧 code 表示新含义。
- `reason_code` 不替代稳定大类；分类结果必须同时包含 `responsibility`、`reason_code`、`confidence` 和 `scope`。调用方只依赖大类和能力声明，能够理解的 adapter 才消费具体原因码。
- reason registry 必须拒绝重复注册、空命名空间、非法版本和不受支持的 scope；注册失败不影响旧 registry 和旧事件生产。

### Evidence map

每个原因码由 registry 声明所需和可选证据，统一放入有界的 `evidence` map，而不是不断向核心事件追加 provider 专属字段：

```text
evidence:
  http_status: 429
  provider_code: "rate_limit_exceeded"
  retry_after_ms: 2000
  error_fingerprint: "sha256:..."
  source: "structured_provider_error"
```

- evidence key 采用稳定的通用键名；provider 专属键必须带命名空间，例如 `provider.newapi.quota_scope`，并受长度、数量和字符集限制。
- evidence 只保存脱敏后的结构化值、布尔值、有限枚举、数值和不可逆指纹；禁止放入请求正文、响应正文、Authorization、Cookie、API Key、Token 或完整错误文本。
- registry 可声明证据之间的最小条件、冲突规则和 confidence 上限。缺少必需证据时只能回退到较宽的大类或 `unknown`，不能凭 provider 名称或模糊文本补全。
- evidence map 对旧消费者是可选的；旧消费者忽略未知 key，新消费者通过 registry 版本解释 key，避免把 map 变成无约束的日志容器。

### Adapter 与 capability registry

平台、provider、上游类型和协议差异通过 adapter/capability registry 注入：

- adapter 负责把 provider 的结构化响应、错误码和流内事件转换为通用证据；它不决定账号是否冷却、不调用 scheduler，也不直接执行重试。
- capability registry 声明 provider 支持的协议、结构化错误字段、Retry-After 语义、流内 failure 事件、共享站点标识方式和可用 reason code 版本。
- 核心 classifier 只依赖通用接口，例如 `ResolveCapabilities(provider_family, protocol)`、`Classify(structured evidence)` 和 `RegisterReason(reason definition)`；禁止出现按 provider 分支的 `switch` 或在核心路径硬编码供应商错误码。
- 未注册 provider 或 capability 缺失时，使用通用 HTTP/transport 适配器并回退到 `unknown`；不得因为未知 provider 阻断网关请求。
- registry 是构造时的只读快照；运行时更新原子发布新 registry_revision，并在新 control_epoch 激活。旧请求使用其冻结 revision，回滚用新 epoch 引用旧内容，不回退 epoch；事件和异步观察携带原版本，不能改变新控制面。

### 事件向后兼容与新增类型

- 新增错误类型只新增 registry 定义、reason code 或 evidence key，不修改既有 `request_id`、`attempt_id`、`responsibility`、`scope`、`origin`、`phase` 等旧事件字段的含义。
- 需要表达新维度时，优先增加可选扩展字段或版本化扩展对象；旧序列化器、旧指标消费者和旧健康 adapter 必须在字段缺失时保持原行为。
- 不允许把新错误类型编码成旧错误类型的别名来绕过兼容检查；如果旧消费者无法区分，应保留稳定大类并把具体原因放入 `reason_code` 与 `evidence`。
- 事件 schema 版本只在字段语义或必填约束变化时递增；增加可选字段不要求旧消费者升级。

### Unknown fallback 与保守动作

- 对未知 provider、未知 reason code、证据冲突、registry 版本不兼容或 classifier 异常，统一输出 `responsibility=unknown`、`reason_code=unknown.unclassified.v1`（或等价的稳定 unknown code）并保留可观察的安全 evidence。
- unknown 不触发新扩展的账号处罚、自动重试、冷却或选号动作；原健康/重试处理仍按原始输入执行，不能把 unknown 当成跳过官方逻辑的理由。后续 fault-domain 或 retry child 只能依据已验证的独立证据给出受约束意见。
- unknown 仍应可观测：保留协议、阶段、状态码、原始 provider/site 安全指纹、错误指纹、control_epoch/registry_revision、证据来源和 bounded reason summary；未知 provider 不归入已知平台共享状态，不暴露原始敏感内容。
- unknown 计数、原因码未注册计数、registry 冲突计数和 adapter fallback 计数进入聚合指标，便于新增 provider 或错误类型后补充 adapter，而不需要回放敏感日志。

### 扩展边界的验证与回滚

- registry seam：验证新增 adapter/reason code 可以注册、查询、版本共存和原子替换；重复或非法注册不会污染旧快照。
- classifier seam：验证核心 classifier 在没有 provider switch 的情况下，通过 capability 注入得到相同的大类合同，并对未知输入回退到 unknown。
- compatibility seam：验证旧事件消费者忽略新可选字段，旧 reason code 语义不变；新增字段成功、缺失或 unknown 时，旧健康和 scheduler 调用均保持原行为，而非只在失败 fallback 时保持。
- privacy seam：验证 evidence map 的 key/value 白名单、长度上限、命名空间和敏感字段拒绝规则。
- conservative-action seam：验证未知错误不追加新处罚/重试/选号动作，但仍产生有界诊断；同时核对原健康反馈未被压掉或重复执行。
- rollback：通过新 control_epoch 选择旧 registry_revision 或通用 adapter，原反馈路径持续运行；不删除历史事件、usage、健康记录或计费数据，不允许旧异步观察触发再次放量。
