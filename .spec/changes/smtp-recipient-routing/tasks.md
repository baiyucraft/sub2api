# smtp-recipient-routing 任务计划

implementation-mode: tdd

## 任务总览

按邮件路由核心、settings 合同、管理员前端和长期文档分块，先 Red 再 Green/Refactor；每项以统一邮件入口为 SSOT。

## 实现模式

tdd

## 1. SMTP 收件域名路由与安全回退

### tdd 模式

- [x] 1.1 Red: UT-01/UT-02 编写匹配、profile 选择、早期 fallback 和 DATA 不重试失败测试；初次运行因缺少路由符号失败
- [x] 1.2 Green: 修改 `email_service.go`，实现规则解析、QQ profile、阶段跟踪和脱敏日志
- [x] 1.3 Refactor: 保持 `SendEmailWithConfig` 主 SMTP 兼容并收敛测试 seam

### CheckList

- [x] 失败测试已确认
- [x] 最小实现后测试通过（`go test ./internal/service -run 'TestNormalizeSMTPRecipientDomains|TestEmailServiceSelectsQQProfileForMatchedRecipient|TestEmailServiceFallsBackBeforeDataButNotAfterData' -count=1`）
- [x] 重构后测试仍通过
- [x] SMTP 不连接真实外部服务

## 2. Settings 与管理员 API

### tdd 模式

- [x] 2.1 Red: UT-03 覆盖默认值、非法规则、部分更新和密码脱敏
- [x] 2.2 Green: 修改常量、SystemSettings、parse/update、DTO、GET/PUT 与测试接口
- [x] 2.3 Refactor: 保持旧主 SMTP 字段和审计行为兼容

### CheckList

- [x] settings Red/Green/Refactor 证据已记录
- [x] GET 不返回密码
- [x] 空密码不覆盖旧值

## 3. 管理端 SMTP 配置体验

### tdd 模式

- [x] 3.1 Red: UT-04 覆盖两套 profile、路由说明、测试模式和错误态
- [x] 3.2 Green: 修改 `SettingsView.vue`、settings API 类型和 i18n
- [x] 3.3 Refactor: 保持现有设置页主题、响应式和无障碍行为

### CheckList

- [x] 前端测试通过（`SettingsView.spec.ts` 42 tests）
- [x] typecheck/lint 通过
- [x] 密码和授权码不回显

## 4. Wiki 与 fork 扩展合同

### tdd 模式

- [x] 4.1 更新 `.wiki/` 邮件配置与路由长期说明（实现后文档校验）
- [x] 4.2 更新 fork extension audit 登记（不登记 `.wiki`、`.spec`、技能路径）
- [x] 4.3 strict validate、review 前检查和路径边界检查

### CheckList

- [x] 文档与实现口径一致
- [x] 无嵌套生成副本

## 用例到任务映射

| 系统测试用例 | 大 task | 小 task / 验证 |
| --- | --- | --- |
| ST-01/ST-02/ST-03 | 1. SMTP 路由 | 1.1 / 1.2 / UT-01/UT-02 |
| ST-04 | 2. Settings 与 API；3. 前端 | 2.1-2.3 / 3.1-3.3 / UT-03/UT-04 |
| ST-05 | 1. SMTP 路由 | 1.2 / 现有调用路径回归 |

## 执行顺序

1. Red 测试与证据；2. SMTP Green/Refactor；3. settings Green/Refactor；4. 前端 Green/Refactor；5. Wiki/audit；6. 局部门禁与 review。

## 暂缓事项

- 真实 SMTP、生产配置、真实邮件、全量门禁、VM Gate 和生产部署均暂缓。

> 两种模式都必须完成测试、review、verification 和 archive 门禁；本 change 只执行授权的局部范围。
