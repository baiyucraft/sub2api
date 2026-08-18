# Fork 扩展最低回归矩阵

| 扩展域 | 最低回归要求 |
| --- | --- |
| 上游配置与管理 | provider 同步、缺失 Key 对账、派生账号绑定、账号编辑白名单、两个一级菜单、局部运行态刷新 |
| 上游派生账号生命周期 | 仅完整同步推进缺失计数；sync_managed 连续缺失 3 次且至少 30 分钟后与 Key 同事务软归档；manual 永不自动归档；仅 `sync_managed+key_missing` 恢复同 ID 并保留分组、定时计划和历史；定时计划过滤 deleted_at；归档/恢复清理账号缓存、Redis、共享并发和健康 Registry |
| 上游模型能力同步 | 仅 sync_managed 自动写白名单；NewAPI 有效 `model_limits` 优先于 live `/models`；无效/空结果保留最近成功映射；30m freshness 跳过重复请求，30m–24h 继续执行旧白名单，超过 24h 放行其他能力回退；成功更新触发 scheduler outbox/快照失效；并发 4、单账号 15s；状态与错误不得泄露 URL、凭据、响应体或原始错误 |
| NewAPI 兼容 | 旧 `data.id + Cookie`、新 `data.user.id + access_token`、Bearer 与 `New-Api-User`、无会话失败、三种认证模式互不影响 |
| 共享并发 | 同上游多 Key 共享 slot/lease/queue/load，不同上游隔离，优先级来源解析，降低上限不终止已有请求 |
| LoadFactor | 普通账号硬并发使用 Concurrency，调度容量使用 LoadFactor 或回退；上游账号忽略派生账号字段；Priority/倍率同步不改 LoadFactor |
| TTFT Guard | 仅真实业务可见首 Token 采样，canonical model 隔离，主动探针 TTFT 不进入 Guard，状态列多模型展示和倒计时 |
| 健康探针 | OpenAI Responses、Anthropic Claude Code profile、Gemini 原生流；首文本、终止事件、challenge、截断流、超时和非 2xx 分类 |
| Probe Guard | 默认 401/403、429/529、5xx、其他 4xx 规则；自定义错误码追加；阈值暂停、成功恢复、人工恢复与业务隔离 |
| 健康趋势 | 列表 24 点、35 天保留、6h/24h/7d/30d 聚合、P50/P95、断点、Tooltip、中英文和暗色模式 |
| 账号页运行态与渲染 | 普通账号页与上游管理页的隐藏列、排序、自动刷新配置、ETag、请求 generation 和静默窗口完全隔离；切换 scope 后旧响应不得覆盖；健康历史单 Tooltip；TTFT 全页单 ticker；DataTable 测量使用可取消的 rAF 合并 |
| 成本与归因 | 倍率/Priority 独立、原始倍率不公开、余额与价格快照、usage/batch-image 归因不随后续解绑变化 |
| Channel Monitor V2 | managed Key 生命周期、倍率趋势、分组权限、隐私默认值、错误分类和缓存/rollup |
| 质量与累计用量 | 质量仅展示不参与调度；coverage/backfill 完整后才允许 raw cleanup；日聚合时区正确 |
| 图片成本路由与展示 | Key 快照 supported/status/stale、共享/独立倍率、1K/2K/4K 成本、免费成本 0、partial/stale/unknown 排序、prefer/strict、无价格回退、普通文本隔离、账号 hydration、API Key auth cache、scheduler cache、账号页与分组配置 UI；成本摘要必须结构化展示能力、倍率来源和分辨率成本；不得绕过健康、共享并发、TTFT Guard 或 Priority 约束 |
| migration/profile/version | migration 233 语义、官方 221–223 本地重编号、历史 profile 233–239 map/checksum/compatibility identity 不可变；当前 profile 240 为 pending/current 合同，58 项 migration，追加 `240_upstream_observation_preference.sql` 与 `241_precise_upstream_effective_rate.sql`，源码 migration map digest 为 `49d31f3f27698d030303c9aa3e202637dee8aec9b6549e57bc53b86412ad0841`；`VERSION = upstream VERSION + -baiyu` |
| 发布运维 skill | release pytest、日志合同、Git Bash、清理 dry-run/apply、profile signer/validator、8211 单实例与成功后收口 |

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

## 当前 profile 240 专项合同

profile 239 已进入不可变历史合同。profile 240 仍属于当前待发布合同，不得提前登记为已发布历史证据：

```text
base profile: 239
version: 0.1.177-baiyu
 migration count: 58
appended migrations: 240_upstream_observation_preference.sql, 241_precise_upstream_effective_rate.sql
migration source map sha256: 49d31f3f27698d030303c9aa3e202637dee8aec9b6549e57bc53b86412ad0841
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
