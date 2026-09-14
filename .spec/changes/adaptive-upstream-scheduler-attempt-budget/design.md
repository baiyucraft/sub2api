# adaptive-upstream-scheduler-attempt-budget 设计方案

## 方案概述

本 child 交付请求级预算限制器，不交付第二套选号器、重试执行器或计费账本。目标是降低未来 upstream merge 冲突：策略核心位于独立 `backend/internal/adaptivescheduler/` 包，官方类型仅在 fork 桥接层转换，官方生命周期只保留父级批准的少量触点。

本 child 调整为第 3 个交付，直接依赖 fork foundation 和归因 child；新增底座后合计七个 child，原六个策略 child 的 ID、相对顺序与既有策略依赖保留。预算仍位于归因之后、健康之前，不接管公共事件/事实的所有权。

共同合同唯一来源为 `../adaptive-upstream-scheduler/split.md` 的“官方兼容与共同合同”章节。本设计直接消费其中的值对象、模式、标识、正交的提交/客户端状态和版本规则，只细化预算模块的消费方式；不建立另一个公共 schema 或公共枚举来源。官方触点和桥接文件前缀遵守已完成的父级白名单，不由本 child 自行扩张合同。

### 前置底座消费边界

- 消费 [fork foundation 设计](../adaptive-upstream-scheduler-fork-foundation/design.md) 的值对象及健康、恢复、global retry 等 legacy 窄端口，不直接访问 `GlobalUpstreamHealthRegistry`、全局 recorder 或具体 legacy Service；只有底座兼容 bridge 可映射原类型并委托原 owner。
- `backend/internal/forkscheduling/` 只放底座值对象和窄契约，旧状态及纯规则位于其 `legacy/` 子包；两者均不 import service、handler、repository、Ent、Gin、Redis 或 adaptivescheduler。`adaptivescheduler` 只能单向依赖契约包，禁止引用 `forkscheduling/legacy` 具体实现，不把预算执行或新策略反向放进底座。
- 底座不定义 `Outcome`、`UsageFacts`、`mode`、`epoch`。公共事件/事实仍由归因 child 按 parent 共同合同拥有和实现；预算消费共同身份/事实，缓存容量 child 仅拥有容量预留和结算，不重定义 UsageFacts。
- 底座 default legacy 保留原健康、probe guard 和 global retry，不以 Noop 全关。预算只读取其有效重试策略并施加已授权上限，不另装全局策略、不重新解析历史账号字段；上游全局空值默认 `401/403/429`、普通账号原语义及原执行 owner 均保留。
- 新预算 disabled/shadow、归因 `unknown` 或观测故障不跳过旧健康或取消官方阻断；底座来源覆盖风险不由预算修复，也不通过新错误分类扩大行为修复授权。

官方 `FailoverState` 保持原有重试副作用执行权，包括计数、等待、账号排除、临时处理和缓存计费标记。新预算只允许、限制或否决已有动作，不返回自行执行的同账号重试/切号计划，不复制 `HandleFailoverError` 或各 provider 内部重试循环。

```text
逻辑请求 + 请求冻结策略
  -> 官方准入/选号/槽位 + fork 预算桥接
  -> 账号尝试 attempt_id
       -> 官方 Forward 内部循环
       -> 外部发送 wrapper 核验并登记 send_id
       -> 原 HTTPUpstream / 已覆盖的协议发送入口
  -> 官方结果处理、FailoverState 和协议终态
  -> 原 usage 提交与货币结算（身份独立）

不可变生命周期副本 -> adaptivescheduler 预算观测/限制
原 usage 调用结果   -> 只读关联观测，不反向授权计费
```

本次按固定审计报告 D01-D03 修正执行权、发送计数和计费边界，同时遵守 D04/D07/D09 的旁路、官方触点和 epoch 约束。不改公开 API、数据库、生产配置或现有计费授权。容量 reservation/settlement 仅由缓存容量 child 所有，不把它变成用户货币结算。

## 接口与稳定合同

### 共同类型与三层标识

- 消费归因 child 按父级合同拥有的 `SchedulingRequestContext`、`SchedulingAttemptContext`、`SchedulingOutcomeEvent`；`Gateway*Attribution` 只是归因视图，不能成为预算模块的第二套输入合同。
- `request_id` 关联一个逻辑网关请求；`attempt_id` 关联一次进入账号转发流程的尝试。同账号重新进入外层 Forward 也生成新 attempt，同一次 Forward 内部重发不伪造新的账号尝试。
- `send_id` 关联每次应用层上游网络发送，绑定一个 request/attempt；内部 HTTP 重试、协议修复后的重发分别登记。账号尝试数不能代替发送数，TCP 重传等传输实现细节不作为新推理发送。
- billing identity 由官方计费产生并保留；调度三层 ID 只能与之建立只读关联，不覆盖、重算或截断官方身份。一个调度请求不构成“只允许一个 money-event”的约束。
- 现有官方 `Account`、`UpstreamFailoverError`、`ForwardResult` 等对象不增加调度必需字段；fork bridge 从它们复制有界值，不把指针或原始 body/header map 交给核心。

### 请求冻结模式与控制版本

直接引用父级的 `disabled`、`shadow`、`enforced`，不另设语义不同的 off/compatibility 开关。关闭意味着关闭本扩展，不是关闭已有 fork 功能后退回纯官方版本。

| 请求冻结模式 | 新预算行为 | 对原有调用的约束 |
| --- | --- | --- |
| `disabled` | 不建立新账本或资源许可，缺省装配可直接委托原实现 | 原选号、Forward、重试、等待、健康反馈、usage 的次数、参数、顺序及副作用不变 |
| `shadow` | 只用副本记账和计算 would-deny，不实际保留容量或阻断 | 不额外调用 Select、HandleFailoverError、上游发送或 RecordUsage；不改变原参数、deadline、错误对象、回调次数和实际等待 |
| `enforced` | 对已声明且验证覆盖的动作执行附加上限或否决 | 不创造官方不允许的动作，不追加健康处罚，不复制官方执行器 |

