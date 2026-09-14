# 自适应上游账号调度 multi-change

## Parent 边界

本 parent 只负责协调一套便于合并 Sub2API 官方代码的调度增强：先收口现有 fork 调度扩展接口，再统一采集调度信号、形成可解释的候选决策、控制重试放大，并以 shadow 和灰度方式验证收益。现有 fork 的健康、TTFT、共享并发和优先池允许相互复用，但后续自适应策略只能经底座窄契约及兼容 bridge 接入，不直接读取全局状态或具体旧 Service。parent 不进入 design/implementation 阶段，不创建 parent design，不直接修改最终 Wiki 页面；本 split 统一七个 child 的边界和依赖合同。

2026-09-13 修订以官方 `98d86915becae9fe9491a91ffc6defd5235c8d2b`、fork `9449571f7e6b03d93c29775ba9cf9d1e892dd2c5` 的源码审计为基线。目标是独立策略核心与少量受约束的官方接入点，不承诺官方源码零改动或未来合并零冲突。历史 research 中的建议落点以本次已核实的合同为准；生产收益、错误分布和新策略效果仍未验证。

```text
Fork 调度扩展兼容底座（全部后续 child 的前置）
        |
错误归因与统一结果事件
        |
        +--> 请求级 attempt budget
        |          |
        |          +--> 故障域健康与分布式状态
        |
        +--> 缓存率感知容量准入
                   |
                   +--> TTFT 与流量类型隔离
                                |
                                +--> shadow / 灰度 / 自动回滚
```

## Child 拆分

| 顺序 | Child | 独立验收边界 | 依赖 |
| --- | --- | --- | --- |
| 1 | [Fork 兼容底座](../adaptive-upstream-scheduler-fork-foundation/design.md) | 现有 fork 能力接口化、状态与官方接入收口；选号及副作用轨迹与当前 fork 等价，不新增策略 | 无 |
| 2 | [错误归因](../adaptive-upstream-scheduler-error-attribution/design.md) | 同一上游结果能稳定归类为请求、凭据、模型、账号、endpoint、站点或客户端责任；不改变实际选号 | 1 |
| 3 | [请求预算](../adaptive-upstream-scheduler-attempt-budget/design.md) | 单请求所有同账号重试、换号和等待共享一个预算；流式提交边界可解释 | 1、2 |
| 4 | [故障域健康](../adaptive-upstream-scheduler-fault-domain-health/design.md) | 同一共享站点或 endpoint 故障可以跨账号聚合摘除，恢复有 half-open 和冷却退避 | 1、2、3 |
| 5 | [缓存感知容量](../adaptive-upstream-scheduler-cache-aware-capacity/design.md) | 以输入缓存命中预估、缓存写入代价、上下文和输出预算进行容量准入；容量预留幂等结算与释放，不涉及用户退款 | 1、2 |
| 6 | [TTFT 与流量隔离](../adaptive-upstream-scheduler-ttft-traffic-isolation/design.md) | TTFT 按模型/协议/流量类型分桶，长推理与交互请求互不饿死，评分与硬熔断分离 | 1、3、4、5 |
| 7 | [Shadow 与灰度](../adaptive-upstream-scheduler-shadow-rollout/design.md) | 新旧策略可并行计算并比较，支持按组灰度、指标门禁和自动回滚 | 1-6 |

原六个 child ID 不变，仅顺序后移并显式依赖 fork-foundation；它们之间的依赖保持。底座不依赖后续事件、预算、版本或自适应运行时，不能形成反向依赖。表中序号仅供导航，跨文档合同优先以 child ID/功能名称引用。

### 1. adaptive-upstream-scheduler-fork-foundation

- 归档状态：[x] archived
- 独立边界：现有 fork 调度能力的窄契约、legacy 规则和 service bridge；不新增自适应策略。

### 2. adaptive-upstream-scheduler-error-attribution

- 归档状态：[ ] pending
- 独立边界：统一上游结果归因，不改变实际选号。

