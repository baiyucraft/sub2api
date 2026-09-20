# 通用宿主服务与 Scoped 插件

本文定义 HostService API 2 的通用契约。插件配置、动作名称和持久状态的业务含义由插件实现负责，宿主只处理身份、资源、版本、隔离和并发控制。

## 版本与功能协商

`plugin_protocol`、`transport_api`、`ui_bridge` 保持 1；`host_service_api` 独立演进，当前为 2。清单中的 `requires.host_service_api` 和 `requires.host_features` 均可省略。旧插件省略这些字段或声明 API 1 时，宿主仍下发 `InitHostServices.host_service_api_version=1`。

新插件只声明实际依赖的功能：

| Feature | 契约 |
| --- | --- |
| `scoped-routing.v1` | 按账号与出站模型声明受管范围，使用配置代次 |
| `admission.v1` | `AdmitBatch` 批量准入 |
| `resources.v1` | 安全资源目录和进程内代理解析 |
| `actions.v1` | 通用管理员动作 |
| `state-cas.v1` | 加密持久状态及版本 CAS |
| `leases.v1` | 带 owner 和 fence 的限时租约 |
| `oauth-like.v1` | 在 OpenAI OAuth 之外支持 Setup Token |

宿主拒绝未知的必需功能和高于自身的 API 版本。声明必需宿主功能的插件必须成功完成 `InitHostServices`，并返回 `ready=true`；缺少服务、返回 `Unimplemented`、超时或拒绝就绪都会阻止启用。旧插件未声明这些要求时保留可选握手行为。

插件通过 `HostBrokerReceiver.SetHostBroker` 接收 broker，用 `host_service_id` 拨号，并验证请求中的 API 版本及 `host_features`。broker 生命周期与插件进程一致。功能声明不赋予插件直接访问数据库或浏览器管理员会话的权限。

## Scoped 配置与准入

`ValidateConfig` 必须只校验并规范化配置。声明 `scoped-routing.v1` 的插件还必须返回 `scoped_routing=true` 和 `managed_targets=[{account_id, models}]`。`models` 是最终出站模型的精确名称，不能用显示名称或模糊匹配代替。空 targets 表示不接管请求；旧插件未声明 scoped 功能时保留旧路由语义。

Scoped 保存顺序为：校验配置和账号范围，原子持久化加密配置、受管范围和新的 `config_revision`，再调用 `ApplyConfig(config_json, config_revision, runtime_active)`。草稿校验不能启动后台任务。`runtime_active=false` 时插件保持暂停；只有已提交配置且处于启用状态时才允许激活。应用失败时已保存范围仍是权威数据，受管请求失败关闭，等待恢复，不能绕回内置路径。

`AdmitBatch` 接收同一配置代次下的候选 `{account_id, outbound_model, identity_revision}`。响应必须回传配置代次，并为每个候选返回同一账号、模型和身份版本的决策。未知、缺失、过期或不匹配的结果视为不可用。准入应有界、快速、无上游副作用。

`ForwardRequestStart` 携带 `outbound_model`、`identity_revision`、`config_revision`。插件必须在真正发送请求前再次校验这些字段，处理准入到发送之间的配置和身份变化。未发送上游的拒绝可返回 `request_sent=false`；已发送或无法证明未发送时必须返回 `true`。

## 资源与出站身份

`ListResources.resources_json` 是以下 JSON 的 UTF-8 字节；管理员资源端点和 UI Bridge 返回同一对象：

```json
{
  "accounts": [{
    "id": 7,
    "name": "account-a",
    "platform": "openai",
    "account_type": "oauth",
    "group_ids": [3],
    "business_egress_configured": true
  }],
  "groups": [{"id": 3, "name": "group-a"}],
  "proxies": [{"id": 9, "name": "egress-a", "protocol": "http", "host": "proxy.example", "port": 8080}]
}
```

