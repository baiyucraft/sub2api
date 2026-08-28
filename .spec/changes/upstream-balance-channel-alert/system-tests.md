# upstream-balance-channel-alert 系统测试

## 测试环境

- runtime/platform：本地 Go、Node/pnpm/Vitest、PostgreSQL repository fixture；通知服务使用 mock SMTP/NotificationEmailService。
- fixture/data：多个 upstream config、Provider、余额快照、阈值、更新时间、币种换算证据和 `balance_low` 事件；前端使用脱敏 API mock。
- 外部依赖：不连接生产数据库、Redis、SMTP 或真实 provider；后台链接只验证 URL 生成，不访问第三方后台。

## ST-001 有效余额与低余额边界

- 类型：normal / boundary
- 前置：渠道 A 有新鲜可换算快照，阈值分别为 100、0 和缺失。
- 操作：提交余额 99.99、100、0，并读取渠道 API/看板。
- 断言：99.99 且正阈值为 `balance_low=true`；等于阈值不低余额；阈值为 0/缺失不告警；列表、卡片和详情口径一致。
- 证据：Go service/handler 测试与 JSON 字段断言。

## ST-002 余额不可用不误报

- 类型：failure / boundary
- 前置：余额字段缺失、解析失败、币种换算率缺失、快照超过 freshness 窗口。
- 操作：运行同步并读取列表、看板和详情。
- 断言：返回 `balance_low=false`/空值及结构化不可用原因；页面显示“余额不可用”，不显示伪造的 `0`，不创建 `balance_low` 事件。
- 证据：Go fixture、handler 响应和 Vitest 文本/颜色断言。

## ST-003 低余额事件与通知去重

- 类型：normal / regression
- 前置：渠道 A 无打开 `balance_low` 事件，管理员额度通知开启且有已验证收件人。
- 操作：连续执行两次低余额同步，再执行一次余额恢复。
- 断言：首次创建/打开一个事件并发送一次邮件；第二次只更新快照/指标，不重复发信；恢复关闭事件且不重复告警。
- 证据：repository 事件行、NotificationEmailService mock 调用次数和日志脱敏断言。

## ST-004 恢复后的新告警周期

- 类型：boundary
- 前置：渠道 A 已完成一次低余额 -> 恢复周期。
- 操作：再次提交低于阈值的有效余额并同步。
- 断言：创建新的可观测低余额周期并再次发送一次通知；历史事件保留，不覆盖旧周期。
- 证据：事件状态/时间线和通知 mock 计数。

## ST-005 通知关闭或失败隔离

- 类型：failure
- 前置：通知开关关闭、收件人为空、邮箱未验证或 SMTP mock 返回失败。
- 操作：执行有效低余额同步。
- 断言：余额快照与 `balance_low` 事件仍正确落库；API/UI 仍显示低余额；发送失败不阻断同步且错误脱敏。
- 证据：事务行断言、mock 错误和响应内容扫描。

## ST-006 看板汇总、排序与详情

- 类型：normal
- 前置：多个渠道中有正常、低余额、不可用、禁用状态，至少一个无真实流量配置。
- 操作：请求看板及详情，切换时间窗口并刷新。
- 断言：顶部余额不足渠道数准确；低余额卡片显示余额/阈值/更新时间/关注原因；不可用卡片显示原因；详情不从列表快照伪装；余额字段不混入真实流量收入、成本或账号 quota。
- 证据：聚合 API JSON、详情 API JSON 和前端组件测试。

## ST-007 后台充值入口与敏感信息边界

- 类型：normal / security
- 前置：普通 provider、特殊 URL provider、无效站点 URL 和缺少后台支持的渠道。
- 操作：点击列表/看板余额告警入口，检查生成的链接及 DOM 属性。
- 断言：支持的 provider 在新标签页打开既有后台路径并含 `noopener,noreferrer`；不支持或无效 URL 不可点击；URL、邮件和日志不含 Key、Token、Cookie 或原始响应正文。
- 证据：Vitest `window.open` mock、通知 payload allowlist 和敏感模式扫描。

## 成功标准映射

| 成功标准 | ST | 证据 |
| --- | --- | --- |
| 有效低余额/正常边界正确识别 | ST-001 | Go service/handler tests |
| 不可用余额不误报且原因明确 | ST-002 | Go + Vitest |
| 事件与邮件按周期幂等 | ST-003、ST-004 | repository + notification mock |
| 通知故障不影响同步与展示 | ST-005 | integration fixture |
| 列表/看板/详情展示和汇总一致 | ST-006 | API contract + Vitest |
| 后台入口可执行且无敏感信息 | ST-007 | URL/accessibility/security assertions |
| 不改账号调度/V2/生产 | ST-001..007 | regression scope and final note |