模式、policy revision、预算上限和能力覆盖快照在请求入口一起冻结。`control_epoch` 单调递增，表示控制发布身份；`policy_revision` 标识可复用的不可变策略内容，回滚产生新 epoch 引用旧 revision，绝不把 epoch 回退。以上字段的共同规格仅由父级协调，公共事件/事实实现仍归归因 child，不迁入前置底座。

普通模式/策略更新只作用于新请求。在途 handle 继续使用开始时的快照、原 deadline 和已消耗额度，不因新 epoch 丢弃自己的有效 outcome，也不能写入新 epoch 的控制状态。late event 必须匹配原 handle 和原 event identity，不通过全局 epoch 相等检查误杀正常在途结算观测。

可选紧急 admission fence 是父级定义的独立撤销机制：发送前校验其单调撤销状态，只能阻止尚未发送的 attempt/send 并进入原 owner 的清理/终止；不切换模式、不转移 owner、不清零计数、不重建账本，不追溯重发或取消已经产生的官方计费事件。

### 预算内部状态与限制接口

核心只保留本 child 的预算状态，不重复列出共同事件 schema：冻结请求快照引用、额外账号尝试/发送额度、已消费计数、去重的事件及许可引用、原 deadline、等待区间、共同 `commit_state`/`client_state` 事实和不可重开的预算终态。`client_state` 的事实来自原请求 context 与协议 owner，不在预算模块另设取消状态类型。

- 官方 `SwitchCount`、`SameAccountRetryCount`、`FailedAccountIDs`、`ForceCacheBilling` 的可写所有权仍属于原流程。账本可以保存只读观测值用于诊断和限制，不能回写同步或建立第二个可执行副本。
- 等待按实际起止区间归因到用户队列、账号/共享槽位或官方重试等待，嵌套区间不得重复累计；核心不 sleep。总 deadline 和总等待额度分开，推理运行时间不冒充等待。
- 账号尝试许可在官方准入/利润终检通过后、进入 Forward 前核验；预算拒绝不是账号失败，不通过排除账号后重选来循环绕过。尚未发送的准入失败与真实发送失败分别记录。
- 发送额度在实际委托前原子预留，以唯一 send/operation identity 消费。成功或失败的已发送请求都耗费额度；只有得到明确未发送证据的预留才能幂等撤销，结果未知不得当成未发送退款。许可过期、重复使用或 fence 撤销后不允许发送。
- 事件去重不等于网络重放缓存：重复观测不重复扣额度，重复调用发送入口不能复用同一许可，也不能复用已读 response body。

下列是预算私有操作职责示意，不是新的跨 child 公共 API：

| 操作 | 输出/作用 | 明确不做 |
| --- | --- | --- |
| `Open` | 引用父级请求/策略快照，创建本地 handle；disabled 无操作 | 不改官方请求 ID、计费身份或配置 |
| `CheckContinuation` | 对官方下一步返回无意见/允许上限/否决及安全原因 | 不选账号，不调用旧执行器模拟动作 |
| `TryBeginAttempt` | 账号尝试许可与诊断关联 | 不增加官方切换计数，不抢第二个槽位 |
| `TryBeginSend` | 发送级原子许可、剩余额度和时间限制 | 不发送，不生成 provider 请求 |
| `ObserveWait/Commit/Outcome` | 合并去重观测及共同 commit_state/client_state 事实，更新预算消费 | 不再次等待，不再次触发健康/TTFT 反馈 |
| `ObserveBillingResult` | 关联官方已有调用身份和结果 | 不调用计费，不接受或签发货币结算 token |
| `Close` | 关闭新许可，保留有界诊断 | 不阻断 late usage，不接管资源释放 |

已覆盖路径在 `enforced` 下优先检查：原客户端取消、紧急 fence、deadline、提交安全、确定的不可重试 outcome、额外尝试/发送/等待上限。仅能否决或收紧；官方零值/显式重试上限、停止动作和硬拒绝始终不能被新层放宽。

未知 reason、未知 retryability 或未知能力在 disabled/shadow 只影响扩展诊断，不取消官方原有重试和健康处理。在 enforced 的已覆盖重试点，无法证明安全的新重试可以保守否决；未知本身不追加账号处罚。未知 provider/未覆盖路径的模式在入口即按父级覆盖规则冻结为 shadow/unavailable，不在请求中途偷偷退回旧路径。

### 官方重试单 owner 与等待

外层重试副作用仍由官方 `FailoverState` 唯一执行；各 provider 的原地重试仍由原官方循环执行，新预算没有第三个执行循环。两层的实际发送由同一个请求预算许可约束。

- fork bridge 对一次原失败处理最多委托一次 `HandleFailoverError`，观察实际前后状态；不得为了 shadow 或获取“候选动作”先调用一次再撤销副作用。真实上游结果仍进入原健康报告，预算否决只作用于后续等待/重试，不以 unknown 为由跳过整个结果处理。若原函数内混有报告与续试动作，限制钩子必须在父级批准的具体动作前，不能把整个函数替换为新处理器。
- 不复制 `sameAccountRetryAllowed`、退避、503 排除重置、利润否决和缓存标记算法。官方动作若没有无副作用的提议出口，只能加总发送/deadline限制；更细的等待/切换前限制须使用父级白名单内的最小钩子，未验证前标记该维度 coverage 不完整。
- 核心不创建重试定时器。enforced 的 fork wait bridge 可在保留原客户端取消链的前提下给原等待施加更短 deadline，并观测区间；不能“预算先等一次，官方再等一次”。disabled/shadow 不改变原等待参数或 context deadline。
- `Retry-After` 是下一次发送最早允许时间，不得裁剪成更短等待后提前发送。官方已有等待满足时沿用；剩余时间不足则否决。官方未实现该等待且无批准的原等待触点时不新增平行 sleep，记录不支持并按覆盖策略处理。
- 本地预算耗尽/撤销在 fork bridge 映射为不可重试终止，不构造 `UpstreamFailoverError`，不交给官方临时封禁或 failover 重试分支。下一次 send 即使被内层循环再次请求也不得放行。

