# adaptive-upstream-scheduler-fault-domain-health 设计方案

## 方案概述

在独立的 `backend/internal/adaptivescheduler/` 核心中实现故障域描述、健康状态机及抽象存储端口，通过 fork 桥接向现有 scheduler 提供过滤和软降级意见。核心不 import `service`、`handler`、`repository`、Ent、Gin 或 Redis，不接收其可变对象，也不复制选择器、重试循环、并发分配或计费逻辑。proposal 中 service/handler/cache 的影响范围在此限定为桥接与装配，不把策略核心放回官方 service 包。

本 child 调整为第 4 个交付，依赖 fork foundation、错误归因和请求级预算 child；新增底座后合计七个 child，原六个策略 child 的 ID、相对顺序及既有策略依赖保留。现有账号健康、持久冷却、管理员禁用和官方资格检查继续有效；新层不得恢复原路径已经硬拒绝的账号。没有注入、请求冻结为 `disabled` 或没有可信能力时，不追加调度动作，保留当前 fork 的原有行为，而不是关闭已有 fork 扩展后模拟纯官方。

共同类型、模式、控制面、排除和官方薄接入白名单唯一引用 [parent split.md 的“官方兼容与共同合同”](../adaptive-upstream-scheduler/split.md#官方兼容与共同合同)。本文件只定义故障域行为，不重新声明公共字段或扩充白名单；共同合同变更由 parent 协调并核对全部 child 的一致性。

### 前置底座消费边界

- 直接依赖 [fork foundation 设计](../adaptive-upstream-scheduler-fork-foundation/design.md) 的 legacy 健康/恢复/投影窄端口；本 child 不直接访问 `GlobalUpstreamHealthRegistry`、全局 recorder 或具体 legacy Service，兼容 bridge 可映射原类型并委托原 owner。
- `backend/internal/forkscheduling/` 只放底座值对象和窄契约，旧状态及纯规则位于其 `legacy/` 子包；两者均不 import service、handler、repository、Ent、Gin、Redis 或 adaptivescheduler。`adaptivescheduler` 只能依赖契约包，禁止引用 `forkscheduling/legacy` 具体实现、反向依赖或把新故障域状态机塞入底座。
- 底座不定义 `Outcome`、`UsageFacts`、`mode`、`epoch`；公共事件/事实仍由归因 child 按 parent 共同合同拥有，预算 child 消费共同身份，本健康 child 只拥有新增故障域状态与意见。
- 底座 default legacy 保持原健康、probe guard 和 global retry，不以 Noop 全关；新层 disabled/shadow 不关闭底座，也不把单一窄入口误当成旧来源已完全隔离的证明。

```text
既有真实结果 -> fork 安全映射 -> 独立故障域核心 -> 抽象分布式状态
真实选号边界 -> 一次只读候选副本 -> 健康意见 -> 既有选择器
既有槽位/等待/终检 -> 发送前恢复许可 -> 原发送生命周期 -> 带 fencing 的终态反馈
```

## 接口与稳定合同

### 共同输入与请求模式

消费 parent 的 `SchedulingRequestContext`、`SchedulingAttemptContext`、`SchedulingOutcomeEvent` 和候选/意见合同，不在本 child 复制公共类型 SSOT。请求开始冻结 `disabled/shadow/enforced`、`control_epoch`、`policy_revision`、`registry_revision` 及预算和资源 owner；普通配置切换不改变在途请求下一次 attempt 的模式。

- `disabled`：不执行新增过滤、摘除或 half-open 分配，不新增候选遍历、快照计算、后台任务或存储请求；官方及当前 fork 原有健康和调度反馈的参数、次数、副作用保持不变。
- `shadow`：只读候选与健康副本，新增意见进入隔离评估统计；不抢真实槽位或 half-open，不写 sticky，不推进生产健康状态。真实请求仍按原路径运行和反馈。
- `enforced`：只有已声明能力和覆盖的桥接才能执行新增意见。未知能力返回 `NoOpinion/unavailable`，不跳过原路径检查，不以兼容路由的规范化平台冒充已知 provider。

沿用 parent 共同合同及归因 child 提供、预算 child 消费的六态 `CommitState`，客户端取消使用独立 `client_state`，不得把取消并入提交状态。提交边界只限制透明重放；首语义事件后的真实上游失败仍可由归因 child 归因并更新有证据的健康域，不能把所有流中失败当成客户端取消。客户端取消和本地预算/准入否决本身不追加新的上游处罚，原健康仍按原输入执行。

### 故障域专属端口职责

以下名称描述本 child 的端口职责；公共输入输出引用 parent，具体驱动由桥接装配，核心不依赖官方类型。

- `FaultDomainHealthStore`：读取版本化状态，原子记录幂等结果、CAS 状态转移、领取/校验/释放带 fencing 的恢复许可；区分 miss、不可用和不支持版本。
- `FaultDomainHealthFilter`：读取安全候选副本及当前域视图，只输出排除类别、软信号或恢复许可需求；不排序、不创建候选、不取得许可，也不改变真实状态。
- `FaultDomainHealthReporter`：消费归因 child 拥有的公共事件及预算 child 使用的共同 attempt/send 身份，对同一终态事件和域幂等投影；不重新解析敏感正文，不截断原有健康回调。
- Fingerprint provider adapter：将已证明的上游身份、共享关系和能力转换为 registry 描述符；不能自己选号、摘除或推断未声明的父域。

配置包含证据阈值、最小样本、窗口、冷却/退避上限、恢复许可时限、最大摘除比例及缓存降级策略，由 parent 控制面以新 epoch 原子发布并引用策略版本；缺省不启用新执行行为。

## Fault-domain fingerprint

故障域由可版本化 scope registry 定义。`account`、`account_model`、`endpoint_protocol`、`upstream_config`、`provider_site`、`proxy` 是首批 registry entries，不是核心的固定分支；新增域不修改通用状态机。

1. Fork adapter 在官方 `NormalizeOpenAICompatiblePlatform` 之前保留安全的原始 provider/upstream/deployment 身份，并把兼容路由平台单独看待。原路由仍调用官方归一化，不修改其 default 分支。
2. Registry 为各 scope 指定必需身份、命名空间、规范化 schema 和共享关系证明。未知必需组件不以统一的 `unknown`、空字符串或规范化后的 `openai` 合成可摘除 fingerprint；返回 `NoOpinion` 和有限原因摘要，不写已知域。
3. 已确认 host 规范化大小写、默认端口和末尾点；endpoint 使用登记的逻辑标识与协议。移除 query、fragment、userinfo，不保存完整敏感 URL；不从 URL 相似、相同模型名或错误文案推断共享站点。
4. 账号/配置使用稳定内部 ID；模型使用 adapter 声明的 canonical 映射；代理使用受控稳定身份。不哈希 API Key、Token、Cookie 或请求正文来代替资源身份。
5. 指纹编码区分存储版本、scope schema 版本和身份 digest，例如 `fdv1:<scope_id>:<scope_version>:<digest>`；不把控制 epoch 纳入资源身份，否则每次发布都会绕过同一故障域的既有保护。

同一 reason 可以投影多个独立 scope，但每个目标必须满足 adapter 证据、聚合要求和该 scope 的策略。账号级失败不能自动升级为 provider/site 摘除；未知平台或上游只能产生未知观测，不污染 OpenAI 或其它已知平台的共享状态。

## 状态机与限摘除

所有注册域共用一套 overlay 状态机，不替换现有 `UpstreamHealthStatus`：

```text
healthy -> degraded -> ejected -> half_open -> recovering -> healthy
             |                       |             |
             +-> healthy             +-> ejected <-+
```

- `healthy` 正常参与原有资格判断；`degraded` 只给软信号，单次慢请求不足以新增摘除。
- `ejected` 表示满足证据阈值并已提交冷却。冷却到期只表示具备申请恢复许可的条件，不自动清状态，也不在候选读取时转为 `half_open`。
- `half_open` 仅由发送前的原子许可取得触发。其它请求不能借普通 fail-open 绕过该恢复许可；失效许可不得继续发送。
- `recovering` 使用有限恢复窗口/许可逐级放量；有充分真实成功证据才回到 `healthy`，恢复失败按带抖动、受上限约束的指数退避回到 `ejected`。
- 对本 child 的新增 overlay，主动探针只能作为辅助证据，不能单独清除真实流量造成的故障。管理员禁用属于原账号健康所有者，不受新增流量成功反馈清除；这里不宣称 legacy 状态机已具备完整来源隔离。

### 既有来源覆盖风险与 enforced 门槛

静态源码已发现：traffic 401 可产生 `SuspensionSource=traffic` 的暂停，随后默认 probe 404 的不计阈值分支可能将状态改为 degraded 并清空来源。全局 guard 重配置只处理 probe-owned 状态、账号投影保护其他来源有效禁用，并不能证明所有 registry 转换互不覆盖。

前置底座只做行为等价收口，保留这项风险，不修复、不宣称已完成来源仲裁。本健康 change 进入 `enforced` 前必须验证 probe/traffic 交替结果、恢复和全局 guard 变更的跨来源仲裁及旧阻断保留；新增状态由本 child 的单一 overlay owner 写入，legacy 仍经底座端口由原 owner 执行。不能利用新归因、`unknown` 或 overlay 恢复静默取消官方阻断。若验证需要改变 legacy 行为，须另行确认修复授权；没有新授权不得扩大行为修复或把该覆盖范围标为已通过 enforced。

`disabled` 是请求模式，不是健康状态。普通开关变化不重置健康窗口、租约或状态版本；重新启用也不能一键把全部域初始化为健康。

最大摘除比例限制新增的统计性摘除，而不是给原硬拒绝账号开例外。分母来自桥接提供的原合法池及其版本，排除已被官方拒绝的对象；新增统计性摘除受共享计数/CAS 约束，避免多实例各自按比例同时超额。达到上限时新增统计性建议留在 degraded/观测并告警。无法取得可信分母时不扩大统计性摘除。

最低容量保护不能撤销 caller 排除、已确认的 hard 拒绝、现有账号禁用或恢复许可要求。若原合法池全部命中不可放宽条件，沿用原有无账号/等待/终止边界，不承诺始终有账号可发。

## 候选、排除与官方分支覆盖

真实选号仍由现有 scheduler 执行。候选输入只能来自 parent 白名单中真实选号边界的一次只读安全值拷贝，保留原资格过滤结果与实际分支覆盖；不二次调用 `Select`，不复制 selector，不在选号后重新查询负载拼接“同一快照”，也不传递 `Account`、仓储实体或可变指针。

桥接必须区分三类排除，不能只传一个可整体清空的 `excluded` 集合：

- caller：原调用者和预算 child 已尝试/禁止重试约束；扩展不得删除。
- hard：官方硬资格、现有硬健康和经共同合同确认的安全否决，包括有效恢复许可要求；不能由兜底、限摘除或缓存错误放宽。
- advisory：可选统计性降级意见；只有这一类能按策略撤回，撤回不等于清除其健康事实。

优先消费底座对 fork-only `upstream_health_scheduling.go`、`openai_ttft_guard.go` 和 `upstream_scheduler_concurrency.go` 暴露的窄端口，兼容 bridge 统一映射排除来源、有效策略 owner 和资源补偿，不由本 child 直连 legacy 实现。现有 health/TTFT wrapper 在耗尽时撤回其排除再调用 core 的行为不能直接用于所有故障域；`enforced` 只撤 advisory，保留 caller/hard 和原分组、模型、权限等限制。`disabled/shadow` 不改变既有 fork 回退行为。

覆盖 previous-response、guardian、session sticky、负载均衡和 legacy fallback 的实际早返回，而不是只在 `selectByLoadBalance` 增加过滤。硬 `previous_response_id` 绑定不擅自迁移账号：新增 hard 拒绝时，在原等待/报错和预算边界终止；advisory 不能破坏其原亲和语义。软亲和只能使用原路径允许的 fallback。原分支已发生的 sticky/统计副作用不能用一次“释放槽位”声称回滚。

不存在安全快照或早返回接入能力的路径标记 `partial/unavailable`；该覆盖范围不得宣称完整 enforced，扩展无意见时保留原路由。真实发送端仍必须守住其明确拥有的安全 fence。新增官方接入点只能由 parent 白名单批准，不能通过包内访问官方私有方法绕过该边界。

## 发送前许可与资源生命周期

候选过滤只读，未选账号不占恢复许可。选中后沿用原 `Acquired/ReleaseFunc` 或 `WaitPlan`，真实账号槽与 fork 共享上游槽仍是已有二选一所有权，不因本 child 再申请第二个真实槽位。

1. 原等待/并发和利润终检继续执行；等待前观察到的域状态不是发送许可。
2. 对最终要发送的每次 send，包括同一 attempt 内部重发，在白名单发送边界重新检查 hard 意见、deadline、紧急 admission fence，并取得或验证 attempt 所属的各 scope half-open/恢复许可与容量有效性。多个 scope 必须全部满足；部分取得而后续失败时归还已取许可，不发请求。领取与状态转移必须原子化，持有外层许可不代表后续 send 可以省略校验。
3. 许可关联冻结请求、attempt/send、owner 和单调 fencing token。实际网络写入前验证 owner、有效期、状态版本及 fence；领取后若又进入队列/等待，恢复执行时必须重验，过期禁止发送。
4. 取消、利润否决、预算耗尽、未发送、切换候选和发送前失效都走同一幂等清理 owner，释放恢复许可并调用原真实槽位 release 一次；本地拒绝不反馈为上游失败。
5. 已发送的恢复尝试需有可约束的 deadline 和续租/取消能力。失去租约不得继续发新的 send；未知在途完成状态不能被直接算成功并立即开放下一批探测。无法保证此能力的 adapter 不启用对应 scope 的恢复执行。
6. 终态反馈校验事件幂等键、原 owner、fencing token 和当前状态版本。过期 owner 的迟到成功不能解开新一轮摘除；重复终态不重复推进恢复或释放资源。审计可以保留迟到事实，但不是健康写入许可。

普通 mode 切换只影响新请求。在途 lease/预算 owner 保持冻结；紧急 admission fence 是独立安全否决，可阻止旧 epoch 尚未发送的 attempt 并清理终止，不为其重建 legacy 路径，不重置预算或转交 lease。已提交响应仍遵守预算 child 的禁止透明重放合同。

## 跨实例状态与版本合同

Store 的抽象操作提供以下原子保证，Redis/Lua/时钟实现位于核心之外：

- 读取返回状态及 `state_version`，明确区分无记录、故障与未知编码；使用可控时钟和权威租约时限，不用客户端时钟判断跨实例 owner。
- 结果窗口更新与状态 CAS、幂等终态标记一致提交。键以共同事件及 attempt/send 身份投影到 domain，不自行用外层 attempt 合并真实多次发送，也不重复计算同一 send 的多次反馈。
- 恢复许可领取、续租、终结和状态转移使用单调 fencing；超时释放也必须检查 owner，不能释放新持有者的许可。
- `control_epoch` 标识单调控制面发布，`policy_revision` 标识可复用策略内容，`state_version` 标识域状态 CAS，fencing 标识恢复执行权；四者不能互相代替。
- 回滚新 epoch 引用旧策略，不能把旧 epoch 恢复为当前值。旧异步结果只按原资源生命周期完成清理/观测；过期策略的结果不得改变新策略状态、解除新摘除或写新控制面，跨版本接受条件必须由 owner 显式裁决。

缓存不可用时只让新增且尚无确定状态的 advisory 返回 `NoOpinion`，记录降级，不因 cache error 全池封禁。已有原账号硬状态和仍有效的确定 hard/fencing 条件不能被抹掉；已知需要恢复许可却无法取得/验证时禁发该次尝试，由原预算和错误边界结束或选择其它原合法候选。写失败不发布“摘除成功”；可保留短期本地 advisory，但不能自封分布式 owner。

## Ownership 与数据/文件流

- 独立核心拥有 registry 驱动的状态机、证据门槛和抽象端口；核心不 import 官方业务包、数据库模型或框架实现。
- Fork bridge 消费底座健康/TTFT/共享并发窄端口，映射原身份、安全值对象和单一 release owner；不代替预算 child、缓存容量 child 或 TTFT 隔离 child 各自的所有权。
- 官方仍拥有资格检查、选号、sticky、重试执行、协议提交及 usage/货币结算。薄接入仅按 parent 白名单做安全副本、意见应用和发送前回调，不在官方文件内落状态机或 provider switch。
- 账号健康 recorder 经底座兼容 bridge 保留原回调路径；新增 reporter 只消费窄端口及归因旁路事件，不访问全局 recorder，不重复调用原健康写入，不用 shadow 结果推动实际健康。
- 缓存适配层拥有分布式 CAS/租约实现；运维只消费聚合与安全 digest，不读取凭据、正文或完整上游错误。

## 正常流程

1. 请求冻结共同上下文；disabled 直接沿用当前 fork，不构造新增候选副本。已启用评估/执行的 fork adapter 在官方路由归一化前保留安全身份，调用原资格/选号路径。
2. 真实分支输出一次不可变候选副本；核心只读匹配 registry 域，返回分级意见和覆盖信息，由当前冻结模式决定是否执行。
3. 原选择器维持优先池、硬绑定、软亲和、负载和 fallback 规则，桥接仅叠加允许的限制；最终选择不是故障域核心的职责。
4. 既有等待/槽位/终检结束后执行发送前许可与 fence，成功才走原发送；拒绝走单一清理路径。
5. 原健康反馈保持其所有权，新增结构化事件按域幂等记录；真实成功经 fencing 推进恢复，失败按责任及证据门槛退避。
6. 聚合输出状态转移、覆盖、排除类别、恢复及存储降级，区分真实执行与只读 shadow。

## 失败、边界与回滚

- 无效配置/registry：拒绝发布，保留当前有效策略；未知能力返回无意见，不把缺少信息转换成共享域 hard 摘除。
- 重复/并发：CAS 冲突采用有界重读；advisory 更新可丢弃并计数，hard/许可条件不以冲突为理由放行。
- 多域部分失败：独立结果写入可以分别成功并标记 partial；一次发送需要的多域许可则必须全满足，部分领取必须补偿。
- 共享故障和限摘除：只放宽 advisory，不扩大到未证明共享的域，不复活 caller/官方硬拒绝，不擅自迁移 previous-response。
- 取消/流式/工具：消费共同 CommitState 和独立 client_state；已提交语义或工具状态不能透明重放，上游确有失败仍按证据记录，客户端取消不作为故障来源。
- 控制面异常：拒绝过期 epoch/CAS，普通切换不改变在途 owner；紧急 fence 只能拒绝未发送并终止清理，不能重建旧执行循环。
- 用户内容/path safety：不生成用户可控文件路径，不读取本地凭据；缓存键仅来自 registry 认可的安全身份，审计不记录 Token、Cookie、请求或响应正文。

## 审计指标

聚合维度限于受控 scope、状态、原因、排除类别、模式和操作结果，限制 cardinality；domain digest 和 epoch 等明细进有界审计，不作为无限增长的 metrics label。

```text
fault_domain_state_transition_total
fault_domain_ejection_total
fault_domain_half_open_lease_total
fault_domain_fencing_rejection_total
fault_domain_store_operation_total / fault_domain_store_latency_ms
fault_domain_filtered_candidates_total / fault_domain_advisory_relax_total
fault_domain_max_ejection_guard_total / fault_domain_recovery_duration_ms
fault_domain_scope_unavailable_total / fault_domain_branch_coverage_total
```

审计说明 control epoch、策略 revision、域状态版本、scope/digest、证据类别、样本窗口、cooldown、原 owner 的安全关联、CAS/fencing 和实际是否发送。必须区分 store unavailable、未知 registry、过期许可、最大摘除限制、本地准入否决与真实上游失败。

## 验证设计

以下是验证接口与验收边界，不生成 tasks 或具体 ST/UT cases，本阶段不执行业务构建或测试。

- 纯核心的内存 store、可控时钟、registry/adapter fixture 验证状态机、退避、限摘除、CAS、事件幂等及多 scope 原子许可补偿；不依赖 Redis。
- 多实例 store 适配验证唯一恢复 owner、队列过期禁发、迟到终态 fencing、混合版本和缓存断连；对不能证明安全续租/终止的路径明确 unavailable。
- 官方调用兼容验证 `disabled/shadow` 的 Select 调用次数、回调参数/次数、真实槽位、sticky/TTL、健康、usage 及原 fork 反馈不变；不得用显示评分接口伪造真实快照。
- 底座与来源仲裁验证 default legacy 非 Noop、窄端口/包依赖及各状态唯一 owner；traffic 401 后 probe 404 的静态风险仍登记未修，跨来源阻断、恢复及 guard 变更未取得验证证据前不得宣称 enforced，行为修复另行授权。
- 分支覆盖验证 previous-response、guardian、session、负载均衡、legacy fallback 和真实发送入口；caller/hard 不被健康/TTFT fail-open 清掉，限摘除不恢复硬拒绝。
- 生命周期观察入口覆盖原 Acquired、WaitPlan、排队后利润终检、取消、未发送、工具/提交边界及紧急 fence；release/预算 owner 不随发布或回滚改变。
- 身份和版本验证 Normalize 前保留原 provider、unknown 不污染已知域、新 reason 多 scope 独立门槛、scope/storage 版本与稳定租约协调身份兼容。
- Import/白名单检查验证核心零反向业务依赖、官方薄接入只在 parent 登记位置；受影响的 service/handler/cache、原健康和 failover 测试范围留待 wiki-plan 固化。

## Wiki 与长期合同落点

实现并通过 review/archive 后，在 `.wiki/` 沉淀故障域 registry、账号健康 ownership、分级排除、恢复/fencing 和存储降级长期合同，再更新索引；不搬运本设计全文。官方薄接入和 fork-only 桥接按 parent 统一登记扩展审计合同，本轮只同步本 child 的 metadata/proposal/design，不修改 Wiki、catalog、parent 或 foundation。

## 参考边界

- [Parent 共同合同](../adaptive-upstream-scheduler/split.md#官方兼容与共同合同)：公共类型、执行模式、epoch、官方白名单和跨 child owner；采用 dependency contract，不复制第二份 SSOT。
- [Parent research](../adaptive-upstream-scheduler/research/scope-and-delivery.md)：现有健康、scheduler、cache 与 CodeGraph Evidence；采用 direct constraints。本 child 无独立 research/codegraph，不能引用不存在的 parent `research/codegraph.md`。
- `.tmp/fork-extension-audit/design-decoupling-9449571f7e6b-98d86915beca/report.md` 的 D05/D06/D08/D09：采用已核实的官方副作用、fork 回退、原身份与 ABA 风险约束。
- 归因/预算 child：公共事件/事实、提交状态和请求/attempt/send 预算；采用依赖合同，本 child 不实现其执行器。
- 既有 fork 健康、TTFT、共享并发：复用桥接入口并明确 hard/advisory 和唯一 owner，不复制官方 selector。
- Envoy outlier detection / retry host predicate：仅借鉴分域、限摘除和恢复原则，不直接迁移其 fail-open 或选择器。

## 回滚

发布新 `control_epoch`，引用旧 `policy_revision` 或对新请求选择 `disabled`，不把 epoch 指针倒退。普通回滚不改变在途模式、预算、租约或 release owner；仍需完成原终态清理和审计。紧急 admission fence 可阻止尚未发送的旧 attempt，但必须终止清理，不能重新按 legacy 路径起跑。

不通过回滚把健康状态批量设为 healthy，不删除账号 health history/cooldown/管理员状态，不停止尚有资源的终结器。代码回退前需先停止接纳并 drain 活跃 owner；新缓存状态保留到 TTL/兼容清理，不执行破坏性批量删除。新请求回到原路由与“旧租约被重置”是两件事。

## CodeGraph-derived design constraints

- 固定事实基线：官方 `98d86915becae9fe9491a91ffc6defd5235c8d2b`，fork `9449571f7e6b03d93c29775ba9cf9d1e892dd2c5`。行号属于这两个版本，不代表未来合并后的行号。
- 来源覆盖静态风险：fork `backend/internal/service/upstream_health.go:699-701` 写入 traffic 暂停，`:428-431` 的不计阈值探针失败可降级并清空来源；`:467-489` 仅证明 guard 重配置的 probe-owned 过滤。`backend/internal/service/upstream_config.go:1275-1295` 仅证明账号投影层的来源保护。前置底座不修此差异，不能将现有局部保护当成 enforced 跨来源仲裁已通过。
- 官方 `service/openai_account_scheduler.go:120` 只有 Select/ReportResult/ReportSwitch/SnapshotMetrics；`:375` Select 记录统计，`:389/:431/:448` 包含 previous-response/guardian/sticky，`:567/:570` 抢槽及刷新 TTL。fork 对应 interface `:135`、Select `:394`、槽位/TTL `:611/:614`。不能把 Select 当纯函数。
- Fork-only `service/upstream_health_scheduling.go:23-25/:46-47` 耗尽后撤 health 排除；`service/openai_ttft_guard.go:559/:581-588` 有 wrapper、硬绑定例外及 guard 回退。它们是优先桥接位置，但必须分清 caller/hard/advisory，不扩大原 fail-open。
- Fork `handler/openai_gateway_handler.go:2146/:2219/:2237` 覆盖已抢槽、排队抢槽及排队后利润终检；`service/upstream_scheduler_concurrency.go:158` 是原账号/共享槽二选一。官方对应 handler 生命周期仍是兼容约束，不由核心复制。
- 官方与 fork `service/openai_gateway_scheduling.go:293-299` 默认 Normalize 到 OpenAI；原身份必须提前保留，unknown registry 不改变官方兼容路由。
- 本 child 无独立 research/codegraph；复用 parent scope-and-delivery 的 CodeGraph Evidence、审计中 pinned `git show` 核验及本轮低深度 `callers ReportUpstreamTrafficFailure`、`callees selectAccountWithScheduler`。未更新索引；图可能缺动态调用，安全分支以固定源码核验为准。
- 影响半径限制为独立核心、fork 桥接、外置缓存实现和 parent 白名单薄接入；既有健康、scheduler、failover、handler/cache 测试为后续兼容验证入口，不延伸到认证、计费 schema 或公开协议。
- 残余：实际 snapshot/发送端的白名单落点、各 adapter 的稳定身份与安全租约能力、跨版本存储协议须由 parent/相关 child 在 wiki-plan 前对齐。无证明的路径返回 partial/unavailable，不临时读取私有状态或扩大官方写集。

## 可扩展性设计

### 可版本化 scope registry 与 provider adapter

Registry entry 声明 scope ID/version、组件 schema、父子关系、证据聚合与摘除/恢复策略。新增区域、租户或路由池只注册 descriptor 和参数，不改通用状态机；provider/site/endpoint/account 等不是核心硬编码。

各上游的 fingerprint provider adapter 声明稳定身份、共享关系及协议/模型/恢复能力。原身份在官方 Normalize 前保留；缺必需身份或未知平台/上游时返回 `NoOpinion/unavailable`，不建立可摘除的 unknown 公共域，不继承 OpenAI 的 provider 状态，也不改变原路由。

Reason registry 可将新 reason code 映射到多个 scope，每个目标独立声明 evidence/aggregation 要求及 unknown 行为。低层成功/失败不自动传播父域；未知 reason 只进入有限观测，不能扩大摘除或清除现有硬健康。

### 核心状态机保持不变

扩展仅改变域描述符、证据映射与参数，不新增平台专属状态机、selector 或 handler switch。所有 scope 复用 healthy/degraded/ejected/half_open/recovering、CAS、退避和 fencing；模式与资源 owner 的合同仍由 parent 唯一维护。

### 跨实例存储 key/version 向后兼容

状态与事件 key 可采用独立前缀，例如 `fdh:v1:<scope_id>:<scope_version>:<digest>`；storage 编码版本、scope schema 版本、policy revision 与 control epoch 分开。兼容读取旧字段时采用保守默认，不把未知状态解析为 healthy，也不把旧域解释为更宽共享范围。

同一真实域的恢复执行权必须有跨版本唯一的稳定 coordination identity 和单调 fencing。不能仅因为新 scope_version 另建一把锁，让新旧实例各自 half-open。只有 registry/adapter 证明等价、所有参与者共用协调协议时，才允许有界双读旧状态、单写新状态；双读不能无条件以旧 healthy 覆盖新 ejection。

不兼容的身份/锁协议升级需要暂停该 scope 的新 enforced 接纳并 drain 原许可，再切换协调协议；不能依靠 TTL 和“双读单写”声称无双 owner。无法证明兼容时未知新增策略无意见，但仍不能吞掉既有 hard 拒绝或未完成许可条件。旧 key 按 TTL 保留，回滚引用可读策略而不重置状态或破坏性删除缓存。
