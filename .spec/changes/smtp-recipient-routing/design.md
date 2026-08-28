# smtp-recipient-routing 设计方案

## 方案概述

在 `EmailService` 内引入主 SMTP与可选 QQ SMTP 两个 profile。`SendEmail` 先读取主配置和路由设置，规范化收件域名后选择 profile；QQ profile 仅在开关、规则匹配和配置完整时尝试。QQ 在连接、TLS、认证、MAIL 或 RCPT 失败时回退主 profile；进入 DATA 后任何写入或结束结果不确定均不回退。

## 接口与稳定合同

- 新 settings keys：`smtp_recipient_routing_enabled`、`smtp_recipient_routing_domains`、`smtp_qq_host`、`smtp_qq_port`、`smtp_qq_username`、`smtp_qq_password`、`smtp_qq_from`、`smtp_qq_from_name`、`smtp_qq_use_tls`。
- 管理员 settings GET 增加路由开关、域名数组、QQ 非密码字段和 `smtp_qq_password_configured`；任何 GET/审计响应不返回密码。
- 管理员 settings PUT 支持上述字段；空密码表示保留已存值。已有主 SMTP JSON 名称和测试接口保持兼容；新增 profile 参数用于 QQ 连接测试和自动路由测试邮件。

## Ownership 与数据/文件流

```text
SettingRepository -> SettingService.parse/update -> admin DTO/SettingsView
SendEmail(to) -> normalize recipient domain -> select profile -> SMTP session -> result/fallback
```

## 正常流程

1. 读取主 SMTP；主配置缺失时沿用现有 `ErrEmailNotConfigured`。
2. 当路由开关打开且收件域名命中精确或 `*.` 规则，并且 QQ profile 的 host、port、username、password、from 合法时，先发送 QQ profile。
3. 未命中、规则无效或 QQ 不完整时直接发送主 profile。
4. QQ 早期失败时关闭会话并只发送一次主 profile；主 profile 成功则返回成功，两个 profile 均失败返回脱敏聚合错误。
5. 日志只记录 profile、阶段、域名哈希和是否回退，不记录地址正文、密码或正文。

## 失败、边界与回滚

- 域名规则仅接受小写规范 DNS 标签、精确域名或单一前缀 `*.`；非法规则被忽略并使对应路由不匹配，避免宽匹配。
- 收件人无合法域名时不启用 QQ 路由，交由主 profile 的既有地址校验返回错误。
- QQ 连接/TLS/Auth/MAIL/RCPT 失败可回退；DATA 调用成功后 `Write` 或 `Close` 失败视为投递不确定，不回退。
- settings 读取部分失败时使用安全默认值（路由关闭、QQ 不可用），不影响主 SMTP 发送；PUT 采用现有单次 `SetMultiple`/审计流程。
- 回滚应用时保留新增 settings key 和 nullable configured 语义；删除路由逻辑后主 SMTP 行为不变。

## 验证设计

- Go fixture SMTP server 断言 profile、From、envelope 和失败阶段；单元测试覆盖精确/通配符/大小写/非法规则、缺失配置、fallback 与 DATA 不重试。
- settings 测试覆盖 GET 脱敏、默认值、部分更新保留密码、审计；handler 测试覆盖两 profile 测试请求。
- Vitest 真实挂载 SettingsView，覆盖路由开关、域名列表、QQ 配置、测试 profile/自动预览、错误态和空值。

## Wiki 与长期合同落点

- `.wiki/03-模块指南/04-数据与任务.md` 或邮件配置相关长期页：记录 SMTP profile、收件域名规范化和 DATA 不回退边界。
- `sub2api-fork-extension-audit` 仅登记产品代码新增的 settings/email routing 扩展，不登记 `.wiki`、`.spec` 或技能路径。

## 参考边界

- `email_service.go`：rewrite，保留现有 SMTP 建连和 MIME 内核。
- settings parse/update/handler 与 SettingsView：direct migration，沿用现有 key-value、脱敏和测试接口风格。
- 业务邮件调用方：inspiration only，通过统一入口自动获得路由。

## 回滚

先关闭路由开关即可恢复主 SMTP；代码回滚仅移除 profile 选择、QQ settings 映射和前端区块，保留主 SMTP key 与历史审计记录。

## CodeGraph-derived design constraints

- entry points and call paths: `SendEmail -> GetSMTPConfig -> SendEmailWithConfig`; settings GET/PUT 和 test-smtp handler。
- ownership and dependency boundaries: 邮件路由归 `EmailService`；配置归 `SettingService`/管理员 settings；业务调用方不分叉。
- impact radius: 10 个邮件调用方、settings DTO/handler、SettingsView 与直接测试。
- affected tests: `email_service_smtp_test.go`、`email_message_test.go`、settings handler/parse 测试及 `SettingsView.spec.ts`。
- rollback boundary: 只影响新增 QQ profile 与选择逻辑，主 SMTP 发送协议不改。
- graph evidence vs source verification: CodeGraph 识别调用路径；具体字段、审计和前端请求以当前源码核验。
- unresolved items: provider 专属后台 URL 不适用于 QQ 邮件；测试邮件 profile 由新增请求字段显式指定。
