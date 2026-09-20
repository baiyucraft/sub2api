# `.s2plugin` 包格式

`.s2plugin` 是 ZIP 文件，根目录必须包含 `manifest.json`，生产包还必须包含 `signature.json`。

## 标准布局

```text
manifest.json
signature.json
runtimes/<goos>-<goarch>/<binary>
ui/index.html
ui/assets/...
```

所有运行时和 UI 文件必须出现在 `manifest.files`，值为小写十六进制 SHA-256。清单和签名文件自身不写入 `files`。

包不允许绝对路径、父目录跳转、重复路径、符号链接、未声明文件或缺失文件。宿主还限制上传大小、解压后大小和文件数量。

## 清单

字段规范见 [`v1/manifest.schema.json`](../v1/manifest.schema.json)。版本字段含义：

- `version`：插件自身语义化版本。
- `requires.sub2api`：宿主硬兼容范围。
- `recommended_sub2api_version`：建议宿主版本。
- `tested_sub2api_versions`：发布者真实验证过的版本。
- `plugin_protocol`：进程握手协议。
- `transport_api`：请求和响应帧协议。
- `ui_bridge`：配置 UI 消息协议。
- `host_service_api`：可选最低宿主服务 API，当前支持 1 和 2；省略保持旧插件兼容。
- `host_features`：可选必需功能列表；宿主逐项核验，不能把未支持的功能当作可选提示。

新 Scoped 插件的 `requires` 可声明：

```json
{
  "sub2api": ">=0.1.179 <0.2.0",
  "plugin_protocol": 1,
  "transport_api": 1,
  "ui_bridge": 1,
  "host_service_api": 2,
  "host_features": ["scoped-routing.v1", "admission.v1", "resources.v1", "actions.v1", "state-cas.v1", "leases.v1", "oauth-like.v1"]
}
```

版本范围只是示例，发布者必须改成实际验证过的范围，并只保留真正需要的 features。Schema 允许将来增加的正整数 API 版本和功能名称；能否运行由宿主能力检查决定。

`account_type` 支持 `oauth` 和 `setup-token`。声明后者必须同时在 `requires.host_features` 包含 `oauth-like.v1`；旧 OAuth 清单无需新增字段，API Key 和其他平台仍不在这项 capability 内。

## 签名

`signature.json`：

```json
{
  "algorithm": "ed25519",
  "key_id": "publisher-key-id",
  "signature": "BASE64_SIGNATURE"
}
```

签名对象是 `manifest.json` 的精确原始字节。发布者私钥不得进入插件包、源码仓库或 Sub2API 运行环境。部署者只配置 Base64 Ed25519 公钥。

默认生产配置拒绝未签名包。官方 OpenAI Transport 使用宿主内置公钥验签；当前 fork 的 `baiyu.codex-state` 也使用仅绑定该插件 ID 的内置公钥。其他发布者仍需配置 `trusted_publishers`。`allow_unsigned` 只用于开发者自己构建的本地包。