### 提交与取消边界

直接消费父级 `CommitState` 六态：`uncommitted`、`http_committed`、`heartbeat_only`、`semantic_committed`、`tool_continuation_committed`、`terminal_committed`，以及共同 `SchedulingOutcomeEvent.client_state`。取消状态单独列为 `client_state`，与六态提交状态正交；两者均引用父级合同，本 child 不另建 enum 或第二套状态模型。

原请求 context 与协议 owner 提供取消事实，bridge 映射为共同 `client_state`；预算同时读取它与 `commit_state`。客户端取消不清空、覆盖或倒退已经发生的提交状态，也不因提交终态而丢失取消事实。该合同已在父级确定，不需要本 child 再定义公共字段。

writer/protocol owner 先记录已提交事实，再产生 outcome 或核验续试。未提交也不等于必然可重放；HTTP header/heartbeat 之后只能保留官方已允许且有明确安全证据的继续行为。新能力声明不能把官方禁止的切换变成允许。语义/工具/终态提交后不允许新透明重放；已经在途的上游和官方计费按既有规则完成。

### 网络发送覆盖边界

优先在不改变签名的 `HTTPUpstream.Do/DoWithTLS` 外注入 fork wrapper，不把预算方法塞入官方接口。不读取请求正文来计数，不接管 provider body 修复；response body 的读取/关闭所有权保持原样。

disabled 分支直接委托原方法一次，保留同一个请求/context、TLS/代理参数、body 和返回的 response/error。shadow 也只委托一次，从现有安全关联或旁路事件取得观测，不为携带 handle 重新构造请求、派生 context、添加 header、读取 body 或包装错误；某路径无法不改参数地关联 request/attempt/send 时标为观测不完整，不假装已经具备 shadow 全量计数。enforced 才能在批准边界施加额外 deadline/发送否决，且仍保留原取消链。诊断通过有界非阻塞 observer 输出，不把第二次原调用当作观测实现。

| 路径 | 发送级接入与当前设计限制 |
| --- | --- |
| 经 HTTPUpstream 的 Messages/Responses/Chat/Gemini | wrapper 覆盖每次委托；内层 Forward 重发产生不同 send_id，不只在 handler 记一次 |
| Antigravity smart retry/单账号原地重试 | 复用同一个 wrapper 与请求 handle，不复制其重试循环 |
| DoWithTLS 内部退回 Do | delegate 保留原实现，wrapper 不自递归，避免一次实际发送计两次 |
| HTTP 客户端内部 redirect/fallback/自动重发 | 一次 Do 未必只有一次网络发送；未接入实际分发观测的路径为 partial，不能宣称发送总额已完整保护 |
| WebSocket ingress/egress、重连、每轮推理发送 | HTTP 握手不等于模型发送；需 fork 协议发送桥接及逐路径能力证明，首版未证明路径保持 shadow/unavailable |
| 官方 PluginManager/OAuth 特殊传输 | 不是通用调度插件入口；绕过 HTTPUpstream 的发送必须单独登记，不能假定 wrapper 已覆盖 |
| 自建客户端、健康探针、后台批任务 | 没有本请求 handle 时不混入业务预算，不因全局注入而改变原调用；另行授权覆盖 |

开启 enforced 前，以每条真实发送/等待出口的覆盖证据校验能力，而非只看平台名称。wrapper 能挡住网络不代表已停止官方内部等待：预算终止必须由批准的桥接/最小终止检查向内外层传播，避免被当普通 transport error 再等待或记健康失败。尚不能证明终止传播、取消链或嵌套发送覆盖的路径不允许宣称 enforced。

### Usage 与容量的独立身份

不提供 `SettleUsage` 计费接口，不向 `RecordUsage`、`UsageBillingCommand`、usage worker 或计费事务增加调度必需字段/settlement token。`ObserveBillingResult` 只是既有调用前后结果的有界副本，不决定是否调用、不去重官方调用、不代为重试或补偿。

保留官方 `(request_id, api_key_id)` 事务去重、request fingerprint 冲突、billing request ID 解析优先级及独立 money-event 身份。多个 attempt 可以关联官方结算，但不能要求一个调度 request 永远只对应一次货币事件，也不能把“上游可能产生消耗”转换为新扣费规则。

官方异步任务、取消后的 detached billing context、失败日志兜底和现有任务丢弃/必达语义不变。账本已关闭或观测丢失时，原 usage 仍可执行；不假定现有 worker 提供新的持久补偿队列，不为补齐观测重新提交 RecordUsage。

缓存事实只从归因 child 拥有的计费改写前不可变 UsageFacts 副本获取；不能用 `ForceCacheBilling` 改写后的 cache-read 或 usage log 推断真实命中。预算只持有 opaque capacity reference，容量扣减/结算/释放由缓存容量 child 独占，容量差额释放不是用户退款。

## Ownership 与数据/文件流

```text
原 handler / selector / concurrency / Forward / FailoverState
  -> fork bridge 复制共同值对象
  -> adaptivescheduler 请求预算 handle
  -> 已覆盖动作前的允许/限制/本地终止意见
  -> 原 owner 单次执行或原 owner 清理/终止

HTTPUpstream 外部 wrapper -> 每次 send 的预算许可 -> 原 Do/DoWithTLS
原 usage owner -> 原计费调用/事务 -> 只读结果关联
缓存容量 child -> 容量 reservation/settlement -> 预算仅保存 opaque reference
```

