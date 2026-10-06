# Fork 扩展最低回归矩阵

| 扩展域 | 最低回归要求 |
| --- | --- |
| 上游配置与管理 | provider 同步、缺失 Key 对账、派生账号绑定、账号编辑白名单、两个一级菜单、局部运行态刷新；合并时若 upstream/main 变更账号管理/编辑，必须同步对比上游管理后端、编辑 modal、白名单、payload 与回归测试 |
| 上游派生账号生命周期 | 仅完整同步推进缺失计数；sync_managed 连续缺失 3 次且至少 30 分钟后与 Key 同事务软归档；manual 永不自动归档；仅 `sync_managed+key_missing` 恢复同 ID 并保留分组、定时计划和历史；定时计划过滤 deleted_at；归档/恢复清理账号缓存、Redis、共享并发和健康 Registry；真实 PostgreSQL 验证 NULL-rate 历史绑定暂停/归档与健康 Key 同步、旧 241 回滚复现、同绑定计费字段保留、归档夹带启用/身份或 source 变更拒绝、未知率恢复回滚与有效零率恢复 |
| 上游模型能力同步 | 仅 sync_managed 自动写白名单；NewAPI 有效 `model_limits` 优先于 live `/models`；无效/空结果保留最近成功映射；30m freshness 跳过重复请求，30m–24h 继续执行旧白名单，超过 24h 放行其他能力回退；成功更新触发 scheduler outbox/快照失效；并发 4、单账号 15s；状态与错误不得泄露 URL、凭据、响应体或原始错误 |
| 全局模型别名同步 | 设置 API 兼容缺省字段并校验对象/字符串/空白；保存只写 `upstream_model_alias_rules` 不触发同步；sync_managed 下一次同步按真实模型生成 identity/alias 并保存 `auto_mapping`；manual 账号不受影响；手工映射目标消失清理、源消失但目标存在保留；失败保留旧映射和快照；成功触发 scheduler outbox/账号快照失效 |
| NewAPI 兼容 | 旧 `data.id + Cookie`、新 `data.user.id + access_token`、数字/空 code、Bearer 与 `New-Api-User`、三种认证模式；过期 JWT 优先同会话 refresh，Origin/SID 且不带旧 Bearer；路径受限 Cookie 的属性、轮转、删除与 JSON 重启恢复；跨源重定向、身份/SID 变化与过期刷新结果拒绝；transport/403/409/5xx 不追加登录，401/旧无 Cookie/404 等有限回退；AUTH 白名单与 HTTP 状态记录，业务 409 不进入认证冲突冷却 |
| 认证持久化时序 | login/refresh 成功、业务失败仍保存新密文；checkpoint 保存失败不发业务；失败记账不覆盖刚发布的 secret；成功业务再次序列化 Cookie 轮转；NewAPI 过期句柄保留 refresh 材料、失败不清零异常，恢复动作不重复；Sub2API/LCodex 原计数与恢复预算保持；脱敏 API/日志不泄露 Token/Cookie |
| 同步错误归因 | persist/account_apply 错误先按真实落库阶段归类，文本包含 key/group/token 不覆盖为分页、分组或认证失败；SQLSTATE 23 类约束拒绝不标记可重试，连接及事务冲突保持可重试；结构化上游 HTTP/AUTH 错误继续按真实端点和状态码分类 |
| 共享并发 | 同上游多 Key 共享 slot/lease/queue/load，不同上游隔离，优先级来源解析，降低上限不终止已有请求 |
| 容量故障转移 | 普通 OpenAI 等待队列满按共享并发目标换号；上游绑定账号 429 在同号重试耗尽后按账号换号；共享运行时开关/独立预算/可配置耗尽状态；设置关闭保留上游 429；流式与 WebSocket 不拼接跨账号语义输出；Ops 保留真实 upstream_status=429 |
| LoadFactor | 普通账号硬并发使用 Concurrency，调度容量使用 LoadFactor 或回退；上游账号忽略派生账号字段；Priority/倍率同步不改 LoadFactor |
| 请求级分组映射 | 旧规则默认本地响应；首条命中；消息精确匹配裁剪首尾空白并重放请求体；身份认证后先映射，再按目标分组重新校验启用、用户权限和路由准入；模型准入、调度、TTFT 使用目标分组，但用户计费模型、倍率、余额/订阅归属、扣费与流水保留映射前分组；API Key 永久绑定和 billing 元信息不变；失败不回落原分组 |
| 请求级模型映射 | JSON Responses/Chat/Messages 以客户端 A 命中首条规则，目标分组对白名单与 A 定价先准入，随后 A→B→渠道 C→账号；可单独改模型或组合改组；用户计费与配额计量始终按映射前模型 A 和映射前分组倍率，审计保留转发链；重复/大小写变体模型键拒绝；WS 首 turn 可改组，首帧后复核目标组路由准入，再向上游请求，计费快照仍使用原分组，后续只能改模型、跨组拒绝；旧规则、本地响应和命中计数不回归 |
| TTFT Guard | 仅带实际 group_id 分组上下文的真实业务可见首 Token 采样；状态按 group_id + account_id + canonical model 隔离；OpenAI/Composite 分组支持 inherit/enabled/disabled；策略写入与 group_changed outbox 原子化且幂等更新不发事件；各实例消费 group_changed 后清 policy cache 与该分组本地运行态；策略缓存失效期间的旧 DB 读取不得重新回填；全局设置通过 fork Redis 通道广播，远端刷新成功后只清配置变化的 inherit/global 状态，刷新失败和自定义策略均保留；无分组后台账号测试、健康探针和其他主动探针不采样、不进入分组 Guard；degradation 携带分组名、策略来源、三类触发原因和恢复倒计时 |
| Codex STATE PR 历史与隐私 | 保留 PR #7315 head/merge 与 PR #7338 head/fork merge 的父历史和作者署名；内置采集、注票、专用 API 和表单退场；旧 accounts.extra 私有字段普通编辑保留、新建/导入剥离、列表/导出/审计脱敏；不迁移旧配置或票据、不批量删除历史数据 |
| 通用插件运行时 v2 | API1 兼容，API2 能力缺失启用前拒绝；配置/代次/受管范围原子保存；AdmitBatch 覆盖普通、粘性和回退，Forward 前权威复核实际模型；未受管流量不依赖插件在线；主动停用放行，崩溃/重启/升级 strict；请求级拒绝不耗上游重试预算、不全账号停调；OAuth/Setup Token 目录脱敏；加密状态 CAS、租约 fencing、旧持有者拒写；签名升级、跨实例排空与失败回滚；动作幂等，Health/目录/校验不采集 |
| 插件原生管理页 Manifest v2 | v1 `ui.entrypoint` 继续走 sandbox iframe/Bridge；v2 `native/iframe/none` 合法组合、能力协商和缺字段拒绝；native definition 必须位于 `ui/`、进入签名哈希并通过体积/深度/节点/文本/图标/绑定/动作校验；禁止脚本、HTML、远程资源、自由表达式和越权数据源；按 pluginKey 深链接、侧栏高亮、停用态、错误态、草稿离开提醒、2～60 秒轮询暂停、Action 幂等和宿主秘密弹窗；升级/卸载按 installation ID + binary SHA 失效缓存；原生渲染器懒加载且不改变插件列表首屏依赖 |
| Codex STATE 独立插件 | baiyu.codex-state 0.1.0 独立模块和双架构包；Astra/Sol/Terra 按账号、实际出站模型、revision、ChatGPT identity 隔离；Pro 10 块/Team 12 块严格 envelope、内部签发时间、TTL 一小时、未来/尾段各 30 秒；active/ready、两次连续异常、迟到响应保护、续期失败保留 active；客户端 STATE 优先、跨账号先剥离；完整成功响应才记账、客户端取消不丢完成事件；八次采集与五分钟冷却；HTTP/WS bridge 覆盖，原生 WS 新受管模型要求重连；默认关闭，不改业务代理/并发/计费、不重放请求 |
| 插件包安装与独立发布 | 首次安装先证明插件 ID 不存在，再走 /plugins/upload 并保持 disabled；JWT 管理会话仍需 step-up，自动发布器的管理员 API Key 只允许 upload/upgrade/delete 包生命周期，配置、秘密、启停、动作和测试仍需 step-up；既有同 ID 包必须走 trusted compatible /:id/upgrade，不得用 upload 或先停用绕过 maintenance/strict；旧包移除当前 config_secrets 或无法读取新状态时停止普通回退；PostgreSQL artifact/installation 为跨实例权威、本地 data_dir 按需复验恢复；仅插件目录和当前宿主已支持的声明式页面定义变化不生成宿主镜像/profile，Manifest parser/schema、Admin UI API、宿主路由/渲染器、安全校验、Host API、migration 或部署配置变化走完整应用发布；正式包双架构、SHA256、Ed25519 受信签名，且两架构 Admin UI 定义语义一致；安装、秘密、启用、动作和真实采集分别授权；逐实例核验版本、binary SHA、Health、受管范围及失败回滚 |
| Codex STATE 参考负向边界 | ccodex-sleep-state 26b22196 为 #7338 原参考点、b18fabf9 为 2026-09-19 fork 审查点；仅作 GPL-3.0 设计参考且不得复制源码或依赖；不得引入其本地 Codex 配置接管、CCS/profile/Web 面板、订阅/代理节点池、纯内存 STATE、响应正文拦截、on_demand/standby 用户策略、本地 relay、原生上游 WS、账号暂停恢复或配置恢复行为 |
| OpenAI OAuth 代理组 | 参考 Go1c/sub2api PR #423、按 fork 版本独立适配；单代理/代理组互斥，OAuth/Setup Token 类型限制，空组/Redis 故障 fail-closed；会话稳定绑定、绑定代理满载不改绑、失效后重绑；账号总闸加 account+proxy 每代理槽；全部成员满进入现有容量换号；列表只返回脱敏摘要与每代理并发标签；HTTP/SSE/JSON/compact/Chat/Messages/Embeddings/Images/Alpha Search/quota/OAuth/WS 握手出口一致 |
| 代理组后台出口与物理代理熔断 | OAuth 刷新、配额、刷新后的隐私设置均解析组内真实出口，组出口不可用时不直连；流式连接熔断只记录已选中成员的真实代理 ID，不把账号保存的统一绑定 ID 当作物理代理；覆盖隐私出口和熔断键回归测试 |
| 管理代理混合目录与统一绑定 | 统一合同要求既有 `/admin/proxies`、`/admin/proxies/all` 默认返回真实代理和代理组绑定行；二者均使用正数绑定 ID，`binding_type=proxy|proxy_ip_group` 是唯一类型依据；代理组摘要可带管理用 `proxy_ip_group_id`，但账号只持久化 `proxy_bindings.id`；旧负数 `proxy_id` 与旧 `proxy_ip_group_id` 只在请求归一化阶段兼容，`proxy_id=0` 清空；绑定 ID 由真实代理和代理组共享的持久序列分配且永不复用；绑定行不得进入真实代理操作；迁移 `284_unified_proxy_bindings.sql` 已登记到保留 profile 257，原始 SHA-256 已入 catalog；回填 group accounts 前必须先移除旧 proxy_id FK 与互斥 CHECK，发布前验证 |
| 账号级 STATE 与代理组 | STATE 所有权固定为 account_id + outbound_model + revision + ChatGPT identity，不包含代理 ID/URL/组成员；动态代理采集后依次使用组内可用代表复验，任一成功发布账号级 active/ready；更换代理或成员不失效，ChatGPT 身份变化必须失效；组无可用出口时 strict 阻断，不创建每代理票据槽 |
| 导入复制代理协议残留 | `account-import-copy-proxies` 为 deprecated/protocol-reject-only；管理端不再提供复制入口，非空 `copy_proxy_ids` 固定返回 `COPY_PROXY_IMPORT_DEPRECATED`；新导入每源账号只创建一条并可统一绑定代理组；旧共享指纹字段不再参与身份派生，历史副本不自动合并、停用或删除 |
| 健康探针 | OpenAI Responses、Anthropic Claude Code profile、Gemini 原生流；首文本、终止事件、challenge、截断流、超时和非 2xx 分类 |
| Probe Guard | 默认 401/403、429/529、5xx、其他 4xx 规则；自定义错误码追加；阈值暂停、成功恢复、人工恢复与业务隔离 |
| 健康趋势 | 列表 24 点、35 天保留、6h/24h/7d/30d 聚合、P50/P95、断点、Tooltip、中英文和暗色模式 |
| 账号页运行态与渲染 | 普通账号页与上游管理页的隐藏列、排序、自动刷新配置、ETag、请求 generation 和静默窗口完全隔离；切换 scope 后旧响应不得覆盖；健康历史单 Tooltip；TTFT 全页单 ticker；DataTable 测量使用可取消的 rAF 合并 |
| 成本与归因 | 倍率/Priority 独立、原始倍率不公开、余额与价格快照、usage/batch-image 归因不随后续解绑变化 |
| Channel Monitor V2 | managed Key 生命周期、倍率趋势、分组权限、隐私默认值、错误分类和缓存/rollup |
| 渠道监控 V1 SSE 探针 | OpenAI Responses 等流式探针允许超过旧 64 KiB 的完整事件；单次有界读取 1 MiB，超限报明确容量错误而非 JSON EOF；损坏 JSON 与未完成流仍分别报错，TTFT、challenge、重试和非流式 64 KiB 上限不回归 |
| 质量与累计用量 | 质量仅展示不参与调度；coverage/backfill 完整后才允许 raw cleanup；日聚合时区正确 |
| 图片成本路由与展示 | Key 快照 supported/status/stale、共享/独立倍率、1K/2K/4K 成本、免费成本 0、partial/stale/unknown 排序、prefer/strict、无价格回退、普通文本隔离、账号 hydration、API Key auth cache、scheduler cache、账号页与分组配置 UI；成本摘要必须结构化展示能力、倍率来源和分辨率成本；不得绕过健康、共享并发、TTFT Guard 或 Priority 约束 |
| Codex 图片权限与固定账号重试 | `openai-image-permission-routing`：自动跟随与手动覆盖、passive/native/explicit/history 分离、原始 body 不可变、图片能力独立成本路由、known denied 保留直到成功 allow、固定 403/SSE 语义状态、图片 scope 30 分钟冷却且健康/文本隔离、同号重试不逃逸、候选/预算耗尽 503 `image_generation_unavailable`、输出后不重放与取消边界；详见下方专项 |
| migration/profile/version | migration 233 语义、官方 migration 编号冲突按内容重编号、历史 profile 233–256 合同不回写；254 保持 `0.2.7-baiyu`、parent 253、三项 278–280 原 migration；历史 255 为 `0.2.8-baiyu`、parent 254，`new_migrations=[281_content_moderation_engine_meta.sql,282_channel_reasoning_effort_multipliers.sql,283_affiliate_ledger_operation_id.sql]`，仅对官方原 238b/239/240 新文件重编号，SQL 字节不变；按完整 filename 排序，新库回填须在历史 265 列创建后执行，升级库按 filename+checksum 保留历史记录；281–283 的原始 SHA-256 与 catalog 核对，release manifest 绑定数据库 catalog 和兼容快照，执行 `backend/migrations/content_moderation_engine_meta_test.go`、`channel_reasoning_effort_multipliers_migration_test.go`、`backend/internal/repository/channel_reasoning_effort_migration_integration_test.go`、`affiliate_repo_test.go`；历史 profile 256 为 `0.2.9-baiyu`、parent 255、`new_migrations=[]`；保留 profile 257 为 `0.2.9-baiyu`、parent 256、`new_migrations=[284_unified_proxy_bindings.sql]`，284 原始 SHA-256 已入 catalog，修改 SQL 须重算；不能因登记完成就推断已签名或发布；须验证 VM/签名器/恢复入口接受相应 profile，`VERSION = official release version + -baiyu`，每次升版新增连续 profile，不回写 254/255、279 或其历史 checksum |
| 官方 #7509 与本地提前引入 | 固定目标 `a3eb7ef302961cba716dc78b39b93b60c467db0e` 包含官方 merge `4318a63bd886b1a64c49015979c61bf34eca19ff`；本地 `f9633c4f51c15cfc7460d610e899431d0a7c1aaa`、`6bf1ab9197e747ddbdd14798f36fef4a2a8561e1` 只保留 Git 历史，运行时同义实现跟随官方；执行 `backend/internal/service/billing_service_test.go`、`openai_codex_models_service_test.go`、`gateway_forward_as_responses_test.go`、`frontend/src/composables/__tests__/useModelWhitelist.spec.ts`，并验证 GPT-6 Sol/Luna/Opus 5.5 的模型发现、协议转换、默认计费和白名单 |
| Grok 4.7 逐故障域归属 | 官方 `935db68517f8beef407db3da016eaef1799fce25` 和本地 `c41574a3ab1eaa05b4d9b61bec97ca87d9c782d5` 对照 xAI 目录/别名、Responses/Chat 路由、xhigh/reasoning、200k 长上下文及缓存价格、Codex 图像输入、OpenCode Go、runtime build 映射与前端白名单；`backend/internal/pkg/xai/models_test.go`、`backend/internal/service/billing_service_test.go`、`openai_responses_tool_schema_test.go`、`frontend/src/composables/__tests__/useModelWhitelist.spec.ts`。已有官方支持不等于全部 fork 边界被覆盖；完整覆盖归 upstream，剩余最小增量继续 fork 并绑定精确合同 |
| 官方 Astra 支持 | 使用目标官方的识别、静态目录、live/pinned 能力和测试；默认 medium 与 low 至 max，live/pinned 额外能力不被 fork 旧规则裁剪；ultrafast 与推理 ultra 区分；Anthropic 桥接、指纹、0 倍率和共享并发单独回归 |
| compact 账号列表与编辑 | 脱敏列表保留上游身份、能力和调度字段；按需详情不被列表刷新覆盖；模型同步 persisted 分支与官方元数据分支独立；图片回填和请求 ID 头字段只放宽精确白名单 |
| 发布运维 skill | `fast/specialized/full` 三层门禁及 0–2/5–15/20–60 分钟预算；`full` 普通发布非阻塞、恢复算法、备份格式、信任链变化或真实恢复事故修复时阻塞；profile 兼容扩展按精确 blob 与模式审阅走 specialized；Redis 惰性过期单调不等式；helper bundle 原子版本；PostgreSQL/Redis/Compose/app/Nginx/backup units/claim/cleanup 幂等 checkpoint；`verify-result` 与 `verify-recovery-result` 分离；release pytest、supervisor/production/skill-pitfalls、日志合同、Git Bash、清理 dry-run/apply、profile signer/validator、8211 单实例与成功后收口 |
| 发布 DMIT 中继 | `test_ssh_output.py`、`test_racknerd_readonly_status.py`；验证 DMIT direct SSH、1080 HTTP CONNECT、RackNerd host key、代理失败 fail-closed，以及命令/SFTP 共用连接入口 |
| AstrBot 渠道状态插件 | `python -m pytest astrbot/tests -q`；`go test ./internal/service ./internal/handler/admin -run 'TestBatchMonitorStatusSummaryIncludesReal24hAndMissingHistory|TestBuildListItemResponsePreserves24hAvailability'`；V1 管理监控列表真实 24h 聚合、近 10 次 history、平台分组、普通倍率、缺失值不回退 7d、/status 与定时推送纯文本分页及密钥保护 |