### 3. adaptive-upstream-scheduler-attempt-budget

- 归档状态：[ ] pending
- 独立边界：请求级重试、换号和等待预算。

### 4. adaptive-upstream-scheduler-fault-domain-health

- 归档状态：[ ] pending
- 独立边界：故障域聚合、摘除、half-open 和恢复冷却。

### 5. adaptive-upstream-scheduler-cache-aware-capacity

- 归档状态：[ ] pending
- 独立边界：缓存感知容量准入、预留、结算和释放。

### 6. adaptive-upstream-scheduler-ttft-traffic-isolation

- 归档状态：[ ] pending
- 独立边界：按流量类型隔离 TTFT 和容量，保持硬熔断与评分分离。

### 7. adaptive-upstream-scheduler-shadow-rollout

- 归档状态：[ ] pending
- 独立边界：新旧策略旁路比较、分组灰度、指标门禁和自动回滚。

## 共同约束

- 不复制现有账号选择器；新增能力通过 fork 桥接向现有 scheduler 提供资格、健康、容量和评分信号，不向官方公共类型叠加各 child 的必需字段。
- 不改变 API Key 鉴权、用户计费、分组优先池、账号绑定和协议转换的业务语义。
- 不把用户计费 quota、上游 RPM/TPM、并发槽位或缓存 token 余额混为一个指标。
- 不保存明文 API Key、Token、Cookie、请求正文或完整上游错误；仅保留结构化类别、脱敏指纹、计数和有限模型标签。
- 所有新策略默认 `disabled`；显式启用 `shadow` 只计算旁路意见，未满足覆盖与验证合同不得进入 `enforced`。
- 首字前允许受控 failover；首个语义事件后禁止透明重放，工具调用和 `previous_response_id` 必须遵守幂等边界。

## 推荐下一步

七个 child 均提供 proposal/design，本轮新增底座并同步原六份文档，不生成 tasks、ST/UT 用例或实现。设计确认后最早进入 `wiki-plan` 的是 fork-foundation；底座先独立证明旧行为等价，错误归因再建立共同事件。请求预算/缓存容量只有在共同事件稳定后才可独立推进，TTFT 流量隔离必须复用缓存容量账本而非新建。文档并行评审不代表允许并行接管官方状态机。

## 跨 Child 集成验收

在 shadow-rollout 实际灰度之前必须完成一组离线回放/受控验证场景；旁路验证不得改变真实选号：

- 长上下文高 cache-read 命中、首次 cache-write、完全未命中三类请求混合运行，检查容量预留、差额释放、独立成本估计和 cache 预测误差；ForceCacheBilling、售价和倍率不改变真实 cache 事实。
- 429 带 Retry-After、共享站点持续 502、单账号 401、模型级 400、代理连接失败分别注入，检查责任层级、冷却范围、重试等待和已尝试账号排除。
- HTTP 200 后无语义事件、首 token 后断流、工具调用后断流、客户端取消分别验证，检查是否重复输出、重复工具执行、重复计费和槽位泄漏。
- 双实例及蓝绿切换验证 Redis/本地状态边界，确保不会因进程内状态清空而瞬间放量或解除共享故障域熔断。

集成验收同时核对：

```text
最终路由
  + 尝试次数
  + 归因类别
  + 账号/站点冷却
  + TTFT/排队
  + usage/成本
  + RPM/TPM/并发释放
```

## 后六个策略 Child 的共同契约

以下自适应合同由错误归因及后续策略 child 建立，fork-foundation 不提前定义或消费它们。底座只拥有既有能力的安全值对象、窄接口及 legacy 实现。

