# adaptive-upstream-scheduler-fork-foundation 设计方案

## 方案概述

先对现有 fork 调度扩展做行为等价的边界收口，再实施后六项策略。底座不是第二个调度器，也不是新插件系统：它将旧健康、TTFT、共享并发/账号 RPM、优先池/倍率和全局上游重试码的可复用部分暴露为窄契约，官方 selector/handler 仍拥有真实执行流程。

本 child 位于 [parent split](../adaptive-upstream-scheduler/split.md) 的第一项，无前置依赖。用户确认的范围及 SC-01 至 SC-08 见 [proposal](proposal.md)，源码依据见 [research/codegraph.md](research/codegraph.md)。设计阶段不生成代码或部署产物；实现阶段使用同一 change 下的 tasks、system-tests 和 unit-tests 作为验收依据。

兼容基线是 fork `9449571f7e6b03d93c29775ba9cf9d1e892dd2c5`。官方 `98d86915becae9fe9491a91ffc6defd5235c8d2b` 只用于识别归属，不能以纯官方行为替代当前 fork 回归基线。

## 包与依赖

以下路径为后续实现落点，并非本轮已创建的代码：

| 层 | 路径 | 职责 |
| --- | --- | --- |
| 窄契约 | `backend/internal/forkscheduling/` | 安全身份/目标/配置值、能力接口和效果说明；不持有官方业务对象 |
| 旧实现 | `backend/internal/forkscheduling/legacy/` | 可机械迁移的 TTFT/健康内存状态及 fork 纯分池/比较规则；沿用原算法 |
| 服务兼容 | `backend/internal/service/fork_scheduling_bridge*.go` | 映射 Account/旧快照；保留旧入口；调用既有存储/设置/协议服务 |
| 协议兼容 | `backend/internal/handler/fork_scheduling_bridge*.go` | 仅适配已有容量/释放能力；不接管 handler、SSE 或重试循环 |
| 存储适配 | `backend/internal/repository/fork_scheduling_*.go` 及 fork 生命周期调用点 | 复用现有存储端口；把提交后生命周期命令交给同一 runtime；不新增 SQL/表 |
| 装配 | fork provider 文件及已有 Wire 注册入口 | 构造同一 legacy runtime，注入已有配置/仓储/时钟，保留原暖启动及后台激活边界 |
| 后续策略 | `backend/internal/adaptivescheduler/` | 只依赖 forkscheduling 的窄契约/值对象，不 import legacy 实现或旧 Service |

```text
官方执行者 -> fork bridge -> forkscheduling 契约 <- legacy 实现
                    |
              既有存储/设置适配

后续 adaptivescheduler -> forkscheduling 契约
forkscheduling 不反向依赖 adaptivescheduler 或官方业务层
```

契约和 legacy 包禁止 import service、handler、repository、Ent、Gin 或 Redis 实现。允许现有 fork 能力相互复用，不额外要求各扩展独立插件化。纯规则抽取不等于搬走官方 selector；同 service package 的新文件只算适配层，不能把换文件位置当成核心已隔离。

## 接口与稳定合同

下列为逻辑接口名与输入/输出约束，最终 Go 名称在 plan 中绑定；不增加公开 HTTP API 或数据库字段。不设计返回 Account、任意执行闭包或整个 Service 的万能 Runtime 接口。

### 安全值与映射

- `AccountRef`：账号、可选 upstream config/Key 的 ID；不存在与 0/未绑定按原含义区分。
- `LegacyModelRef`：原 canonical model 结果；归一化仍由原适配入口负责，不重新猜平台或改模型别名。
- `GroupRelationView`：最终实际分组与既有 preferred 关系；不扩大候选集，不读取其他分组替代当前关系。
- `ConcurrencyTargetView`：account/upstream kind、ID、真实上限，以及独立的调度 load capacity；零上限仅在原路径表示 unlimited 时保持此义。
- `LegacyTTFTSample`：原 account/model、success、可选 firstTokenMs；nil 与无效非正值保持原反馈行为，不被改为 0ms 成功。
- `LegacyCandidateView`：原候选安全 ID、优先关系、Priority、精确倍率、负载、排队、LRU 和已判定的能力/成本层；不包含 credentials、URL query、请求或响应正文。
- 快照返回拥有独立生命周期的副本；不能把内部 map/slice 可变引用交给管理端或后续策略。比较输入保留原数值精度、missing 与 zero，不以格式化字符串或展示四舍五入替代原值。