## Codex 图片权限与固定账号重试专项

以下精确文件对应 `openai-image-permission-routing`。最低清单不是已通过证据；不新增 migration/profile，不改变 Alpha Search 或发布恢复门禁。

| 入口与精确路径 | 最低测试与断言 |
| --- | --- |
| `backend/internal/service/openai_gateway_upstream_errors.go`、`openai_gateway_passthrough.go`、`ratelimit_service.go`、`gateway_service.go`、`openai_apikey_health_breaker.go` | `openai_image_permission_test.go`：`TestOpenAIImagePermissionDeniedClassification`、`FailoverDisallowsPooledSameAccountRetry`、`DoesNotReclassifyOrdinaryFailover`、`StreamUsesSemantic403`、`TestOpenAIImagePermissionFailoverIncludesLocalAdmission`、`TestOpenAIImagePermissionDeniedCoolsOnlyImageCapability`、`TestHandleOpenAIImagePermissionDeniedOnlyHandlesFixedOpenAI403`；固定原文与仅 `error.message` 精确 `403: ` 前缀；HTTP 200 SSE 缺省状态推导 403、显式 502 不误判；pool/custom policy 不全账号停调，30 分钟仅图片冷却，写失败仍换号；上游与四个本地 reason（含 WS 权限刷新失败）在 schedule 与直接 post-output health 两个入口均不惩罚账号，普通 502 保持原语义 |
| `backend/internal/service/upstream_config.go`、`lcodex_upstream_sync.go` | `upstream_image_permission_snapshot_test.go`：`TestUpstreamImagePermissionSnapshotRetainsDeniedUntilAllowed`、`TestUpstreamImagePermissionSnapshotHistoryReadFailureDoesNotRewriteSnapshot`；Sub2API/LCodex missing、unavailable、invalid 保留旧 negative 与 observed_at 并标记 stale；只有成功 allowed 解除；历史读取失败在写入前停止；同时回归 `upstream_key_image_pricing_test.go`、`lcodex_upstream_sync_test.go`，既有定价、倍率与文本调度不变 |
| `backend/internal/service/openai_image_request_policy.go`、`image_generation_intent.go`、`codex_image_generation_bridge.go`、`openai_gateway_forward.go`、`openai_ws_forwarder_ingress.go` | `openai_image_request_policy_test.go`、`openai_image_request_forward_test.go`、`openai_image_request_ws_test.go`、`openai_image_generation_controls_test.go`；不猜 prompt；passive 在 negative 账号只剥未使用声明，历史/续接/native/explicit 保守准入；手动 strip 保持明确覆盖；两次候选各自派生正文，允许后仍受 bridge/group/Responses/lite/冷却约束；HTTP/WS 权限复核与文本兼容 |
| `backend/internal/service/openai_ws_image_send_policy.go`、`openai_ws_forwarder_ingress.go`、`openai_ws_forwarder_support.go`、`openai_ws_forwarder_v2.go`、`openai_ws_v2_passthrough_adapter.go` | per-send 核心覆盖在 `openai_ws_image_send_policy_test.go`，该文件不带 `unit` build tag，须单独执行下方无标签命令；HTTP 转原生 WS 的 `openai_ws_forwarder_v2.go` 必须在物理发送前重新准入，新增回归仍归此测试文件，不新增测试路径；`openai_image_request_ws_test.go` 只负责请求策略 helper 边界，不代替 per-send 测试。关联回归保留 `openai_ws_forwarder_ingress_session_test.go`、`openai_ws_http_bridge_test.go`、`openai_ws_v2_passthrough_lifecycle_test.go` |
| `backend/internal/service/openai_ws_image_send_policy_test.go` | `TestWSImagePermissionRefreshPreservesChosenIdentity`：刷新权限与 extra 但保持选中凭据、代理、分组及并发，原账号快照不变；`TestWSImageWriteGuardSessionInheritanceAndFreshPermission`：文本/二进制帧继承 session 工具，发送前读取新 negative 或图片冷却并阻止 response.create |
| `backend/internal/service/openai_ws_image_send_policy_test.go` | `TestWSImageSendPassiveStripAndContinuation`：negative 仅剥 passive 声明、原始 body 不变，previous_response_id 与图片调用历史保守拒绝；`TestWSImageStandalone403ClassificationAndCooldown`：error/response.failed 的结构化固定原文与 HTTP/SSE 分类一致，无显式 status/type 仍推导 403，只图片冷却、account_capability scope、禁止同号 retry；普通消息、仅 403 无固定原文、显式冲突 502（顶层、嵌套及外层 502/内层 403）不误分类，不扫描任意文本或 prompt |
| `backend/internal/service/openai_ws_image_send_policy_test.go` | `TestWSImageCtxPoolRefreshAfterHooksAndRecovery`：after_hook、rejected_field_recovery、reconnect、previous_response_recovery 四种发送/恢复场景重新准入；negative 阻止物理发送或恢复重发，turn hook 只一次；`TestWSImagePassthroughStandalone403NoReplay`：真实 relay 收到独立图片 403 后 capability failover、只图片冷却，不重放初始帧 |
| `backend/internal/service/openai_ws_image_send_policy_test.go` | `TestWSImagePassthroughFreshPermissionAfterBeforeTurn`：后续二进制 response.create 在 BeforeTurn 更新权限后重新准入，negative 不发往上游，hook 只一次，以 1013 关闭结束而非 session failover；`TestWSImagePassthrough403AfterOutputDoesNotReplay`：语义输出后固定图片 403 只冷却图片并以 1013 关闭，不返回可重放 failover，不新增发送 |
| `backend/internal/service/openai_ws_image_send_policy_test.go` | `TestWSImageSendHeaderLiteSuppressesHostedInjection`：header descriptor 的 lite 状态继续约束物理发送，即使全局 bridge 开启也不注入 Hosted 工具 |
| `backend/internal/service/openai_ws_forwarder_v2.go`；`backend/internal/service/openai_ws_image_send_policy_test.go` | `TestWSImageHTTPNativeSendRefreshAfterAcquireAndPrewarm`：HTTP 转原生 WS 在 acquire 后及 prewarm 后重新读取权限，原始 descriptor 即使声明已被先前变换剥离仍约束业务发送；negative 不发业务帧、不触发 reconnect 或 force-HTTP/fallback 冷却，原始请求不变；prewarm 场景仅允许一次 generate=false 预热写入 |
| `backend/internal/service/openai_ws_forwarder_v2.go`；`backend/internal/service/openai_ws_image_send_policy_test.go` | `TestWSImageHTTPNativeSendAllowedAndPassiveDenied`：allowed 保留 native 工具及 tool_choice，negative 仅剥无状态 passive 声明；原始 body、JSON 大整数、上游模型映射与客户端模型回填不变，usage 正确返回，仅一次物理业务发送 |
| `backend/internal/service/openai_ws_forwarder_v2.go`、`openai_gateway_forward.go`；`backend/internal/service/openai_ws_image_send_policy_test.go` | `TestWSImageHTTPNative403BeforeAndAfterOutput`：error/response.failed 固定原文无 status 推导 403；输出前 account_capability failover 且无同号 retry，输出后保留 Forward result、恰好一次失败终止事件且不是可重放 failover；两种情况均只图片冷却，不 reconnect、force-HTTP 或新增物理发送 |
| `backend/internal/service/openai_ws_forwarder_v2.go`；`backend/internal/service/openai_ws_image_send_policy_test.go` | `TestWSImageHTTPNativeExplicit502DoesNotCooldown`：error/response.failed 显式 502 不分类为图片权限故障、不图片冷却；`TestWSImageHTTPNativePrewarm403StopsBusinessSend`：预热固定 403 仅图片冷却并阻止后续业务帧，仅一次 generate=false 写入，不提交下游、不 reconnect 或 force-HTTP/fallback 冷却 |
| `backend/internal/service/openai_ws_forwarder_v2.go`、`openai_gateway_forward.go`；`backend/internal/service/openai_ws_image_send_policy_test.go` | `TestWSImageHTTPNativeImageOutputThen403PreservesBillingWithoutReplay`：stream=false 与 buffered_stream 各覆盖 error/response.failed；已产生图片即使 HTTP 尚未提交也不返回可重放 failover，返回 openAIWSImageOutputError；公开 Forward 同时传回 partial usage（input/output/image tokens）、ImageCount、ImageOutputSizes、图片 BillingModel/尺寸档和模型映射，terminal event 为 response.failed；非流式返回 failed JSON，缓冲流式保留图片事件并仅一次失败终止；原始 body 不变、只图片冷却、仅一次连接及业务发送，无 reconnect 或 force-HTTP/fallback 冷却。此断言验证计费输入返回，不代表实际扣费落库验证 |
| `backend/internal/service/openai_ws_forwarder_v2.go`、`openai_gateway_forward.go`；`backend/internal/service/openai_ws_image_send_policy_test.go` | `TestWSImageHTTPNativeOutputItemDoneThen403DoesNotReplay`：无 delta 的完整 output_text message 或 function_call 的 output_item.done 已构成语义输出；stream=false 与 buffered_stream 各覆盖 error/response.failed，HTTP 尚未提交也返回不可重放 openAIWSImageOutputError 而非 failover；公开 Forward 保留 partial input/output usage、文本 BillingModel、ImageCount=0 和 response.failed terminal；非流式 failed JSON、缓冲流式保留语义项并仅一次失败终止，只图片冷却、仅一次连接/业务发送，无 reconnect 或 force-HTTP/fallback 冷却，原始 body 不变 |
| `backend/internal/service/openai_ws_forwarder_v2.go`；`backend/internal/service/openai_ws_image_send_policy_test.go` | `TestWSImageHTTPNativeEmptyOutputItemDoesNotCommit`：空项、未知类型、空/纯空白/无类型文本、函数缺 name/call_id 或 arguments 非字符串均不构成语义输出；非流式与缓冲流式不提交 HTTP，后续固定 403 返回 account_capability failover 且禁止同号 retry，result=nil，只图片冷却、仅一次连接/业务发送 |
| `backend/internal/service/openai_ws_forwarder_v2.go`、`openai_gateway_forward.go`；`backend/internal/service/openai_ws_image_send_policy_test.go` | `TestWSImageHTTPNativeOutputFailurePreservesUsageWithoutReplay`：文本、函数、图片 output_item.done 后的普通 disconnect、timeout、error、invalid_json，各覆盖非流式与缓冲流式；公开 Forward 同时返回错误和 partial result，保留 input/output usage 及已观测图片数量/尺寸，terminal 为 response.failed；不返回可重放 UpstreamFailoverError 或 openAIWSFallbackError，仅一次连接/业务发送；非流式 failed JSON、流式恰好一次 response.failed；普通失败不误触发图片权限冷却。此断言验证计费输入保留，不表示实际扣费落库验收 |
| `backend/internal/service/openai_sse_concatenated_json_test.go` | `TestOpenAIWSv2RejectsMalformedEventAfterWritingDownstream`：已写文本 delta 后收到非法事件，公开 Forward 返回错误和非 nil partial result，未观测 usage 保持 OpenAIUsage 零值，terminal 为 response.failed；下游保留原 delta 并追加一个合法 response.failed，帧序列严格为 response.output_text.delta、response.failed；非法 unexpected-tail 与 response.in_progress 不泄露，损坏连接关闭。替代旧 nil result/无 failed 断言，登记不表示已执行或通过 |
| `backend/internal/service/openai_image_capability_scheduler.go`、`openai_account_scheduler.go`、`openai_gateway_scheduling.go`、`openai_fixed_account_retry.go` | `openai_image_capability_scheduler_test.go`：`TestResponsesImageCapabilityRoutingIndependentOfCost`、`TestImageCandidateCooldownAndMappedModel`；图片成本开关关闭仍过滤负向能力，known allowed 优先而 unknown 兜底，冷却与文本 model mapping 分离；`openai_fixed_account_retry_test.go`：`TestFixedOpenAIRetryDoesNotSelectAnotherAccount`、`TestFixedOpenAIRetryRechecksModelAndExclusion`，只选原账号并重新准入/并发 |
| `backend/internal/handler/openai_gateway_handler.go`、`openai_chat_completions.go`、`openai_images.go`、`openai_fixed_account_retry.go`、`openai_image_routing.go` | `openai_image_permission_routing_test.go`：`TestResponsesImagePermission403DoesNotAmplifyRetries`、`TestResponsesNativeImageSkipsDeniedAndPrefersKnownAllowed`、`TestResponsesPassiveImageDeclarationCanUseDeniedTextAccount`、`TestResponsesPoolRetryStaysOnOriginalAccount`；固定 403 不执行池内同号重试，候选或预算耗尽返回稳定 503；等待前释放 slot，后续换号预算正确；`openai_responses_failover_cancel_test.go`：取消后不新增请求、在线客户端正常 failover；Responses/Messages/Chat/Images 同号规则一致，输出后不得重放；Images 另回归 `openai_images_failover_test.go` |
| `backend/internal/handler/openai_fixed_account_retry_test.go` | `TestOpenAIUpstreamAttemptBudgetCannotBeBypassedByReselection`；独立同号预算保留，但不能通过重复候选选择绕过 upstream account 切换预算；plugin admission 不消耗该预算 |
| `backend/internal/handler/openai_fixed_account_retry_test.go`（unit） | `TestOpenAIFixedRetryFinalRPMConsumesUpstreamCapacityBudget`：固定账号最终 RPM 拒绝后，换号计入上游容量预算与 monitor switch，耗尽返回 503；`TestOpenAIFixedRetryAttemptBudgetBeforeRPM`：新账号的 attempt budget 必须先于 RPM 预留检查，耗尽不预留新账号 RPM；均覆盖 Responses/Messages/Chat/Images |
| `backend/internal/handler/openai_fixed_account_retry_test.go`（unit） | `TestOpenAIFixedRetryFinalCapacityConsumesUpstreamBudget`：固定选择成功后的最终代理容量拒绝仍受换号预算约束，不预留被拒请求 RPM，释放初始/固定账号槽与已取得代理槽；`TestOpenAIFixedRetryFinalProfitConsumesUpstreamBudget`：最终 profit 拒绝计入换号预算，不预留被拒请求 RPM，释放初始及否决槽，耗尽保持 429；profit 测试覆盖 Responses/Messages/Chat，独立 Images 保留既有不执行 profit gate 语义 |
| `backend/internal/handler/openai_fixed_account_retry_test.go`（unit） | `TestOpenAIFixedRetryAbandonBudgets`：handler 与容量预算分别限制 abandon 后换号，excluded、monitor 和容量 switch 同步；`TestOpenAIFixedRetryAbandonOAuthStop`：OpenAI/Grok OAuth 429 stop 条件保留并正确计数；`TestOpenAIFixedRetryAbandonIgnoresNonUpstreamFailures`：无上游 failure、plugin admission 拒绝及已取消请求不增加 switch/excluded |
| `backend/internal/handler/openai_image_permission_routing_test.go` | `TestResponsesNativeImageExhaustedWithoutUpstreamAttempt`：all denied 不发送上游且返回 503 `image_generation_unavailable`；`TestResponsesImagePermissionExhaustedAfterSSEStarted`：已提交 SSE 写 `response.failed` 与 OpsStreamError marker，不伪装成功；`TestResponsesImagePermissionAfterSemanticOutputDoesNotReplay`：语义输出后固定 403 不新增尝试、不重放 |
| `frontend/src/components/account/EditAccountModal.vue`、`codexImageToolPolicy.ts`、`frontend/src/i18n/locales/zh/admin/accounts.ts`、`frontend/src/i18n/locales/en/admin/accounts.ts`、`backend/internal/service/upstream_account_edit_policy.go` | `__tests__/codexImageToolPolicy.spec.ts`、`__tests__/EditAccountModal.spec.ts`、`backend/internal/service/upstream_account_edit_policy_test.go`；上游绑定默认 auto，未知与负向权限可区分；既有 bridge/explicit 手动值准确回显并保持；恢复自动同时移除 canonical 与嵌套/旧名图片覆盖字段，不改运行时字段/凭据/其它配置；普通账号、双语与存量模式不回归 |