- `SchedulingRequestContext`：schema_version、request_id、group/model/protocol、原始 provider 身份摘要、workload、deadline、重放能力、请求冻结的 mode/control_epoch/policy_revision/registry_revision。
- `SchedulingAttemptContext`：request_id、attempt_id、attempt_index、账号/目标的安全引用、已取得槽位引用和容量引用；每次网络发送另有 send_id，不用账号切换次数代替发送次数。
- `SchedulingOutcomeEvent`：event_id、request_id/attempt_id、可选 send_id、stage、commit_state、client_state、status、归因、scope、故障域、UsageFacts 引用、retryability；一个 attempt 只有一个终态，流内观测不是第二个终态。
- `UsageFacts`：计费改写前的真实 token/cache 分项、字段存在性、input 是否包含 cache-read、完整性、来源及 capability revision；不包含价格或可变官方对象指针。
- `CapacityReservation`：预计输入、cache-read、cache-write、最大输出、reasoning、预留量、实际结算量、差额释放状态及创建时能力版本；只属于上游容量，不是用户余额。
- `SchedulerDecisionTrace`：快照 ID/覆盖范围、候选数、淘汰原因、故障域、分数组成、选择结果、剩余预算和预测置信度；只保留安全 ID/指纹。

上述类型由同一个 adaptivescheduler 核心包拥有；error-attribution 的 GatewayRequest/Attempt/OutcomeAttribution 是共同对象的归因视图，不再生成一套 ID、提交状态或生命周期。后续 design 只细化自己的字段和端口，不重新定义公共语义。底座的旧反馈参数不构成第二套 outcome。这些合同不新增公开 API，也不要求 handler 直接依赖 Redis 或具体 provider 实现。

## 官方兼容与共同合同

### 包与依赖边界

以下是后续实现的受约束落点，本次没有创建任何产品代码文件：

| 层 | 落点 | 允许依赖与职责 |
| --- | --- | --- |
| Fork 底座契约 | `backend/internal/forkscheduling/` | 仅现有能力值对象与窄契约；不得 import legacy 实现、service、handler、repository、Ent、Gin、Redis 或 adaptivescheduler；不定义新事件/预算/版本控制 |
| Fork 旧实现 | `backend/internal/forkscheduling/legacy/` | 可机械迁移的旧状态和纯规则，实现底座契约；不得 import 官方业务层、存储实现或 adaptivescheduler；只由装配及兼容适配引用 |
| 策略核心 | `backend/internal/adaptivescheduler/` | 值对象、分类/预算/健康/容量/质量/灰度策略、注册表及抽象 store/clock/observer；可依赖 forkscheduling 的值对象/契约，不依赖其具体 legacy 实现或官方 service、handler、repository、Ent、Gin、Redis |
| 底座桥接 | `backend/internal/service/fork_scheduling_bridge*.go`、对应 handler/repository 适配 | 保留旧公共入口并映射安全值，组装同一个 legacy runtime；全局兼容入口受白名单限制，不让新调用方绕过窄契约 |
| 服务桥接 | `backend/internal/service/adaptive_scheduler_bridge.go` 及职责明确的同前缀文件 | 把 Account/ForwardResult/官方错误映射成有界值；实现候选、usage、传输与旧反馈适配；不得复制官方选择器或价格计算 |
| 协议桥接 | `backend/internal/handler/adaptive_scheduler_bridge.go` | 从原请求生命周期传递上下文、真实 commit/cancel，限制旧 failover 动作；公开 writer/协议仍由原 handler 拥有 |
| 状态适配 | `backend/internal/repository/adaptive_scheduler_*` | 实现核心定义的 store 端口，可采用 Redis；策略核心不反向依赖此层，不改官方计费 repository |
| 既有 fork 扩展 | upstream_health_evidence/scheduling、openai_ttft_guard、upstream_scheduler_concurrency | 先经底座收口状态/读写职责，仍可互相复用；后续策略不直接读取全局 registry、具体 Guard 或 ConcurrencyService，不另建通用插件层 |
| 装配 | fork provider 文件加已有构造入口 | 默认装配完整 legacy 能力；只有未注入的新增 adaptive 旁路可以 no-op，不能关闭已有保护；官方装配文件只增加必要注册，不在 wire_gen 内手写策略 |