| Owner | 独占职责 | 不得转移给预算核心的职责 |
| --- | --- | --- |
| `adaptivescheduler` 请求预算 handle | 附加额度、send 许可、去重观测、时间限制和不可重开的终态；单请求串行/CAS 推进 | 不 sleep、不选号、不持有可执行的官方状态副本 |
| 官方 `FailoverState` / 原 provider 循环 | 各自原有 retry/switch、等待、排除、临时处理及缓存标记 | 不变成新预算的存储 adapter，不移植其算法 |
| 原 handler / writer | 原取消链、真实 commit、协议输出、原结果处理与生命周期清理 | 不交出 writer 给分类器，不由预算伪造成功/usage |
| fork bridge / transport wrapper | 值对象映射、请求 handle 关联、已批准动作限制、本地终止信号转换；只委托一次 | 不复制官方流程，不通过回调把官方业务服务暴露给核心 |
| 原并发 owner 与现有 fork 并发桥接 | 接管已经取得的 `Acquired/ReleaseFunc`；账号槽与共享上游槽二选一，按原生命周期释放一次 | 预算 child 只保存引用，不再次抢槽、不接管 Redis lease、不改变原 fail-open |
| 官方 usage / billing owner | 原身份解析、任务提交、事务去重、fingerprint、扣费/日志/兜底 | 不接受调度 settlement token，不受预算 Close 或观测失败阻断 |
| 缓存容量 child | 唯一新增 token 容量账本、预留、未知用量处理、幂等结算与差额释放 | 预算 child 不扣 token、不退款、不授权用户货币结算 |
| 归因 child / observability | 公共事件/事实、共同 outcome 的归因视图及有界诊断 | 不替代旧健康回调，不把丢失观测当成可重新发送 |

### Fork-only 落点与官方触点

以下均为后续实现的拟议新文件，本轮不创建代码；名称沿用父级 `adaptive_scheduler_*` 桥接前缀：

| 拟议落点 | 内容与依赖方向 |
| --- | --- |
| `backend/internal/adaptivescheduler/attempt_budget.go` 及预算私有辅助文件 | 独立核心；消费归因公共事实及底座 forkscheduling 值对象/窄端口，不 import service/handler/repository/Ent/Gin/Redis |
| `backend/internal/handler/adaptive_scheduler_bridge.go` | 父级共用协议桥接中的预算部分；映射 context/commit、对原 failover 施加限制，保留协议 owner |
| `backend/internal/service/adaptive_scheduler_transport_bridge.go` | 实现现有 HTTPUpstream 接口的外部 wrapper，受请求冻结模式和实际路径覆盖约束 |
| `backend/internal/service/adaptive_scheduler_usage_bridge.go` | 归因 child 拥有公共事实复制，缓存容量 child 经消费 adapter 使用；预算只订阅已有观测，不另加 RecordUsage 触点 |

官方可写触点只引用父级白名单中预算 child 的 `HandleFailoverError/HandleSelectionExhausted/sleepWithContext`、已列 handler 生命周期边界，以及共用装配入口；HTTP 接入是在端口之外包装，不修改 `http_upstream_port.go` 的声明。usage 触点由归因事实 owner 共用，不能七个 child 各插一次；新增底座不生产另一套 usage 事实。

`gateway_forward.go`、Antigravity、其他 handler、WS 和插件的定向源码证据说明覆盖需求，不构成新增修改授权。某条路径需要白名单外的终止检查、wait hook 或 send 关联时，先报告覆盖缺口并修订共同设计，再进入计划。把策略放进官方同 package 的新文件仍不算核心隔离。

## 正常流程

1. 原 handler 按原顺序执行鉴权、准入等流程。fork 入口冻结父级请求模式、各 revision/epoch、能力覆盖及预算；调度 ID 不替代原 billing ID。disabled 直接使用原路径，shadow 只建立独立的观测状态。
2. 原 selector 只执行一次真实选号；原并发流程取得/等待原槽位。bridge 从原生命周期复制等待区间和已有 lease 引用，不为预算采样再选号、再抢槽或再 sleep。
3. 官方准入和利润终检通过、即将进入 Forward 时核验 `TryBeginAttempt`。enforced 拒绝时，由原资源 owner 清理并终止，不排除当前账号后反复重选来绕过预算。
4. 每次已覆盖的真实应用层发送前，wrapper 使用同一请求 handle 核验取消、fence、许可和 `TryBeginSend`，然后最多委托一次原 Do/DoWithTLS。内层重发产生新 send；不以 account attempt 数代替。shadow 只观测，不改变传输参数、body、返回值或原 context。
5. 原 writer 先登记真实 commit，bridge 将它与来自原 context/协议 owner 的 client_state 分别写入共同 outcome 的副本。两个维度正交；流观测可以多条，attempt 终态只合并一次，send 计数不被终态去重抹去。原健康/模型恢复/TTFT 回调仍按旧调用链处理真实结果，不再次调用它们采样。
6. 原 failover/provider 循环决定其既有下一步，预算只在已批准动作边界增加限制。允许时仍由原 owner 等待或续试；同账号再次进入 Forward 生成新 attempt。未知安全性在 enforced 下可否决新增续试，但不追加惩罚，也不取消原真实错误处理。
7. 预算耗尽、fence 或不可重放状态走明确的本地终止边界，不进入普通 transport/failover 错误分支；未提交时用原协议错误出口，已提交时只写协议允许的终态，不补写第二个 HTTP 响应。
8. 原 usage 提交和 billing 无论同步、异步或 detached context 都按原语义继续。budget 只观察原身份/结果；缓存容量 child 消费归因 child 拥有的计费改写前 UsageFacts，结算其容量预留，不触发货币写入。
9. 原 owner 负责释放其资源，`Close` 关闭新的预算许可并保留有限诊断。迟到的原计费结果/旧容量清理仍可被关联，不重新打开请求，不给已发请求退还发送额度。

## 失败、边界与回滚