还须回归普通 403、HTML 403、账号/workspace 停用、API Key 健康熔断与既有图片 rate limit/capability loss：`ratelimit_service_403_test.go`、`ratelimit_service_403_html_test.go`、`openai_access_state_failover_test.go`、`openai_apikey_health_breaker_test.go`、`ratelimit_service_openai_image_test.go`。支持 CGO 的环境应执行对应 service/handler 定向 `go test -race -tags unit`；不以不支持 race 的本地 compile 代替。前端执行上述 Vitest、typecheck 与构建；实际 Gate/发布仍由 production-deploy 门禁决定。

固定重试最低回归还必须覆盖最终 RPM、代理 capacity、profit 准入失败后的切换预算：失败不得漏计或靠重新选中同一账号放大尝试；预留 RPM 应遵守最终准入顺序，失败不泄漏 RPM、共享槽或代理槽。service/handler 的 `openai_fixed_account_retry_test.go` 必须按最终实现覆盖这些边界；局部旧快照通过不代替最终整套验证。

handler 固定重试专项从 `backend` 执行 `go test -tags unit ./internal/handler -run '^Test(OpenAIFixedRetry|OpenAIUpstreamAttemptBudget)' -count=1`；上述具体名称为最低断言登记，不表示已执行或通过。

WS per-send 专项从 `backend` 执行 `go test ./internal/service -run '^TestWSImage' -count=1`，不得仅以 unit helper 回归代替；支持 CGO 的环境另执行 `go test -race ./internal/service -run '^TestWSImage' -count=1`。上述登记只说明最低覆盖，不表示已执行或通过。

