# smtp-recipient-routing 系统测试

## 测试环境

- runtime/platform：本地 Go 测试、Vitest；不连接真实 SMTP。
- fixture/data：可控双 SMTP fixture，记录 profile、From、envelope 和命令阶段；settings repository stub。
- 外部依赖：mock SMTP、mock API/路由。

## ST-01 路由成功与默认发送

- 类型：normal
- 前置：开关开启，规则含 `qq.com` 与 `*.example.com`，两套 profile 完整。
- 操作：向 `User@qq.com`、`a@sub.example.com` 和未匹配域名发送各类统一邮件。
- 断言：前两者使用 QQ From/profile，未匹配使用主 SMTP；所有业务模板调用无需改动。
- 证据：focused Go/Vitest 输出与 fixture 记录。

## ST-02 QQ 早期失败回退

- 类型：failure
- 前置：QQ fixture 在连接、认证、MAIL 或 RCPT 阶段分别失败。
- 操作：发送命中收件人邮件。
- 断言：主 SMTP 恰好发送一次；返回成功或主 SMTP真实错误；日志不含密码/正文。
- 证据：SMTP fixture 命令计数和错误断言。

## ST-03 DATA 不确定失败不重发

- 类型：boundary
- 前置：QQ fixture 在 DATA 后 Write/Close 失败。
- 操作：发送命中收件人邮件。
- 断言：不尝试主 SMTP第二次发送，返回脱敏错误。
- 证据：双 fixture 计数、阶段状态断言。

## ST-04 设置与管理员操作

- 类型：normal/failure
- 前置：settings 缺失、非法规则、部分更新和已配置密码状态交替存在。
- 操作：GET/PUT settings，分别测试主 SMTP、QQ SMTP 和自动路由测试邮件。
- 断言：GET 不返回密码；空密码保留旧值；非法规则不启用路由；测试 profile 选择明确。
- 证据：Go handler 测试和 Vitest API mock。

## ST-05 全部统一邮件调用路径

- 类型：regression
- 前置：验证码、密码重置、通知、余额/运维和订阅提醒调用共享 EmailService。
- 操作：触发各路径的 mock 发送。
- 断言：均按收件域名使用同一选择逻辑，通知去重、退订和队列语义不变。
- 证据：现有路径测试加路由 fixture。

## 成功标准映射

| 成功标准 | ST | 证据 |
| --- | --- | --- |
| 路由匹配、profile 与 From 正确 | ST-01 | Go fixture/Vitest |
| 早期失败回退且 DATA 不重复 | ST-02/ST-03 | SMTP 阶段计数 |
| settings 脱敏、默认和部分更新 | ST-04 | handler/API 测试 |
| 所有统一邮件获得路由 | ST-05 | 调用路径回归 |