- 无效输入：策略负值、能力冲突在发布/入口准备阶段拒绝，不能被解释为无限上限；原请求未设 deadline 时 disabled/shadow 不增加 deadline。enforced 入口可以按批准策略冻结有限额外 deadline，但绝不晚于原有效 deadline。账本 ID 错配或许可无效在 enforced 下本地终止，shadow 仅标观测无效。
- 重复/并发：每个请求预算只由一个串行/CAS owner 推进，attempt/send 许可在委托前原子核验。重复事件去重与重复发送分别处理，不把重复回调当新额度，也不把重复发送视为可幂等返回旧 response 的操作。终态与 Close 不可重开。
- 取消/超时：原 context 链不被 background context 替换；取消事实通过共同 client_state 表达，与 commit_state 正交，enforced 只在批准的执行边界限制新动作。协议提交事实不被取消清空，已有 slot cleanup 与 detached billing 由各自旧 owner 完成。disabled/shadow 不改变原取消/超时行为和错误参数。
- `Retry-After`：是最早发送时间，不与总 deadline 直接做 min 后提前重试。有效等待超过剩余时间则拒绝；非法/负值归为 unknown，不转换为零等待。disabled/shadow 不替换官方解析/等待行为。
- 提交/工具：共同六态不是由 HTTP 状态码推导的重放授权，只有原协议 owner 的事实与已验证能力可支持附加决策。enforced 不新增语义提交后的透明重放；未知 commit 安全性只会收紧，不能扩大官方许可。
- 本地预算终止：fork bridge 使用可辨识的本地终止结果，不能映射为可重试 `UpstreamFailoverError` 或普通上游 429/502。必须验证内层循环不会吞掉信号继续 sleep、重发或处罚账号；未证明的路径保持 shadow/unavailable。
- 原真实结果与 unknown：unknown 不追加新健康处罚，不能消除原 `ReportOpenAIAccountScheduleResult` 等回调或既有临时处理。预算拒绝下一次动作不改写上一条真实上游 outcome；shadow 下分类结果不改变任何旧处理。
- usage 部分失败：原失败、任务队列容量、日志兜底和补偿能力保持原状；观测丢失可以丢失诊断，不重投 RecordUsage、不补造成功、不从调度层扣费。ledger Close 不影响原 usage 执行，不假定存在尚未实现的可靠补偿队列。
- capacity 部分失败：只有明确未发送才能由缓存容量 child 幂等撤销预留；已发送但 usage 不完整按其 unknown/有限保守窗口处理，不全额释放、不把预估作为实测。预算 child 不建立结算 token 或第二本 token 账。
- 预算/观测故障：disabled 不依赖新状态可用性，shadow 失败仅标 partial/unavailable。已冻结 enforced 的有效账本若损坏或丢失，不能重建为空后继续，也不能切回旧 owner；撤销未发送许可并清理/本地终止。首版不新增分布式预算存储，进程退出不自动重放在途请求。
- mode/epoch：普通变更只影响新请求；旧 handle/lease 保持原 owner 和创建时 revision。紧急 fence 是显式发送撤销，不是热切 legacy 路径，迟到事件不能更改新 epoch 的控制状态。
- 用户内容/基数：只记录有界 ID、安全原因、阶段、计数、耗时和只读结算身份引用；不保存明文 API Key、Token、Cookie、正文或完整错误。event/evidence 数量和长度受父级上限限制；request/attempt/send/billing ID 不作为 metrics label。
- path safety：运行时核心不接收用户路径、不写诊断文件、不读取本地凭据或任意 URL；导出交给既有受控观测管道。本轮仅同步本 child 的 meta.yaml/proposal.md/design.md，不随引用修改 parent、foundation、代码或 Wiki。

## 验证设计

以下只定义验证入口、可观察输出和成功标准映射，不生成具体 ST/UT 用例、tasks 或执行实现验证：

| 验证边界 | 验证入口与可观察输出 | 对应合同/成功标准 |
| --- | --- | --- |
| 独立核心/官方写集 | import/依赖约束及固定基线差异检查；核心只依赖值对象，桥接只在父级批准函数触点注入 | D07、proposal 非目标 21-22；同 package 新文件不能充当隔离证明 |
| 底座消费与默认策略 | 检查 forkscheduling 单向依赖、窄端口调用、归因事实 owner 与 default legacy 委托 | 不直连全局 registry/recorder/具体 legacy Service，不以 Noop 关闭旧健康、probe guard 或 global retry |
| 请求预算状态 | fake clock、共同值对象、有界 observer，检查许可原子性、重入/迟到事件、实际等待区间与终态 | proposal 28-29；核心无需 Gin、Redis、真实 provider 或 billing fake 授权端口 |
| disabled/shadow 精确委托 | 对比加入本模块前的当前 fork 调用轨迹：Select、Forward、HandleFailoverError、Do/DoWithTLS、原健康回调、RecordUsage 的次数/参数/顺序/结果及等待 | proposal 15、33；不通过第二次运行真实操作获取对照，不包装原错误、不偷改 context deadline |
| 原重试单 owner | 原 failover/协议循环的执行观测加预算限制端口；原 retry/switch、排除、临时处理、等待和 ForceCacheBilling 各只有一个执行者 | D01、proposal 29、31；附加限制只能否决或收紧，unknown 不抹去旧健康处理 |
| 发送与终止传播 | 在原 HTTPUpstream 端口下放置只记录委托的 transport，覆盖 Forward 内循环、Antigravity、DoWithTLS 委托、redirect/fallback；逐路径输出 coverage 和真实 send 序列 | D02、proposal 28-29；本地预算耗尽不能触发内外层续试、sleep 或新账号处罚 |
| 协议提交/取消 | writer/protocol observer 提供父级六态与共同 client_state，检查两个维度正交、已提交后终态及重放限制，不以 streamStarted 单值代替语义 | proposal 12、23、30；未知安全性不得创造透明重放许可 |
| 槽位与容量 owner | 观察原 Acquired/ReleaseFunc 和缓存容量 child opaque reference；已取得槽位只接管一次，账号/共享上游槽不双占 | proposal 13、31；预算拒绝不漏释放、不重新抢同槽，容量结算不触发货币事件 |
| 原货币结算 | 只读 billing observer 加原 RecordUsage/usage worker/事务兼容入口；比对原 billing ID、money-event、fingerprint、去重和失败兜底 | D03、proposal 21、31；不能把多个合法事件压成一次，不添加 settlement token，不额外提交或漏掉原调用 |
| 原始缓存事实 | 归因 child 的计费前 UsageFacts 副本、字段存在性与来源版本；对照 ForceCacheBilling/TTL 改写前后事实隔离 | proposal 16、34；调度预算不得从售价/倍率或改写后日志反推实测用量 |
| 冻结/回滚/fence | 请求快照、单调 control_epoch、旧 policy_revision 引用、在途 owner 与未发送许可撤销的状态轨迹 | D09、proposal 15、33；不重置 deadline/额度，不因旧事件切回新控制状态 |
| unknown/不完整覆盖 | registry 缺失、观测丢失、能力冲突的安全诊断及不同模式轨迹；WS/插件缺少桥接明确 unavailable/partial | 扩展不追加未知错误惩罚，disabled/shadow 不改变旧行为，未证明完整传播不能进入 enforced |