## 插件运行时本轮专项

以下路径与 `generic-plugin-runtime-v2`、`codex-state-plugin` 登记对应；只补精确文件，不扩大通配符。最低回归以实际断言为准，登记不表示已执行或通过。

| 入口与精确路径 | 最低测试与断言 |
| --- | --- |
| `backend/internal/service/openai_gateway_admission.go`、`openai_account_scheduler.go`、`openai_gateway_scheduling.go` | 同目录 `openai_gateway_admission_test.go`：`TestOpenAISchedulingAdmissionCandidatePoolsUseOneBatch`、`BatchesAndReusesOnlyWithinPass`、`PropagatesCancellationAndAcceptsReorderedDecisions`、`RejectsMalformedBatch`、`SendRechecksAfterCachedAllow`、`SendRechecksNewManagedScope`；候选池一次批量、仅调度轮内复用、取消传播、响应校验、发送前重新权威准入 |
| `backend/internal/service/openai_codex_models_service.go`、`openai_models_discovery_transport.go` | 同目录 `openai_models_discovery_transport_test.go` 的五个 `TestOpenAIModelsDiscovery*`：仅内部目录 GET 标记可绕过 scoped 准入；请求指针、完整 URL、账号/身份、GET/无正文约束，克隆/变更/业务 metadata 拒绝；普通业务缺 model 仍拒绝；旧插件传输、认证、代理、ETag 保留；取消或未知路由读取失败 fail-closed |
| `backend/internal/service/openai_live.go` | 同目录 `openai_plugin_admission_live_test.go`：`TestPluginAdmissionLiveSessionModel`、`TestPluginAdmissionLiveUnknownModelWithoutManagedScopePreservesRequest`、`TestPluginAdmissionLiveCreatePreservesRetryBudget`；唯一实际 Session model 与正文一致，缺失/非字符串/重复/大小写歧义在受管范围拒绝；不以计费默认模型或未应用映射补值；换号保留上游重试预算并释放临时槽位和失败 Live 租约 |
| `backend/internal/service/openai_gateway_outbound_model.go`、`openai_ws_http_bridge.go`、`openai_ws_forwarder_ingress.go`、`openai_ws_forwarder_payload.go`、`openai_ws_v2_passthrough_adapter.go` | 同目录 `openai_gateway_outbound_model_test.go`、`openai_ws_plugin_scope_test.go`、`openai_plugin_admission_bridge_test.go`：实际映射模型与每轮 metadata 一致；受管账号强制 HTTP bridge；原生连接新增受管范围先拒绝再要求重连，disabled binding 不强制桥接；桥接后续轮新增受管模型在输出前拒绝 |
| `backend/internal/service/openai_plugin_admission_routing_test.go`；`backend/internal/handler/openai_plugin_admission_handler_test.go`、`openai_plugin_admission_extended_handler_test.go` | Alpha Search 两类 builder 使用实际映射模型；HTTP/WS 及扩展入口准入拒绝不记全账号健康失败、不耗上游重试预算或改计费，换号前释放槽位；已发送/已输出不重放。两个 handler 测试文件要求 `unit` build tag |
| `backend/internal/service/plugin_codex_process_test.go`、`plugin_codex_process_forward_test.go` | `TestPluginCodexStateRealProcessHandshakeAndDisabledDraft`、`TestPluginCodexStateRealProcessForwardAndCompletion`：真实进程握手、默认关闭、AdmitBatch/Forward；合成 STATE 经指定 loopback 代理且客户端 STATE 优先，正文/SSE 字节及响应头不变；回执持久化后真实 broker `CompleteRequest` 恰好一次释放内存/持久在途保护，进程仍存活，不能用 End 或 kill 替代该专项；禁止采集、外联、直连与重放 |
| `backend/internal/service/plugin_codex_package_test.go` | `TestPluginCodexStateBuiltArchivesMatchHostContract`：两架构 Linux 发布包均由宿主检查 manifest、SDK 必需能力、secret 声明、UI/许可证/来源锁及维护文件 |

