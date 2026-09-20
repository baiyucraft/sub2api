# 插件安全边界

## 能提供的隔离

- 私有实现以独立二进制交付，宿主公开源码不包含其业务逻辑。
- 进程协议避免 Go 动态链接和共享内存 ABI。
- 包签名和文件哈希防止未授权替换。
- UI 使用短时 URL、独立 Bridge Token 和 sandbox iframe。
- 插件故障时 OAuth 插件路径失败关闭，不静默切回另一种网络行为。

## 不能提供的保证

- 闭源二进制仍可能被逆向分析。
- 子进程不是操作系统沙箱。
- 插件拥有 Sub2API 服务用户可访问的文件、环境变量和网络权限。
- 包签名证明发布者身份，不证明实现无漏洞或符合 Provider 条款。

## 部署要求

- 官方 OpenAI Transport 和当前 fork 的 `baiyu.codex-state` 使用分别绑定固定插件 ID 的宿主内置公钥；只向 `plugins.trusted_publishers` 添加经过审核的其他第三方公钥。
- 使用专用低权限系统用户运行 Sub2API。
- 限制该用户的文件权限、出站网络和环境变量。
- 不向插件环境注入无关密钥。
- 对插件升级保留旧包和回滚流程。
- 记录安装、启用、停用、配置和删除操作，但不记录配置明文。

## 敏感数据

插件进程可以处理真实 Authorization、出站身份和代理认证 URL，必须避免将请求头、请求体、代理凭据和上游敏感响应写入日志、错误、Health 或动作结果。UI 不接收账号 token、代理密码、lease owner 或持久状态原文。

`ListResources` 使用字段白名单，读取不启动插件。`ResolveProxy` 和 `ResolveOutboundIdentity` 只存在于进程间 RPC；资源目录中的 `business_egress_configured` 不是网络探测或直连授权。

## 状态与并发

数据库持久 state value 使用宿主密钥加密，按连接绑定的 pluginKey/namespace/key 隔离；索引 key 和 namespace 不应含秘密。旧 Redis KV 没有加密 CAS 和 lease 语义。插件不持有数据库凭据，不能选择另一插件的所有者身份。

使用 CAS 的当前版本更新或删除；tombstone 保留版本，旧版本不能重建覆盖。lease owner/fence 必须匹配同一槽位且仍有效，失去租约后停止工作。加密存储不能保护已经授予插件进程的明文，仍须信任发布者。

Scoped 配置在持久提交后才能激活；AdmitBatch 与 Forward 必须校验配置代次及账号身份版本。受管请求在能力缺失、维护或恢复期间失败关闭。动作和升级需要管理员 step-up，超时不能当作未执行证明。
