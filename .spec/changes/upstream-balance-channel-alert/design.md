# upstream-balance-channel-alert 设计方案

## 方案概述

余额的 SSOT 仍是上游配置的余额快照和现有 `upstream_balance_low_threshold_cny` 设置。读取层以最近 24 小时作为快照新鲜窗口。同步流程在写入快照后调用现有 `evaluateUpstreamBalanceIncident`，由服务层统一计算可用性、低余额状态和事件转移；通知只在事件从关闭/不存在转为打开时排队一次。恢复时关闭事件，不发送恢复邮件，下一次重新跌破形成新的通知周期。

余额判定只接受“快照新鲜、余额解析成功、人民币换算完整且阈值为正”的结果：`balance_cny < threshold_cny` 为低余额；等于阈值不告警。其它情况输出 `balance_unavailable_reason`，不改变渠道健康状态。该状态与账号健康、V1/V2 监控和调度解耦。

## 接口与稳定合同

- 渠道和看板 DTO 新增可空字段：`balance_low`、`balance_threshold_cny`、`balance_unavailable_reason`、`balance_updated_at`；看板聚合新增 `balance_low_upstream_count`。
- `balance_low` 仅在余额判定有效且低于正阈值时为 true；无效输入为 false/NULL（按现有 DTO 约定），并必须带不可用原因。
- 余额事件 key 继续为 `balance_low`，事件状态转移保持 open/resolve 语义，事件唯一范围为上游配置。
- 通知复用管理员额度告警设置：未启用、无收件人、邮箱未验证或发送失败不阻塞快照/事件落库。
- 后台链接使用现有 `buildUpstreamDashboardURL`/`openUpstreamDashboard` 规则；仅输出脱敏 URL，不携带凭据。

## Ownership 与数据/文件流

```text
provider billing probe
  -> upstream balance snapshot (CNY + freshness + conversion evidence)
  -> evaluateUpstreamBalanceIncident(config, snapshot, threshold)
  -> balance_low incident open/resolve + deduplicated notification
  -> channel DTO / dashboard aggregate DTO
  -> admin channel list, dashboard card/detail and alert link
```

## 正常流程

1. provider 余额同步成功并得到可换算的人民币余额与更新时间。
2. 服务读取最新阈值；阈值和快照通过 freshness/validity 检查后计算低余额。
3. 低于阈值且没有打开事件时，在同一事务边界内写入/打开 `balance_low` 事件，并向现有通知服务提交一次告警。
4. 同步结果映射到渠道列表、看板摘要、卡片和详情；余额正常时显示余额和更新时间，低余额时显示醒目提醒和后台入口。
5. 余额恢复到阈值以上时关闭事件；后续再次跌破阈值生成新的通知周期。

## 失败、边界与回滚

- 无效输入：阈值缺失、非正数、余额解析失败、币种/换算率缺失或快照过期均返回余额不可用，不得变成低余额或零余额。
- 重复/并发：事件按上游配置和 `balance_low` key 幂等；并发同步不能重复打开事件或重复发送邮件。
- 部分失败：通知服务失败只记录可脱敏错误并保留已落库快照/事件；不能回滚余额快照，也不能阻塞其它渠道同步。
- 数据边界：不使用账号 `quota`、`credits`、站内用户 `balance` 推导渠道余额；多币种无法统一时不输出金额/利润推断。
- 敏感数据：事件、邮件、日志只允许渠道名称、Provider、余额、阈值、更新时间、不可用原因和后台链接；禁止 Token、Cookie、Key、认证头和原始响应。
- 回滚：仅回退服务/handler/frontend/通知代码；不删除快照、事件或历史记录，不需要 schema 回滚。

## 验证设计

- Go service/repository fixture 覆盖阈值边界、快照 freshness、换算失败、事件开关/恢复/重复同步和通知失败隔离。
- handler/DTO 测试覆盖低余额、正常、不可用和看板聚合计数；断言敏感字段不在响应中。
- 前端组件测试覆盖列表、卡片、详情的一致文案、颜色、不可用原因、缺失值和后台跳转属性。
- 使用通知 mock 验证收件人设置、重复同步只一次、恢复后重新告警。
- 只执行 focused Go/Vitest、typecheck、lint 和 `git diff --check`；无生产或 VM 证据伪装。

## Wiki 与长期合同落点

- `.wiki/03-模块指南/03-网关与上游.md`：渠道余额 SSOT、阈值/不可用判定、事件周期和通知脱敏边界。
- `.wiki/03-模块指南/05-分叉扩展与兼容性.md`：新增上游渠道余额告警扩展入口及不可变 invariants。
- `.agents/skills/sub2api-fork-extension-audit/references/extensions.yaml`：新增唯一扩展登记；不登记 `.wiki/`、`.spec/` 或 skill 资产路径。

## 参考边界

- 来源：既有 upstream operations balance snapshot/incident 实现
  - 目标落点：复用余额和事件 SSOT
  - 采用方式：direct migration
- 来源：管理员额度通知设置与 provider dashboard URL helper
  - 目标落点：通知与充值入口
  - 采用方式：direct migration

## 回滚

回退本 change 的代码和文档即可恢复原有余额展示/事件调用方式；保留既有余额快照、事件记录和已发送通知，不执行数据删除或迁移回滚。若通知模板发布失败，先关闭新模板调用，余额同步和事件落库仍保持可用。

## CodeGraph-derived design constraints

- entry points and call paths: provider billing probe -> `UpstreamBalanceSnapshot` -> `evaluateUpstreamBalanceIncident` -> upstream config/dashboard mapper -> admin channel/dashboard views.
- ownership and dependency boundaries: service owns validity, threshold and event/notification transition; repository owns snapshot/incident persistence; handler owns DTO redaction; frontend owns presentation and navigation only.
- impact radius: upstream channel balance sync, admin channel page, upstream dashboard and notification template; no account scheduler, user balance or V2 aggregation.
- affected tests: upstream operations service/repository tests, admin upstream config/dashboard handler tests, notification service tests and dashboard/config Vitest.
- rollback boundary: no schema or data rewrite; code/template rollback only.
- graph evidence vs source verification: existing balance snapshot and incident symbols are the source contract; CodeGraph impact should be rechecked by implementer if symbol names or DTO boundaries differ.
- unresolved items: exact freshness duration and notification setting field names must reuse current implementations; if no generic notification template seam exists, add the narrowest provider-neutral template without exposing raw upstream data.
