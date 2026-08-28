# smtp-recipient-routing

## 问题

系统当前只有一套 SMTP 配置。QQ 收件箱对域名邮箱发件地址的投递拦截较多，导致验证码、密码重置和通知邮件可能无法到达 QQ 用户；同时管理员没有按收件域名选择不同发件账号的能力。

## 目标

- 保留现有主 SMTP 作为默认发件账号，为 QQ SMTP 增加独立配置。
- 增加可关闭的收件域名路由，支持精确域名和通配符域名。
- 所有统一经过 `EmailService.SendEmail` 的系统邮件按收件域名自动选择 SMTP 和对应 From 地址。
- QQ SMTP 在配置、连接、TLS、认证、MAIL 或 RCPT 阶段失败时回退主 SMTP；DATA 阶段已开始或投递结果不确定时不重复回退。
- 管理员可以分别测试两套 SMTP，并能按测试收件人预览自动路由结果。

## 非目标

- 不修改现有主 SMTP 的字段名称、默认发件行为或邮件模板、队列、去重和退订语义。
- 不新增数据库表或迁移；配置继续使用现有 settings key-value 存储。
- 不在本 change 内重构现有 SMTP 密码加密机制；新增 QQ 授权码沿用主 SMTP 当前的持久化和脱敏语义。
- 不发送真实生产邮件、不直接修改生产 SMTP 配置、不改变 QQ 或其他邮箱服务商的收信策略。

## 成功标准

- 路由开关关闭、列表不匹配或 QQ 配置不完整时，邮件使用现有主 SMTP。
- 路由域名精确匹配和 `*.domain` 通配符匹配稳定、大小写无关，非法规则不会启用路由。
- 匹配 QQ 路由且 QQ SMTP 成功时，使用 QQ SMTP 和 QQ From；其他收件人继续使用主 SMTP 和域名 From。
- QQ SMTP 在允许的早期失败阶段回退主 SMTP；DATA 阶段的不确定失败不产生第二次发送。
- settings GET 不返回任一 SMTP 密码，只返回 configured 状态；空密码部分更新保留已存密码。
- 验证码、密码重置、余额/运维告警、订阅提醒和通知邮件均复用同一自动路由行为。
- 管理端可配置、保存、清空域名列表，分别测试主 SMTP/QQ SMTP，并显示路由说明和错误状态。
- 相关 Go/Vitest、类型检查、Lint 和 `git diff --check` 通过；本阶段生产保持未修改。

## 影响范围

- `backend/internal/service/email_service.go`：收件域名路由、SMTP profile 选择和失败阶段回退。
- `backend/internal/service/setting_*`、管理员 settings DTO/handler：主/QQ SMTP 配置、路由开关和域名列表。
- `frontend/src/views/admin/SettingsView.vue` 与 settings API/i18n：配置、测试和路由预览界面。
- 邮件服务、settings handler 和前端设置测试；`.wiki/` 与 fork extension audit：长期合同。

## 交付形态

single-change

邮件路由在统一邮件服务边界内可独立 review、测试和回滚，不改变数据库结构和业务邮件调用方。

## 风险

- SMTP DATA 阶段错误可能代表服务器已接收邮件，错误回退会造成重复投递；必须保留发送阶段状态并禁止不确定阶段重试。
- QQ SMTP 的 From 与 QQ 账号不一致可能被拒收；每个 profile 使用自己的 From，并在配置/测试中明确提示。
- 通配符规则过宽可能误路由；只接受规范域名和单一前缀 `*.`，不允许任意字符串匹配。
- QQ SMTP 配置缺失时静默回退可能掩盖配置问题；日志需记录脱敏的 route profile、回退阶段和结果，管理端测试需明确显示。

## 参考资料

- 来源：`backend/internal/service/email_service.go` 及其 SMTP 测试
  - 目标落点：复用 SMTP 建连、TLS、消息封装和单收件人发送边界
  - 采用方式：rewrite
- 来源：`backend/internal/service/setting_parse.go`、`setting_update.go`、管理员 settings handler 和 `SettingsView.vue`
  - 目标落点：沿用 key-value 配置、部分更新、密码 configured 状态和审计
  - 采用方式：direct migration
- 来源：现有验证码、重置、通知、余额和运维邮件调用路径
  - 目标落点：统一由 `EmailService.SendEmail` 获得自动路由
  - 采用方式：inspiration only