验收观测分开输出 request/account attempt/send 的计数、mode/control_epoch/policy_revision、coverage、commit、剩余额度、本地终止原因和只读 billing outcome。诊断不使用敏感正文或无限高基数指标，模拟结果标 would-deny，不冒充实际被阻止或真实成功。

受影响的已有验证入口优先为 `backend/internal/handler/failover_loop_test.go`、`gateway_handler_stream_failover_test.go`、对应协议 handler/forward tests、并发/helper tests、原 usage/cache/billing tests。发送 wrapper 的接口级委托验证与原内部循环的端到端传播验证不能互相替代。具体选择由后续 wiki-plan 依据最终白名单及覆盖清单确定；当前不运行这些测试。

本轮仅做指定文档的顺序、依赖、owner 和格式一致性检查；parent 正由主线程同步，不运行全局 validate，完整图验证由主线程整合后执行。文档检查不代表运行时兼容或真实路径覆盖已经验证。

## Wiki 与长期合同落点

- `.wiki/`：未来实现/review 完成后沉淀“官方网关生命周期与 fork 预算限制”长期合同，记录独立核心、函数级接入、三层身份、冻结模式与容量/计费 owner；本轮不创建 Wiki 页面，不搬运本次文档全文。
- fork extension audit catalog：未来登记实际最小写集和官方行为保护合同；目录被登记不能成为整文件重写许可，本轮不改 catalog。
- parent split：共同类型/事实规格、CommitState、registry、mode/epoch 和官方白名单的唯一协调来源；公共事件与 UsageFacts 实现由归因 child 拥有，底座独占 legacy 值对象/窄契约/纯规则，本 child 只维护预算私有接口。
- parent research：复用 `../adaptive-upstream-scheduler/research/scope-and-delivery.md` 的背景，源码归属以固定审计证据为准，不复制生产正文。
- 归因 child：拥有公共事件/事实，提供共同 outcome 的归因视图、UsageFacts 和安全 reason；不通过健康回调二次调用来采集，预算不反向定义分类。
- 缓存容量 child：独占容量预留/结算，消费归因 UsageFacts；预算 child 只持有 opaque reference，不提供第二个容量账本或用户结算权。
- shadow 发布 child：消费 would-deny、coverage 和终止指标，管理共同控制发布；本 child 服从请求冻结，不定义另一套热切换或回滚阈值。

## 参考边界

| 来源 | 目标落点 | 采用方式 |
| --- | --- | --- |
| `../adaptive-upstream-scheduler/split.md` 的“官方兼容与共同合同” | 公共 SSOT、函数白名单和跨 child owner | direct migration：仅引用合同，不复制 schema |
| `../adaptive-upstream-scheduler/research/scope-and-delivery.md` | 已有生命周期和历史 CodeGraph 背景 | inspiration only：以固定源码审计和新共同合同校正历史落点 |
| `.tmp/fork-extension-audit/design-decoupling-9449571f7e6b-98d86915beca/report.md` D01-D03、D04/D07/D09-D11 | 单 owner、send 层、计费边界及兼容约束 | direct migration：采纳已核实约束，不重跑审计脚本 |
| 固定版本 `failover_loop.go`、协议 handler、`http_upstream_port.go` | 原副作用/协议/传输不变，fork bridge 外部映射与限制 | inspiration only：理解接口和所有权，不迁移或重写原算法 |
| 固定版本 `gateway_forward.go`、`antigravity_gateway_retry.go` | 每次实际 send 与内部终止传播的覆盖约束 | inspiration only：覆盖证据，不扩大官方写集 |
| 固定版本 `gateway_service.go`、usage/billing、concurrency | 原失败零值、身份、事务及 lease 兼容验证 | direct migration：保留既有合同，绝非复制实现 |
| proposal 已列 Envoy retry host predicate / Netflix concurrency-limits | 重试放大和时间/并发分离的背景 | inspiration only：本轮未新增外部资料核验，不引入其实现作为依赖 |

## 回滚

回滚由父级控制发布产生更大的 `control_epoch`，可以引用旧 `policy_revision` 并把新请求冻结为 disabled/shadow。不是把 epoch 回退，也不是把当前请求的下一次 attempt 切给 legacy 账本。

