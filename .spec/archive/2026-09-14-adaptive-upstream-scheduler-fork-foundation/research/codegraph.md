# Fork 调度兼容底座的源码与图证据

phase: exploration
service-boundary: scope-and-delivery-shape
observed-date: 2026-09-13

## 基线与授权

调研以 fork `9449571f7e6b03d93c29775ba9cf9d1e892dd2c5` 和本地官方 `98d86915becae9fe9491a91ffc6defd5235c8d2b` 为基线；后者经 git merge-base 验证已合入前者，不声称为远端最新。用户已确认增加前置 child，本轮只建立 proposal/design 与依赖文档。

已梳理 fork catalog，并深入核对调度相关能力，不声称完整审计所有支付、活动、邮件、展示或运维扩展。此前一次性研究结果在此压缩沉淀为本 change 的可随仓库阅读证据，不复制原始工具输出或敏感数据。

## CodeGraph 查询

| 工具 | 查询 | 本地结果及限制 |
| --- | --- | --- |
| status | 当前工作区 | 索引 3908 files、117869 nodes、420515 edges；最近状态有 7 个待同步新增文件，本轮未 sync/index |
| context | OpenAITTFTGuardConfigProvider UpstreamHealthEvidenceRecorder ConcurrencyTargetCache | 配置/证据接口已存在，证据通过全局 getter 调用；不是完全没有接口 |
| explore | OpenAIAccountScheduler ReportOpenAIAccountScheduleResult | 官方 scheduler 接口、具体 gateway 依赖、原健康反馈和 fork TTFT 调用位置 |
| explore | ProvideConcurrencyService ProvideRateLimitService NewOpenAIGatewayService | 官方构造与 fork 状态/装配混合；已有 AccountRuntimeBlocker 不能再造 |
| explore | GatewayRouteOptions UpstreamProviderAdapter | 路由通用 middleware 与 provider 同步接口可复用；与调度 runtime 不是同一合同 |
| impact | SetOpenAITTFTGuardConfigProvider --depth 1 | 12 个受影响符号，包含 mapped model、fail-open、caller exclusions、sticky、关闭高级调度仍反馈、upstream-only、previous-response/跨组测试 |
| callers | SetGlobalUpstreamHealthEvidenceRecorder | ProvideUpstreamConfigService 与两项 403 健康回归；图结果不能代替全部动态调用审计 |
| callees | ReportOpenAIAccountScheduleResult | 原 API Key health success/failure、fork TTFT、模型瞬态清除与 scheduler ReportResult；不能将整段移给新 Guard |
| affected | TTFT/健康/并发/优先池文件 | 返回 283 个广泛测试路径，包含明显无关前端/发布路径；仅作为过宽候选，专项测试以源码/精确符号核对为准 |
| git show/diff/merge-base | 上述两个 SHA | 确认官方已有接口、fork-only 文件及嵌入官方文件的局部规则 |

当前 CLI help 支持 context，但不提供 trace。跨层关系通过 callers/callees、explore 与精确源码补核。upstream_config.go、account_service.go 等被图标记过期时以磁盘内容为准；历史图行号不作为未来实现位置的保证。

## 已确认事实

