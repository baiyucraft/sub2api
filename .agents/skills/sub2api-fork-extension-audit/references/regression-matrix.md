# Fork 扩展最低回归矩阵

| 扩展域 | 最低回归要求 |
| --- | --- |
| 上游配置与管理 | provider 同步、缺失 Key 对账、派生账号绑定、账号编辑白名单、两个一级菜单、局部运行态刷新；合并时若 upstream/main 变更账号管理/编辑，必须同步对比上游管理后端、编辑 modal、白名单、payload 与回归测试 |
| 上游派生账号生命周期 | 仅完整同步推进缺失计数；sync_managed 连续缺失 3 次且至少 30 分钟后与 Key 同事务软归档；manual 永不自动归档；仅 `sync_managed+key_missing` 恢复同 ID 并保留分组、定时计划和历史；定时计划过滤 deleted_at；归档/恢复清理账号缓存、Redis、共享并发和健康 Registry |
| 上游模型能力同步 | 仅 sync_managed 自动写白名单；NewAPI 有效 `model_limits` 优先于 live `/models`；无效/空结果保留最近成功映射；30m freshness 跳过重复请求，30m–24h 继续执行旧白名单，超过 24h 放行其他能力回退；成功更新触发 scheduler outbox/快照失效；并发 4、单账号 15s；状态与错误不得泄露 URL、凭据、响应体或原始错误 |
| 全局模型别名同步 | 设置 API 兼容缺省字段并校验对象/字符串/空白；保存只写 `upstream_model_alias_rules` 不触发同步；sync_managed 下一次同步按真实模型生成 identity/alias 并保存 `auto_mapping`；manual 账号不受影响；手工映射目标消失清理、源消失但目标存在保留；失败保留旧映射和快照；成功触发 scheduler outbox/账号快照失效 |
| NewAPI 兼容 | 旧 `data.id + Cookie`、新 `data.user.id + access_token`、Bearer 与 `New-Api-User`、无会话失败、三种认证模式互不影响 |
| 共享并发 | 同上游多 Key 共享 slot/lease/queue/load，不同上游隔离，优先级来源解析，降低上限不终止已有请求 |
| 容量故障转移 | 普通 OpenAI 等待队列满按共享并发目标换号；上游绑定账号 429 在同号重试耗尽后按账号换号；共享运行时开关/独立预算/可配置耗尽状态；设置关闭保留上游 429；流式与 WebSocket 不拼接跨账号语义输出；Ops 保留真实 upstream_status=429 |
| LoadFactor | 普通账号硬并发使用 Concurrency，调度容量使用 LoadFactor 或回退；上游账号忽略派生账号字段；Priority/倍率同步不改 LoadFactor |
| TTFT Guard | 仅带实际 group_id 分组上下文的真实业务可见首 Token 采样；状态按 group_id + account_id + canonical model 隔离；OpenAI/Composite 分组支持 inherit/enabled/disabled；策略写入与 group_changed outbox 原子化且幂等更新不发事件；各实例消费 group_changed 后清 policy cache 与该分组本地运行态；策略缓存失效期间的旧 DB 读取不得重新回填；全局设置通过 fork Redis 通道广播，远端刷新成功后只清配置变化的 inherit/global 状态，刷新失败和自定义策略均保留；无分组后台账号测试、健康探针和其他主动探针不采样、不进入分组 Guard；degradation 携带分组名、策略来源、三类触发原因和恢复倒计时 |
| Codex STATE PR 基线 | 保留 PR #7315 head/merge 与 PR #7338 head/fork merge 的父历史和作者署名；#7315 由 #7338 历史带入，不重复合并；全局开关、动态代理采集、固定代理复验、持久化、watchdog、一小时生命周期、提前十分钟续期、八次尝试和五分钟冷却保持回归 |
| Codex STATE fork 可靠性增强 | Astra/Sol/Terra 按账号、实际出站模型、revision、ChatGPT identity 隔离，代理 ID/URL/组成员不进入票据所有权；严格 envelope、内部签发时间、Pro 10 块/Team 12 块；active/ready、连续两次异常、旧版本保护和续期失败保留 active；客户端同账号 STATE 优先、跨账号剥离、无客户端 STATE 才注入；缺票 strict 不被 TTFT fail-open 绕过；Redis/PostgreSQL 分布式采集锁；普通编辑保留、新建/导入不继承、列表/导出/审计脱敏；HTTP 与 WS-to-HTTP bridge 回归，原生上游 WS 明确不支持 |
| Codex STATE 参考负向边界 | ccodex-sleep-state 26b22196 为 #7338 原参考点、b18fabf9 为 2026-09-19 fork 审查点；仅作 GPL-3.0 设计参考且不得复制源码或依赖；不得引入其本地 Codex 配置接管、CCS/profile/Web 面板、订阅/代理节点池、纯内存 STATE、响应正文拦截、on_demand/standby 用户策略、本地 relay、原生上游 WS、账号暂停恢复或配置恢复行为 |
| OpenAI OAuth 代理组 | 参考 Go1c/sub2api PR #423、按 0.2.7-baiyu 独立适配；单代理/代理组互斥，OAuth/Setup Token 类型限制，空组/Redis 故障 fail-closed；会话稳定绑定、绑定代理满载不改绑、失效后重绑；账号总闸加 account+proxy 每代理槽；全部成员满进入现有容量换号；列表只返回脱敏摘要与每代理并发标签；HTTP/SSE/JSON/compact/Chat/Messages/Embeddings/Images/Alpha Search/quota/OAuth/WS 握手出口一致 |
| 账号级 STATE 与代理组 | STATE 所有权固定为 account_id + outbound_model + revision + ChatGPT identity，不包含代理 ID/URL/组成员；动态代理采集后依次使用组内可用代表复验，任一成功发布账号级 active/ready；更换代理或成员不失效，ChatGPT 身份变化必须失效；组无可用出口时 strict 阻断，不创建每代理票据槽 |
| 导入复制代理协议残留 | `account-import-copy-proxies` 为 deprecated/protocol-reject-only；管理端不再提供复制入口，非空 `copy_proxy_ids` 固定返回 `COPY_PROXY_IMPORT_DEPRECATED`；新导入每源账号只创建一条并可统一绑定代理组；旧共享指纹字段不再参与身份派生，历史副本不自动合并、停用或删除 |
| 健康探针 | OpenAI Responses、Anthropic Claude Code profile、Gemini 原生流；首文本、终止事件、challenge、截断流、超时和非 2xx 分类 |
| Probe Guard | 默认 401/403、429/529、5xx、其他 4xx 规则；自定义错误码追加；阈值暂停、成功恢复、人工恢复与业务隔离 |
| 健康趋势 | 列表 24 点、35 天保留、6h/24h/7d/30d 聚合、P50/P95、断点、Tooltip、中英文和暗色模式 |
| 账号页运行态与渲染 | 普通账号页与上游管理页的隐藏列、排序、自动刷新配置、ETag、请求 generation 和静默窗口完全隔离；切换 scope 后旧响应不得覆盖；健康历史单 Tooltip；TTFT 全页单 ticker；DataTable 测量使用可取消的 rAF 合并 |
| 成本与归因 | 倍率/Priority 独立、原始倍率不公开、余额与价格快照、usage/batch-image 归因不随后续解绑变化 |
| Channel Monitor V2 | managed Key 生命周期、倍率趋势、分组权限、隐私默认值、错误分类和缓存/rollup |
| 质量与累计用量 | 质量仅展示不参与调度；coverage/backfill 完整后才允许 raw cleanup；日聚合时区正确 |
| 图片成本路由与展示 | Key 快照 supported/status/stale、共享/独立倍率、1K/2K/4K 成本、免费成本 0、partial/stale/unknown 排序、prefer/strict、无价格回退、普通文本隔离、账号 hydration、API Key auth cache、scheduler cache、账号页与分组配置 UI；成本摘要必须结构化展示能力、倍率来源和分辨率成本；不得绕过健康、共享并发、TTFT Guard 或 Priority 约束 |
| migration/profile/version | migration 233 语义、官方 migration 编号冲突按内容重编号、历史 profile 233–253 合同不可变；当前 profile 254 对应 `0.2.7-baiyu`、parent 为 253、`new_migrations=[278_fork_group_ttft_guard_policies.sql,279_proxy_ip_groups.sql]`，由 release manifest 绑定数据库 migration catalog 与生产兼容快照；migration 279 默认每代理并发 10、范围 1–1000、单代理/代理组互斥；用户专属倍率以 `rate_percent` 为业务真值，兼容绝对倍率按当前普通倍率派生；`VERSION = official release version + -baiyu`，源码 VERSION 滞后的正式 tag 必须按目标 commit 显式固定版本；fork VERSION 每变化一次都新增下一个连续 profile，不得回写旧 profile |
| 官方 Astra 支持 | 使用目标官方的识别、静态目录、live/pinned 能力和测试；默认 medium 与 low 至 max，live/pinned 额外能力不被 fork 旧规则裁剪；ultrafast 与推理 ultra 区分；Anthropic 桥接、指纹、0 倍率和共享并发单独回归 |
| compact 账号列表与编辑 | 脱敏列表保留上游身份、能力和调度字段；按需详情不被列表刷新覆盖；模型同步 persisted 分支与官方元数据分支独立；图片回填和请求 ID 头字段只放宽精确白名单 |
| 发布运维 skill | release pytest、日志合同、Git Bash、清理 dry-run/apply、profile signer/validator、8211 单实例与成功后收口 |
| 发布 DMIT 中继 | `test_ssh_output.py`、`test_racknerd_readonly_status.py`；验证 DMIT direct SSH、1080 HTTP CONNECT、RackNerd host key、代理失败 fail-closed，以及命令/SFTP 共用连接入口 |
| AstrBot 渠道状态插件 | `python -m pytest astrbot/tests -q`；V2 snapshot 优先、V1 history 回退、平台分组、普通倍率、不泄露管理员 API Key、/status 与定时推送均为单图 |

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

## 额外成本金额输入

- `ExtraCostsDialog` 的金额状态接受 `string | number`；原生数字输入事件不得触发 `trim is not a function` 或卸载弹窗。
- 空值、负数和非有限值禁止提交；整数、小数及 `0` 可提交且发送数值金额；只输入不调用新增 API。
- 成本摘要复用 `frontend/src/components/admin/usage/AccountCostAmount.vue`：默认只显示合计，悬浮、点击和键盘聚焦显示加数；Escape/失焦关闭。用量总消费保留“成本：”，仪表盘今日/累计均无前缀；保留服务端合计（含零）、旧字段回退和隐藏账号成本行为。
- 最低回归：`frontend/src/components/admin/usage/__tests__/ExtraCostsDialog.spec.ts`、`frontend/src/components/admin/usage/__tests__/UsageStatsCards.spec.ts`、`frontend/src/views/admin/__tests__/DashboardView.spec.ts`。