- 普通回滚不撤销旧请求的策略快照、额度、deadline 或 owner；已持有 lease/容量的清理由创建时 owner 负责，原 late usage 仍可执行。
- 必须紧急停止在途新增发送时，发布独立 admission fence，原 owner 撤销未发送许可后清理/终止。已经发出的请求不因 fence 被重复提交、虚构未发送退款或重新生成 billing identity。
- 新预算服务/观测故障不能让 enforced 请求无预算继续；未发生故障的旧账本可按冻结策略完成，故障账本只能在本地停止新增动作，不以恢复默认值重开。
- 代码装配卸载只对后续请求取消新预算依赖；现有 handle 要完成或由 fence 终止后才能释放其实现。进程退出由原协议/cleanup 生命周期收尾，不引入在途恢复重放机制。
- 不删除 usage、余额、账号绑定、原健康历史或计费事务，不重置原 FailoverState。首版没有新持久化账本，不把清空未来存储设计成绕过额度的回滚步骤。

## CodeGraph-derived design constraints

本轮复用已完成只读审计的 pinned `git show` 证据，官方完整 SHA 为 `98d86915becae9fe9491a91ffc6defd5235c8d2b`，fork 完整 SHA 为 `9449571f7e6b03d93c29775ba9cf9d1e892dd2c5`；以下行号绑定这两个版本，不当成未来 HEAD 的永久行号。不重跑 audit 脚本、fetch 或更新 CodeGraph 索引。

### 固定源码决定的边界

| 官方/本地源码位置 | 已核实事实 | 本 child 设计约束 |
| --- | --- | --- |
| `backend/internal/handler/failover_loop.go` 官方 193-267 / 本地 193-268 | HandleFailoverError 有计数、sleep、临时处理及排除副作用，不是纯判断；既有差异仅一行 monitor 上报 | 保留执行者，只在父级批准动作点限制；禁止迁移算法和二次调用试算 |
| 同文件官方 79-102、310-324 | 原重试上限/OAuth deadline、503 排除重置、利润否决 ID 和 ForceCacheBilling 有既有规则 | 不放宽硬上限、不重新建立可写副本、不从新 unknown 覆盖这些语义 |
| `backend/internal/service/gateway_service.go` 官方 655-727 / 本地 660-732 | 官方 failure 类型未被当前 fork 修改；NextAccountAction 零值兼容旧 retry | 核心不依赖官方类型，bridge 映射；unknown 在 disabled/shadow 不变成 Stop，新本地终止不伪装成该错误 |
| `backend/internal/service/gateway_forward.go` 两版 378-390 | 一次 Forward 内可循环 DoWithTLS | account attempt 之外必须计 send；不通过重写 Forward 循环实现预算 |
| `backend/internal/service/antigravity_gateway_retry.go` 官方 374-406、531-544 / 本地 401-433、558-571 | provider 内有原地/外层发送循环 | 共用请求 send 许可，逐路径证明终止传播；wrapper 不代表所有内部等待已覆盖 |
| `backend/internal/service/http_upstream_port.go` 两版 11-23 | 已有 Do/DoWithTLS 接口 | 外部 wrapper 实现原接口，不加预算参数或新必需方法 |
| `backend/internal/repository/usage_billing_repo.go` 两版 35-109 | 官方事务 claim/去重及 fingerprint 冲突 | repository 不接入新 token，不更改 `(request_id, api_key_id)` 和原事务所有权 |
| `backend/internal/service/gateway_usage_billing.go` 官方 206-234、517 / 本地 223-251、534 | billing identity 解析、money-event 与 detachedBillingContext 已有独立规则 | 三层调度 ID 只关联，Close/取消不切断原结算或增设可靠补偿假设 |

### 图、依赖与验证约束

- entry points and call paths：CodeGraph-first 定位 `HandleFailoverError`，本轮仅补充 `impact HandleFailoverError -d 1`、`callers HandleFailoverError`、`callees HandleFailoverError` 的只读定向查询。图连接各协议入口、原 failover 及相关验证入口；覆盖范围须进一步按父级白名单和实际 send 出口判断，不以图节点数作为完整性证明。
- ownership and dependency boundaries：独立核心与 handler/service/repository 禁止反向依赖；官方 `UpstreamFailoverError/ForwardResult/Account/HTTPUpstream/UsageBillingCommand` 不增加调度必需字段。bridge 只传共同值对象，不把任意官方方法包装成可从核心调用的 callback；传输委托仅在外部 wrapper 中执行。
- impact radius：外层 retry、内部 sends、协议 commit、原健康和计费均可能受生命周期钩子影响。可接受的实现写集是父级函数白名单的子集，不因为 CodeGraph 关联某文件就授权修改该文件；官方源码变化优先局限在 bridge 和最小触点适配。
- affected tests：沿用上文验证入口，尤其核对禁用/旁路的调用等价、原 retry 副作用、nested send、本地不可重试终止、旧计费身份和原 lease 清理；新增验证不得依赖生产流量或必须启用 Redis 才能证明核心行为。
- rollback boundary：控制面新 epoch 与请求冻结 handle，而非“撤掉 adapter 后旧请求重走一次”。已发/已占用/已提交事实不能因关闭 feature 被遗忘，旧 billing 及缓存容量 child 的清理各按原 owner 完成。
- graph evidence vs source verification：泛型/同名符号、动态委托及陈旧索引可能连错边；图只能定位，具体 sleep/commit/send/计费顺序以固定审计中的官方和本地源码为准。本轮没有重跑全图 affected/trace，也不把历史索引未同步数量写成当前事实。
- child research/codegraph fallback：当前 child 无独立 `research/codegraph.md`，使用 parent research 的历史图记录、上述浅层定向图查询和已核实 pinned 源码审计作为 fallback；不创建 research 文件、不 reindex。WS/PluginManager、未在白名单的 handler/内部终止点、HTTP 内部重发与细粒度等待尚缺覆盖证明，必须在能力清单中保留 partial/unavailable。
- extension boundary：注册 reason/capability 只能扩展父级可选证据和动作前置条件，不能增加官方必需字段、重试执行器、provider switch、账单去重规则或白名单外的发送钩子。没有覆盖证据不得以 registry 声明代替验证。