进程测试必须提供指向独立本机插件二进制的 `SUB2API_TEST_CODEX_STATE_BINARY`；包测试必须以 `SUB2API_TEST_CODEX_STATE_PACKAGES` 提供两个架构归档，按宿主 OS 的路径列表分隔符分隔。未提供环境变量产生的 skip 仅表示未验证，不算专项通过；审计本身不构建插件、不运行这些测试。

## 全量门禁

按串行顺序执行：

```text
go test -p 2 -parallel 2 ./... -count=1
go test -tags=unit -p 2 -parallel 2 ./... -count=1
Vitest
typecheck
ESLint
frontend production build
release pytest
git diff --check
```

审计 skill 只输出清单，不执行这些应用与发布门禁。构建和环境验证由 `sub2api-production-deploy` skill 决定。

## 历史 profile 240 专项合同

profile 239 和 profile 240 均属于不可变历史合同，不得改写或提前登记为其他发布证据：

```text
base profile: 239
version: 0.1.177-baiyu
 migration count: 58
appended migrations: 240_upstream_observation_preference.sql, 241_precise_upstream_effective_rate.sql
migration source map sha256: b4a160cc14979fedbf1099591b1e7a47e309ed12090837ba3f72c5feeb1a3b5a
migration 239 source sha256: 022c4031ec02f3118ad4dbced90089f2fe8ea6000b43008385751a5ab849e147
migration 240 source sha256: 7e5958d2a430b8b107f84c91beecdb5f3f3ad418f78ebe6d916d77ccd6a34175
migration 241 source sha256: e2b7e4f17be261e3021820e11e827fa4bba3837baddb4f1e43aeb6d97261ef2e
migration release-manifest map sha256: 0a0afac9d991533476f66210d1a7ed1cc18dc81f664910cee240ea0ed12eaf9a
```

