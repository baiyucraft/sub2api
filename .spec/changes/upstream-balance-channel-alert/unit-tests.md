# upstream-balance-channel-alert TDD 单元测试

## UT-001 余额快照有效性与阈值判定

- Test：`backend/internal/service/upstream_operations_balance_test.go` 或现有 upstream operations 测试文件。
- Modify：余额快照映射/阈值解析及低余额判定 helper。
- 映射：ST-001、ST-002；低余额安全边界。
- Red：缺失/过期/换算失败快照被当成余额 0，或等于阈值被误报。
- Green：只接受新鲜、成功换算、正阈值；低于阈值才返回低余额，否则返回明确不可用原因。
- Refactor：集中 validity/threshold helper，保持现有快照字段和 provider 分支语义。

## UT-002 低余额事件打开、恢复和重复同步幂等

- Test：`backend/internal/repository/upstream_operations_repo_balance_test.go`。
- Modify：`evaluateUpstreamBalanceIncident` 及其事务调用。
- 映射：ST-003、ST-004。
- Red：重复低余额同步重复创建事件/通知，恢复不关闭事件，或新周期覆盖旧历史。
- Green：按 config + `balance_low` key 原子打开/更新/关闭；关闭后再次跌破进入新周期。
- Refactor：复用现有 incident 状态转移和时钟 seam，避免新增并行事件模型。

## UT-003 通知设置与失败隔离

- Test：`backend/internal/service/notification_email_service_test.go` 与 upstream operations service test。
- Modify：余额告警通知编排和模板 payload。
- 映射：ST-003、ST-005、安全边界。
- Red：通知关闭/无收件人/发送失败导致事件或快照回滚，或 payload 泄露凭据。
- Green：复用管理员额度告警收件人和去重，通知失败只记录脱敏错误；payload 仅含 allowlisted 渠道余额字段和后台链接。
- Refactor：抽取 provider-neutral balance alert payload builder，保持其它额度通知不变。

## UT-004 DTO 与看板聚合

- Test：`backend/internal/handler/admin/upstream_config_handler_test.go`、`backend/internal/service/upstream_dashboard_test.go`。
- Modify：渠道/看板 DTO mapper 与余额不足计数聚合。
- 映射：ST-001、ST-002、ST-006。
- Red：列表与看板对 null/不可用余额报错或把缺失显示为 0，汇总计数不准确。
- Green：可空字段和原因稳定透传，只有有效 `balance_low` 进入汇总；不改变流量/收益聚合。
- Refactor：统一 mapper 的空值/非有限数防御和本地化输入。

## UT-005 前端余额状态与后台入口

- Test：`frontend/src/views/admin/__tests__/UpstreamConfigsView.balance.spec.ts`、`frontend/src/views/admin/__tests__/UpstreamDashboardView.balance.spec.ts`。
- Modify：渠道列表、看板卡片/详情余额区域和 URL click helper。
- 映射：ST-006、ST-007。
- Red：低余额无醒目标记、不可用显示 0、列表与卡片口径不一致或后台链接未使用安全属性。
- Green：正常/低余额/不可用三态渲染，余额不足汇总和详情一致，支持的 provider 新标签页安全跳转。
- Refactor：复用现有状态徽标、`formatCurrency` 和 `openUpstreamDashboard`，不引入账号级余额逻辑。

## 覆盖边界

- 测试只使用本地 fixture/mock，不连接生产数据库、SMTP 或真实 provider。
- 不测试实现细节；以 API 字段、事件状态、通知次数、UI 文案/颜色和安全属性作为 observable contract。
- 不把账号 quota、credits、站内用户余额或健康状态推导为渠道余额。
- TDD Red 必须由目标行为缺失导致；依赖、编译或环境故障不算 Red。