### 已收敛的范围解释

- `proposal.md:42` 的“一次逻辑请求对应一次结算”是影响范围措辞，不是新增货币产品目标。本设计通过只读观察原计费调用及其正常幂等来落实：保留原 billing identity、money-event 与事务去重，不新增计费授权，不把多个合法货币事件压成一次；缓存容量 child 独立负责容量幂等结算。这是已收敛的范围解释，不构成 proposal 目标变更。
- `proposal.md:15` 的“回退到旧逻辑”落实为仅对新请求生效，不转移在途 owner；第 38、68-73 行的适配/rewrite 落实为 fork 外部桥接，不移植官方算法。第 39-41 行所列源码按已完成的父级白名单约束，不构成追加官方触点授权。
- `proposal.md:16`、第 28-34 行的全请求预算/不重复计费保留为最终验收目标，不把每次真实 send 的容量消耗压成一次，也不以仅有 HTTP wrapper 宣称所有路径完成。实际残余项仅为未覆盖协议、内部发送/等待和终止传播的证据，不降低成功标准。

上述范围解释保持不变；本轮仅同步本 child 的 proposal/meta/design 与前置依赖，不新增任务或扩大实现授权。parent 与其余 child 由主线程整合，完整依赖图待其统一验证；运行时覆盖仍须按本设计的 partial/unavailable 边界验证后才能进入 enforced。

## 可扩展性设计

### 官方隔离优先于 provider 抽象

独立 `adaptivescheduler` 核心只消费归因公共事实、底座 forkscheduling 值对象/窄端口和受版本控制的能力，不 import 官方类型或框架。新版本官方改变错误结构、ForwardResult 或调用顺序时，应优先修订兼容 bridge 的映射及窄触点验证，不能把变化扩散到公共事件必需字段或复制官方状态机。

平台/provider/上游差异由父级 adapter/capability registry 注入；预算核心不得编写 provider switch，也不把闭包形式的 Select、HandleFailoverError、RecordUsage 作为扩展端口。核心回答的是“原动作是否还满足附加预算与安全条件”，不是“下一步执行哪套选号/重试算法”。

### Reason 与策略版本只引用共同注册表

错误分类采用父级的稳定责任大类、可注册 versioned `reason_code`、有界 evidence map 和 unknown fallback；不是封闭的 provider 错误枚举。本 child 消费归因 child 已归一化的 retryability、scope、等待及安全证据，不另设 reason_id/profile/capability schema 作为第二份公共 SSOT。

- 新 reason 不需要修改旧事件字段；扩展证据必须可选、有长度/数量上限，不能把自定义错误字符串作为强制动作代码。
- reason/capability/预算配置的发布受父级 registry_revision、policy_revision 和单调 control_epoch 管理；已打开请求继续使用冻结版本，回滚只以新 epoch 引用旧策略。
- 注册冲突、版本未知或缺少强证据不能默认为可重试、放大处罚范围或扩展故障域。预算没有账号健康写入权。
- 新预算维度属于本 child 私有策略内容；需要新增共同值对象字段时由父级一次定义，不能改官方 Account、UpstreamFailoverError、ForwardResult 或 billing 命令来满足它。

### 能力与覆盖是不同约束

幂等发送、Retry-After、流续接、工具 continuation、usage/cache 完整性和 precommit 安全只是能力前置条件；能力为真不代表相关官方执行路径已经接入。

覆盖按父级的 provider/protocol/entry path/transport/lifecycle 维度声明，包含 account attempt、每次 send、内部重试、wait、取消、本地终止和清理的实际出口。新增 provider 若完全复用已证明覆盖的端口，可扩展 registry/adapter；需要新官方触点时必须先修订父级白名单，不能以“插件扩展”绕过审计。

HTTP wrapper 未覆盖的 WS、PluginManager、自建客户端或内部重发保持 partial/unavailable；不因平台名称相同而继承 enforced。原 provider 循环仍执行其旧动作，新 adapter 不增设循环或第二个等待器。无法解析 usage/cache 只形成 unknown UsageFacts，不改官方费用、不奖励未知缓存命中。

### Unknown 必须区分模式

| 请求冻结模式 | unknown/证据缺失的扩展行为 | 原路径保证 |
| --- | --- | --- |
| disabled | 无新预算决策或自动重试 | 原调用参数、次数和错误处理不变 |
| shadow | 有界诊断、would-deny 或 unavailable；不产生新动作 | 不取消原 retry、健康、模型恢复或 usage，不重跑旧回调 |
| enforced 且该动作已完整覆盖 | 无法证明安全则否决新的 attempt/send，并由原 owner 清理和协议终止 | 不追加未知错误账号处罚；上一条真实上游结果的既有处理仍保留 |

未知 provider/能力不足在入口按覆盖策略冻结为 shadow/unavailable；不能先运行 enforced 再因 unknown 改走 legacy 路径。如果强制路径在请求中途出现未知新字段或丢失必要证据，保留原账本消费，只能拒绝新的动作，不清零。扩展本身绝不基于 unknown 自动创建重试。

### 向后兼容与扩展验证

可选字段缺失与真实零值分开；预算私有维度未配置时不在 disabled/shadow 增加限制，也不默认解释成无限额度。enforced 只能使用发布时完成版本/能力校验的维度，老 adapter 不具备所需事实时明确覆盖不足，不靠猜测补值。

扩展验证复用上文设计入口：检查核心无 provider 分支及官方反向依赖，旧事件缺少可选字段时兼容，registry/profile 冻结不发生 ABA，未知错误不会增加处罚或第二次 retry，能力声明不掩盖传输缺口。原健康回调、并发 release、货币结算身份和调用轨迹仍是兼容对照；容量 settlement 仍由缓存容量 child 独占。本节不建立新的计费授权、补偿队列或具体测试任务。