目录隔离由 import/依赖测试约束，不能只靠注释。同 package 新文件只是桥接层，不算独立策略核心；核心禁止接收官方对象、其可变指针或通过任意回调间接调用选号/计费。

### 官方接入点白名单

以下是允许进一步细化的函数级范围，不是整文件修改授权。每个 child 在 plan 中绑定其用到的调用点、桥接映射、失败行为和兼容验证；超出列表必须先修订设计，不能以通配符扩充审计目录消除告警。

| 官方文件/端口 | 允许的接入形态 | 必须保留的行为 | 负责 child |
| --- | --- | --- | --- |
| `backend/internal/service/openai_account_scheduler.go`: Select/SelectAccountWithScheduler、候选各分支和 ReportOpenAIAccountScheduleResult | 底座仅提取 fork 分池/比较及保持原 TTFT 调用位置；后续增加单次真实选择快照/意见。Report 原公共签名保留，私有 reportOpenAIAccountScheduleResultShared 与 guardDispatch 仍仅为后续 TTFT owner 接管而抽取 | 官方资格/分层/绑定/抢槽/指标主体及原健康反馈，不另建 Select，不复制反馈主体，不重排原条件/顺序/early return | fork-foundation/error-attribution/fault-domain-health/cache-aware-capacity/ttft-traffic-isolation/shadow-rollout；共享反馈抽取仍归 TTFT child |
| `backend/internal/service/gateway_scheduling.go`: selectAccountForModelWithExclusionsCore/selectAccountWithLoadAwarenessCore/newSelectionResult、现有 RPM 预检/最终准入 | 底座仅隔离既有 fork 规则及容量/RPM 委托；后续快照/意见接口沿用已有 Acquired/ReleaseFunc | 原平台/模型/组资格、目标和扣次时机，不重新申请同一槽位 | fork-foundation/fault-domain-health/cache-aware-capacity/ttft-traffic-isolation/shadow-rollout |
| `backend/internal/service/openai_gateway_scheduling.go`: selectAccountForModelWithExclusions/selectAccountWithLoadAwareness/newSelectionResult | 底座保留旧分池/TTFT/容量行为并委托；后续覆盖旧选号分支的同一桥接 | 不修改 NormalizeOpenAICompatiblePlatform，不放宽原硬资格 | fork-foundation/fault-domain-health/cache-aware-capacity/ttft-traffic-isolation/shadow-rollout |
| `backend/internal/handler/failover_loop.go`: HandleFailoverError/HandleSelectionExhausted/sleepWithContext | 在旧副作用执行前检查统一预算/取消，返回明确本地终止；不事后再执行新一套循环 | 原 retry/switch/排除/等待/ForceCacheBilling 执行者唯一；底座不改此循环 | attempt-budget |
| `backend/internal/handler/openai_gateway_handler.go`、`gateway_handler_responses.go`: 请求/attempt/commit/终态边界 | 调用同一协议桥接，传递摘要与取消，终态幂等清理 | 不重写协议响应、工具执行和原结算调用 | error-attribution/attempt-budget/fault-domain-health/cache-aware-capacity/ttft-traffic-isolation |
| `backend/internal/service/http_upstream_port.go`: 现有 HTTPUpstream.Do/DoWithTLS 端口 | 在 fork wrapper 内实现原接口，通过装配包装调用方 | 接口声明、请求/响应格式、原传输及内部重试逻辑保留；底座不包装 transport | attempt-budget/fault-domain-health |
| `backend/internal/service/gateway_usage_billing.go`: RecordUsage/recordUsageCore 调用前或其第一处改写前；`openai_gateway_usage.go` 对应 RecordUsage 边界 | 只复制尚未被 ForceCacheBilling/TTL 改写的 usage，旁路观察原结算结果引用 | 原 request/money-event ID、价格、usage 写入、事务去重和扣费顺序；底座不改此边界 | error-attribution/cache-aware-capacity |
| `backend/internal/service/openai_gateway_service.go`: TTFT 专属字段及构造装配 | 具体 Guard/资格状态改由窄 runtime 拥有，保留原构造兼容及 upstream-only 默认值 | 不搬迁官方 scheduler stats、API Key 健康或模型瞬态状态 | fork-foundation |
| `backend/internal/service/account.go`: GetPoolModeRetryStatusCodes/IsPoolModeRetryableStatus 中 fork 分支 | 委托兼容重试策略读取入口 | 普通账号字段、已安装全局配置和空配置默认值保持原义，不添加 Account 必填依赖 | fork-foundation |
| `backend/internal/service/concurrency_service.go` 既有 target-aware 方法；`backend/internal/handler/gateway_helper.go` 目标等待/抢槽适配 | 复用现有窄接口并接管原释放函数，不扩大官方缓存必需合同 | 原等待、复核重取、失败回退与取消恰好一次释放 | fork-foundation |
| `backend/internal/service/wire.go`: 相关 fork 配置/健康/TTFT/容量 provider 与全局注册 | 实现移至 fork provider，入口只注册同一 legacy runtime | 原暖启动、后台激活门禁和停止顺序不变，不重构发布控制器 | fork-foundation |
| `backend/cmd/server/wire.go`: initializeApplication 的 provider 装配入口 | 注册 fork bridge/provider，生成内容仅允许工具产生 | 不加入策略分支和新的核心业务职责 | 共用装配 owner |