| 文件/符号 | 事实 | 影响 |
| --- | --- | --- |
| `backend/internal/service/openai_account_scheduler.go:135` | 官方已有 OpenAIAccountScheduler；具体实现持有 OpenAIGatewayService | 保留官方执行者，不能复制 selector 来解耦 |
| `backend/internal/service/openai_gateway_service.go:468` | TTFT 指针、provider、eligibility map 仍在官方网关对象 | 把 fork 自有状态收口到 runtime，官方网关只留窄委托 |
| `backend/internal/service/openai_ttft_guard.go:30`、`:53`、`:320` | 配置/只读展示接口已存在；排除调用在`:360` 推进 probeSequence | 复用接口，区分读与命令，不重复试放 |
| `backend/internal/service/openai_account_scheduler.go:2842` | 公共反馈无请求 context；`:2867` 在原健康后调用 fork TTFT | 底座只委托原位置，后续冻结 owner 由 TTFT child 单独设计 |
| `backend/internal/service/upstream_health_evidence.go:16`、`:38` | 已有健康 Recorder/恢复接口，但全局注册 | 收口装配，保持旧入口兼容 |
| `backend/internal/service/upstream_config.go:286`、`:366` | 存在具体 Service 依赖与健康仓储接口 | 不重建仓储；引入少量 settings/probe/lifecycle 适配 |
| `backend/internal/repository/upstream_config_repo.go:1499`、`:1503`、`:1518` | 生命周期事务成功后直接 Forget/SetObservation 全局 registry | 原提交后时点委托窄命令，不能提前到事务内 |
| `backend/internal/service/ratelimit_service.go:42` | AccountRuntimeBlocker 在指定官方基线已有 | 不移动官方认证、限流或恢复逻辑 |
| `backend/internal/service/upstream_scheduler_concurrency.go:158`、`:194` | target 为 account/upstream 二选一，普通 LoadFactor 仅作调度容量 | 复用，不新增共享 RPM 或双槽 |
| `backend/internal/service/concurrency_service.go:61` | 已有 ConcurrencyTargetCache 可选接口 | 不扩大原缓存接口的必需方法 |
| `backend/internal/service/rpm_cache.go:12`、`gateway_scheduling.go:1528` | 账号 RPM 有原子准入接口，预读与最终扣次分离 | 保留账号维度、原扣次时点和回退 |
| `backend/internal/repository/concurrency_cache.go:1096`、`:1132` | 批量负载读取按 target 去重，读取中清理 Redis 过期成员 | 不用第二次真实读取生成 shadow 快照 |
| `backend/internal/handler/gateway_helper.go:188` | sync.Once 协调取消与正常释放 | 接管同一个释放能力，不创建并行释放链 |
| `backend/internal/service/preferred_account_pool.go:20`、`openai_account_scheduler.go:1096` | 部分纯 helper 已独立，部分分池/排序及图片层级仍嵌入官方 selector | 需要提取纯规则，不仅包装 Account 标签 |
| `backend/internal/service/account.go:1351` | 官方 Account 方法内读取 fork 全局 retry 策略 | 保留签名，用受约束兼容入口隔离策略来源 |
| `backend/internal/service/account_quality.go:197`、`:269` | 已有只读 reader，明确不参与调度 | 展示无需重构，也不是实时缓存能力来源 |
| `backend/internal/service/gateway_usage_billing.go:745` | ForceCacheBilling 可改写输入/cache token | 真实 UsageFacts 留给原归因/缓存 child |

## 已知风险与静态推演

`upstream_health.go:699` 的业务 401/403 可设置 Suspended/traffic；`:428` 的未计入阈值探针失败直接设置 Degraded 并清空来源。观测/guard 开启且404不在额外暂停码时，按这两条转换顺序推演存在 traffic401 -> probe404 覆盖风险。未执行业务复现，不声称生产已发生，也不推断官方独立阻断同时被清除。

账号投影与设置重评估有来源保护，但不能据此宣称整个 registry 已实现来源隔离。底座只记录这项风险；如修正必须独立声明行为与验证，不能混入等价重构或把既有缺陷当正确产品不变量。

## 验证线索与范围

- TTFT：openai_ttft_guard_test.go、openai_ttft_guard_settings_test.go，状态/试放/取消/分组/续链及配置兼容。
- 健康：upstream_health_service_test.go、ratelimit_service_403_html_test.go，以及实际生命周期/持久化回退调用测试。
- 共享并发：upstream_scheduler_concurrency_test.go、concurrency_cache_integration_test.go、gateway_helper_test.go。
- 优先池与成本：group_preferred_account_pool_test.go、group_preferred_account_pool_gateway_test.go、openai_account_scheduler_upstream_cost_test.go、openai_image_cost_routing_test.go。
- RPM：account_rpm_test.go；调研精确检索未发现直接覆盖 TryAcquireAccountRPM/TryAcquireRPM 的对应测试，plan 阶段继续核查并补准入回归。
- 展示不变：account_quality_test.go、usage_log_repo_account_quality_test.go。

本轮没有运行这些测试。图查询返回的范围不是调用覆盖证明，更不是 Gate/生产通过证明。最终计划必须在当时源码上核实函数白名单和接口等价测试。

## 对交付形态的影响

新增第一个可独立验收的 single-change child；其余六个保持 ID 与相互依赖，只添加本底座前置。核心单向依赖：adaptivescheduler -> forkscheduling 契约；底座不依赖后续事件、预算、版本或新算法。
