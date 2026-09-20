# 插件开发指南

## 稳定边界

当前宿主只支持 `openai.oauth.outbound_transport.v1`。插件负责建立实际上游 HTTP/TLS 连接，Sub2API 负责账号选择、OAuth Token 生命周期、下游协议、响应解析、SSE、错误映射、用量统计和计费。

插件不应修改 API Key 路径，也不应自行刷新或持久化 OAuth Token。

旧能力仅覆盖 OAuth；Setup Token 需要声明 `oauth-like.v1`。需要账号/模型范围、持久任务和跨实例协调时，按[通用宿主契约](host-services.md)声明所需 features，HostService API 使用 2，传输协议仍为 1。

## 推荐结构

```text
plugin/
├── cmd/<plugin>/main.go
├── internal/config/
├── internal/transport/
├── ui/index.html
├── ui/assets/
├── tools/packager/
├── manifest.source.json
└── README.md
```

入口只调用 `pluginv1.Serve`。配置解析和传输实现放入独立包，以便不启动子进程就能单元测试。

## 运行时方法

| 方法 | 要求 |
|---|---|
| `GetInfo` | ID、版本、协议和能力必须与清单一致 |
| `Health` | 返回进程是否可以接受新请求，不执行昂贵探测 |
| `ValidateConfig` | 严格解析并返回完整规范化 JSON |
| `ApplyConfig` | 原子应用配置；失败时保留旧配置 |
| `TestConfig` | 验证当前环境和已保存配置，返回简短诊断 |
| `Forward` | 双向流式传输请求与原始 HTTP 响应 |
| `InitHostServices` | 接收 broker，核验宿主 API/features，成功后返回 ready |
| `AdmitBatch` | 对指定配置代次、账号、出站模型和身份版本返回快速准入决策 |
| `RunAction` | 使用 action_id 处理幂等，返回不含秘密的动作结果 |

请求帧顺序：`start`、零到多个 `body_chunk`、`body_end`。响应帧顺序：`start`、零到多个 `body_chunk`、`end`。不能继续处理的错误使用 `error` 帧。

`request_sent` 必须如实表示请求是否可能已经到达上游。值为 `true` 时宿主禁止自动切换账号重放；只有能确认尚未调用上游 Transport 时才能返回 `false`。

## 配置

- JSON 字段统一使用 `snake_case`。
- 拒绝未知字段、非法范围和受保护请求头。
- 默认配置必须完整，空对象应规范化为所有默认字段。
- Scoped 插件在 `ValidateConfig` 返回规范配置、`scoped_routing=true` 和精确 `managed_targets`，不得启动后台任务。
- 宿主先原子保存加密配置、范围及 `config_revision`，再调用 `ApplyConfig`；只有 `runtime_active=true` 才能激活工作。新配置激活失败时受管范围仍失败关闭。
- 旧非 scoped 插件继续使用先应用再保存、保存失败恢复旧配置的流程。所有插件都必须允许幂等应用。
- 配置代次、账号身份版本和 state CAS 版本分别描述不同对象，不能相互代用。

## 目录、动作与持久状态

UI 使用 `plugin.resources` 获取无凭据目录；插件进程才可以解析账号身份和代理认证 URL。`egresses=[]` 不代表直连，代理组不能自行跳过宿主的活跃和过期检查。

管理员动作通过 `plugin.action`/`RunAction`，必须有 action_id，不能借配置保存或状态轮询触发工作。宿主自动填写安装记录中的配置代次；动作幂等、任务取消和状态投影由插件负责。

使用宿主的加密状态 CAS 保存插件私有字节；用返回的版本处理写入竞争及删除后的 tombstone。跨实例互斥使用同一状态槽位的 lease，并在写入时带回 owner/fence。失去 lease 后停止工作。完整分页、TTL、限制和错误码见[通用宿主契约](host-services.md)。

## 资源管理

- 复用 HTTP Transport 和连接池，不要为每个请求创建新连接池。
- 配置切换后关闭旧空闲连接。
- 使用 stream context 取消 DNS、连接、上传和响应读取。
- 始终关闭上游响应体。
- 不在插件内无限缓存按账号区分的客户端。

## 最低测试集

- 配置默认值、未知字段、边界值和深复制。
- 插件身份及协议版本。
- 请求体分块、无请求体、固定 Content-Length。
- 响应状态、重复请求头、流式响应和响应读取错误。
- 上下文取消、插件退出和超时。
- 代理开启与禁用。
- 包哈希、签名、路径穿越和目标平台运行时。
- UI Bridge 加载、保存、测试、错误和超时。
- API 1 旧插件协商、必需 feature 缺失和握手失败关闭。
- Scoped 范围空集、配置提交失败、激活失败、代次不一致和准入到转发之间的身份变化。
- 目录脱敏、Setup Token capability、空出口、过期代理和代理组成员排序。
- 动作重复 ID、参数不一致、超时后重试及停用/升级时拒绝。
- 加密状态 CAS 冲突、tombstone 重建、跨插件隔离、lease 过期和旧 fence 写入拒绝。

发布前还应使用真实构建包运行宿主的插件进程集成测试。
