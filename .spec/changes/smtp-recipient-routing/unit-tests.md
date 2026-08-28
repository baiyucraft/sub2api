# smtp-recipient-routing TDD 单元测试

## UT-01 收件域名匹配

- Test：`backend/internal/service/email_service_routing_test.go`
- Modify：`backend/internal/service/email_service.go`
- 映射：ST-01 / 路由域名成功标准
- Red：当前没有规则解析与 profile 选择，精确、通配符和大小写用例失败。
- Green：实现规范化、严格匹配和 QQ profile 可用性判断。
- Refactor：保持纯函数 seam，避免发送流程重复解析。

## UT-02 SMTP 阶段回退

- Test：`backend/internal/service/email_service_routing_test.go`
- Modify：`backend/internal/service/email_service.go`
- 映射：ST-02/ST-03 / 防重复发送边界
- Red：当前 SendEmail 只调用单一 SMTP，无法验证 fallback 和 DATA 边界。
- Green：引入可注入发送阶段 seam，早期错误回退，DATA 后不回退。
- Refactor：统一脱敏聚合错误与结构化阶段日志。

## UT-03 settings 默认、校验与脱敏

- Test：`backend/internal/service/setting_parse_test.go`、`backend/internal/handler/admin/setting_handler_partial_payload_test.go`
- Modify：`setting_parse.go`、`setting_update.go`、settings DTO/handler
- 映射：ST-04 / settings 安全边界
- 输入：缺失值、非法域名、空密码部分更新。
- 断言：安全默认、规范化规则、密码 configured-only 和旧值保留。

## UT-04 前端配置与测试 profile

- Test：`frontend/src/views/admin/__tests__/SettingsView.smtpRouting.spec.ts`
- Modify：`frontend/src/views/admin/SettingsView.vue`、`frontend/src/api/admin/settings.ts`
- 映射：ST-04 / 管理端成功标准
- 输入：开关、规则列表、QQ profile、自动/主/QQ 测试模式及失败响应。
- 断言：正确加载/保存、密码已配置提示、路由预览和错误展示。

## 覆盖边界

- 不测试 SMTP 库内部协议；通过 fixture 观察 profile、阶段和副作用。
- 不发送真实邮件，不读取或输出生产凭据。
