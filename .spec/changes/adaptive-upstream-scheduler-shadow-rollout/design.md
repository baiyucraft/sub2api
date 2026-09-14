# adaptive-upstream-scheduler-shadow-rollout 设计方案

## 方案概述

在独立的 `backend/internal/adaptivescheduler/` 核心中增加只读评估、受控灰度和自动回滚策略，通过 fork bridge 消费真实选号产生的安全值副本及已有结果事件。核心不 import `service`、`handler`、`repository`、Ent、Gin 或 Redis，不持有官方 Account/ForwardResult 的可变指针，不创建另一套账号选择器。

本 child 的交付顺序调整为 7，首先依赖前置 fork 调度底座，并保留对错误归因、请求预算、故障域健康、缓存容量和 TTFT/流量隔离五个策略 child 的依赖；整体为前置底座加原六个策略 child，共七个 child。只负责评估与控制面，不重写错误归因、预算、健康、容量或 TTFT 所有者。proposal 的 service 影响范围限于消费前置窄端口的桥接与装配，dashboard 只读聚合，不以本 child 为由修改公开 API、usage、货币结算或生产配置。

共同类型、执行模式、版本、原身份和官方函数级接入白名单唯一引用 [parent split.md 的“官方兼容与共同合同”](../adaptive-upstream-scheduler/split.md#官方兼容与共同合同)。本文件只细化评估与发布行为，不复制公共类型 SSOT，也不自行扩大官方写集。

```text
请求冻结模式 -> 真实官方选号 -> 该分支一次安全快照 -> 只读策略评估
                         |                         |
                         +-> 原发送/反馈 -> 实测观察 +-> 预测/覆盖审计
                                                     |
                                            有界聚合 -> 新 epoch 控制决策
```

## 接口与稳定合同

### 前置底座衔接

本 child 依赖[前置 fork 调度底座设计](../adaptive-upstream-scheduler-fork-foundation/design.md)，对现有 fork 能力只消费其值对象和窄端口。新增调用（包括本 child 的桥接调用）不得绕过端口直读 global registry、具体旧 `openAITTFTGuard` 或 `ConcurrencyService`；原实现访问只留在底座外的 legacy 适配层，不因接入新策略扩散。

`backend/internal/forkscheduling/` 只放底座值对象和窄契约，旧状态及纯规则位于其 `legacy/` 子包；两者均不 import `service`、`handler`、`repository`、Ent、Gin、Redis 或 `adaptivescheduler`。`adaptivescheduler` 只消费契约包，禁止引用 `forkscheduling/legacy` 具体实现或反向依赖；Outcome/UsageFacts 仍由错误归因 child 按 parent 共同合同负责，mode/epoch 由策略与控制层负责，前置底座不定义或依赖这些对象。

前置只机械委托现有实现，默认装配 legacy，不改变现有 TTFT/健康算法。新增策略 `disabled` 保留当前 fork 真实行为及原配置开关，不能装配 Noop 全关旧保护。现有 TTFT `exclusions` 会推进 probe 试放计数，真实负载读取可能执行 Redis 过期清理，均不得由 shadow 再调；旁路仅消费真实路径已产生的一次性副本，无副本即 unavailable。

共享 slot 沿用原 `Acquired/ReleaseFunc` 和排队生命周期，不新增第二次真实获取或释放；account RPM 仍是账号维度，不因共享上游 slot 被解释或合并为共享 RPM。质量展示的评分与账单缓存率不是计费前真实 cache facts，不能通过底座端口包装后改称事实输入。


### 公共对象与发布模式

复用 parent 的 `SchedulingRequestContext`、`SchedulingAttemptContext`、`SchedulingOutcomeEvent`、`UsageFacts` 和 `SchedulerDecisionTrace`。request、attempt、send 的身份和去重职责不在这里重新定义；评估关联使用其已有标识和策略版本，不生成一套与真实尝试脱节的生命周期。

请求开始一次性冻结 `mode/control_epoch/policy_revision/registry_revision` 及各资源 owner。模式只有 `disabled/shadow/enforced`；按组或受控比例的 canary 是选择请求模式的分配规则，不是第四个执行模式，也不能在同一请求的重试中重新抽样。

- `disabled`：直接保留加入本方案之前的当前 fork 路径；不新增请求体读取、候选遍历、快照计算、后台评估任务或存储请求，不关闭现有 fork 健康/TTFT/并发保护。
- `shadow`：原路由、Select 调用次数、参数、返回和真实副作用保持不变；新评估只读副本，admission 使用隔离模拟状态，不申请业务资源，不写真实健康。
- `enforced`：只有请求分配规则、适配能力、分支覆盖及前置底座及上述五个策略 child 的验证均满足时应用受约束意见，最终选号与动作仍由原执行者完成。无能力不能以 OpenAI 兼容路由为理由直接生效。

### 本 child 端口职责

下列端口只描述局部职责，输入输出的共同语义和字段由 parent 拥有：

- 请求模式解析端口：根据已验证控制快照及请求稳定分组决定模式；不在每个 attempt 动态改 mode。首次读取失败或未知强制行为时，新请求冻结为 disabled 并保留原路由。
- 只读评估端口：消费 parent 安全快照，输出策略意见、预测分量、比较限制和 coverage；超时/异常返回 unavailable，不回调 Select 或索取更多业务候选。
- 结果观察端口：幂等关联真实 outcome 与 trace；不是账单或容量“结算”接口，不拥有原健康、usage 或资源释放权。
- 控制面端口：以期望 `control_epoch` CAS 发布新 epoch，引用已验证 `policy_revision/registry_revision` 及分配/门禁配置；失败保留旧已发布快照。
- 聚合/审计端口：区分预测、真实观测、不可比和缺失；输出有界版本化记录，不保存原错误正文、凭据或请求内容。

## 真实候选快照与 Shadow 边界

### 来源与副作用隔离

固定官方 scheduler interface 只有 `Select/ReportResult/ReportSwitch/SnapshotMetrics`，没有候选快照或信号接口。官方 Select 会写统计、抢槽和刷新/绑定 sticky；再次 Select 后释放槽位也不能撤销这些副作用。因此以下都是禁止项：二次调用 Select 做 shadow、复制 selector 做 dry-run、调用显示评分接口冒充真实调度评分、选号后重查仓储/负载合成历史快照。

快照来自 parent 白名单中真实选号执行时已构造的候选和分支结果，经同一个 fork service bridge 一次复制为不可变安全值。副本只保留实际边界已有的资格/优先层、绑定限制和许可需求等摘要，不读取凭据，不从核心访问官方对象，不改变原过滤/分层/排序/抢槽逻辑。对同一真实选号调用不为评估增加任何额外 Select；原重试或原 fallback 自身的合法选号次数不被误当成评估调用。

现有 fork 健康、TTFT 和共享并发实现只通过前置窄端口的 legacy 适配层接回；本 child 新增桥接不直读 global registry、具体旧 Guard 或 ConcurrencyService。官方文件只增加 parent 已登记的快照/意见/结果旁路薄接入，不把状态机、provider 特判或评估查询塞进官方 core。`SnapshotMetrics` 和 `BuildScoreSnapshot` 都不是可直接复用的完整候选能力。

### 分支 Coverage 与不可比

每份快照按 parent 带 `branch/coverage`，描述实际访问的 previous-response、guardian、session、负载均衡或 legacy fallback 边界，保留硬资格和绑定约束。没有访问的分支不补造候选，不用负载均衡池代表硬绑定可迁移池。

- 仅实际分支具备足够安全值时，比较限于该分支和声明的策略分量，不能称为全局最优选号证明。
- 只覆盖部分路径/信号时标记 `partial`，列出缺口和不可评估维度；未评估候选不能补零、算未命中或推导完整排名。
- 无安全快照、未知能力或无法保持资格语义时标记 `unavailable`；记录有限原因，真实请求继续原路由。
- 原 scheduler 之后的抢槽、队列或利润终检仍可能改变实际结果；快照中的候选不是预留承诺。反事实不代表该时刻一定可以获得槽位或通过发送准入。

Evaluator 可以计算扩展自己的过滤意见、增量分数和受硬资格约束的反事实排序，不能复制完整官方选择算法。缺少可复用的纯信号或未覆盖实际分支时，反事实推荐账号保持缺失；不调用另一遍 selector 来凑齐“新旧账号”字段。真实最终账号始终来自原执行链，不由评估结果伪造。

### 零真实副作用合同

Shadow 不发送请求、不申请真实账号/共享槽、half-open、RPM/TPM 或容量预留，不写 sticky/TTL/健康，不执行真实 cooldown、sleep、failover 或账单操作。它只能读取可用的有界副本；模拟 admission 和模拟窗口使用独立 namespace、TTL、并发上限及可丢弃队列，不能复用生产资源 key 或 lease owner。

真实请求的官方和当前 fork 回调仍以原参数、原次数执行，新增分类/unknown 不能替换或拦截其健康恢复、错误处理和 usage。disabled 不启动评估；shadow 超时、队列满或观察存储失败可以丢样并计数，不阻塞或重跑业务请求。

## 灰度执行与资源所有权

按分组和受控比例以 request 稳定身份分配，跨实例使用相同已验证策略；attempt 重试、内部 send、排队和最终反馈都沿用冻结模式。升级到 enforced 的覆盖单位是 `provider + protocol + entry path + transport + lifecycle`，不是平台名；未映射的 WS/插件/特殊上游不视作已保护。

Enforced 只向原选择器提供 parent 允许的意见，caller/hard/advisory 排除独立，只有 advisory 能按策略撤回。不得借现有 fork health/TTFT 的耗尽 fail-open 恢复 hard 条件或原已拒绝对象；覆盖 previous-response/guardian/session 早返回，硬绑定不得通过清 sticky 或改账号迁移。

候选只读，实际每次 send 前由原生命周期 bridge 获取或验证 attempt 所属 half-open 许可、容量有效性、deadline 与必要 fence。原 Acquired/ReleaseFunc、WaitPlan 和利润终检继续有效；排队后过期、取消或终检拒绝禁发并由原 owner 清理，不能由评估层补发。原账号槽与 fork 共享上游槽仍是二选一，不重复申请。

预算、恢复许可、容量和 workload 子配额由相应 child 和 parent 规定的唯一 owner 执行。评估只观察其引用和释放结果，不重新领取、归还或结算。提交状态消费 parent / 请求预算 child 的六态 CommitState，取消使用独立 client_state；不能因采样或 mode 回滚清除已提交语义，也不能透明重放工具调用或已提交响应。

## 预测、实测与统计归并

未实际选择账号的 TTFT、cache-read/write 命中和成本只能是预测；记录估计来源、能力/策略版本、置信度和缺失。不能把账号 A 的实际响应、usage 或成本当成反事实账号 B 的实测结果，不能根据不可观测的反事实收益自动放量。

真实观察来自原发送/首事件/终态及计费改写前的 `UsageFacts` 副本。缺少原 usage 就保持 unknown，不能用 ForceCacheBilling/TTL 改写后的账单或展示倍率反推命中。供应商成本仍需已确认价格来源；客户账单是独立观察，不能冒充真实供应商成本。延迟和费用的估计与实测分别呈现。

这里的 shadow 是只读评估模式，不是新增上游账号。若当前 fork 已有按父账号派生的 shadow 账号，桥接仅消费其已有安全身份映射，用于识别共享资源/重复观测；不读取真实凭据做归并，不把同一父资源下不同请求、model 或 send 合为一次。一个实际 outcome 可关联多个 policy trace，但成功/失败/usage 事实计数一次；评估不创造新的 usage 或 money-event 身份。

聚合按 provider、model、protocol、workload、group、cache 状态和发布版本分层，区分 request 终态与 send 重试放大。部分覆盖、能力差异、未知价格或缓存事实进入相应数据质量桶，不能算零成本、高命中或新策略收益。cache unknown 可以排除该缓存/成本收益比较，但实际失败率、预算耗尽和资源泄漏仍必须进入安全门禁，不能因为样本不利或信息缺失被整体剔除。

真实收益依靠受控灰度与旧策略对照组的实际结果、最小样本和稳定观察窗口验证。单个请求不可能同时观测两个真实选择结果；dashboard 必须分开显示 observed、prediction、partial/unavailable 及其分母，候选差异不等于因果收益。

## 控制面、回滚与在途请求

`control_epoch` 严格单调递增，`policy_revision` 引用不可变策略内容并可回退，`registry_revision` 冻结能力解释；schema version 不代替任何控制版本。域 state_version 和资源 fencing 由相应 owner 管理，不能只靠策略版本判断有效执行权。

控制发布先验证内容、覆盖、阈值和兼容性，再对期望 epoch 做 CAS。回滚分配一个更大的新 epoch，引用旧 policy revision 并为新请求选择 disabled 或已验证策略；不恢复旧 epoch 数字。多个管理者/自动回滚并发时仅一个 CAS 生效，其余有界重读，禁止旧评估任务覆盖新的发布决定。

普通模式切换只影响新请求。在途请求继续使用冻结 mode、预算、slot、容量及恢复 owner，包括后续原本允许的 attempt；不在下一次 attempt 因为控制读取失败或模式变化而重建 legacy 账本，不重置已消耗预算。旧完成事件可以清理/结算其原预留及记录历史观察，不能用来修改新控制面、自动放量或解除新 epoch 的健康摘除。

紧急 admission fence 是与普通 mode 切换分开的受限机制：可撤销旧 epoch 尚未开始的发送，发送边界拒绝后由原 owner 终止并清理，不移交给另一个执行器，不重建 legacy 请求，不重复发包。已发送/已提交输出遵守原取消和提交合同，不通过热切换制造重放；许可过期和迟到完成仍需相应 fencing 校验。

开关读取失败只决定新请求的安全冻结默认值；指标/评估存储失败不能临时改变在途 owner。已冻结 enforced 的必要许可或 fence 无法验证时，按对应子模块安全合同拒绝未发送动作，不把它伪装成上游错误去重试，也不因控制面不可用清空既有 hard 状态。

## 门禁与审计

门禁分为安全停止、数据质量与收益放量，不共享含糊的“评分通过”：

- 安全停止观察真实失败率、预算/发送放大、排队和 TTFT 恶化、真实资源释放异常；严重完整性或泄漏信号可触发新 epoch 停止接收新灰度请求，必要时使用独立紧急 fence。
- 数据质量检查实际分支覆盖、unknown 比例、usage 完整性、预测来源和观察延迟；不具备可比性时暂停放量，不能因看不到错误而判断健康。
- 收益放量必须基于实际灰度/对照样本达到门槛并稳定通过窗口；预测只解释决策，不单独授权放量。

有界审计保存快照引用、branch/coverage、真实选择、可用的反事实建议、预测与 observed 区分、控制 epoch、策略/registry revision、原因、样本分母和 CAS/fence 结果。Metrics label 仅用受控枚举和聚合维度，epoch/request/domain 明细不造成无限基数。保留失败证据，自动回滚不删除旧聚合以制造收益。

## Ownership 与数据/文件流

- 核心拥有纯评估与门禁决策；依赖安全值对象及 store/clock/observer 抽象，不反向 import 官方包。
- Parent 指定的 service/handler bridge 负责一次快照、结果旁路及必要意见调用，新增 fork 能力调用仅消费前置窄端口；前置及原六个策略 child 不能各自注册一套真实 Select/健康/并发 hook。
- 原 scheduler/协议/FailoverState/usage 继续拥有实际选择、发送、重试、提交和货币结算，请求预算、故障域健康、缓存容量和 TTFT/流量隔离 child 依共同合同限制动作和管理增量资源。
- 外置状态适配层实现控制 CAS、隔离模拟存储和有界聚合；dashboard 只读，不能靠点击评估结果绕过配置校验或创建后台请求。
- 控制 owner 发布新请求分配和受限 fence；它不接管在途 lease、预算或原 billing owner。

## 正常流程

1. 新请求读取已验证控制快照并冻结模式；disabled 直接沿用当前 fork，未授权覆盖不进入 enforced。
2. 原选号执行实际分支，白名单薄出口提供一次安全快照；缺口明确 partial/unavailable，不补走未访问分支。
3. Shadow 只评估副本；enforced 仅应用已授权意见。最终选择仍来自原执行者，发送前由同一生命周期检查许可/容量和 fence。
4. 原结果与回调照常完成；观察端按共同身份把实际结果关联到 trace，区分预测、observed 和缺失，不重复结算。
5. 有界聚合按可比维度保留真实安全指标和数据质量，分开计算灰度/对照结果与反事实估计。
6. 门禁以当前 epoch CAS 发布下一策略或回滚；新请求生效，在途资源按原 owner 完成，紧急 fence 只拒绝尚未发送并清理。

## 失败、边界与回滚

- 无效配置/未知 schema：拒绝发布，保留旧有效控制快照；新请求不能把不可理解的配置当成 enforced，旧实例不能以 policy revision 重用旧 epoch。
- Snapshot 缺失或部分：返回显式 coverage，不重新 Select、遍历候选或假装完整比较；unsupported entry 保留原路由。
- Shadow 失败/队列满/指标延迟：有界丢弃评估并计数，不能影响真实调用次数、延迟预算、健康或结算；不会转为真实补测请求。
- 并发/重复：epoch CAS、观察事件及 request/attempt/send 身份独立去重；一个 attempt 只有一个终态，流中事件不再生成第二个终态。
- 未知原始 provider：Normalize 前映射安全身份，registry 无可信匹配即 NoOpinion/unavailable；记录未知观察但不写 OpenAI 等已知故障域，不改原兼容路由。
- 全部候选被拒：只可放宽 advisory，hard/caller/绑定保持；half-open/容量无效、本地预算或紧急 fence 否决不包装成上游失败或触发新请求。
- 部分版本切换：旧 owner 清理能力保留到受控 drain/TTL 完成，旧结果仅作为原资源完成和历史观察；不得因回滚重新放量或解除新健康状态。
- 用户内容/path safety：不读取凭据和请求正文，不拼用户可控本地文件路径；外置存储只接受受控安全引用，模拟 key 不与真实资源 key 共用。

## 验证设计

本节定义验证边界与观察入口，不生成 tasks 或具体 ST/UT cases；本阶段不执行构建、Gate、部署或线上试验。

- 以官方/fork 原调用 spy 和只读快照 fixture 验证 disabled/shadow 的 Select 次数、参数、返回、抢槽、sticky/TTL、原健康回调、usage 与 release 均兼容；评估没有第二次 Select 或候选来源查询。
- 覆盖 previous-response/guardian/session/负载均衡/legacy 分支、硬资格与绑定；缺快照及未评估分支必须 partial/unavailable，不能伪造完整 counterfactual。
- 内存控制 store、可控时钟和延迟事件入口验证单调 epoch CAS、新 epoch 引用旧策略、并发回滚、控制读取失败和迟到门禁不能重新放量。
- 在途预算/lease/容量 owner 观察验证普通 mode 切换只作用新请求，受限紧急 fence 拒绝旧未发送尝试并幂等清理，不重建 legacy；每 send 许可/容量、排队过期与终态 fencing 由真实 owner 验证。
- 结果关联验证 request/attempt/send/流事件不重复计数，六态 CommitState 与 client_state 不混用，工具/语义提交后不透明重放；统计不改变原 money-event/usage 去重。
- 原始 UsageFacts 和预测 fixture 验证 missing/zero、ForceCacheBilling、实际 cache-read/write、预测成本和客户费用相互独立；未选账号不获得虚构实测值，unknown 不掩盖安全失败。
- 灰度分组/比例、样本门槛和稳定窗口验证安全门禁、数据质量和收益分离；覆盖缺失不自动放量，失败证据与历史聚合保留。
- Import/白名单与 adapter 覆盖检查验证核心零反向依赖、未知 Normalize 身份隔离、fork 单一 bridge、混合版本读取以及 WS/插件未覆盖不 enforced。具体测试入口在 wiki-plan 依 parent 白名单收敛。

## Wiki 与长期合同落点

实现并完成 review/archive 后，向 `.wiki/03-模块指南/03-网关与上游.md` 沉淀真实/预测、shadow、灰度和资源回滚边界；向 `.wiki/02-开发指南/03-测试与质量.md` 沉淀兼容验证与门禁证据合同。官方触点由 parent 统一登记 fork 扩展审计，本次仅同步本 child 的 proposal、metadata 和 design，不修改 Wiki 或 catalog。

## 参考边界

- [Parent 共同合同](../adaptive-upstream-scheduler/split.md#官方兼容与共同合同)：公共对象、冻结模式、版本、执行 owner 及官方函数级白名单；采用 dependency contract。
- [Parent research](../adaptive-upstream-scheduler/research/scope-and-delivery.md)：当前 scheduler/report/cache、CodeGraph Evidence 及原始研究边界；采用已核实约束，不假设有候选快照接口。
- `.tmp/fork-extension-audit/design-decoupling-9449571f7e6b-98d86915beca/report.md` 的 D05/D06/D08/D09：采纳真实 Select 副作用、全分支/许可、原身份及 ABA 修订。
- 错误归因、请求预算、故障域健康、缓存容量和 TTFT/流量隔离 child：共同结果、实际发送预算、故障域恢复、真实缓存容量和 TTFT 唯一裁决者；不在评估器重实现其状态机。 前置底座只提供现有行为窄端口，不拥有这些策略合同；冻结 Guard owner 与 private shared Report helper/guardDispatch 仍由 TTFT/流量隔离 child 负责，评估层不另建分派。
- Envoy、LiteLLM Router、Portkey：仅借鉴评估和门禁思路，不复制 selector，也不迁移与当前硬绑定/健康冲突的 fail-open。

## 回滚

以新 `control_epoch` 发布对新请求的 disabled 或旧 `policy_revision`，不倒退 epoch，不覆盖用户配置、usage、账单、绑定和原健康历史。普通回滚立即停止新灰度接纳，在途请求按冻结生命周期完成；不能简单拔掉还负责资源终结的模块。

需要提前停止旧请求新发送时使用独立紧急 admission fence，由原 owner 清理终止，禁止清预算、换 owner、重建 legacy 路径或重放已提交结果。评估故障可停止新评估任务，保留有界失败证据；代码回退先 drain owner，混合版本不得复用过期控制 epoch。

## CodeGraph-derived design constraints

- 固定事实：官方 `98d86915becae9fe9491a91ffc6defd5235c8d2b`，fork `9449571f7e6b03d93c29775ba9cf9d1e892dd2c5`。官方 `service/openai_account_scheduler.go:120` 无候选端口，`:375/:384-386` 的 Select 记统计，`:567/:570` 抢槽/刷新 TTL，`:1183/:1227-1228` 负载分支也抢槽/绑定 sticky；不是 dry-run API。
- Fork 对应 interface `:135`、Select `:394`、槽位/TTL `:611/:614`，`BuildScoreSnapshot:3092` 使用 `:3122-3124` 中性错误率/TTFT；官方显示接口 `:2656/:2686-2688` 也不是实际负载选择快照。不得用展示数据补全运行时未知。
- 官方 Select 的 previous-response/guardian/session/负载路径与 fork `service/openai_ttft_guard.go:581-588`、`upstream_health_scheduling.go:23-25/:46-47` 的例外/fail-open 决定 coverage；统一桥接必须保持 caller/hard/advisory，不能仅覆盖 LB。
- Fork `handler/openai_gateway_handler.go:2146/:2219/:2237` 的已抢槽、排队和利润终检、`service/upstream_scheduler_concurrency.go:158` 的二选一槽位决定资源 owner；shadow 不能申请后再释放来模拟无副作用。
- 官方及 fork `service/openai_gateway_scheduling.go:293-299` 默认 Normalize 到 OpenAI；先保存原始 provider/upstream/deployment 安全身份，unknown registry 不代表原路由也要拒绝。
- 本 child 没有独立 research/codegraph；复用 parent scope-and-delivery 的 CodeGraph Evidence、审计的 pinned git show 核验和既有低深度 caller/callee 定向查询。未更新索引，图的动态分支覆盖不足时以固定源码核验为准，不伪称图已证明所有入口。
- Ownership/影响半径限制为单向消费 `forkscheduling` 的独立核心、仅消费前置窄端口的 fork bridge、外置控制/评估存储及 parent 白名单薄接入；scheduler、failover、TTFT、usage、并发和缓存测试是后续兼容入口，不修改官方计费类型或另建通用插件接口。
- 残余：具体快照副本字段与逐分支 coverage、provider/transport 能力及门禁阈值/TTL 必须在 wiki-plan 前按 parent 合同验证。没有证明的反事实收益和未覆盖路径不能作为 enforced 或生产收益证据。

## 可扩展性设计

平台、上游、错误 reason 和评估维度通过 versioned registry/adapter 增加，核心只消费 parent 值对象，不把 provider/site/account 类型写成 evaluator 分支。Adapter 在官方 Normalize 前保留原身份，声明候选/协议/transport/lifecycle/usage 能力；未知实现返回 NoOpinion/unavailable，仅有安全副本时允许有限只读观察，不能把未注册实现一概称为完整 shadow 支持。

控制存储明确区分 schema version、单调 epoch 和可复用 policy/registry revision。可选展示字段允许追加兼容；未知执行模式、安全字段或能力版本不能被旧实例忽略后强制执行，必须保留已验证策略或对新请求禁用扩展。CAS 比较 epoch 而不是策略内容版本，混合版本实例必须共用该协议。

评估 key 与生产健康/容量/许可 key 隔离，聚合按策略和证据版本区分。新增维度只增加可选预测/观察，不改变旧 trace 含义，不重置原资源 owner；未知维度计入受控数据质量，不能自动放量或扩大共享故障域。
