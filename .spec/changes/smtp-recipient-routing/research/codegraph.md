# CodeGraph 影响证据

## 入口与调用路径

- `EmailService.SendEmail` (`backend/internal/service/email_service.go`) 是验证码、密码重置、通知、余额和运维邮件的统一发送边界。
- `SendEmail` 当前调用 `GetSMTPConfig`，再进入 `SendEmailWithConfig`；后者负责 MIME、SMTP 建连、认证、MAIL、RCPT、DATA 和 QUIT。
- `TestSMTPConnectionWithConfig` 与发送复用 `connectSMTP`，管理员测试接口位于 `backend/internal/handler/admin/setting_handler_email.go`。
- 管理员 settings GET 由 `setting_handler.go` 组装 `dto.SystemSettings`；PUT 由 `setting_handler_update.go` 使用反射映射和显式 SMTP 密码保留逻辑写入 settings。
- 前端 `SettingsView.vue` 负责 SMTP 表单、连接测试和测试邮件，`api/admin/settings.ts` 提供客户端类型与请求封装。

## 依赖边界与影响半径

- 路由只属于 `EmailService`，业务邮件调用方不增加 provider 分支，因此不改变模板、队列、退订或通知去重语义。
- 主 SMTP 的 `SMTPConfig`/建连实现继续作为 profile 发送内核；QQ profile 仅复用同一 profile 结构和建连函数。
- 设置存储继续使用现有 key-value `SettingRepository`，不新增数据库表或 migration；GET 仅暴露 password configured 状态。
- 早期 SMTP 阶段失败可回退；DATA 已开始后的不确定失败必须终止，不得第二次发送。

## 测试与回滚约束

- 重点测试 `email_service` 路由匹配、profile 选择和阶段状态；SMTP server fixture 记录 envelope/from 与命令阶段，不连接真实服务。
- settings handler/parse/update 测试覆盖默认值、非法规则、部分更新、密码脱敏和审计字段。
- 回滚只需移除路由选择和新增 settings 映射，旧主 SMTP key 与邮件调用路径保持可用。

## 图证据与源码核验

CodeGraph `explore`/`impact` 结果确认上述统一入口和 10 个主要调用方；`affected` 给出的范围包含大量间接测试，因此实施时按 SMTP、settings handler 和 SettingsView 的直接合同收敛，并以源码核验为准。