目录只列出活跃、未删除、非影子的 OpenAI OAuth/Setup Token 账号及其活跃分组；代理只列出活跃且未过期的记录。`business_egress_configured` 表示账号配置了代理或代理组，不是可用性或连通性证明。空列表编码为 `[]`。读取目录不启动插件，不刷新 token，也不探测上游。

带凭据的两个 RPC 仅提供给获得账号能力的插件进程，不经过 iframe：

- `ResolveProxy(proxy_id)` 返回 `found` 和 `proxy_url`；未知、停用或过期代理返回 `found=false`。URL 可能含代理认证材料。
- `ResolveOutboundIdentity(account_id)` 从 live repository 读取账号，再解析 token、宿主出站 headers、`identity_revision` 和 `egresses=[{proxy_id,proxy_url}]`。

身份版本是 `SHA-256(accountID + NUL + type + NUL + trim(chatgpt_account_id) + NUL + trim(chatgpt_user_id))` 的小写十六进制字符串，accountID 为十进制。它在 token 刷新前计算；token 轮换、代理、代理组成员和名称不参与 hash。

代理组出口复用宿主的代理组解析，只包含真实、活跃、未过期的成员，按代理 ID 升序排列。空组不产生直连出口；普通账号未配置代理时 `egresses=[]`，插件不能据此假定存在业务出口。旧 `proxy_url` 字段是首个可用出口的兼容投影，不代表唯一出口，也不预留并发槽位。

旧 capability 的账号枚举和身份 RPC 保持 OAuth-only。新插件需声明 `oauth-like.v1` 才能通过这些敏感 RPC 使用 Setup Token；资源目录本身仍可提供两类账号的非敏感元数据。

## 管理员动作

`RunAction` 请求字段是 `action_id`、`name`、`payload_json` 和 `config_revision`。动作名称与 payload 由插件定义；宿主仅校验请求并路由到已经启用且正在运行的实例。调用不会启动临时进程，也不会隐式应用配置。

管理员 API 接收：

```json
{"action_id":"refresh-42","name":"refresh","payload":{"account_id":7}}
```

宿主从安装记录填充 `config_revision`，核验当前 route 的 runtime 和代次，并限制调用为 30 秒。生命周期变更、版本未对齐或插件未运行时拒绝动作。`action_id` 和 `name` 各不超过 128 字节，仅允许 `[A-Za-z0-9._-]`；payload 必须是最多 256 KiB 的 JSON 对象。

插件返回相同 `action_id`、`accepted`、简短 `status` 和 `result_json`。`status` 使用上述字符集且不超过 64 字节；结果最多 256 KiB，必须是 JSON 对象。API 输出的是 `result` 对象，不是 protobuf bytes 的 Base64 字符串；空结果归一化为 `{}`。`accepted=true` 仅表示插件接受动作，不保证任务已经完成。

幂等由插件引擎负责：以 `action_id` 识别重复请求，校验同一 ID 的参数一致性，并持久记录结果。超时不能证明动作未执行；重试应复用原 ID，不能以新 ID 自动重放。`action_id` 与 UI Bridge 的 `request_id` 是不同的标识。

## 加密持久状态与 CAS

`StateGet`、`StateCompareAndSwap`、`StateList` 提供跨实例共享的数据库状态。宿主从当前连接绑定 `pluginKey`，按 `(pluginKey, namespace, key)` 隔离。value 是插件定义的不透明字节，以宿主密钥加密后存储；升级同一插件 ID 不改变状态身份。namespace、key、版本等索引元数据不是加密 value，不应包含秘密。

- `StateGet` 返回 `found/value/version`。从未创建的槽位版本为 0；删除后的 tombstone 返回 `found=false` 但保留正版本。
- `StateCompareAndSwap(expected_version=0)` 只创建从未使用过的槽位；更新或恢复 tombstone 必须提交当前版本。成功写入或 `delete=true` 都递增版本；删除不存在的值发生冲突。
- `StateList` 按 key 的字节序返回未删除的 keys，用响应 `next_key` 作为下次 `after_key`。默认 100 条，最多 200 条；多页读取不是事务快照。
- namespace 最多 128 字节，使用 `[A-Za-z0-9._-]`。key/cursor 最多 512 字节，为合法 UTF-8 且不能含 NUL；`/`、`%` 等字符按普通 key 处理。
- value 最多 256 KiB。当前持久状态不支持 TTL，`StateCompareAndSwap.ttl_seconds` 必须为 0。KV 的过期机制不能套用到这个接口。