`gateway_service.go` 的失败类型、`http_upstream_port.go` 的接口声明、`concurrency_service.go` 的原缓存合同、`repository/usage_billing_repo.go` 和 UsageBillingCommand 不因本方案增加必需字段。允许只读映射，不为了让新增策略编译而反向扩张这些合同。底座另在 fork-only `upstream_config_repo.go` 的生命周期成功后入口，将 registry 直接写改为窄命令委托；原事务、outbox、返回及提交时机不变，不能将 fork-only 文件当整文件重写授权。

覆盖能力按 `provider + protocol + entry path + transport + lifecycle` 登记，不能只按平台名称声明支持。通过同一 HTTPUpstream 的调用也需证明内层 send、取消和终态均被覆盖；WS/插件/特殊 provider 路径未映射时只能 `unavailable` 或纯观测，不进入 enforced。新增接入在此白名单之外时先设计评审，不能将现有 OAuth PluginManager 当通用调度插件入口。

### 单一执行者与旧路径兼容

- `disabled` 的基线是加入本方案之前的当前 fork，不是纯官方或关闭既有 fork 扩展后的行为；不增加请求体读取、候选遍历、后台任务或存储请求。
- `shadow` 保持旧调用次数、参数、返回与副作用；新证据/错误归因不能替代 RecordUpstreamTrafficFailure、ReportOpenAIAccountScheduleResult 或既有失败处理。unknown 只禁止新扩展追加处罚，不取消旧健康动作。
- 官方 FailoverState/原协议循环是重试动作的唯一执行者；预算只限制/否决，不能独立 sleep、换号或执行第二次 cooldown。本地预算耗尽不可包装成可重试的上游错误。
- 官方计费是货币结算唯一 owner。调度 request_id、attempt_id 和 send_id 只关联观察；原 request_id/api_key_id/money-event ID 与 fingerprint 保持，不引入调度 settlement token，不把多个合法计费事件压成一次。
- 现有 ConcurrencyTarget 对应账号或共享上游槽二选一，bridge 接管已经获得的 ReleaseFunc 并确保恰好一次释放。cache-aware-capacity 独占新增 token 账本，ttft-traffic-isolation 的 workload 子配额只增量预留；新增检查失败不得吞掉原并发错误。
- 新旧 TTFT Guard 通过 fork 入口按请求模式选择唯一实际裁决者；可旁路双采样，但排除、恢复、half-open 和状态展示不能混用两套有效结论。官方健康/模型恢复/scheduler 反馈仍执行。

