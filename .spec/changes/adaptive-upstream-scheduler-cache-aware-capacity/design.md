# adaptive-upstream-scheduler-cache-aware-capacity 设计方案

## 方案概述

在 `backend/internal/adaptivescheduler/` 内设计 `CacheAwareCapacity` 策略与 `CapacityReservationStore` 抽象端口。缓存容量 child 是新增 token 容量需求、预留、结算和差额释放的唯一账本 owner；TTFT/流量隔离 child 只增加 workload 子配额，不能再次估算或预留同一份 token。现有计费、RPM、真实并发槽位和选号主体保持各自 ownership。

共同身份、事件、模式、控制面、官方触点白名单及桥接规则引用 parent [split.md 的“官方兼容与共同合同”](../adaptive-upstream-scheduler/split.md#官方兼容与共同合同)，不在本 child 另设公共 SSOT。核心不得 import `service`、`handler`、`repository`、Ent、Gin 或 Redis 实现；fork 桥接只通过前置底座窄端口消费已有健康、TTFT 和共享并发扩展。官方文件仅允许 parent 白名单内的薄观测/既有桥接接入，不能把新容量策略、存储状态或必需参数扩散到官方类型。

本 child 的交付顺序调整为 5，首先依赖前置 fork 调度底座，并保留错误归因 child 依赖；请求预算、重放和提交语义只消费 parent 共同合同，不引入对请求预算 child 实现的反向依赖。接入使用前置底座提供的窄端口：只接管真实选择已取得的 `ConcurrencyTarget`、`Acquired` 和 `ReleaseFunc`，不把一次 Select 当成纯候选查询，也不再抢一个真实槽位。新增 token 准入与 workload 子配额全部成功后才向原执行链交付许可。

模式遵循 parent 的请求冻结合同，本 child 的局部行为如下：

| 请求冻结模式 | 本 child 的行为 |
| --- | --- |
| `disabled` | 不执行新增估算、store 读取或预留；保留当前 fork 全部既有保护与副作用。 |
| `shadow` | 只消费真实选择边界的一次性不可变快照并做纯计算；无快照返回 unavailable。不得二次 Select、抢槽、Reserve、写真实计数或向上游发送；预测不能冒充实测 usage。 |
| `enforced` | 在可信 capability 与事实映射覆盖的路径上追加 token 准入；新增模块失败只能撤销自身限制，不吞掉原并发/RPM/权限错误，不重置请求或 lease owner。 |

## 接口与稳定合同

### 前置底座衔接

本 child 依赖[前置 fork 调度底座设计](../adaptive-upstream-scheduler-fork-foundation/design.md)，对现有 fork 能力只消费其值对象和窄端口。新增调用（包括本 child 的桥接调用）不得绕过端口直读 global registry、具体旧 `openAITTFTGuard` 或 `ConcurrencyService`；原实现访问只留在底座外的 legacy 适配层，不因接入新策略扩散。

`backend/internal/forkscheduling/` 只放底座值对象和窄契约，旧状态及纯规则位于其 `legacy/` 子包；两者均不 import `service`、`handler`、`repository`、Ent、Gin、Redis 或 `adaptivescheduler`。`adaptivescheduler` 只消费契约包，禁止引用 `forkscheduling/legacy` 具体实现或反向依赖；Outcome/UsageFacts 仍由错误归因 child 按 parent 共同合同负责，mode/epoch 由策略与控制层负责，前置底座不定义或依赖这些对象。

前置只机械委托现有实现，默认装配 legacy，不改变现有 TTFT/健康算法。新增策略 `disabled` 保留当前 fork 真实行为及原配置开关，不能装配 Noop 全关旧保护。现有 TTFT `exclusions` 会推进 probe 试放计数，真实负载读取可能执行 Redis 过期清理，均不得由 shadow 再调；旁路仅消费真实路径已产生的一次性副本，无副本即 unavailable。

共享 slot 沿用原 `Acquired/ReleaseFunc` 和排队生命周期，不新增第二次真实获取或释放；account RPM 仍是账号维度，不因共享上游 slot 被解释或合并为共享 RPM。质量展示的评分与账单缓存率不是计费前真实 cache facts，不能通过底座端口包装后改称事实输入。


以下只定义 缓存容量 child 自有的容量合同；公共字段的类型、身份和版本语义以 parent 为准，不新增公开 HTTP API、数据库字段或官方 handler 必需参数。

### 请求上下文

消费 parent `SchedulingRequestContext` 的请求身份、冻结模式、`control_epoch`、`policy_revision`、deadline，以及 `SchedulingAttemptContext` 的 attempt/send 身份与执行状态。`CommitState` 仅引用 parent/请求预算 child 既有六态，取消单列 `client_state`，不能把取消改写为某个提交态。容量侧只附带 demand、capability 引用和脱敏 target 描述，不持有 Gin context、官方 Account/ForwardResult 指针或请求正文。

fork adapter 必须在官方 platform normalize 之前保留原始 provider/upstream/deployment 身份，按 parent 的可信 registry 映射为安全身份。官方把未知平台兼容归一为 OpenAI，不代表该上游获得 OpenAI 缓存能力。映射未知时本扩展返回 NoOpinion/unknown，不给缓存奖励、不写已知 provider 容量域，也不改变原路由。

### Provider capability

```text
ProviderCapacityCapability
  status                       // known / partial / unknown
  tokenizer                    // provider tokenizer / unavailable
  supports_cache_read_usage
  supports_cache_write_usage
  cache_read_capacity_factor   // 非货币容量权重，可为 1 或 provider 专用值
  cache_write_capacity_factor  // 创建/写入容量权重
  uncached_input_factor
  output_capacity_factor
  reasoning_capacity_factor
  tpm_scope                    // account / key / deployment / site / unknown
  quota_window                 // 配额窗口及已消耗量的保留规则；未知不能强制限流
  cache_domain                 // 可复用缓存的物理域、有效期及版本
  input_accounting             // input 与 cache-read/write 的包含或互斥关系
  output_includes_reasoning    // true / false / unknown
  max_context_tokens
  max_output_tokens
  usage_event_shape
  capability_version
```

`cache_*_capacity_factor` 不是价格、倍率或用户扣费系数。provider 未明确说明时必须是 unknown，不能使用经验值给 cache-read 奖励。capability 只解释通用维度、配额和事实口径，不修改 parent 总预算或旧并发语义；物理 quota 身份不因 capability/policy 版本变化而获得新的全额配额。

### 需求估算

```text
CapacityDemandEstimate
  context_tokens              // 已知上下文总量；未知时为空
  cache_read_tokens           // 预计可复用的缓存量；未知时为空
  cache_write_tokens          // 预计需要创建/写入的缓存量；未知时为空
  uncached_input_tokens       // 未命中输入；未知时为空
  max_output_tokens           // 请求/策略/模型上限后的保守值
  reasoning_budget_tokens     // reasoning effort 或 provider 上限映射
  capacity_units              // 按 capability 转换后的非货币容量向量/总量
  confidence                  // exact / estimated / partial / unknown
  source                      // request_metadata / continuation / tokenizer / adapter
  warnings                    // 有界枚举，不放原始请求内容
```

映射为互斥输入桶后，已知时满足 `context_tokens >= cache_read_tokens + cache_write_tokens + uncached_input_tokens`；原始 input 是否包含缓存分项由 capability 与 UsageFacts 明示，不能重复相减或把负值静默截断为零。不满足时返回 partial 与保守上界。输出包含 reasoning 时不得再把 reasoning 加到同一总输出配额；可分别约束独立维度，但不能重复计数。

### 预留生命周期

```text
CapacityReservation detail    // 公共 envelope/身份引用 parent；这里仅为容量明细
  reservation_id              // 桥接一次分配的稳定 ID，引用 parent request/attempt/send
  target_id                   // account/upstream/fault-domain 脱敏 ID
  capability_version          // 创建时冻结，用于解释；不另造物理 quota 池
  demand                      // 预估向量
  reserved_units              // 各维度预留量
  actual_units                // 已确认 usage；可 partial
  state                       // reserved / settled / released / expired / unknown
  expires_at
  settlement_source           // immutable_usage_facts / unknown_expiry
  release_state               // 只表示容量差额释放，不是用户退款
```

服务接口至少包含：

```text
Estimate(ctx, request, candidateSnapshot) -> DemandEstimate
Reserve(ctx, request, adoptedLeaseRef, demand) -> Reservation or AdmissionDecision
Settle(ctx, reservationID, usageFacts, outcome) -> SettlementResult
Release(ctx, reservationID, reason) -> ReleaseResult
```

`Reserve`、`Settle`、`Release` 均必须幂等。稳定 ID 不因控制面热更新重建；重复 reserve 必须比对已冻结需求，冲突返回诊断错误。请求可能发生内层重发，桥接按 parent 的实际 send 边界续订/分配明细，不能仅靠外层 attempt ID 把多次真实消耗去重成一次。`adoptedLeaseRef` 是值引用，真实 `ReleaseFunc` 只留在 fork 桥接，不进入核心或 Redis。

### 计费前不可变 UsageFacts

只消费 parent `SchedulingOutcomeEvent` 引用的不可变 `UsageFacts`，不新建另一套公共 usage 事件。fork usage adapter 在 provider usage 解析后、任何 `ForceCacheBilling`、cache TTL 分类覆盖、下游 JSON 分类或其他计费改写前复制事实；浅拷贝可变指针不满足隔离。若现有调用边界已经发生改写，必须通过 parent 白名单的薄观测点提前采集；未覆盖路径保持 unknown，不从后续数据反推。

本 child 对共同事实合同的消费要求：每个 token 字段有显式存在性，input 是否包含 cache-read/cache-write、output 是否包含 reasoning 有明确口径，记录原始安全身份、事实来源/完整性、创建时 capability 版本及 parent 事件身份。`0` 只表示明确报告的零，字段缺失不能默认零；完整、增量与累计 usage 必须由 adapter 标注并归一化一次。

禁止从 `ForceCacheBilling` 后的 `UsageLog`、`AccountStatsCost`、售价/倍率或改写后的下游 JSON 推断命中。官方 `account_stats_pricing.go` 是价格消费者，不是事实生产者；`ForwardResult.Usage` 也不能未经采集时点核验就共享给异步 sampler。Anthropic passthrough 的分类 helper 只改响应 JSON，不改该 usage 指针，但下游 JSON 已不再代表原始事实。

UsageFacts 供容量与 TTFT 只读消费，不新增计费授权 token，不改变 `RecordUsage`、money-event 身份或官方事务去重。容量预留差额释放不是用户退款，即使 proposal 使用“退款”一词也只对应此非货币行为。

## Ownership 与数据/文件流

```text
原始身份 + parent 冻结上下文 -> fork 桥接 -> 纯 Estimate
真实选择/原排队取得槽位 -> 接管 target/ReleaseFunc（不二次 Acquire）
  -> 缓存容量 child token reservation -> 可选 TTFT/流量隔离 child workload 子配额
  -> 原发送/反馈链 -> 计费前 UsageFacts -> 缓存容量 child Settle
  -> 桥接幂等补偿/完成 -> 释放未使用预留及原 lease
shadow: 只从一次性安全快照纯计算，不进入上述预留/发送链
```

ownership 约束：

- 核心 `CacheAwareCapacity` 唯一拥有新增 token 账本、差额释放及 admission 决策；不与 TTFT/流量隔离 child 各算一份。
- fork 桥接通过前置底座窄端口接管已获槽位并统一补偿；`upstream_scheduler_concurrency.go` 的具体实现仍由 legacy 适配层封装。真实 target 为 account 或 upstream 二选一，普通账号 LoadFactor 仅保留原排序语义，不能当硬并发上限；账号 RPM 口径不变。
- provider adapter 位于核心之外，提供安全身份、tokenizer/缓存能力和计费前事实映射；store 实现位于核心之外，拥有原子操作与清理，不理解计费。
- TTFT/流量隔离 child 只读本 child 的 demand/reservation 摘要，额外持有 workload 子配额；不得调用旧 Acquire 再占真实槽或操作 token 账本。
- 原 billing/pricing、handler、scheduler 保留其结算、协议、排队与选号 ownership。官方接入和 mode/epoch 路由只按 parent 白名单；旧健康反馈不能因容量结果缺失被跳过。

## 容量模型

### 多维需求而非缓存率奖励

容量计算至少保留以下独立分项，按 capability 的物理配额映射约束，不无条件将有包含关系的分项相加：

```text
input_uncached = uncached_input_tokens * uncached_input_factor
cache_read     = cache_read_tokens * cache_read_capacity_factor
cache_write    = cache_write_tokens * cache_write_capacity_factor
output         = max_output_tokens * output_capacity_factor
reasoning      = reasoning_budget_tokens * reasoning_capacity_factor
```

这只是 provider 容量约束的映射，不是计费公式。最大输出和 reasoning 使用保守上界；若 reasoning 已包含于输出上限，只对总输出预留一次，另有独立 reasoning 配额时才能增加独立约束。cache-read 是否计入 TPM、cache-write 是否独立成池均需可信 capability；语义未知不提供缓存折扣，不把价格换算为容量 factor。TTFT/流量隔离 child 只能读取这里的结果，不再复制此公式。

### cache-read 预估

优先级：

1. provider adapter 明确返回的可复用 token 数；
2. continuation/session 的前缀证据，同时确认候选共享同一 cache domain、能力版本和有效期；仅有会话亲和不能证明命中；
3. 请求 metadata 中由上游协议明确声明的缓存字段；
4. 无证据时 unknown。

不在容量层读取完整请求正文，也不基于用户可控的字符串相似度猜测缓存命中。估算结果必须带 `source` 和 `confidence`；换号、跨 deployment 或缓存过期后重新评估，不能把上一候选的缓存奖励直接转给下一候选。预测值和 UsageFacts 实测值分别保存。

### cache-write/创建成本

cache-write 是新建或刷新缓存前缀的容量需求，不因为历史缓存率高而自动降低。对于首次长上下文请求，adapter 可以把预计未命中前缀标为 `cache_write_tokens`；如果 provider 不报告创建语义，则保持 partial，不把全部输入静默记为 cache-read。

### 未命中输入、上下文、输出和 reasoning

- `uncached_input_tokens` 是未进入 cache-read 的输入部分，不等于原始 `input_tokens` 的必然值。
- `context_tokens` 用于检查模型上下文上限；缺失 tokenizer 时可使用协议/adapter 提供的上限，不能使用不受控的字符数猜测作为精确值。
- `max_output_tokens` 取请求值、分组/模型限制和 provider 上限的最小保守上界；请求未提供时使用 adapter 的安全默认值或标记 unknown。
- `reasoning_budget_tokens` 来自已归一化的 reasoning effort、模型能力或显式上限；不能从用户计费倍率推导。

## Redis/adapter 边界

### Store 接口

`CapacityReservationStore` 提供：

```text
ReserveAtomic(quota_scope, window, dimensions, reservation_id, expires_at)
SettleAtomic(reservation_id, facts_revision, actual_dimensions)
ReleaseAtomic(reservation_id, releasable_dimensions, reason)
GetSnapshot(target)
```

store 实现使用 Lua 或等价原子机制同时检查同一物理 scope 的维度、写 reservation 并更新计数；不同 scope 无法原子完成时显式登记已成功部分，由桥接补偿，不能伪称跨 store 事务。物理 quota key 按真实 account/upstream/deployment/site、维度和配额窗口区分，不用 capability version、`control_epoch` 或 policy revision 分裂成多个全额容量池。旧 reservation 保存原映射版本解释结算，新旧版本共用物理硬上限。

区分 inflight 预留和配额窗口内已消耗量。事实已确认的使用量保留到对应窗口结束；已发送且 usage 未知的量保留到声明的有界保守窗口，不能因 HTTP/semantic 尚未提交或客户端取消全额释放。只有已证实未发送或未使用的部分可立即释放；窗口身份/上界无法确定的 provider 不能进入 enforced。

Redis 服务器时间用于窗口与 expiry。过期 reservation 不能先被删掉再期待 aggregate 自动减少；store 在后续原子读写前进行有界 lazy cleanup，并保留足够的到期索引、去重及迟到结算 tombstone。过期通知只作提示，不作为正确性条件；空闲时无需 goroutine，下一次准入不得读取未经清理的旧计数。已发送 unknown 到期按窗口失效处理，仍记录 unknown，不伪造零 usage。

### Adapter 接口

`ProviderCapacityAdapter` 负责：

- 根据 normalize 前保留的可信原始身份及安全候选摘要返回 capability；
- 估算上下文、缓存读写、未命中输入、输出和 reasoning；
- 将计费改写前解析的 provider usage 深拷贝为 parent `UsageFacts`，不扩展官方结果类型承担新领域模型；
- 声明 usage 是否完整以及 cache 字段的可信度。

adapter 不访问用户余额、不计算用户实际费用、不更新 channel pricing、不写 usage log。事实来源缺失、已改写或只有 unknown provider 身份时返回不确定性，不按官方兼容路由标签猜能力。

### 接管、原子性与补偿

真实 lease 继续由原并发实现创建；fork 桥接将已有 `ReleaseFunc` 包装为请求局部的一次性释放句柄。原 handler 与扩展清理都只调用这一句柄，不再各自保留可独立执行的原 callback。`Acquired=false` 的 WaitPlan 继续由原排队链取得槽位；排队前可估算，但不能重复 Acquire 或预留假想真实槽位。

接管后按 token reservation、可选 workload 子配额的顺序完成附加准入；全成功才允许发送。任一明确拒绝、发送前 deadline/取消或紧急 admission fence，撤销已取得的未用子配额与 token 预留，最后调用原 lease 句柄一次。后续清理竞争、handler defer 和迟到结果只执行各自尚未完成的幂等步骤；一次原 lease 释放不代表未知远端 token 扣减已成功补偿。

本地 callback 的一次执行与分布式配额操作不是跨系统原子事务。store 超时后先按稳定 reservation ID 查询/重试同一操作，不换 ID 再扣一份；仍未知则保留有界占用并诊断，不确认其已释放。清理使用独立有界 cleanup context，不能被已取消的请求 context 阻断。token/workload 清理失败不妨碍调用原真实 lease 释放；进程异常沿用各自原 lease 与新增 store 的到期机制。

新的 fail-open 只表示不追加新增容量限制。不得掩盖原并发申请错误、把原 RPM 已消费量退款或复活官方硬拒绝候选；也不得再次执行 Select 来“恢复”当前请求。需要换候选/重发时仍由 parent 预算和原执行者决定，新 attempt 不继承上一 attempt 已消耗量为可用余额。

## 正常流程

1. fork 桥接在官方 normalize 前保留原始身份，读取 parent 冻结请求/attempt 合同；不重复解析正文或修改旧路由。
2. 原选择和排队流程保持原副作用与硬限制顺序。`disabled` 无新增操作；`shadow` 仅从一次性快照估算，不能调用有副作用的 Select。
3. `enforced` 对已取得的目标槽位执行接管，adapter 按该候选估算；未知映射返回 NoOpinion，不从历史缓存率推导奖励。
4. 缓存容量 child 取得唯一 token reservation，TTFT/流量隔离 child 如启用则取得额外 workload 子配额。发送前检查 parent fence 与原最终准入，失败统一补偿，绝不二次抢真实槽位。
5. 原转发、commit 和健康反馈继续执行；桥接将计费改写前的 UsageFacts 一次归一化并关联对应 send/reservation，原计费独立按原合同执行。
6. 缓存容量 child 按已知实际量 Settle 并释放已证实未用差额；只有完成/取消/原执行终止时清理 lease，不因首 token 提前释放。未知已发送消耗保留有界窗口。
7. 重试、failover、迟到事实和回滚均使用原 owner/稳定身份；不转移已消耗容量、不改变 money-event，也不新建一套旧路径预算。

## 失败、边界与回滚

- 无效输入：负 token、cache 分项大于上下文、超过 provider 明确硬上限时拒绝该 capacity decision；不修改用户请求，不把错误写成账号健康失败。
- 未知 capability：不使用 cache-read 折扣，不把 cache-write 当作零；在入口按覆盖门禁冻结为仅观测/未启用该维度。运行中丢失可选能力只返回该新增维度 NoOpinion，保持原路径既有检查、冻结模式和 owner；必需硬许可无法验证则拒绝本次发送，不在途中切回 legacy 或重建预算。
- tokenizer 不可用：使用 adapter 的保守边界或 `unknown`；不能在容量层读取完整正文做临时猜测。
- Redis 不可用：本扩展默认受控 fail-open，只撤销新增限制并记录 `reservation_store_unavailable`；原并发错误仍返回，已有 reservation 的 owner 和未知占用保留。不能重放 Select、回退整条请求或清空其他账本来制造可用性。
- 部分 usage：已确认维度 settle，已发送未知部分保留有界配额窗口；不以零结算，不调用计费服务推断。迟到事实按原 reservation/facts revision 去重，不改当前控制面。
- 重复/并发：稳定 reservation ID 与原事实身份去重；control epoch 只 fencing 控制写入，不能让一次真实发送在新 epoch 再申请一份。能力版本只解释维度，不拆物理配额。
- 客户端取消：独立检查 `client_state` 与实际发送状态；即使 CommitState 仍 uncommitted/heartbeat_only，已发送未知消耗也不能全额退回。已确认未发送则统一补偿；提交后不得透明重放。
- 首 token 后断流：清理真实 lease 与 workload 子配额，token 已知量按窗口保留、未知尾部保守到期；不得重复工具调用或用户结算。
- provider capability 更新：新 reservation 使用新 version，旧 reservation 按创建时 version 结算，不回写旧记录。
- 用户内容：不记录请求正文、API Key、Token、Cookie 或完整错误文本；诊断只保存脱敏 target、模型名、枚举原因、token 聚合和 bounded sample。
- path safety：本 change 不创建文件；任何离线 shadow 导出若后续需要，必须由独立运维 change 管理固定目录和路径 containment。
- 控制切换：普通 mode 切换只影响新请求；parent 单调 `control_epoch` 与可回退 `policy_revision` 分离，回滚用新 epoch 引用旧策略。在途请求、预算及 lease/reservation 不换 owner，不清理仍被引用的旧版本数据。
- 紧急停止：parent admission fence 可撤销未发送 attempt，执行上述幂等补偿并终止，不能重建旧路径或重置已消费预算。已发送请求仍按原事实/窗口结算，不因 fence 获得全额容量释放。

## 验证设计

验证 seam：

- capability/facts adapter seam：区分 known、partial、unknown、missing 与显式零；核验 input 包含 cache-read/write 及 output 包含 reasoning 的归一化、capability 版本和 normalize 前原始身份。
- immutable usage seam：观测计费改写前副本；真实 cache-read=0 且 ForceCacheBilling=true 时仍为 miss，修改账单倍率/售价/TTL 分类和下游 JSON 不改变容量事实。异步消费者不得共享可变 Usage 指针。
- lease adoption seam：在原并发限额 1、同 upstream 多 Key、普通账号 LoadFactor 不等于 Concurrency、WaitPlan 与终检拒绝边界，观察原 Acquire/Release 次数、target 二选一和子配额补偿。原并发错误不得被新增 fail-open 吞掉。
- single ledger seam：TTFT/流量隔离 child 只消费 demand/reservation 摘要；token Reserve/Settle 只进入 缓存容量 child，一次真实 send 不因多条流事件或热更新重复记账。
- store/clock seam：观察部分写入、timeout 后结果不确定、稳定 ID 重试、原子过期清理、迟到结算、多实例窗口和能力更新；相同物理 quota 不因新版本增容。
- modes/fence seam：disabled 对当前 fork 无新增操作，shadow 不二次 Select/Reserve；普通 mode 切换不更换在途 owner，回滚新 epoch 引用旧 policy，紧急 fence 仅清理未发送 attempt，不重建 legacy 预算。
- cancellation/commit seam：消费 parent 六态 CommitState 与独立 client_state，分别验证未发送完整补偿、已发送 unknown 有界保留及终态幂等；记录清理失败但仍释放真实 lease。
- import/official-boundary seam：核心依赖图不得出现 service/handler/repository/Ent/Gin/Redis 或计费回调；官方变更严格对应 parent 函数白名单，billing spy 确认旧结算身份、次数和副作用不变。

验证输出至少包括：parent request/attempt/send 与事件引用、reservation/lease 安全引用、冻结 mode/control_epoch/policy_revision、capability 版本、物理 quota/window、事实存在性/可信度、预测与实测分项、admission/补偿结果及 fallback reason。不得输出请求正文或凭据，也不能把 capacity release 标为用户退款成功。

成功标准映射：

- 需求向量与 unknown 语义对应 proposal 的缓存/上下文/reasoning 标准；
- 原子预留、非货币差额释放及已发送 unknown 有界保留对应 reservation 生命周期标准；
- disabled/shadow/enforced 与 Redis 故障回退对应兼容性标准；
- billing spy、现有 scheduler 测试和 usage 测试对应解耦与无回归标准；
- mixed workload fixture 对应 parent split 中长上下文、高 cache-read、首次 cache-write 和完全未命中验收。

## Wiki 与长期合同落点

- `.wiki/03-模块指南/05-分叉扩展与兼容性.md`：补充“容量准入与计费解耦”稳定合同、unknown/partial 口径和 reservation TTL 约束；实现完成并 review 通过后再更新。
- parent `adaptive-upstream-scheduler/split.md`：共同请求/attempt/结果、UsageFacts、epoch 和官方白名单保持唯一 SSOT；本 child 只细化 token 账本与消费要求，不复制公共类型。
- 不新增公开 API、数据库 schema、管理员配置页面或外部文档索引。

### Proposal 表述协调

目标、非目标和成功标准不变；本次同步交付顺序为 5，在原错误归因依赖之前增加前置 fork 调度底座，proposal/meta 同步该依赖，不改变其他策略 ownership。proposal:39 的 service 影响范围在此细分为独立核心与仅消费前置窄端口的 fork 桥接；proposal:42、69-70 对 account_stats_pricing.go 的来源建议经固定源码核验后修正为只读价格隔离证据，不作为 usage 生产者。proposal 的“退款”只指非货币容量差额释放。后续计划采用本设计，不能沿旧来源建议扩大官方写集或将 Outcome/UsageFacts 下沉到前置。

## 参考边界

- 来源：`../adaptive-upstream-scheduler/research/scope-and-delivery.md`
  - 目标落点：现有调度入口、usage/cache 字段及并发历史证据；默认模式与跨 child 约束以已修订 parent split 为准。
  - 采用方式：rewrite
- 来源：`../adaptive-upstream-scheduler/split.md`
  - 目标落点：“官方兼容与共同合同”的模式/身份/UsageFacts、原 lease、官方白名单及 mixed workload 约束。
  - 采用方式：引用唯一 SSOT，不重复定义。
- 来源：`.tmp/fork-extension-audit/design-decoupling-9449571f7e6b-98d86915beca/report.md`，D10/D11 及共同 D07-D09。
  - 目标落点：单槽接管、token 单一 owner、计费前事实及版本回滚边界。
  - 采用方式：rewrite；采用已完成的固定 SHA 审计证据，不重跑机器审计。
- 来源：官方 `98d86915becae9fe9491a91ffc6defd5235c8d2b` / fork `9449571f7e6b03d93c29775ba9cf9d1e892dd2c5` 的 scheduling/concurrency、`handler/failover_loop.go`、`gateway_usage_billing.go`、`openai_gateway_usage.go`。
  - 目标落点：官方槽位/提交/计费兼容事实；接入仅按 parent 白名单。
  - 采用方式：只读兼容约束，不复制或改写主体；fork `upstream_scheduler_concurrency.go` 仅由前置窄端口的 legacy 适配层封装，新增策略不直连具体实现。
- 来源：同一基线的 `account_stats_pricing.go` 与 usage 展示模块。
  - 目标落点：核实价格消费者和账单展示不能替代原始 UsageFacts。
  - 采用方式：反例/隔离证据，不作为事实生产者或容量依赖。
- 来源：Netflix `concurrency-limits`、LiteLLM Router、Portkey Gateway
  - 目标落点：delay/capacity admission、TPM 预留、Retry-After 和未知状态的设计参考。
  - 采用方式：inspiration only

## 回滚

采用 parent 的回滚合同：新请求可从 enforced 降为 shadow/disabled，控制面总是增加 `control_epoch` 并引用目标 `policy_revision`，不会回到旧 epoch。默认 disabled；普通切换不改变在途 request/attempt、token 账本、真实 lease 或 cleanup owner。

已建立的 reservation 仍接受原身份的事实结算和幂等清理；其完成事件不能改写新控制面。旧 capability 解释与 tombstone 至少保留到所有引用及迟到结算窗口结束，禁止按旧版本前缀一删了之。紧急 fence 只撤销未开始发送的 attempt 并终止，不建立另一套旧预算；已发送 unknown 保守到期，不回填零 usage。不回滚数据库、用户余额、既有 RPM 或原健康状态。

## CodeGraph-derived design constraints

- entry points and call paths: 本轮 `codegraph impact AcquireTargetSlot --depth 1 --json` 返回 5 个节点/4 条边，包含两个 scheduling 文件的 `tryAcquireAccountSlot` 与 `AcquireAccountSlot`。固定审计核实官方选择已取得 `Acquired/ReleaseFunc`；fork `upstream_scheduler_concurrency.go:158` 选择 account/upstream 二选一。接管发生在该已有返回/排队完成边界，不新增并发 API。
- ownership and dependency boundaries: 以 parent 包图/官方白名单为准；核心通过 `forkscheduling` 值与窄端口消费 fork 扩展，前置不反向依赖策略；具体状态/I/O 实现位于外部 adapter。`account_stats_pricing.go` 不是 usage/cache owner；`gateway_usage_billing.go:745` 的 ForceCacheBilling 改写前才允许采集事实，官方货币合同不变。
- impact radius: 新策略和 token store 只落 fork 核心/桥接/状态适配；官方 concurrency cache、Account/ForwardResult 必需字段、UsageBillingCommand 和价格主体不扩展。只使用 parent 已登记函数的薄观测/映射，不借 child 身份增加白名单。
- affected tests: 图确认 `upstream_scheduler_concurrency_test.go:TestAcquireTargetSlot_UnlimitedStillTracksAndReleases`；原 scheduling/concurrency、failover、usage/cache 和 parent import/触点兼容验证共同约束本设计。不生成 ST/UT 或任务清单。
- rollback boundary: 请求冻结模式；控制面新 epoch 引用旧策略，原 owner 完成 lease/容量清理，TTL 不代表已发送消耗为零；原健康/计费副作用保留。
- graph evidence vs source verification: 缓存容量与 TTFT/流量隔离 child 均没有独立 `research/codegraph.md`；fallback 到 parent research 与固定 SHA 审计，沿用上述既有低深度只读图查询，未 reindex、未重跑机器审计。图只证明定位/邻接，不证明未来原子性或全部 provider 覆盖。
- unresolved items: 各 provider 的 cache/TPM 窗口、usage 原始采集与 send 覆盖、tokenizer、跨 scope 原子支持须按 parent coverage 合同证明；未覆盖只允许 unknown/旁路，不得扩大官方触点或宣称 enforced 已可用。

## 可扩展性设计

### 可注册的 capability profile/version

`ProviderCapacityCapability` 不绑定 OpenAI、Anthropic 或其他固定平台名称。能力由可注册的 `CapabilityProfileRegistry` 提供，注册键由 provider、上游适配器、模型族或 deployment scope 组成，具体匹配顺序和 fallback 由 adapter 自己声明。每个 profile 必须带不可变的 `profile_id`、单调递增或可比较的 `version`、有效期和能力状态；reservation 保存创建时使用的 profile/version，结算时继续使用该版本，不因热更新重算旧预留。

profile 可以逐步增加新能力，例如新的缓存层级、批处理配额、图像/音频 token 或 provider 专属并发维度，但不能改变核心字段的既有含义。新增 profile 未注册、版本不兼容或声明不完整时，adapter 返回 unknown/partial，对该新增维度不追加意见；原准入始终保留，不启动第二条 legacy 链路，不切换在途模式或资源 owner。

### provider-specific usage extensions

parent `UsageFacts` 的扩展槽只接收 adapter 产生的有界不可变值，不另立 `CapacityUsageEvent` 或公共事件 ID。扩展示意：

```text
extensions: map[string]ProviderUsageExtension
  "provider.example/cache_v2": {
    schema_version: "v2",
    dimensions: { ... },
    completeness: "complete|partial|unknown"
  }
```

扩展只由对应 provider adapter 解释；核心 reservation/store 不需要理解扩展内容。扩展字段缺失、版本未知或无法验证时，核心字段仍按 `unknown` 处理，不能静默映射到 `cache_read_tokens` 或 `cache_creation_tokens`。扩展不得携带请求正文、凭据或未限制大小的原始 provider 响应。

### 可扩展 named dimensions

容量向量使用 extensible named dimensions，而不是固定长度数组：

```text
CapacityDimensions {
  "input_uncached": n,
  "cache_read": n,
  "cache_write": n,
  "output": n,
  "reasoning": n,
  "provider.example/custom_dimension": n
}
```

核心维度始终保留并维持当前语义：未命中输入、cache-read、cache-write/创建、output、reasoning。provider 扩展维度必须使用带 namespace 的稳定名称、明确单位、scope、factor 和 unknown 语义；不能复用核心名称表达不同单位。store 只负责按名称原子预留、结算、释放和 TTL，不决定维度含义。

扩展维度可以被 capability profile 标记为硬上限、软容量或仅观测。未识别的扩展维度在 `shadow` 中保留诊断，在 `enforced` 中不作为强制拒绝依据，除非存在兼容的 profile、版本和 store 实现。扩展维度不得改变稳定幂等身份、token 单一 owner、物理配额或非货币差额释放语义；更不能增加一次真实并发占用。

### 新平台/上游的默认行为

新平台或上游未声明 capability 时：

- 不提供 cache-read 容量奖励，也不根据历史缓存率推断奖励；
- 不因为未知缓存字段而强制拒绝请求；
- 使用既有 scheduler 的 RPM、并发、模型、协议和冷却硬约束；
- 在 `shadow` 记录 `capability_unknown`、缺失字段和估算置信度；
- 未覆盖的维度在入口不启用 enforced；仅保留原 admission，记录新增 reservation 未启用原因，不把此行为理解为在途切换模式或重新选号；
- 等 profile 注册并通过验证后，才允许该 provider 进入对应维度的强制容量控制。

这保证新 adapter 可以先接入真实流量观察，而不会因未声明的缓存语义误放量或误杀账号。

### 核心接口语义保持稳定

扩展只允许增加 profile、usage extension 和 named dimension 的解释能力，不修改以下核心语义：

- `Reserve` 表示一次带 TTL、可幂等的容量预留；
- `Settle` 只按计费前 UsageFacts 的已知实际量结算，释放已证实未用差额；
- `Release` 只撤销已确认未发送/未用的容量，已发送 unknown 服从有界保守窗口；
- unknown/partial 不等于零；
- 相同 reservation ID 的重复操作必须幂等；
- billing/pricing 不因容量 profile 或扩展维度获得反向调用；
- feature 关闭/profile 不可用只撤销相应新增限制，不重建在途 legacy 路径、预算或 owner；控制面版本仍引用 parent。

因此新增 provider、缓存协议或维度优先通过 registry/profile 和 fork adapter 扩展，不在核心写平台分支、不修改官方规范化或计费。扩展必须符合现有维度/窗口/事实合同；超出合同先修订设计，不以“adapter”名义改变核心账本或 parent 总预算。