最低专项测试：

```text
backend/internal/repository/upstream_account_lifecycle_test.go
backend/internal/repository/upstream_key_reconcile_integration_test.go
backend/internal/repository/scheduled_test_repo_lifecycle_test.go
backend/migrations/profile_239_migrations_test.go
frontend/src/views/admin/__tests__/AccountsView.upstreamManagement.spec.ts
frontend/src/components/account/__tests__/UpstreamHealthHistory.spec.ts
frontend/src/components/account/__tests__/TTFTGuardStatusBadge.spec.ts
frontend/src/components/common/__tests__/DataTable.spec.ts
frontend/src/components/account/__tests__/UpstreamImagePricingSummary.spec.ts
```

后续补强测试：

```text
混合 manual/sync_managed 绑定时不自动归档
不完整或失败同步不推进缺失计数
归档事务失败时 Key 与账号均不发生部分删除
恢复后原账号 ID、分组、定时计划和历史保持不变
Redis 调度缓存、共享并发 lease/queue 和健康 Registry 不残留旧状态
普通账号页与上游管理页切换时列、排序、自动刷新和异步响应完全隔离
多行健康历史只存在一个 Tooltip 宿主
大量 TTFT 徽标只存在一个全页 ticker
DataTable 高频 ResizeObserver 通知只触发一帧测量
图片成本摘要字段缺失、partial、stale 和免费成本 0 的结构化展示
```

