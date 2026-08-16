# Fork 扩展最低回归矩阵

| 扩展域 | 最低回归要求 |
| --- | --- |
| 上游配置与管理 | provider 同步、缺失 Key 对账、派生账号绑定、账号编辑白名单、两个一级菜单、局部运行态刷新 |
| NewAPI 兼容 | 旧 `data.id + Cookie`、新 `data.user.id + access_token`、Bearer 与 `New-Api-User`、无会话失败、三种认证模式互不影响 |
| 共享并发 | 同上游多 Key 共享 slot/lease/queue/load，不同上游隔离，优先级来源解析，降低上限不终止已有请求 |
| LoadFactor | 普通账号硬并发使用 Concurrency，调度容量使用 LoadFactor 或回退；上游账号忽略派生账号字段；Priority/倍率同步不改 LoadFactor |
| TTFT Guard | 仅真实业务可见首 Token 采样，canonical model 隔离，主动探针 TTFT 不进入 Guard，状态列多模型展示和倒计时 |
| 健康探针 | OpenAI Responses、Anthropic Claude Code profile、Gemini 原生流；首文本、终止事件、challenge、截断流、超时和非 2xx 分类 |
| Probe Guard | 默认 401/403、429/529、5xx、其他 4xx 规则；自定义错误码追加；阈值暂停、成功恢复、人工恢复与业务隔离 |
| 健康趋势 | 列表 24 点、35 天保留、6h/24h/7d/30d 聚合、P50/P95、断点、Tooltip、中英文和暗色模式 |
| 成本与归因 | 倍率/Priority 独立、原始倍率不公开、余额与价格快照、usage/batch-image 归因不随后续解绑变化 |
| Channel Monitor V2 | managed Key 生命周期、倍率趋势、分组权限、隐私默认值、错误分类和缓存/rollup |
| 质量与累计用量 | 质量仅展示不参与调度；coverage/backfill 完整后才允许 raw cleanup；日聚合时区正确 |
| 图片成本路由与展示 | Key 快照 supported/status/stale、共享/独立倍率、1K/2K/4K 成本、免费成本 0、partial/stale/unknown 排序、prefer/strict、无价格回退、普通文本隔离、账号 hydration、API Key auth cache、scheduler cache、账号页与分组配置 UI；不得绕过健康、共享并发、TTFT Guard 或 Priority 约束 |
| migration/profile/version | migration 233 语义、官方 221–223 本地重编号、历史 profile 233–237 map/checksum/compatibility identity 不可变；当前 profile 238 为 pending/current 合同，54 项 migration，追加 `237_image_cost_routing.sql`，migration map digest 为 `322ec9fe133e3209611a0c1cad357732512a381baed685334d00d8c8ede0cdf5`；`VERSION = upstream VERSION + -baiyu` |
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

## 当前 profile 238 专项合同

profile 238 仍属于当前待发布合同，不得登记为已发布历史证据：

```text
base profile: 237
version: 0.1.177-baiyu
migration count: 54
appended migration: 237_image_cost_routing.sql
migration sha256: f34d5ed6ae8c7b9fba9cf20d80f78308fa0b562657f936f2b2617a0a48b27d33
migration map sha256: 322ec9fe133e3209611a0c1cad357732512a381baed685334d00d8c8ede0cdf5
```

最低专项测试：

```text
backend/internal/service/upstream_key_image_pricing_test.go
backend/internal/service/openai_account_scheduler_upstream_cost_test.go
backend/internal/service/openai_image_cost_routing_test.go
backend/internal/service/admin_group_image_cost_routing_test.go
backend/migrations/profile_238_migrations_test.go
frontend/src/views/admin/__tests__/AccountsView.upstreamManagement.spec.ts
```

后续补强测试：

```text
GroupsView 图片成本创建、编辑、复制、校验和提交
API Key auth cache 图片成本字段 round-trip
scheduler cache UpstreamImagePricing round-trip
account hydration 的 Key 定价快照映射
1K/2K/4K 尺寸成本排序
stale/partial/unknown 与免费成本 0 的区分
关闭 image_cost_routing_enabled 时保持原调度顺序
prefer_lowest/strict_lowest 与健康、共享并发、TTFT Guard、Priority 的组合回归
```