现有 ReportOpenAIAccountScheduleResult 不带 ctx/mode，因此冻结 Guard 不能靠全局开关或按账号反查补齐。ttft-traffic-isolation 的 fork 带快照反馈入口显式绑定不可变 owner，和旧公共入口共用上述私有反馈实现；旧公共入口默认 legacy。两入口只选一个，不先执行旧整函数再补 adaptive 反馈；私有 helper 只接收窄 Guard dispatch，不接收 Redis/策略对象，不重排或迁移原健康逻辑到策略核心。异步任务在入队前复制 owner 引用，失效引用只拒绝新增 Guard 写入，不跳过原健康主体。底座先只委托原 TTFT 调用，其通过不代表该后续接管已完成；未完成这个精确接入点的兼容验证不得启用 adaptive Guard。

### 底座的副作用与已知风险

- 旧 TTFT exclusions 会推进试放计数；批量并发负载读取可能清理 Redis 过期成员。底座接口必须标记这些效果，shadow 只能使用原调用产生的安全副本，不重复调用并宣称只读。
- 账号 RPM 与 upstream_config_id 共享并发是不同维度；底座不引入共享 RPM、不改变消费次数或读取失败回退。
- 健康 registry 存在业务 401 暂停后被不计暂停阈值的探针失败改写来源的静态风险。详见 [底座研究](../adaptive-upstream-scheduler-fork-foundation/research/codegraph.md)。底座不修该状态机、不宣称来源已完全隔离；故障域设计必须显式验证/界定仲裁，不能静默清除官方独立阻断。立即修复需另行声明行为范围。
- 质量展示已有只读 reader，既有 usage 聚合不保证是计费改写前的 cache facts；底座不把质量分接入选号，UsageFacts 仍由后续归因/缓存合同负责。

### 事件、提交与缓存事实

- request_id 是逻辑调度请求，attempt_id 是一次账号转发尝试；内部 HTTP 重发每次独立 send_id。attempt 终态去重不得压掉 send 次数，也不得把同一 send 的多个流事件当多次故障。
- CommitState 唯一枚举为 `uncommitted/http_committed/heartbeat_only/semantic_committed/tool_continuation_committed/terminal_committed`，由协议 owner 提供。取消用独立 client_state 表达，不能清除已发生的语义提交；分类器不能根据状态码或耗时推断重放安全。
- UsageFacts 是 provider adapter 在计费或下游 JSON 分类改写前生成的不可变副本；保留 missing 与 zero 的区别和 input 是否包含 cache-read，按能力版本只归一化一次。采集不到原始事实时明确 unknown，不能事后从账单还原。
- 实际 cache-read/write、命中概率预测、货币成本估计分别保存来源/可信度；高命中预期不等于实际退款依据。已发送但 usage 未知的请求不按零消耗全额释放，只按有限保守窗口/TTL 处理并标记未知；未发送才可完整撤销新增预留。
- capacity settlement 与 billing outcome 互不触发；预算关闭后允许收到幂等清理或原结算观察，但不能重新打开请求或追发上游。

### 选择快照与发送准入

- 只从真实选号执行中的已构造候选和分支结果复制安全值；不额外调用 Select、不复制官方排序实现、不重复查询上游。快照含 branch、coverage、硬资格/优先层及绑定限制，不能暗示具有未访问分支的完整候选集。
- Shadow 只计算候选意见和受约束的反事实排序，不抢槽、不获取恢复租约、不写 sticky/健康/真实容量；快照/能力不足显式 partial/unavailable。shadow admission 使用独立模拟状态，不借用业务状态。
- 未选账号的 TTFT、缓存与成本是预测，只有实际请求结果是观测；灰度验收区分 prediction、observed 和缺失值，不用无法观测的反事实收益自动放量。
- caller/hard/advisory 排除分开，只有 advisory 能放宽。previous-response、guardian、session 和负载均衡路径都需覆盖；不可迁移的硬绑定只能等待或终止，不通过清绑定实现换号。
- 新策略候选过滤只读，不额外调用旧 TTFT 有状态排除；实际每次 send 前获取或验证 attempt 所属 half-open 许可和容量有效性。排队、利润终检拒绝、取消和租约过期均不能继续发送；许可去重、释放和完成反馈带 fencing，迟到结果不能改变新一代状态。

