# UI Bridge v1

## 加载方式

宿主为每次打开配置页创建短时 UI 会话：

```text
/api/v1/plugin-ui/<asset-token>/index.html#bridge_token=<bridge-token>
```

资源 Token 用于读取包内 `ui/` 文件，Bridge Token 只存在于 URL fragment，不会发送到服务器。iframe 使用 `sandbox="allow-scripts"`，不授予 `allow-same-origin`。

UI 只能加载包内、已在清单声明的资源。CSP 禁止外部网络连接、表单提交和外部 frame。

## 消息信封

UI 到宿主：

```json
{
  "source": "sub2api-plugin-ui",
  "bridge_token": "TOKEN",
  "type": "config.load",
  "request_id": "UNIQUE_ID"
}
```

宿主到 UI：

```json
{
  "source": "sub2api-plugin-host",
  "bridge_token": "TOKEN",
  "request_id": "UNIQUE_ID",
  "ok": true
}
```

## 方法

| `type` | UI 参数 | 成功响应 |
|---|---|---|
| `sub2api.plugin.ready` | 无 | 无响应 |
| `config.load` | 无 | `config` |
| `config.save` | `config` 对象 | 规范化后的 `config` |
| `config.test` | 无 | `result` |
| `plugin.status` | 无 | `result`（`Health`：`{healthy, message, status_json}`） |
| `plugin.resources` | 无 | `resources`（无凭据的 accounts/groups/proxies） |
| `plugin.action` | `action_id`、`name`、`payload` 对象 | `result`（`{action_id, accepted, status, result}`） |
| `plugin.secrets.edit` | 无 | `configured`（字段名到布尔值的映射；不返回秘密值） |
| `ui.resize` | `height` | 无响应 |
| `ui.notify` | `level`、`message` | 无响应 |

`config.test` 在 v1 中测试已保存配置（需二次验证，可产生副作用）。UI 若要测试当前表单，应先调用 `config.save`。

`plugin.status` 是只读运行时状态通道：无副作用、免二次验证、不弹宿主提示，供状态面板轮询。它映射到插件 `Health`，`result.status_json` 是插件自定义的不透明 JSON 快照。带状态展示的插件应使用它，而不是把 `config.test` 当作状态轮询。

`plugin.resources` 映射到 `GET /admin/plugins/:id/resources`，停用时也可读取，不启动插件。字段见[通用宿主契约](host-services.md)。iframe 不提供 `ResolveProxy` 或 `ResolveOutboundIdentity`，不能通过目录取得认证 URL、token 或请求头。

`plugin.action` 经 step-up 后调用 `POST /admin/plugins/:id/actions`，仅适用于已启用且运行中的插件。`payload` 必须是 JSON 对象；`action_id` 和 `name` 使用 `[A-Za-z0-9._-]`，各最多 128 字节。`config_revision` 由宿主填写，UI 不传入。示例：

```json
{
  "source": "sub2api-plugin-ui",
  "bridge_token": "TOKEN",
  "type": "plugin.action",
  "request_id": "ui-request-1",
  "action_id": "refresh-42",
  "name": "refresh",
  "payload": {"account_id": 7}
}
```

Bridge 成功响应的 `result` 是动作信封，其内部 `result` 是插件定义的 JSON 对象，不是 Base64。`accepted` 不代表完成；重试同一动作保持 action_id，新的 Bridge request_id 仅用于匹配新的 UI 消息。动作结果、Health 和通知只能包含可公开给管理员 UI 的信息。

## 必须执行的校验

UI 接收消息时必须验证 `event.source === parent`、消息来源标识、Bridge Token 和等待中的 `request_id`。每个请求必须有超时和卸载清理。

宿主不会向 iframe 提供管理员 Token。插件 UI 不得尝试访问管理 API、Cookie、父页面 DOM 或浏览器存储中的宿主数据。

## Protected configuration values

Packages may declare a signed, top-level `config_secrets` array of configuration
field names and require `config-secrets.v1`. For these packages, config reads and
save responses replace each protected value with an empty string and return
`_host_secrets: {field: boolean}`. Normal config saves preserve stored secrets
and discard this metadata before plugin validation.

`plugin.secrets.edit` accepts no payload values. It opens a trusted host dialog;
only that dialog calls `PUT /admin/plugins/:id/config/secrets` with
`{"values":{"field":"replacement"}}`. Omission keeps a value, an empty string
clears it, and a non-empty string replaces it. The iframe receives configured
booleans only, never old values or newly entered credentials. The endpoint uses
the same administrative step-up protection as configuration writes. Packages
without this opt-in keep their existing configuration behavior.