安全映射只剔除与决策无关的敏感字段，不通过截断模型名、重新归一化身份或合并 unknown 来改变旧匹配。日志/指标的模型示例与维度单独受固定边界限制，不反向影响决策身份。

### 能力与效果分类

| 能力 | 输入与输出 | 读写效果与责任 |
| --- | --- | --- |
| `LegacyHealthReader` | Key/账号安全引用 -> 原健康快照及来源/存在性 | 只返回快照，不推进恢复、不清状态；不能代替官方所有健康机制 |
| `LegacyHealthCommands` | 现有 traffic/probe 观测、原人工恢复、观察开关或生命周期通知 | 交给唯一健康协调入口；原算法、阈值和持久化流程不变 |
| `LegacyTTFTObserver` | LegacyTTFTSample + 原配置 -> 原状态变化 | 有状态命令，原反馈点调用一次 |
| `LegacyTTFTExclusions` | 原候选、caller exclusions、原配置 -> 原排除集合 | 有状态命令，会推进原试放序列；禁止当 Reader 使用 |
| `LegacyTTFTReader` | 账号集合 -> 原降级展示视图 | 复用 DegradationReader 语义，不推进试放或执行新采样 |
| `LegacyCapacityAdapter` | 原 account/target -> 目标与既有负载结果；原准入 -> 原 lease | 目标计算纯；真实负载查询可能有清理效果；抢槽/排队为命令 |
| `LegacyRPMAdapter` | 账号 ID -> 原读数；最终准入 -> allowed/count/retryAfter/error | 预读/判断与原子扣次分开，维度仍是 account，不是共享 config |
| `LegacyPoolPolicy` | 原合法候选值和既有层级 -> 原分池/比较结果 | 纯规则，不自行遍历仓储、不抢槽、不绑定或产生随机数 |
| `LegacyRetryPolicyReader` | 原上游绑定身份 + 已安装策略快照 -> 原状态码匹配意见 | 只隔离上游全局分支，普通账号仍使用旧账号级语义 |

上表可拆为更窄的 reader/command 接口，但不得增加新控制功能来“补齐”接口。底座只注入现有准入/释放能力，不因接口存在而预先调用它们。

### 复用与兼容入口

- 复用 OpenAITTFTGuardConfigProvider、OpenAITTFTGuardDegradationReader、UpstreamHealthEvidenceRecorder、UpstreamHealthManualRecoveryHandler、ConcurrencyTargetCache、AccountRPMLimiter 和 RPMCache 的现有语义；官方类型通过 bridge 映射，不反向扩张它们的必需方法。
- 官方 OpenAIAccountScheduler、HTTPUpstream、AccountRuntimeBlocker、UsageBillingRepository 保持原合同；不以底座命名重建它们。
- 原公共构造和反馈入口继续可用，包括当前 fork ReportOpenAIAccountScheduleResult 的 accountRef 兼容行为。原 nil/可选依赖路径沿用原默认或回退，不新增 Account 必填依赖。
- 默认装配完整 legacy 能力，各旧开关仍决定对应模块是否启用。没有新增 adaptive 实现时只是新增旁路无动作，绝不能让整个底座退为 Noop。
- 不借底座新增默认联网、请求体读取、后台 goroutine、数据库/Redis 请求、文件输出或额外候选遍历。原本存在的启动预热、探针和状态写入仍按原配置运行。

## Ownership 与生命周期

### 一个应用运行时，多个窄视图

装配层为每个应用实例建立一个 legacy runtime，注入网关、配置服务、健康任务及管理端读取。TTFT Observer/Exclusions/Reader 引用同一个 Guard；健康 Reader/Commands 引用同一个 registry 和既有协调器。不能分别懒创建导致管理端和网关看到不同状态。

