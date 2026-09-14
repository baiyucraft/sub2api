# adaptive-upstream-scheduler-ttft-traffic-isolation 设计方案

## 方案概述

在 `backend/internal/adaptivescheduler/` 内设计 TTFT/质量分桶、Guard 决策和 workload 子配额策略；fork `openai_ttft_guard.go` 是新旧 Guard 的统一入口，只通过前置底座窄端口消费既有 TTFT、health 与共享并发扩展，`account_quality.go` 仍是只读展示，不另建相互隔离的插件系统。TTFT/流量隔离 child 不拥有 token 账本，不复制 缓存容量 child 的估算/结算；现有 scheduler 保留候选遍历、优先池、会话与最终选择主体。

共同类型、官方白名单、桥接包、冻结模式、六态 CommitState、独立 client_state 和控制 epoch 均引用 parent [split.md 的“官方兼容与共同合同”](../adaptive-upstream-scheduler/split.md#官方兼容与共同合同)，本设计只细化 TTFT/流量隔离 child 的消费和执行边界。核心不得 import `service`、`handler`、`repository`、Ent、Gin 或 Redis 实现；官方文件仅按 parent 白名单进行薄观测/既有桥接接入，不能装入 workload/Guard 状态机或新增必需领域参数。

本 child 的交付顺序调整为 6，首先依赖前置 fork 调度底座，并保留请求预算、故障域健康和缓存容量 child 的依赖；灰度控制由灰度发布 child（交付顺序 7）负责。本 child 不另行实现请求预算、错误归因或故障域状态机，前置也不承担本 child 的新 Guard 接管。

设计的核心原则：

- 新 adaptive Guard 先满足有效样本、连续异常和冷却要求再阻断，不能复制旧 Guard 的单样本 critical_sample 捷径；disabled/shadow 的旧 Guard 行为不因此改动。
- 真实业务、probe、shadow 分离统计；probe/shadow 不改变业务 Guard。
- 缓存只消费 缓存容量 child 的需求/容量摘要和 parent 计费前 UsageFacts；unknown 不追加缓存奖励或处罚，不取消原健康处理。
- 所有新策略默认 disabled；显式 shadow 仅旁路计算，真实裁决仍由旧 Guard 负责。enforced 请求按冻结模式与覆盖门禁选定唯一 Guard owner，观测可双写、实际排除/恢复不可双写。
- 保留现有首 token、语义事件、usage、计费和取消边界；本 child 不扩大透明 failover。

## 接口与稳定合同

### 前置底座衔接

本 child 依赖[前置 fork 调度底座设计](../adaptive-upstream-scheduler-fork-foundation/design.md)，对现有 fork 能力只消费其值对象和窄端口。新增调用（包括本 child 的桥接调用）不得绕过端口直读 global registry、具体旧 `openAITTFTGuard` 或 `ConcurrencyService`；原实现访问只留在底座外的 legacy 适配层，不因接入新策略扩散。

`backend/internal/forkscheduling/` 只放底座值对象和窄契约，旧状态及纯规则位于其 `legacy/` 子包；两者均不 import `service`、`handler`、`repository`、Ent、Gin、Redis 或 `adaptivescheduler`。`adaptivescheduler` 只消费契约包，禁止引用 `forkscheduling/legacy` 具体实现或反向依赖；Outcome/UsageFacts 仍由错误归因 child 按 parent 共同合同负责，mode/epoch 由策略与控制层负责，前置底座不定义或依赖这些对象。

前置只机械委托现有实现，默认装配 legacy，不改变现有 TTFT/健康算法。新增策略 `disabled` 保留当前 fork 真实行为及原配置开关，不能装配 Noop 全关旧保护。现有 TTFT `exclusions` 会推进 probe 试放计数，真实负载读取可能执行 Redis 过期清理，均不得由 shadow 再调；旁路仅消费真实路径已产生的一次性副本，无副本即 unavailable。

共享 slot 沿用原 `Acquired/ReleaseFunc` 和排队生命周期，不新增第二次真实获取或释放；account RPM 仍是账号维度，不因共享上游 slot 被解释或合并为共享 RPM。质量展示的评分与账单缓存率不是计费前真实 cache facts，不能通过底座端口包装后改称事实输入。


以下是 TTFT/流量隔离 child 自有合同，不新增对外 HTTP API、数据库字段或官方前端必须依赖的结构。公共类型定义不在这里复制。

### 共同对象的局部视图

请求摘要引用 parent `SchedulingRequestContext` 的原始安全身份、模型/协议/workload、deadline 和冻结 mode/control_epoch/policy_revision/registry_revision；执行引用 `SchedulingAttemptContext` 的 attempt/send、已取得槽位和 缓存容量 child 容量引用。TTFT/流量隔离 child 仅附加有界 context/reasoning/cache bucket、质量快照和 workload 子配额引用。

结果消费 parent `SchedulingOutcomeEvent` 与其计费前不可变 `UsageFacts`。不重新生成 request/attempt/event ID，不重定义六态 CommitState；取消单独读取 client_state，不清除已发生的工具或语义提交。事实缺失不能从 billing 后 UsageLog、AccountStatsCost 或下游改写 JSON 补齐。

原始 provider/upstream/deployment 在官方 normalize 前由 fork adapter 保存；未知身份不能因官方兼容归一为 OpenAI 而进入 OpenAI 质量/Guard 域。可信 registry 不匹配时新扩展给 NoOpinion/unknown，保留原路由和原健康处理；未知维度不能清除已存在保护。bucket 只使用有界安全摘要，不存原始正文/URL/凭据。

### `TTFTQualitySnapshot`

```text
bucket_key
sample_count
effective_sample_count
ttft_p50_ms
ttft_p95_ms
ttft_p99_ms
queue_p50_ms
queue_p95_ms
success_rate
first_token_timeout_rate
cache_read_ratio
cache_write_ratio
cache_status             known/partial/unknown
quality_state            low_sample/healthy/degraded/guarded/recovering
observed_at
expires_at
state_revision           本 child 快照 CAS 版本，不是控制 epoch
owner_ref                请求冻结的 guard owner 安全引用
```

分位数、比率和 cache 字段都必须带样本量或有效性状态。不存在足够样本时使用 `low_sample`，不能输出伪造的零值。

### `TTFTDecision`

```text
hard_guard              no_opinion/allow/recovery_only/block
soft_penalty            bounded numeric penalty
reason_codes            fixed allowlisted codes
state_scope             account_model/account_model_protocol/site_bucket
cooldown_until
source                  business_observed/shadow_prediction
state_revision          同一 owner 状态的版本
owner_ref               与请求冻结的有效裁决者一致
```

此 decision 仅是新 adaptive Guard 的意见，不能另加一套旧 Guard 排除集合。`block` 必须满足最小/有效样本、连续异常、窗口和冷却规则；低样本不能仅凭 critical_sample 变成硬阻断。`recovery_only` 指受控真实业务 half-open，不是主动探针成功即可解封；许可执行引用 parent/故障域健康 child 的发送前准入合同。`soft_penalty` 有界，不把孤立异常样本当永久不可用。

`owner_ref` 在 fork 入口按 parent 冻结请求选择并持有，指向有效 legacy 或 adaptive 状态；模式切换不能在同一请求换 owner。`state_revision` 仅用于该状态的 CAS，与 parent 单调 control_epoch、可回退 policy_revision 各司其职，不再用含混 generation 同时表达三者。

### workload 分类

分类优先级固定，避免一个请求同时被多个池计算：

1. `shadow`：由旁路 evaluator 标记的模拟，不发送真实请求、不占真实 token/workload/并发或恢复许可；不能把被旁路观察的原业务请求重分类为 shadow。
2. `probe`：由主动探针/健康检查明确标记，永不混入业务 TTFT。
3. `tool_continuation`：存在工具调用续接、Responses continuation 或等价会话约束。
4. `long_reasoning`：reasoning/effort 标记、预计上下文+输出预算或历史分桶达到配置阈值。
5. `batch`：由调用方的 batch/background 流量标签明确标记。
6. `interactive`：普通用户交互请求。
7. `unknown`：无法可靠识别时的保守池。

分类只使用现有通用 context 和 provider adapter 摘要；不根据模型名称猜测 reasoning，不根据请求正文内容猜测 batch。

## Ownership 与数据/文件流

```text
parent 冻结上下文 + 原始安全身份 -> fork TTFT 统一入口选择 owner
  -> 只读 bucket/质量决策 -> 原选择/排队取得唯一真实 lease
  -> fork 接管 -> 缓存容量 child token reservation -> TTFT/流量隔离 child workload 子配额
  -> 原发送/commit/健康反馈 -> parent Outcome + 计费前 UsageFacts
  -> 单一 owner 状态更新；其他观测单独存储、不得参与真实裁决
```

- 核心只拥有分桶、质量算法、adaptive Guard 状态转换与 workload 子配额端口；不能通过回调反向调用官方 Select/计费。
- fork `openai_ttft_guard.go` 统一选择、反馈、恢复和管理端读取的 owner，legacy 行为只通过前置 TTFT 窄端口委托；`account_quality.go` 维持只读展示，其质量总分和 billing 后缓存率不作为调度或真实缓存事实。
- parent 服务桥接通过前置窄端口接管原 target/ReleaseFunc、组织子配额及幂等补偿，具体 `upstream_scheduler_concurrency.go` 只由 legacy 适配层封装；真实 account/upstream 槽二选一，TTFT/流量隔离 child 不另取全局槽，不合并账号 RPM。
- 缓存容量 child 唯一拥有 token 估算、Reserve/Settle/Release。TTFT/流量隔离 child 只消费其不可变摘要，额外预留 workload 占比/并发子配额，不操作 token 或 RPM 消费。
- handler 保留协议与取消，官方 scheduler 保留选择和完整健康副作用；provider/Redis adapter 位于核心之外。原反馈实现仅执行一次，只有其中的 fork TTFT 分支按显式 owner 分派；具体入口见“无 context 反馈的显式接管”，不能跳过健康主体或再调用旧整函数。
- dashboard 显示唯一生效 owner 的结论及 epoch/policy 引用；双写观测用明确 observed/shadow 标签分开，不能把两套降级列表合并为“当前生效”。

## 正常流程

1. parent 冻结请求 mode/epoch/policy/registry，fork 入口据路径覆盖选定 owner；默认 disabled。未知原始身份不写入已知 provider 的新状态，旧路由与健康调用不变。
2. normalizer 生成有限 context/reasoning/workload bucket，缺失值归 unknown。shadow 只消费真实选择的一次性快照，不再次 Select，也不领取 half-open 或真实配额。
3. 在原硬资格和绑定约束内消费唯一 owner 的 Guard 决策；adaptive 低样本只产生有限软意见，不能再被旧 critical_sample 分支排除。caller/hard/advisory 分层沿用 parent。
4. 原选择/排队完成后由 fork 接管唯一真实 lease，缓存容量 child 先预留 token，TTFT/流量隔离 child 只追加 workload 子配额；任何失败均由同一桥接补偿。发送前按 parent fence/许可/原终检复查，不更改请求 owner。
5. 原请求执行，协议 owner 产生真实六态 CommitState/client_state；带冻结快照的 fork 反馈入口调用原共享实现一次，保留原参数与 API Key 健康、模型瞬态恢复及 scheduler stats 副作用，fork TTFT 分支只送给选定 owner。不额外调用旧公共整函数。
6. 计费改写前 UsageFacts 与 Outcome 供新 sampler 只读使用，事实/预测分开。有效 Guard 的状态更新按 owner/state_revision CAS；可旁路双观测，但观察方不能领取恢复许可、解除保护或影响旧反馈。
7. 完成/取消统一释放 workload 子配额和原 lease；缓存容量 child 独立按真实 usage 结算，已发送 unknown 不按零消费全额释放。迟到事件仅完成原 owner 的幂等清理/历史观察，不写新控制面或新 epoch 的恢复状态。

## 采样、衰减与质量模型

### 采样规则

- 真实业务样本优先；probe/shadow 单独存储。
- 每个 parent attempt 的业务首 token 最多计一次；send 级观测保留关联但不压掉 parent 的真实发送计数，同一 send 的多个事件不重复记成样本。请求级聚合按 request ID 去重，不能把多个重试当多个独立用户成功。
- TTFT 从上游请求发送或现有定义的 attempt start 起算，到首个有效协议 token/事件为止；连接建立、排队和 provider preamble 分别保留，不能把所有延迟塞进 TTFT。
- 没有首 token 的超时/连接失败进入 outcome/error 统计，但只有经过统一错误归因 child 认可的责任类别才影响账号/模型 Guard。
- 每个 bucket 设置最大样本数、最大模型标签数和最大状态条目数；超限按时间淘汰或折叠到父 bucket。

### 衰减与分位数

- 使用时间衰减权重，短期窗口用于快速识别趋势，长期窗口用于 baseline；具体半衰期必须配置化并在后续压测中确定。
- adaptive Guard 至少要求 `min_samples` 和 `min_effective_samples`；低于门槛只显示 `low_sample`，单条极慢样本仅记 outlier/有界软惩罚。不能把旧 critical_sample 直接作为新模型训练标签或处罚条件。
- p95/p99 采用有界近似分位数结构或固定窗口，不能保存无限原始延迟列表。
- 极端延迟需要截断到系统最大观测范围，并另外记录 `outlier_count`，避免一个异常值污染所有分位数。
- quality score 由成功率、首 token 超时率、TTFT、queue delay 和状态新鲜度组成；每项归一化后再加权，输出分量便于 shadow 对比。

### 缓存与 workload 关系

缓存容量 child 是 cache-read/cache-write/未命中输入/上下文/输出/reasoning 估算和 token 账本的唯一 owner，算法与窗口语义引用其 design。本 child 不保留 estimated_cost 公式、不计算售价/倍率、不重复 Reserve/Settle token；只用 缓存容量 child 的有界 demand 摘要辅助 workload 分类与子配额选择。

业务缓存率来自 parent 计费前不可变 UsageFacts，经 缓存容量 child/可信 adapter 统一口径后只读消费。保留 missing 与 zero、input 是否包含 cache-read/write、output 是否包含 reasoning、完整性、来源与 capability 版本；cache unknown 不奖励、不从 ForceCacheBilling 后 UsageLog、AccountStatsCost 或改写 JSON 推断命中。已有 `account_quality.go` 保持只读展示；计费前事实由错误归因 child 及可信 adapter 提供，不能用展示总分或账单缓存率替代，也不要求前置定义 UsageFacts。

实际 cache-read、预测可复用前缀与货币成本是三种不同信号，分别标注来源/可信度。高缓存请求仍占原真实并发与 workload 子配额；换号/跨 cache domain/过期时复用量由 缓存容量 child 重估，不把会话粘性当命中。字段不可信时只失去新增缓存意见，不改变官方兼容路由或旧健康行为。

## 排队、并发与隔离模型

### 池隔离

以已经取得的原 ConcurrencyTarget 为真实硬上限，账号与共享上游槽二选一。workload 仅是该物理 target 内的逻辑子配额，不新增一个“全局真实槽”或新的 token 池：

```text
原 ConcurrencyTarget（桥接接管原 ReleaseFunc）
  +-- interactive reserve
  +-- long_reasoning reserve
  +-- tool_continuation reserve
  +-- batch reserve
  +-- unknown conservative subquota

probe: 仅已登记隔离资源，不借业务子配额
shadow: 独立模拟，无真实预留/发送
```

interactive、long_reasoning、tool_continuation 和 batch 的保留/受控借用只改变子配额归属，不提高真实 target 硬上限。TTFT/流量隔离 child 的 store 仅按 target/workload 及稳定 lease 引用原子检查子配额和幂等占用；不复制 ConcurrencyCache 的全局槽位计数。多 Key 共用同一 upstream 时不能按派生 account ID 获得多份全局额度；LoadFactor 仍只保留原排序用途。

探针必须有显式已登记的隔离资源才能执行，不能借新 workload 功能从业务池抢槽；没有覆盖的探针路径不纳入 enforced，仍保留原流程，不由本 child 临时补发探针。shadow 永远只是模拟，不领子配额/真实 lease/half-open，也不因被观察请求是业务流量而污染其分类。

### 排队信号

- 同时记录 queue wait、active duration、TTFT 和 completion latency。
- 质量评分优先使用 queue delay 与 TTFT 分离后的趋势，避免把本地排队误判为上游生成慢。
- 原选择/WaitPlan 取得真实槽后，fork 桥接接管 `Acquired/ConcurrencyTarget/ReleaseFunc`；先申请 缓存容量 child token reservation，再申请本 child 的 workload 子配额，不能二次调用原 Acquire。
- 子配额失败返回本地结构化拒绝，由同一桥接补偿已取得的未用 token 预留与子配额，并释放原 lease 一次，再由原执行者按 parent 预算决定等待/换候选。新拒绝不是上游故障，不进入账号处罚。
- 一次性 lease 句柄替代原 handler 与扩展各自独立的 ReleaseFunc；完成、取消、终检拒绝、超时、fence 和 defer 只争取同一个完成标记。远端子配额清理稳定 ID 幂等，cleanup context 独立有界，清理失败仍需释放原真实槽并保留故障诊断/有界到期信息。
- token、workload 和原槽位不是跨系统事务；store 超时结果不明确时按同一 ID 查询/重试，不生成新预留。新的 fail-open 仅不追加新增限制，不能吞原并发申请错误、退还旧 RPM 消费或撤销已消耗的 token。
- 已发送且 usage 未知时，可按真实终止释放 inflight workload 与 lease，但 缓存容量 child 保留其 token 有界保守窗口；不能把客户端取消或未语义提交等同于 provider 零消耗。

### TTFT 与 Guard 分离

```text
质量观测
  -> low_sample / healthy / degraded
  -> bounded soft penalty

连续业务异常 + 最小样本 + 冷却规则
  -> hard Guard / half-open recovery
```

上述是 adaptive owner 的状态机，不是叠在旧 Guard 上的第二个过滤器。软评分有上限；硬 Guard 带 reason/scope/state_revision/cooldown，恢复许可与 parent/故障域健康 child 的发送前 fencing 合同集成，不另外抢一套许可。probe/shadow 成功不能清除业务 Guard。

### 唯一 Guard 入口与误判边界

`openai_ttft_guard.go` 的选择包装、TTFT dispatch、恢复许可和 `OpenAITTFTGuardDegradations` 都经同一个 fork owner 路由。旧公共 `ReportOpenAIAccountScheduleResult` 保留原签名、默认 legacy；新带快照入口与其共用同一份原反馈实现，API Key 健康、模型瞬态恢复和 scheduler.ReportResult 的原条件、顺序、参数及执行次数保持不变。

### 无 context 反馈的显式接管

当前公共 `ReportOpenAIAccountScheduleResult` 只接收原 account/model/success/firstTokenMs/observedErr 参数，不包含 ctx/mode；其内部直接调用 `reportOpenAITTFTGuard`。不能声称在该调用中自动获得请求冻结 owner，也不能用当前全局 mode、按账号反查“最近请求”或 goroutine-local 补上下文。

前置只把现有 `reportOpenAITTFTGuard` 的实现委托给 TTFT 观测接口及共享的 legacy 状态，不抽取公共 Report 主体，不增加冻结 owner、private shared Report helper 或 `guardDispatch`。下面的 `ReportOpenAIAccountScheduleResultWithSnapshot`、`reportOpenAIAccountScheduleResultShared`、`guardDispatch` 及冻结 Guard owner 全部由本 TTFT/流量隔离 child 在前置之后实现，且不进入 `forkscheduling` 或形成前置反向依赖。

本设计使用 parent 白名单明确登记的私有薄接入点，名称为拟议接口，不是已存在代码：

```text
旧/未覆盖调用者
  -> ReportOpenAIAccountScheduleResult(原参数，签名不变)
  -> reportOpenAIAccountScheduleResultShared(原参数, 显式 legacy guardDispatch)

已覆盖请求，异步任务也持有创建时的不可变快照
  -> fork ReportOpenAIAccountScheduleResultWithSnapshot(冻结快照, 原参数)
  -> reportOpenAIAccountScheduleResultShared(原参数, 绑定冻结 owner 的 guardDispatch)

Shared: 原健康前段 -> 原 TTFT 位置调用 guardDispatch 一次 -> 原健康/metrics 后段
```

`reportOpenAIAccountScheduleResultShared` 留在 service 的原反馈所有者中，不是 `adaptivescheduler` 核心。仅对原短反馈实现做机械抽取，原 early return、健康更新、模型恢复和 scheduler stats 主体不重排、不复制；旧公共方法成为默认 legacy 的兼容委托，不新增官方必需字段/接口参数。私有实现只接收窄 `guardDispatch`，不接收 mode/策略/Redis 或新的公共领域对象。

fork `adaptive_scheduler_bridge.go` 提供带快照入口；创建 callback 时显式绑定不可变 `guard_owner_ref` 及该反馈的 account/model/success/TTFT 值，在原 TTFT 位置调用 fork `dispatchOpenAITTFTGuard(ownerRef, ...)`。owner 引用从 parent 请求/attempt 快照传入，异步入队前复制并保持其有效期；callback 不读取可变全局 mode，也不把裸 request context、官方可变对象或任意选号/计费 callback 传入策略核心。旧 `reportOpenAITTFTGuard` 保持 legacy 语义供旧入口调用。

同一反馈只能选择上述一个入口。带快照入口不得先/后调用旧公共整函数再补 adaptive report，也不能先更新 legacy Guard 再试图回滚；观测双写在 fork dispatch 外的独立只读样本流中进行，不触发第二份真实 Guard 状态转换。未知/失效快照不能临时选择新全局 owner：保留原健康主体，拒绝不合法的新增 Guard 写入并诊断，由 parent 生命周期保障原 owner 清理。

parent 白名单的 `openai_account_scheduler.go` Report 行已限定原公共签名的薄委托、私有 `reportOpenAIAccountScheduleResultShared` 机械抽取及 TTFT 原位置的显式 callback；其余主体不变。fork 带快照入口/dispatch 属于统一桥接范围，不授权进一步抽取、移动官方反馈主体或新增其他官方接口参数。这个接入点的实现与兼容验证完成前，相关路径不得进入 adaptive enforced。

| 请求冻结模式/覆盖 | 有效裁决者 | 观测和执行边界 |
| --- | --- | --- |
| disabled | legacy | 与当前 fork 相同，包括旧 critical_sample、恢复和 fail-open；不增加新采样/store 操作。 |
| shadow | legacy | 旧报告/选择副作用不变；新 sampler 仅从副本观测并给预测，不能写旧健康、取得恢复许可或叠加排除。 |
| enforced 且覆盖/激活门禁满足 | adaptive | fork TTFT 分支分派给唯一 adaptive owner；legacy 可隔离旁路观测，但不能继续贡献真实 exclusions/恢复/half-open。 |
| 路径未覆盖或激活证据不足 | 请求开始即冻结 legacy | 新层 NoOpinion/unavailable，不把不完整覆盖当作新保护已启用；不得在请求中途因为错误切回另一 owner。 |

启用门禁必须能解释原有效保护及其来源、截止时间和样本口径；缺少可信状态时继续冻结 legacy，不能用空的新 bucket 清掉旧 Guard。旧 active critical_sample 不直接导入为满足新 min_samples 的证据；不能安全迁移的路径在保护到期或获得足够证据前不启用 adaptive。普通 mode 切换只影响新请求，旧 owner 仍完成其在途清理。

- **低样本/false positive**：adaptive 的单条三倍阈值样本只产生 outlier 和有限软惩罚，需连续业务异常与有效样本门槛才可 block；记录低样本拦截原因、队列误归因和后续恢复观察，不用旧 Guard 处罚结果给新模型自证正确。
- **critical_sample 兼容**：legacy 的三倍阈值单样本降级只在 legacy owner 下生效；不能在 adaptive 已判 low_sample 后，再由旧 report/exclusions 暗中排除。
- **全候选耗尽**：legacy 保留原 fail-open。adaptive 下 parent caller/hard 排除不可放宽；只有 advisory 可退让，新 block 不能被旧 wrapper 的全候选 fail-open 撤销。可按已登记许可进行有限 half-open，否则按原预算等待/终止，不复活官方硬拒绝对象。
- **hard 绑定**：legacy 保留原有效 previous_response 特例。adaptive 对不可迁移绑定不删除绑定、不换号；如果该绑定目标被真实硬保护阻断，只允许同目标受控恢复/等待或终止。guardian/session/其他提前选择分支同样需要 parent coverage，未覆盖不宣称 enforced。
- **回滚与迟到事件**：旧请求仍持有原 owner 引用，不能在新 epoch 解除 Guard 或自动放量；观测可保留版本化历史。管理端按有效 mode/owner 展示，混合在途状态作为独立历史信息，不合并成一套有效裁决。

## 进程内与共享状态边界

### 允许进程内保存

- parent 冻结 request/attempt 上下文、owner/lease/reservation 引用、六态 commit 与独立 client_state。
- 短期无副作用的本地读取缓存和最近 snapshot，用于降低调度读取延迟。
- 本地计算中的候选 score components；进程重启后可丢失，不得承担唯一熔断依据。

### 必须可共享或可重建

- adaptive owner 的 account/model/protocol/workload 硬 Guard、state_revision、cooldown 和 half-open fencing 状态；legacy 是否进程内沿用其原合同，不为 shadow 强制迁移。
- 关键质量窗口的计数、有效样本数和恢复样本。
- workload 池的全局保留容量和 reservation 去重状态（若跨实例共享同一上游）。

共享状态通过核心抽象端口与外部 adapter 注入，不把 Redis/数据库实现带入核心或官方 handler。更新校验 parent owner/epoch fencing 与局部 state_revision；迟到请求可清理其旧 lease，但不能恢复新 epoch 的健康状态。共享状态暂时不可用时：

- 新的软评分可退化为 neutral/unknown；
- 现有未过期硬 Guard 继续生效至 deadline；
- 不能因一次读取失败立即解除摘除；
- workload 子配额失败按本设计补偿和 parent 的受控 fail-open/fence 合同处理，不能吞旧并发错误或自行重试；token reservation 仍只由 缓存容量 child 管理。

## 失败、边界与回滚

- 无效输入：provider/model/protocol/workload/cache 摘要不符合白名单时归入 `unknown`；不根据分组名、模型名或错误文本猜 workload/cache。
- 低样本：adaptive 只输出 low_sample/有界 penalty；legacy critical_sample 行为在 disabled/shadow 不改变，两种 owner 不能同时裁决。
- 数据异常：负延迟、超过观测范围、倒退时间戳或重复 outcome 被拒绝并计数，不污染窗口。
- 取消与提交：客户端取消、无首 token 断流、首 token 后断流必须按现有 commit state 处理；不把客户端责任当账号 TTFT 失败，不重复释放 reservation。
- 工具续接：存在 continuation 约束时优先保持原 session/账号能力；本 child 不授权跨账号透明重放。
- 共享状态不可用：保留最近有效硬 Guard 到期，软信号降级为 neutral/unknown；记录脱敏的状态不可用原因。
- 并发重复：snapshot 使用 owner/state_revision CAS，子配额按稳定 lease 引用去重；原 release 句柄与 缓存容量 child token 操作各自只由唯一 owner 执行，重复取消/完成不能负计数。
- 部分失败：质量旁路写失败不阻塞业务；新子配额失败不替代旧并发错误合同，清理竞争按同一桥接补偿。不可因新 store 错误重新 Select、切 Guard owner 或放宽原硬准入。
- 模式切换：默认 disabled；普通变更仅新请求生效，单调 control_epoch 与 policy_revision 分离，回滚新 epoch 引用旧策略，不恢复旧 epoch 数字或重置在途预算。
- 紧急 fence：只撤销未发送 attempt 并幂等清理/终止，不能重建 legacy 执行；已发送请求的 unknown token 由 缓存容量 child 保守窗口处理，不全额释放为零消费。fence 也不清除已提交的工具/语义状态。
- path safety：本 child 不创建文件、不写请求正文、不引入新的日志路径；若后续实现落盘诊断，必须沿用现有固定安全路径和权限边界。

## 验证设计

验证只定义 seam，不提前生成 system tests 或 implementation tasks：

- bucket/identity seam：输入 normalize 前原始身份与安全摘要，观察 unknown、折叠和 cardinality；未知 provider 不因官方兼容 OpenAI 路由污染已知质量域。
- workload classifier seam：观察各流量类型的稳定优先级；shadow 模拟不能占业务资源或重分类其观察对象，probe 缺少隔离覆盖不得自发抢业务槽。
- quality/facts seam：验证 attempt 首 token 去重、send 关联、衰减、低样本和业务/probe 隔离；计费前 UsageFacts 显式 missing/zero/input 包含关系，真实 miss 不因 ForceCacheBilling/售价变化变成缓存命中。
- single-token-ledger seam：以 缓存容量 child 端口 spy 观察同一请求/token 只由其估算和结算，TTFT/流量隔离 child 只调用 workload 子配额；缺失或未知事实不按零消费退款。
- guard-owner seam：观察 legacy/adaptive 的有效报告、exclusions、half-open、恢复及管理端读取，任何请求只一个 owner；低样本与单条三倍 critical_sample、全候选 fail-open、hard previous_response、关闭高级调度均有明确兼容边界。
- lease-compensation seam：原限额 1、同 upstream 多 Key、LoadFactor 与 Concurrency 不同、WaitPlan、子配额拒绝、取消/终检/fence 等路径，观察真实 Acquire/Release 次数、account/upstream 二选一和幂等补偿；新 fail-open 不吞旧错误。
- epoch/rollback seam：请求冻结 mode/control_epoch/policy/registry，普通切换只影响新请求；回滚新 epoch 引用旧 policy，旧 owner 清理不写新健康状态，紧急 fence 不重建旧预算。
- official-feedback seam：旧无快照入口默认 legacy，带快照入口只调用一次共享实现且不调用旧整函数；异步反馈跨 mode 切换仍分派创建时 owner。spy 比较两个入口的原健康参数/条件/顺序/次数、实际 Guard dispatch 次数，禁止全局 mode/goroutine-local 取 owner；双观测不替代官方反馈。
- protocol/fencing seam：消费 parent 六态 CommitState 与独立 client_state，发送前重新验证绑定/恢复/容量许可，排队过期或已取消不得发送；终态不重复、不扩展透明重放或用户结算。
- import/whitelist seam：核心不依赖官方层/框架实现或可变对象；官方触点只用 parent 白名单，策略与 workload store 落 fork 文件，禁止 child 自行扩展 ConcurrencyCache/计费接口。
- observability/performance seam：只输出安全 bucket、owner、mode/epoch/policy/state_revision、来源/覆盖/计数及 reason。采样旁路有界，必要的准入 store I/O 单独受 deadline/超时约束；disabled 无新增 I/O，shadow 不写真实状态，不承诺跨 store 无同步延迟。

以上验证分别映射 proposal 的分桶/低样本、流量隔离、真实缓存输入、提交/计费兼容、共享状态与回滚成功标准。独立核心可用 fake clock/store、已有 fork Guard 与并发端口 spy 验证；后续实现按真实白名单触点收敛原 scheduling/Guard/concurrency/usage 测试及依赖检查。本阶段只校验文档，不生成 tasks/ST/UT、不运行构建或发布门禁，也不替代 parent 的灰度验收。

## Wiki 与长期合同落点

- `.wiki/03-模块指南/03-网关与上游.md`：记录 TTFT bucket、workload 隔离、cache unknown、硬/软 Guard 和恢复边界。
- `.wiki/03-模块指南/05-分叉扩展与兼容性.md`：记录通过通用 scheduler signal 接入、不得复制选择器和不得让 handler 依赖具体状态存储的约束。
- `.agents/skills/sub2api-fork-extension-audit/references/extensions.yaml`：如实现形成 fork 专属扩展入口，登记唯一 owner 和兼容性说明；本 design 不直接修改该文件。

### Proposal 表述协调

目标与原请求预算、故障域健康、缓存容量依赖保持不变；本次把交付顺序调整为 6，并在 proposal/meta 的依赖首位加入前置 fork 调度底座。proposal 的 fork Guard 复用落实为前置窄端口委托，account quality 仍只读展示，不把展示分数或账单缓存率当调度事实。concurrency 影响范围为经前置端口接管原 lease 加独立 workload 子配额，而非直连 ConcurrencyService 或在官方缓存中存新状态。最小样本规则约束本 child 的新 adaptive Guard，不改变 disabled/shadow 的旧 critical_sample；冻结 Guard owner 与 private shared Report helper/guardDispatch 仍由本 child 拥有，不提前挪给前置。以上不改变 proposal 的目标/成功标准，也不扩大官方写集。

## 参考边界

- 来源：parent research `scope-and-delivery.md`
  - 目标落点：现有 scheduler、TTFT Guard、并发、缓存、failover 和测试入口
  - 采用方式：direct migration of constraints
- 来源：parent `split.md` 的“官方兼容与共同合同”，以及 缓存容量 child `design.md` 的 token 账本/UsageFacts 消费合同。
  - 目标落点：共享类型/模式/epoch/官方白名单引用，单 token owner 与 workload 增量边界。
  - 采用方式：引用 SSOT，不复制公共模型或容量公式。
- 来源：`.tmp/fork-extension-audit/design-decoupling-9449571f7e6b-98d86915beca/report.md`，D10-D12 及 D04/D07-D09。
  - 目标落点：原 lease 补偿、计费前事实、fork Guard 单一有效 owner 和官方反馈保留。
  - 采用方式：rewrite，沿用已完成固定审计，不重跑机器审计。
- 来源：官方 `98d86915becae9fe9491a91ffc6defd5235c8d2b` / fork `9449571f7e6b03d93c29775ba9cf9d1e892dd2c5` 的 scheduling/concurrency 及 fork `openai_ttft_guard.go`、`account_quality.go`、`upstream_scheduler_concurrency.go`。
  - 目标落点：官方主体只保留白名单薄接入；fork 扩展经前置窄端口衔接，现有副作用由唯一 owner 执行。
  - 采用方式：官方只读兼容证据，fork 入口局部整合，不把同 package 新文件误称完全解耦。
- 来源：Netflix concurrency-limits、Envoy、LiteLLM/Portkey/NewAPI 研究
  - 目标落点：延迟趋势、负载隔离、最小样本、恢复退避、缓存/容量和预算思想
  - 采用方式：inspiration only，不复制实现
- 来源：child `research/**` 与 `research/codegraph.md`
  - 目标落点：当前不存在 child 独立 research/codegraph 文件
  - 采用方式：fallback to parent research、CodeGraph status 和已核实源码入口；实现前必须重新获取定向图证据

## 回滚

按 parent 控制合同，回滚在新 control_epoch 下引用旧 policy_revision，后续新请求使用目标模式；不能移除仍有在途引用的 sampler/owner/lease 清理能力，也不能让请求中途从 adaptive owner 跳回 legacy。

新请求恢复 legacy 前须验证原有效保护仍可解释；隔离双写的 legacy 观察不自动成为新 epoch 的恢复/放量证据。旧请求完成仅记历史并清理原 lease/子配额，token 始终由 缓存容量 child 原 owner 结算。紧急 admission fence 只撤销未发送 attempt 并终止，不清除已提交状态、不重建旧预算、不全额退回已发送 unknown。

保留聚合历史、原健康/绑定/usage/并发记录，不回滚数据库。旧状态按所有在途引用、fencing 与有界清理窗口完成后再失效；禁用新信号不改变官方报告函数整体副作用。

## CodeGraph-derived design constraints

- entry points and call paths: 本轮 `codegraph impact ReportOpenAIAccountScheduleResult --depth 1 --json` 返回 19 节点/17 边；固定审计核实 HEAD `openai_account_scheduler.go:2867` 无 ctx 地调用 fork `reportOpenAITTFTGuard`。显式快照入口/私有共享反馈薄接入是拟议符号，不是图中已有实现；旧签名默认 legacy，fork dispatch 按快照路由，原健康主体共用一次。parent 白名单已经精确限定此 hook，实际实现仍需验证参数、顺序、次数及兼容性，不得据此扩大原报告函数职责。
- ownership and dependency boundaries: 核心包/桥接/状态实现按 parent 分层，缓存容量 child token owner 不变，TTFT/流量隔离 child 仅 workload 子配额。TTFT/健康/共享并发新增调用仅通过前置窄端口，account quality 保持展示职责；前置只机械保留 legacy，新 Guard owner 及 private shared Report helper/guardDispatch 留在本 child；不能为避免 fork 内部依赖而把策略搬入官方 concurrency/scheduler 主体。
- impact radius: 只新增 fork 策略/桥接/状态适配，必要官方观测和意见调用限定在 parent 函数白名单。`openai_ttft_guard.go:234` 的 critical_sample、:585 的全候选 fail-open、:581 的硬绑定特例均由模式 owner 明确处理；不是改官方 Guard，因为该文件在固定官方基线不存在。
- affected tests: 深度 1 图确认旧 Guard 单账号 fail-open、hard/movable previous_response、关闭高级调度仍反馈、upstream-only eligibility、原成功清除模型瞬态故障等兼容入口；并发影响图另确认共享 target 原测试。仅登记验证边界，不产生 ST/UT/tasks。
- rollback boundary: 普通模式切换只影响新请求，新 epoch 引用旧 policy；原 owner 执行在途清理，迟到事件不得解除新 Guard。新的 fail-open 不能吞原并发错误或绕过 parent hard 排除。
- graph evidence vs source verification: child 独立 `research/codegraph.md` 不存在；采用 parent research 与上述固定 SHA 报告作为 fallback。沿用既有低深度只读 impact，本次仅同步文档依赖，未 reindex、未重跑机器审计；图的测试邻接不是未来行为已通过的证明。
- unresolved items: 分桶阈值/衰减和子配额比例需后续验证，所有 provider/protocol/transport/selection 分支及原事实覆盖按 parent coverage 登记。私有共享反馈 hook 的设计范围已明确，运行时兼容尚未验证；active Guard 状态迁移证据不足的路径保持冻结 legacy/unavailable，不能把 owner 决策留到请求中途或以 unknown 静默清保护。

## 可扩展性设计

workload、provider、protocol、cache profile 和 reason code 都不是封闭集合。它们属于可注册的内部维度，不能通过核心 sampler/guard 中的固定枚举分支来定义全部未来行为。

- **Registry/adapter 扩展**：每个维度通过 registry 注册 canonical name、版本、能力声明、归一化函数、unknown fallback 和兼容策略。原始身份在官方 normalize 前保留，provider adapter 只映射通用事实；能力变化不能扩大核心总预算、接管官方反馈或产生第二个 Guard/token owner。
- **Versioned bucket schema**：bucket key 和 snapshot 带 `schema_version`。新增维度或改变归一化规则时生成新的版本，旧版本仍可读取、统计和解释；读取器必须支持旧 schema，并将缺少的新维度映射为 `unknown` 或父 bucket，不能因升级直接丢弃旧质量数据。版本迁移采用显式 adapter，不在请求路径隐式重写历史 bucket。
- **开放集合与 unknown 保守池**：未知 provider、平台、protocol、workload、cache profile 或 reason code 进入对应的 `unknown` 保守池，并带 `unknown_reason`。unknown 允许被观测和参与中性/有限软评分，但不能单独触发 hard Guard、清除已有 Guard 或获得缓存/质量奖励。只有经过 registry 声明且通过 adapter 可信映射的值，才可进入专用 bucket。
- **Reason code 兼容**：reason code 使用稳定字符串和版本化语义；新增 code 默认按 `unknown/neutral` 处理，旧读者不得因无法识别而扩大封禁范围。停用或重命名 code 时保留 alias 映射和原始安全标签，避免历史诊断失真。
- **维度缺失与降级**：当某一 adapter 无法提供 cache profile、stream 或 reasoning 信息时，只降级该维度，不把整个请求改写成其他 workload。bucket 构造器允许按维度折叠到父级，但折叠必须可解释，并在 snapshot 中保留缺失字段和匹配模式。
- **基数与注册治理**：registry entry 必须声明允许的基数、标签长度、父级折叠规则和敏感字段过滤器。禁止把原始 URL、模型正文、Key、Token、Cookie 或任意用户输入直接作为 bucket 维度；超过基数上限时进入有界 `other`/`unknown` 池并计数。
- **核心逻辑无 provider-specific 分支**：TTFT sampler、分位数聚合、workload 子配额和 Guard 状态机只消费通用 capability/result、缓存容量 child 容量摘要、版本化 bucket 和 allowlisted reason，不包含 provider switch 或私有协议判断；不重建本 child 的 token 容量模型。
- **安全演进**：新维度遵循 parent 的冻结 mode/epoch/registry 合同，先 neutral/旁路、再经验证允许 enforced；schema_version/state_revision 不能充当可回退控制 epoch。兼容读取不会把旧观察自动转换为新 epoch 的健康恢复证据，回滚新 epoch 引用旧 policy，原 owner 收尾。