## V1 渠道状态缓存率与展示分组

- V1 主动探针与 V2 用量后台汇总同时启用，但 V2 专用页面/API 仍只在 V2 模式放行。
- `go test ./internal/repository ./internal/service ./internal/handler ./internal/server/routes -run 'Test(MonitorCache|BatchPrimaryCacheRates|ListUserView_|ChannelMonitorRuntimeActiveProbesAllowed|ChannelMonitorModeV2Guard)' -count=1`（在 backend 下执行）。
- `vitest run src/utils/__tests__/channelMonitorGrouping.spec.ts src/components/user/__tests__/MonitorCard.quota.spec.ts src/components/user/__tests__/MonitorCardGrid.spec.ts src/components/user/__tests__/MonitorTimeline.ttft.spec.ts`（在 frontend 下执行）。
- 重点核对当前用户无权分组、监控未绑定分组、零分母、未满 `minimum_sample`、15d/30d 回填未完成都不显示百分比；四类导航和卡片原始厂商标识一致，详情仍保留探针延迟。

## 额外成本金额输入

- `ExtraCostsDialog` 的金额状态接受 `string | number`；原生数字输入事件不得触发 `trim is not a function` 或卸载弹窗。
- 空值、负数和非有限值禁止提交；整数、小数及 `0` 可提交且发送数值金额；只输入不调用新增 API。
- 成本摘要复用 `frontend/src/components/admin/usage/AccountCostAmount.vue`：默认只显示合计，悬浮、点击和键盘聚焦显示加数；Escape/失焦关闭。用量总消费保留“成本：”，仪表盘今日/累计均无前缀；保留服务端合计（含零）、旧字段回退和隐藏账号成本行为。
- 最低回归：`frontend/src/components/admin/usage/__tests__/ExtraCostsDialog.spec.ts`、`frontend/src/components/admin/usage/__tests__/UsageStatsCards.spec.ts`、`frontend/src/views/admin/__tests__/DashboardView.spec.ts`。

## 官方 0.2.10 与 PR #7730

- 固定官方基线 `a60a29549f488a854966aaec9541abbe006cac22` 和 PR head `e6d191a83f0b37df3cb5b183e9cfd42e47b47a1d`；核对普通 merge 第二父、作者提交历史及 fork 专属账号编辑、WebSocket 映射和输入长度准入。
- 运行 `go test ./internal/handler ./internal/service ./internal/pkg/openai ./internal/pkg/apicompat ./migrations ./cmd/server`；重点覆盖 GPT-6.1 Sol 模型目录与兼容协议、Codex 套餐、Astra Ultrafast 计费和复合 WebSocket 路由。
- 运行前端 Vitest、i18n、`vue-tsc`、ESLint 和生产构建；重点覆盖账号创建/编辑模型白名单、套餐徽章和仪表盘切换，检查定价 JSON 无重复模型键。
- profile 257 保留 `0.2.9-baiyu`、parent 256、迁移 284；profile 258 为 `0.2.10-baiyu`、parent 257、`new_migrations=[]` 的历史合同。

## 官方 0.2.11 主线

- fork 预留兼容：运行带 unit 标签的 TestReserveInflightBalance_CustomizationUsesBillingIdentity、TestInflightEstimate_CustomizationUsesOriginalGroupAndModel 和 TestInflightEstimate_ExplicitZeroSuppressesIndependentImageRate。原订阅映射到余额组不预留，原余额映射到订阅组仍预留；定制模型不得借用目标价格，显式免费用户不被图片预留拒绝。Grok Realtime 必须在统一准入后重新读取候选账号，凭据、RPM、业务代理和握手均使用最新快照。