底座拥有迁移后的 fork 内存状态；官方 scheduler EWMA/stats、API Key health breaker、模型瞬态状态和用户额度仍由官方 owner 持有。健康仓储/设置/探针协议编排留在 bridge，不移进纯包。

当前依赖全局函数的公共入口可保留受约束兼容外观：

- global recorder、manual recovery、retry snapshot 的 getter/setter 只能在列明的兼容文件及装配入口使用；旧入口委托应用默认 runtime，不创建第二份状态。
- 原无 context 的 Account 重试方法不能通过给 Account 塞 Service 字段解决；只在其已有 fork 分支调用兼容 policy reader，保留已安装/未安装全局策略的区别。
- 新 bridge 显式注入窄接口；后续 adaptivescheduler 不直接访问 global getter。测试可注入独立 runtime，兼容入口测试必须恢复默认引用，不能跨并发测试污染状态。
- runtime 身份在应用构造后固定；本 change 不支持请求执行中替换整个 runtime。设置更新只沿用原原子快照发布及旧清理语义，不发明 mode/epoch 或热接管协议。

这是一项明确的兼容例外，不宣称进程内所有全局引用被删除。验收要求全局依赖集中、可枚举、不能被新策略绕开，而不是隐藏在另一层通用服务定位器后。

### 健康命令与持久化

```text
旧 traffic/probe/管理入口 -> 同一健康协调器
                              -> 原 key 锁与状态转换
                              -> 原持久化/失败回退/账号投影

原生命周期事务提交成功 -> 窄生命周期通知 -> 同一 registry
原事务失败             -> 不发送成功后的状态通知
```

归档后的 Forget、恢复后的 SetObservation 在原提交成功后的同一位置委托。仓储只通知已发生事实，不决定健康算法，不在事务内提前解除状态。健康证据的原 key 锁、持久化节流、错误传播/回退与业务临时禁用来源保护保持原义，不为了得到“原子性”而新增事务、队列或 outbox。

命令协调入口对 runtime 的唯一写入不是将多个合法事件折叠为一次。原协议已经触发几次反馈就保留几次；底座不自行以 request ID 去重，因为该层还没有统一 outcome 合同。

### TTFT 状态与官方反馈

迁移原状态机并保留 canonical model、upstream-only 资格、单样本阈值、连续异常、EWMA、快速恢复、试放节奏、TTL/LRU 与配置变化清空。失败结果中有效 TTFT 的旧采样行为不能被改成“只有 success 才采样”。

原反馈函数中仅 fork reportOpenAITTFTGuard 后面的实现委托 LegacyTTFTObserver；原健康 success/failure、模型清除和 scheduler ReportResult 的顺序与次数不变。高级调度关闭时原 TTFT 仍应按自身设置反馈。

排除调用通过 LegacyTTFTExclusions，保留 caller exclusions 不被修改、全候选受限时原 fail-open、硬 previous_response 特例及 sticky 保护。它是有状态操作，不能由管理展示或 shadow 调用。

后续 ttft-traffic-isolation 才选择请求冻结的唯一 Guard owner，并按已审计设计引入共享反馈 helper/guardDispatch。底座不提前抽取整段官方反馈，不新增冻结上下文；新旧 Guard 接管不属于本 change 的通过证明。

### 并发、RPM 与释放

- 共享 target 仍由 upstream_config_id 决定；普通账号真实 Concurrency 与虚拟 LoadFactor 分开，上游派生账号不读取其自身 Concurrency/LoadFactor 作为共享容量。
- legacy cache 不实现可选 target 接口时保留现有兼容回退，不能新增必需接口导致旧实现无法编译。无限共享容量仍沿用原计数/释放行为。
- 原 selector/handler 仍在原点调用抢槽、等待、最终复核；底座不独立新增 retry/wait，也不能忽略已发生的并发错误。
- `Acquired` 和 `ReleaseFunc` 由原路径创建。桥接只能转移/包装同一个释放能力，取消与完成共用原 once 语义；不能让两层各自调用底层 Release。
- 账号状态/target/limit 复核变化时保留原“释放旧槽再重取”流程，不把这种合法重取误判为重复申请。
- RPM 读数与预检查不是扣次；最终 TryAcquireRPM 仍由原执行点调用。拒绝不额外补扣，存储不可用按当前 fail-open/回退合同处理，不把它等同于健康限流事件。
- 真实负载读取可能清理过期 Redis 成员。后续 shadow 只能接收真实读取结果副本，不能再次调用 LegacyCapacityAdapter 查询来冒充零副作用观察。

