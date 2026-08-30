# upstream-balance-channel-alert 任务计划

implementation-mode: tdd

## 任务总览

按余额有效性、事件/通知、API 聚合、前端展示、文档和局部门禁六个能力块实施。每个产品代码修改前先取得对应 UT 的目标行为 Red 证据；不增加 migration，不改账号调度和 V2。

## 实现模式

tdd

先写失败测试并确认是目标行为缺失，再写最小实现；Green 后整理共享 helper 和回归覆盖。

## 1. 余额快照有效性与阈值判定

- [ ] 1.1 Red：UT-001 覆盖正阈值、等于阈值、零阈值、缺失/过期/换算失败快照。
- [ ] 1.2 Green：复用现有余额快照与设置解析，产出低余额/正常/不可用结构化结果。
- [ ] 1.3 Refactor：保持 provider billing probe、账号数据和现有余额历史字段不变。

### CheckList

- [ ] Red 失败原因对应误报或边界缺失
- [ ] Green 与重构后 UT-001 通过
- [ ] 不新增数据库字段或迁移
- [ ] 相关服务测试与注释检查通过

## 2. 事件生命周期与管理员邮件通知

- [ ] 2.1 Red：UT-002/UT-003 验证重复同步重复告警、恢复不关闭和通知失败回滚等缺陷。
- [ ] 2.2 Green：接入 `evaluateUpstreamBalanceIncident`，按渠道周期幂等打开/关闭事件；渠道余额通知使用独立默认开启开关，有效管理员通知邮箱优先，空列表回退启用中管理员注册邮箱。
- [ ] 2.3 Refactor：模板和 payload 只保留 allowlisted 字段，复用 provider 后台 URL helper。

### CheckList

- [ ] 事件首次打开、重复同步、恢复和再跌破测试通过
- [ ] 独立通知开关关闭/无有效管理员邮箱/发送失败不影响快照和事件
- [ ] 邮件、日志和事件不含敏感信息
- [ ] 相关 repository/service/notification tests 与注释检查通过

## 3. 渠道与看板 API

- [ ] 3.1 Red：UT-004 验证 null 扫描、不可用余额伪造 0 和余额不足计数错误。
- [ ] 3.2 Green：扩展渠道/看板 DTO 和 mapper，透传余额状态、阈值、更新时间、不可用原因及汇总数量。
- [ ] 3.3 Refactor：确保真实流量、探针、收益、账号 quota 和 V2 聚合口径不变。

### CheckList

- [ ] DTO/API 低余额、正常和不可用三态覆盖
- [ ] 看板汇总仅统计有效低余额渠道
- [ ] 左连接/NULL 数据安全
- [ ] handler/service focused tests 通过

## 4. 管理端渠道列表与看板展示

- [ ] 4.1 Red：UT-005 验证缺少低余额徽标、不可用显示 0、列表/卡片口径分叉和后台链接不安全。
- [ ] 4.2 Green：增加渠道余额提醒、看板摘要/卡片/详情展示和安全后台入口。
- [ ] 4.3 Refactor：沿用 Sub2API 现有徽标、主题、响应式和 accessibility 组件，不引入账号级余额列。

### CheckList

- [ ] 正常/低余额/不可用文案与颜色通过
- [ ] 余额不足渠道数和详情一致
- [ ] `noopener,noreferrer`、无效 URL 和不支持 provider 覆盖
- [ ] Vitest、typecheck、lint 通过

## 5. 长期合同与局部门禁

- [ ] 5.1 更新 `.wiki/` 模块页与 fork extension catalog，登记余额 SSOT、阈值、不可用和通知周期；不登记 `.wiki/.spec`。
- [ ] 5.2 `spec-wiki-lite validate upstream-balance-channel-alert --strict --json` 通过。
- [ ] 5.3 运行 focused Go/Vitest、`pnpm typecheck`、`pnpm lint:check` 和 `git diff --check`。

### CheckList

- [ ] 文档、catalog 和实现边界一致
- [ ] 所有局部验证通过并记录证据
- [ ] 未执行全量门禁、audit、VM Gate、生产部署或真实 provider 请求
- [ ] staged 路径无 `.wiki/.wiki`、`.spec/.spec` 或生成副本

## 用例到任务映射

| 系统测试用例 | 大 task | 小 task / 验证 |
| --- | --- | --- |
| ST-001、ST-002 | 1、3、4 | 1.1-1.3、3.1-3.3、4.1-4.3 / UT-001、UT-004、UT-005 |
| ST-003、ST-004 | 2 | 2.1-2.3 / UT-002、UT-003 |
| ST-005 | 2、3 | 2.1-2.3、3.1-3.3 / UT-003、UT-004 |
| ST-006 | 3、4 | 3.1-3.3、4.1-4.3 / UT-004、UT-005 |
| ST-007 | 2、4 | 2.3、4.1-4.3 / UT-003、UT-005 |

## 执行顺序

- 1 → 2 → 3 → 4 → 5。
- 每个 TDD task 的 Red 证据必须先于对应生产代码修改。
- 本 change 仅局部验证；全量门禁、fork audit、VM Gate 和生产发布另行授权。

## 暂缓事项

- 账号级余额字段、账号临时不可调度联动、用户端通知、生产数据回写和真实 provider 充值动作不在本 change 内。

> 两种模式都必须完成测试、review、verification 和 archive 门禁；Lite 不使用 readiness 字段或额外确认步骤。

## 2026-08-30 通知开关纠偏证据

- 已将渠道余额通知从 `account_quota_notify_enabled` 解耦为缺失时默认开启的 `upstream_balance_notify_enabled`。
- 已实现收件人优先级：有效管理员通知邮箱优先；空列表回退启用中管理员的合法注册邮箱。
- 已通过相关 Go service/handler/server 测试、SettingsView Vitest、`pnpm typecheck`、`pnpm lint:check`、`git diff --check` 与 strict validate。
- 本次未执行 VM Gate、生产部署、真实 SMTP 投递或生产配置写入。