### 版本、回滚与可扩展性

- 每个请求冻结 `mode/control_epoch/policy_revision/registry_revision`；mode 为 disabled/shadow/enforced。控制面 control_epoch 严格单调，policy_revision 可引用旧内容；回滚生成新 epoch，不恢复旧 epoch 数字，不存在 g1 -> g2 -> g1 的 CAS 复用。
- 普通模式切换只影响新请求；在途账本/slot/capacity 仍由创建时 owner 完成，有限 TTL 保留对应清理能力。紧急 admission fence 可撤销旧 epoch 未开始的发送并清理/终止，禁止以重建 legacy 账本来绕过冻结预算。
- 旧请求的完成事件可结算其旧预留，不能作为新 epoch 的自动放量或健康恢复依据；可继续记录带版本的历史观察。自动回滚不删除 usage、用户余额、绑定或旧健康历史。
- 原始 provider、upstream/site、deployment 与 protocol 身份在官方平台规范化之前映射；registry 无可信匹配时 NoOpinion/unavailable，不改变官方兼容路由，也不将 unknown 合并到 OpenAI 故障域。
- 新错误、平台、上游能力通过 versioned registry/adapter 扩展，稳定责任大类与具体 reason 分开；注册重复/冲突、未知证据和不支持能力不能提高处罚范围。核心禁止 provider switch，证据字段数量/长度和指标基数有固定上限。

### 文档与验收责任

本 split 是七个 child 的共同内部合同；底座负责现有能力边界，后六个策略 child 不得重定义上述 owner、ID、状态或回滚语义。接入新 provider 时先补映射与覆盖合同，不要求修改官方核心类型。

后续验证必须同时覆盖：off/旧 fork 行为等价、shadow 零真实副作用、预算实际发送计数、原货币结算不变、单槽释放、真实 cache 事实、绑定/排除分层、许可 fencing、Guard 单一生效者、单调 epoch 和 import/官方写集约束。这些是设计验收维度，具体测试用例由各 child 的 wiki-plan 生成。

未来实现和 review 通过后，稳定的 fork 扩展合同需登记到 `.agents/skills/sub2api-fork-extension-audit/references/extensions.yaml` 并沉淀 `.wiki/`；本轮不修改目录、迁移/profile、产品或线上配置。profile 250/249、270/271 迁移登记属于独立仓库门禁，不通过放宽白名单掩盖。

### 审计发现落实索引

| 审计编号 | 合同落点 | 对应 child |
| --- | --- | --- |
| D01-D03 | 单一重试执行、transport send、货币计费只观察 | attempt-budget |
| D04 | 原健康反馈不由归因替换，unknown 不追加动作 | error-attribution |
| D05 | 单次真实选择快照，反事实是预测 | shadow-rollout |
| D06/D08 | 分级排除、发送许可、原始平台身份 | fault-domain-health |
| D07 | 独立核心、桥接层、官方函数级白名单 | parent 与全部 child |
| D09 | control_epoch 与 policy_revision 分离 | shadow-rollout/fault-domain-health |
| D10-D11 | 原 lease 接管、容量唯一账本、计费前 UsageFacts | cache-aware-capacity |
| D12 | fork TTFT 单一有效 Guard 与旧反馈兼容 | ttft-traffic-isolation |
| F01 | 现有接口复用、legacy 状态隔离、纯规则抽取及官方 hook 收口 | fork-foundation |
| F02 | 有状态 TTFT 排除、负载读取清理及原调用副作用次数 | fork-foundation/shadow-rollout |
| F03 | Probe/Traffic 来源覆盖静态风险登记，非等价重构修复范围 | fork-foundation/fault-domain-health |