### 分池、倍率和原硬资格

只提取 fork 的 preferred 分池、倍率中立比较、精确倍率比较及已有 compact/图片成本组合规则。输入是原执行者已经构造的候选值；输出为局部分池/比较结果，不直接提供 Select 接口。

官方 Top-K、随机源与随机调用位置、候选读取、previous-response/guardian/session 分支、资格与利润终检、绑定、抢槽及回退执行均留在原处。纯规则不得把优先账号移到其不合法的能力/分组层，也不得把合法 sticky 让给新排序。

保留精确倍率、缺失成本非零价、优先池忽略倍率以及满载立即尝试普通池等既有规则；不重新计算价格、读取质量分或新增预测字段。若某个组合块无法不搬动官方执行而抽取，则 plan 必须明确保留的最小调用块和理由，不能复制整段 selector 到 fork 包。

## 官方函数级修改边界

以下是后续 plan 可细化的白名单，不是本轮代码编辑授权。原行号以 research 基线为线索，实施前重新核实；未列出的接入点需先修订设计。

| 文件/函数域 | 允许变化 | 禁止变化 |
| --- | --- | --- |
| openai_gateway_service.go 的 TTFT 专属字段/构造 | 具体状态改为窄 runtime 依赖，原构造保持兼容 | 移动官方 scheduler stats/health、改变普通网关构造语义 |
| openai_account_scheduler.go 的 fork 分池/比较及原 TTFT 调用 | 委托纯规则/旧反馈接口 | 搬走完整 Select、反馈主体、Top-K/随机流程 |
| gateway_scheduling.go/openai_gateway_scheduling.go 的 fork 分池/容量/RPM/TTFT 包装 | 安全值映射、原执行点窄委托 | 新选择算法、额外查询、扩大候选或改变扣次时机 |
| account.go 的 GetPoolModeRetryStatusCodes/IsPoolModeRetryableStatus 已有 fork 分支 | 委托上游 policy reader | 改普通账号字段/默认状态码，新增 Account 依赖字段 |
| concurrency_service.go 的已有 target-aware 方法 | 复用接口与值映射 | 扩大 ConcurrencyCache 必需合同、另造槽位/队列 |
| handler/gateway_helper.go 的 target-aware 抢槽/等待/释放适配 | 注入旧能力并沿用原 once | 第二个释放链、改用户并发或协议响应 |
| service/wire.go 的相关 fork provider/配置暖启动/注册 | 实现移至 fork provider，旧入口仅装配 | 重构发布激活控制器、新增默认后台任务 |
| cmd/server/wire.go 必要 provider 注册 | 仅注册既定 provider；生成代码必须由工具产生 | 手写 wire_gen 策略、额外服务生命周期行为 |

fork-only `upstream_health*`、`upstream_config`、`upstream_config_repo`、`openai_ttft_guard*`、`upstream_scheduler_concurrency`、`preferred_account_pool`、`pool_mode_retry_settings` 也只允许本设计具体职责范围的改动，不能整文件重写。官方 ratelimit/usage billing/HTTPUpstream/failover 循环不由底座重构。

## 正常流程

1. 装配建立同一个 legacy runtime，原 settings/repository adapter 和原配置暖启动照常工作；不新增 adaptive 开关。
2. 原请求完成鉴权与准入后，selector 在既定调用点读取旧配置/候选并调用窄接口；纯规则只返回原分池/比较结果。
3. 原流程处理 TTFT 有状态排除、账号 RPM、并发等待/抢槽/复核、绑定和转发；调用次数及顺序不变。
4. 原完成/失败路径向同一个旧健康/TTFT runtime 反馈，原官方健康/计费/usage 继续由原执行者处理。
5. 原取消或完成路径释放同一个 lease；管理端读取原视图，生命周期操作在原提交成功位置更新同一 registry。