- 固定目标 `42bc7f6cffe24bcb471608e48e66b4a0afa1f882`，核对普通 merge 第二父及官方 PR #7730 merge `327c32218406cef47186736c7473275cd1b39918` 的祖先关系。原 fork PR 合入提交和作者历史不删除；已完整进入官方的模型与套餐能力改由 upstream 维护。
- 验证余额模式在途预留跨 HTTP、SSE、WebSocket、音频和图片请求只预留一次，并在计费任务扣减后释放；保留 fork 原有配额、转发和并发约束。验证 API Key 创建数量/频率限制及 Claude reset 兑换的幂等性、锁和前端入口。
- 核对 Codex 远程模型目录、套餐徽章、账号编辑、WebSocket 首帧模型快照、Grok Realtime 握手前探测、RPM、读限额与最长会话。运行相关 Go 测试、前端 Vitest、i18n、类型检查、ESLint、生产构建和 release pytest。
- profile 259 与已 signed、2026-10-01 生产 verified 的 260 保留历史合同；保留 261 为 `0.2.13-baiyu`、parent 260、仅新增 286/287，完整官方目标为 `b8dece9000c68815a5b867ca5a1e6f236e173905`。当前 pending 262 保持相同版本、parent 261、仅新增分布探针状态迁移 288。原始字节 checksum 已登记，post-merge 审计通过不代表新的 VM Gate 或生产发布完成。
- 285 必须同时有隔离 VM 正负向行为断言、生产只读函数/trigger 校验及 signed Gate 必需证据；已应用迁移的 verified replay 也重新核验。空 actual 只允许既有同绑定、身份/价格/来源/优先级不变的暂停或归档，新增、启用、恢复与换绑拒绝；0 仍是有效倍率。专用断言接线的精确审阅只覆盖明确路径和 before/after blob 与模式，不能放行恢复算法变更；追加修改、模式变化、删除及跨路径复用应恢复阻断。
- profile 执行闭包：运行 release pytest 全量及 `test_profile260_release_contract.py`、`test_profile260_maintenance_contract.py`、`test_profile262_release_contract.py`、`test_migration_planner_v2.py`、`test_recovery_gate.py`、`test_production_space_clean.py`。260、261 与当前 262 必须通过全部验证/签名/生产/恢复/清理入口，259 保持历史版本、parent 和空新增迁移合同，263 必须拒绝；VM validator 必须拒绝 260 的错误版本、parent、空迁移清单、284 或混合清单，所有祖先 migration assertions 保留。`billing_inflight_cache.go` 必须精准触发 `specialized/redis_changed`，相邻业务路径不得误升级。Linux VM 签名、隔离恢复和候选验证必须另行完成，不能用本机 Shell 片段测试冒充。
- VM-only 与生产恢复点保留入口额外执行 `test_vm_only.py`、`test_production_recovery_retention.py`：VM-only 拒绝未知 profile、错误 scope 与 release/profile 不匹配；恢复点枚举的 Shell/Python profile 范围一致，保留当前、上一和受引用恢复点的原保护规则。
- 运行镜像身份执行 `test_production_snapshot.py` 的版本化 mock 回归：doctor、Python 快照和 Shell preflight 共用签名 `runtime-identity.sh`；在 VM 实际执行 digest/唯一标签、缺失标签、冲突标签、同 commit 多标签与快照漂移，验证规范化 checksum 一致或明确拒绝。本机缺 jq 时跳过不能冒充 Linux 执行成功。

## 官方 0.2.13 普通 Merge 合同

- 普通 merge 的第二父必须为 `b8dece9000c68815a5b867ca5a1e6f236e173905`。两个官方同 prefix 241 新文件原字节改名为 `286_add_payment_order_bonus_amount.sql`、`287_add_typesafe_platform.sql`，保持旧 local241 和全部历史 migration/profile/checksum。
- 执行 `test_profile261_release_contract.py`，覆盖历史 260 与 261 不变、未知 263 拒绝、官方 SQL 原始字节、261 合同只新增 286/287、部分应用与 verified replay、checksum 冲突及旧官方文件名 unknown；执行新增 `test_profile262_release_contract.py` 验证当前 262 与迁移 288，保持 285 的祖先语义 Gate 证据。
- Profile 261 的恢复兼容审阅仅绑定 `f670e8051529776fa681b628eb05ce073d73c17c` → `a541a5c4bb6ad72e7198d2802fc6dfac22b305ee` 的同路径 blob/mode。`test_recovery_gate.py` 验证 35 项敏感对象精确命中、恢复算法和信任文件不被豁免，以及新增修改、删除、跨路径复用和模式漂移仍阻断；最终 SHA 必须重新分类为 specialized 并在 VM 完成真实隔离恢复。
- TypeSafe 的平台目录、账号/分组编辑与独立网关适配按 `registered-platform-catalog` 精确登记；保留官方 API-key/业务校验，接入既有 shared account slot、RPM 和 failover。测试文件存在和静态合同不等于实际网关、PostgreSQL、VM 或恢复验证通过。

## OpenAI Sol 单次探针与 128 次滑动分布

- 262 恢复兼容审阅固定生产基线 `bb47353679b65ba37dad6ce9fde06c01e8afed7e` → `b710ec5b15b1baf0d01d43bed5aec1eb26836ab4`，仅匹配 35 项敏感路径的 blob/mode。`test_recovery_gate.py` 验证准确覆盖、恢复算法/备份格式/信任资产不变，以及每项的内容、模式、删除和跨路径复用均恢复阻断；显式 Full 不绕过。最终候选仍须重新分类并执行真实 specialized 隔离恢复。

- 每次真实探针恰好一次请求，固定 Sol、空历史、system 点号、low、128 输出 token、流式；Responses `store=false` 与 Chat 合同分别测试，无 padding／额外挑战／即时重试。
- 验证固定随机排列循环，任何连续 128 次维持 64／16／48 配额，第 129 次只淘汰最早一次；失败／空回答／不完整／崩溃占槽，未发送的配置失败与跳过不消费。
- 验证跨实例 revision CAS 与 120 秒租约、手动／定时互斥、重启恢复、旧 token／series 拒绝、凭据／端点／模型映射／协议／基准变更重建，以及 35 天保留不补历史成功样本；闲置清理保留系列／题序／序号，遵循观测事务回滚，并不能覆盖并发 Claim／Finish，PostgreSQL 缺迁移错误不得静默。
- SSE 与 JSON fallback 保留原始文本和空白分段后统一规范化，覆盖原始长度上限及空回答，不得在长度检查之前预先 trim。
- 对照授权基准 oracle fixtures 验证 Sol／Astra／other、unknown 答案、39／10／29 最低有效门槛、并列与阈值、基准缺失、旧 Juice 隔离；分数只表示行为匹配，相邻窗口不是独立证据。
- 验证 mismatch 不影响健康状态／可用性／调度，不写旧 24h／7d 指标；告警失败原子回滚、首次 mismatch 与充分判定切换生成事件，重复 mismatch／insufficient 静默，事件 payload allowlist。
- Go：`upstream_confidence_distribution_test.go`、`upstream_confidence_distribution_state_test.go`、`upstream_confidence_distribution_repo_test.go`、`upstream_confidence_distribution_integration_test.go`、`profile_262_migrations_test.go`；真实 PostgreSQL integration 与 VM Gate 独立记录，Docker 不可用不得声称通过。
- 前端：`UpstreamHealthCell.spec.ts`、`UpstreamManagementSettingsDialog.spec.ts`、`HelpTooltip.spec.ts`、i18n／类型检查及桌面／移动布局；采集进度、状态色、有效样本、分布、候选浮点匹配分数和采样时间完整，无身份概率文案。
- profile 262 为 `0.2.13-baiyu`、parent 261，仅新增 288；原始字节 SHA-256 与 audit catalog 一致，Gate catalog 使用 strip 后 SQL checksum。执行 `test_profile262_release_contract.py` 及现有 Python／Shell closure 测试，保留 261／历史合同，未知 263 拒绝；恢复敏感变更仍按最终 blob／mode 分类，不扩大精确审阅豁免。