CAS 冲突返回 gRPC `Aborted`。插件应重新读取并重新计算更新，不能跳过版本检查覆盖写入。删除保留版本是为了防止旧客户端将删除后的同名槽位当成从未创建过。

## Lease 与 Fence

`AcquireLease`、`RenewLease`、`ReleaseLease` 的作用域同样是 `(pluginKey, namespace, key)`。租约用于协调同一状态槽位的多实例写者。

1. Acquire 使用 `fence=0`、`ttl_seconds=1..300`。宿主签发随机 owner 和正数 fence，调用者必须使用响应值；请求 owner 只是可选提示。
2. Renew 提交相同 owner/fence 和新的 TTL；`expires_at` 是数据库时间对应的 Unix 秒值。
3. 在该槽位的 CAS/delete 中携带 `lease_owner` 和 `lease_fence`。有效 lease 会阻止无 lease 的写入；已失效、过期或不匹配的凭证不能写入。
4. Release 提交相同 owner/fence，`ttl_seconds=0`。释放保留 fence 计数，下次获取使用更高代次。

被占用的获取或写入冲突返回 `Aborted`；lease 丢失返回 `FailedPrecondition`。失去 lease 后应停止对应工作，不能凭缓存中的 owner 继续写。owner 是秘密，不得发送给 UI 或写入日志。

旧 `KV*` RPC 继续保留为独立的 Redis 命名空间设施；它没有这里的加密持久 CAS、tombstone 和 fence 语义，不能替代这套一致性协议。

## 请求完成确认

`request-completion.v1` 提供通用 `HostService.CompleteRequest(request_id)`。它只确认 Forward 及其收尾已完成，不传递请求正文、凭据或业务观察数据。宿主按当前进程的 broker 连接隔离登记，重复确认无副作用。

插件收到有效 `ForwardRequestStart` 后应立即注册最外层 defer：在响应体关闭、请求所属的状态写入及清理全部完成后，使用独立的 `context.Background()` 加 5 秒超时调用 CompleteRequest，不能复用已取消的 Forward context。正常 `ForwardResponseEnd` 也具有完成确认语义，必须在这些工作结束之后发送。

宿主 HTTP Body.Close、RPC 取消、流 EOF 或错误帧都不能证明插件已完成，不能据此删除分布式 inflight 登记。宿主在收到 CompleteRequest、正常 End 或确认该进程已退出时才清理；尚未发送 Start 的本地失败可直接撤销登记。确认后的数据库清理失败仍保留分布式登记，维护流程不得猜测其已排空。旧未登记请求的传输行为保持兼容。

## 管理端入口

路径均在 `/api/v1/admin/plugins` 下，要求管理员认证：

| 方法与路径 | 行为 | Step-up |
| --- | --- | --- |
| `GET /:id/resources` | 读取安全目录，停用状态也可读取 | 否 |
| `POST /:id/actions` | 对已运行插件执行通用动作 | 是 |
| `POST /:id/upgrade` | multipart 字段 `plugin` 上传 `.s2plugin` | 是 |

Scoped 升级要求相同插件 ID、已受信任签名和兼容能力。宿主保留已提交范围并在维护期间阻止受管新请求，等待已有请求完成；不能确认排空或候选版本不可用时不能悄悄绕回内置路径。旧非 scoped 插件继续使用显式停用再安装的升级流程。

UI Bridge 只暴露 `plugin.resources` 和 `plugin.action`，不暴露代理解析、出站 token、持久状态原文或 lease owner。插件的 Health、动作结果和错误消息必须自行投影为不含凭据的可展示数据。