## 失败、边界与回滚

- 无效输入：沿用当前 nil、未知模型、无目标、缺失倍率、非法 TTFT 的处理。新值映射失败不能冒充健康、无限容量或零成本；不能把原错误吞为成功。
- 配置读写失败：仍由原设置服务执行校验、持久化及原子发布。失败时沿用原旧快照/默认/错误行为，不设计新的覆盖策略，也不重建 runtime 丢掉旧状态。
- 并发/重复：同一实例的原锁与原去重边界保留；不新增无依据的全局事件去重，不复制状态到每个接口实现。
- 部分持久化失败：旧健康回退与投影逻辑保持；事务失败不发送成功生命周期通知，提交后通知失败不自动重放数据库事务或伪装整笔失败。具体错误路径以原函数为回归基线。
- 依赖缺失：旧可选端口/测试构造沿用旧 fallback；必需依赖装配错误不能静默安装全局 Noop。新增自适应实现缺失不影响 legacy。
- shutdown/release：保留原后台激活与停止机制；不新增 runtime 热替换，不改变蓝绿候选是否启动任务的规则。
- 用户内容与 path safety：不修改凭据、用户配置或历史数据；不新增运行时文件路径。后续验证产物限工作区 .tmp，禁止覆盖用户文档或扩张生产配置范围。
- 回滚：本 change 没有数据格式或算法迁移，可撤销本 change 的边界适配并回到上一代码版本。原设置键、持久化快照、Redis key/lease 格式保持兼容，不清账单/健康历史或活动槽位；不能靠关闭所有保护回滚。

### 不混入的独立行为风险

现有 registry 的 traffic401 -> 未计阈值 probe404 序列存在改写暂停来源的静态风险，见 research。账号投影有来源保护不代表全部状态机已隔离；尚未执行复现，也不推断生产结果。

底座的等价回归不把该序列宣布为正确合同，不在接口化中偷偷修复。fault-domain-health 必须在接管前验证跨来源仲裁、旧阻断与新意见的边界；若修正既有转换需独立声明行为范围和测试，不能把更换 owner 当作已消除此风险。

## 验证设计

本节仅定义可观察边界与验证入口，不创建具体 ST/UT case 或任务。

| 成功标准 | 验证入口 | 可观察输出 |
| --- | --- | --- |
| SC-01/08 | import/AST/调用边界检查，官方差异白名单 | 核心无官方实现依赖；新增策略无 global/legacy 具体依赖；剩余官方调用块及理由 |
| SC-02 | 同输入、时钟、随机源的离线 golden/调用轨迹对照 | 选号、分层、随机消耗、调用次数与参数一致，不在真实流量双跑 |
| SC-03 | legacy Guard 与窄接口等价；Observer/Reader/Exclusions 同实例 | 采样、试放、恢复、TTL/LRU、配置变化、nil/失败 TTFT、sticky/续链保持 |
| SC-04 | 既有健康事件/持久化/生命周期适配 | 来源、锁、快照、事件、节流、投影和失败回退一致；风险序列单独标识 |
| SC-05 | selector/helper/cache 资源轨迹 | 正常/取消/失败释放一次，target 变化合法重取，无双槽或泄漏 |
| SC-06 | 账号 RPM 与全局 retry 兼容适配 | 读数/最终扣次区分，拒绝不额外扣次、存储故障回退、普通账号原规则 |
| SC-07 | 无 adaptive 注入、原模块分别启停、旧构造入口 | 当前 fork 原功能仍运行，新增旁路无动作，不新增后台/存储/文件操作 |

已有专项测试入口见 research；还需在 plan 中为边界副作用、原子 RPM 准入和跨接口状态一致性补覆盖。图工具广泛返回的路径不全部作为本 change 必测范围。

后续实施的本地验证命令在 `backend` 目录运行，例如：

```text
go test ./internal/forkscheduling/...
go test ./internal/service ./internal/handler ./internal/repository
go test -race ./internal/forkscheduling/... ./internal/service
go test ./...
```

