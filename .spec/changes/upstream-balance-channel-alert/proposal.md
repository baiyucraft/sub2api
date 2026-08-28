# upstream-balance-channel-alert

## 问题

上游渠道已有余额快照和 `balance_low` 事件，但管理端不能在渠道列表、配置看板和通知链路中一致地识别余额不足。管理员可能只能在余额历史中手动发现低余额，无法及时定位需要充值的渠道；同时余额查询失败、没有快照或币种转换失败不能被误报为“余额为 0”。

## 目标

- 复用现有渠道余额快照和 `upstream_balance_low_threshold_cny` 阈值，在渠道列表与配置看板展示一致的低余额、正常和余额不可用状态。
- 在余额同步完成后沿用 `balance_low` 事件生命周期，首次进入低余额周期通知一次，恢复后关闭事件，再次跌破时允许新的通知周期。
- 复用管理员额度告警邮件设置和 provider 后台 URL 规则，为管理员提供可执行的充值入口。
- 保持渠道级余额口径，不将账号 quota/credits 或站内用户余额混入渠道余额。

## 非目标

- 不新增数据库字段、迁移、账号级余额字段或账号调度规则。
- 不把余额查询失败、缺失快照、阈值为零或币种不可换算判定为低余额。
- 不改变真实业务流量、主动探针、利润控制、TTFT Guard、V2 监控或账号临时不可调度语义。
- 不直接操作生产数据，不部署、不执行 VM Gate 或全量门禁。

## 成功标准

- 渠道最新可用人民币余额低于正阈值时，渠道 API 和看板 DTO 返回 `balance_low=true`、当前余额、阈值和更新时间。
- 余额缺失、转换失败、快照过期或阈值无效时返回可空余额和结构化不可用原因，前端不显示伪造的 `0`。
- 首次跌破阈值只创建/打开一个 `balance_low` 事件并触发一次管理员告警；重复同步不重复发送，恢复后事件关闭，再次跌破可重新通知。
- 渠道列表、看板卡片和详情抽屉均显示相同的余额口径；余额不足渠道数可聚合统计并可定位到渠道。
- 告警中的后台入口按现有 provider URL 规则生成，使用新标签页和 `noopener,noreferrer`，不暴露凭据或伪造账号级充值链接。
- 相关 Go/Vitest、类型检查、Lint 和差异检查通过；本 change 完成前生产保持未修改。

## 影响范围

- `backend/internal/service/upstream_operations.go`：余额快照、阈值解析和告警通知编排。
- `backend/internal/repository/upstream_operations_repo.go`：`balance_low` 事件幂等生命周期和快照读取。
- 上游渠道/看板 DTO、管理 handler、`frontend/src/views/admin/UpstreamConfigsView.vue` 与 `UpstreamDashboardView.vue`：余额状态和不可用原因展示。
- 管理员通知模板、provider 后台 URL helper 和相关 Go/Vitest 测试。
- `.wiki/` 与 fork extension audit：长期沉淀余额口径、通知去重和敏感信息边界。

## 交付形态

single-change

余额告警可以独立 review、测试和回滚；它只复用现有快照、事件、设置和 URL 能力，不改变数据库结构或其它监控/调度 change。

## 风险

- 将“余额查询失败”误分类为低余额会导致错误告警和充值动作。
- 同步任务重复执行可能产生邮件风暴，必须以打开事件周期做幂等去重。
- 多币种或缺少换算率时输出金额会误导管理员，必须显式显示估算/余额不可用。
- provider 后台 URL 拼接错误可能把管理员带到错误页面；应复用已有 helper 并测试普通 provider 与 LCodex 等特殊规则。
- 余额通知不能将 Token、Cookie、Key 或原始上游响应写入邮件、事件或日志。

## 参考资料

- 来源：`upstream_operations.go`、`upstream_operations_repo.go` 的余额快照与 `balance_low` 事件实现
  - 目标落点：阈值判定、事件生命周期和通知去重
  - 采用方式：direct migration
- 来源：现有 `UpstreamConfigsView.vue`、`UpstreamDashboardView.vue` 和 provider 后台 URL helper
  - 目标落点：渠道列表、看板卡片/抽屉和充值入口
  - 采用方式：rewrite
- 来源：现有管理员额度通知设置与 `NotificationEmailService`
  - 目标落点：收件人、退订、失败处理和通知模板
  - 采用方式：direct migration