repository 集成测试使用项目受控测试依赖，不能连接生产；缺少测试依赖时记录未执行，不能声称通过。文档阶段在仓库根运行 `spec-wiki-lite validate <id> --strict --json`、父子依赖/链接核验与 `git diff --check`。VM Gate 与生产部署需独立阶段授权，不以本设计代替。

## 与后六项的单向衔接

- error-attribution 消费安全身份和旧反馈兼容入口，自行拥有共同请求/attempt/outcome/UsageFacts；底座不 import 这些新类型。
- attempt-budget 在官方执行点限制行为，消费原容量/重试意见，不让底座执行新的循环或退避。
- fault-domain-health 使用健康快照与显式命令边界，新增故障域状态/仲裁由该 child 负责，不重写 legacy 来源而不说明。
- cache-aware-capacity 消费原 target/lease 与后续真实 UsageFacts，新增 token 账本归它唯一拥有，不能拿展示缓存率替代。
- ttft-traffic-isolation 复用 legacy 窄接口与状态视图，自行建立新算法及冻结 owner；底座既没有新 Guard，也没有新 epoch。
- shadow-rollout 只消费真实执行生成的安全副本；不调用 legacy 有状态排除、负载清理、RPM 扣次、抢槽或恢复命令来收集反事实数据。

后续增平台/上游时，只新增值映射/能力适配，不在底座建立 provider switch 来解析响应；新错误原因注册仍由归因 child 拥有。未覆盖的能力明确不可用，不能把 unknown 归并到 OpenAI 或解除旧硬资格。

## Wiki 与长期合同落点

实现 review 通过后，将稳定的依赖方向、legacy owner、效果分类、官方函数白名单和专项回归登记到 fork 扩展 catalog，并整理到 `.wiki/03-模块指南/05-分叉扩展与兼容性.md`、必要的后端请求链路说明。本轮不修改 Wiki/catalog，也不把未实现的包或接口描述成现状。

## 参考边界

- 来源：本地官方与 fork 两个固定 SHA、research 中的精确源码和图查询。
- 目标落点：官方执行保留、fork 状态/纯规则机械迁移、兼容 bridge 和受约束装配。
- 采用方式：existing-contract reuse / behavior-preserving extraction；不是参考新框架重写现有业务。
- Observer 的通用 middleware 和 provider 同步接口仅作为已有边界的参照，不扩大本次写集；本设计无新增外部项目依赖。

## CodeGraph-derived design constraints

- entry points and call paths：SetOpenAITTFTGuardConfigProvider 的低深度 impact、SetGlobalUpstreamHealthEvidenceRecorder 的 callers、ReportOpenAIAccountScheduleResult 的 callees 与当前源码共同约束入口；图给出的调用不是全部动态协议覆盖。
- ownership and dependency boundaries：官方已有 OpenAIAccountScheduler/AccountRuntimeBlocker 不重建；forkscheduling/legacy 持旧状态和纯规则，bridge 映射官方类型，adaptivescheduler 只能依赖契约，禁止反向依赖。
- impact radius：TTFT 专属状态/包装、健康证据与生命周期通知、fork 容量/RPM/分池/重试读取与装配；官方只修改本设计白名单，不涉及计费或请求协议。
- affected tests：impact 命中 12 个 TTFT 符号；affected 返回 283 个过宽路径，不能当覆盖证明。以 research 中已核对专项入口及后续边界对照验证为准。
- rollback boundary：无数据库/配置/Redis 格式变化；恢复旧代码不能清理活动 lease、旧健康或用户数据，不提供新的热切换协议。
- graph evidence vs source verification：当前 CLI context 可用，未提供 trace；upstream_config 等过期节点已读磁盘核实。仓储生命周期明确位于事务成功之后，全局命令不能在抽取时前移。
- unresolved items：健康跨来源覆盖未运行复现；原子 RPM 准入测试覆盖须继续核查；最终适配函数与机械可迁移范围在 plan 对当时源码复核。超出白名单或发现必须修改算法时先回到 design，不以扩展通配符或新 fallback 掩盖。
