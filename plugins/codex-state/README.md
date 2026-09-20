# Codex STATE 插件

`baiyu.codex-state` 是独立的 Sub2API 插件，包含 Go 运行时与独立 Vue 管理 UI。支持 `gpt-6-astra`、`gpt-5.6-sol`、`gpt-5.6-terra`，各账号、模型分别选择 Pro / Team。宿主仅提供通用插件管理、资源目录、动作和隔离 UI Bridge。

## 默认与配置

全新安装默认关闭；不导入、不迁移旧内置 STATE 配置、account-extra 字段或票据。管理员需在插件页面重新配置并显式保存。启动插件运行时与配置中的 `enabled` 是不同开关。

配置版本固定为 `1`。`enabled` 为布尔总开关，`active` / `ready` 只属于只读状态对象，不能写入配置。

```json
{
  "version": 1,
  "enabled": false,
  "harvest_proxy_url": "",
  "dial_proxy_url": "",
  "accounts": [
    {
      "account_id": 123,
      "models": {
        "gpt-6-astra": { "enabled": false, "ticket_plan": "pro" },
        "gpt-5.6-sol": { "enabled": false, "ticket_plan": "pro" },
        "gpt-5.6-terra": { "enabled": false, "ticket_plan": "pro" }
      }
    }
  ]
}
```

示例账号 `123` 不是自动选中的默认账号；未配置时 `accounts` 为 `[]`。`ticket_plan` 仅允许 `pro` / `team`。代理 URL 可含敏感凭据，不得贴入日志、工单或截图。采集代理支持 HTTP / HTTPS / SOCKS5 / SOCKS5H，拨号代理只支持 HTTP / HTTPS；具体校验以 `core/config.go` 为准。业务出口仍属于宿主管理的账号资源，不通过插件表单改写普通账号凭据。

UI 支持中文 / 英文、账号和分组筛选、状态倒计时、strikes / cooldown、手动采集与取消。页面加载和状态轮询仅执行只读操作；草稿在当前页面内存中保留，不写浏览器持久存储。刷新浏览器或关闭页面会丢失未保存草稿。

首次配置缺少 version 时，页面显示“请先保存初始配置”，允许显式保存全关闭默认配置，然后才可编辑凭据；不会在加载时自动保存。代理 URL 由宿主可信弹窗管理，插件 iframe 仅显示已配置 / 未配置，不包含密码输入框。

## 宿主要求

当前打包工具声明 Sub2API `>=0.2.7-baiyu`。版本字符串本身不能代替能力检查，宿主必须同时满足：

| 能力 | 最低版本 |
| --- | --- |
| `plugin_protocol` | 1 |
| `transport_api` | 1 |
| `ui_bridge` | 1 |
| `host_service_api` | 2 |

当前打包器要求原七个 `host_features`：`scoped-routing.v1`、`admission.v1`、`resources.v1`、`actions.v1`、`state-cas.v1`、`leases.v1`、`oauth-like.v1`，以及新增的 `request-completion.v1`、`config-secrets.v1`，共九项。不能把其他三个协议版本也误写为 2，不能跳过缺失能力的拒绝检查。

## 管理接口与隔离

这些 HTTP 接口由宿主调用，插件 iframe 不直接访问：

- `GET /api/v1/admin/plugins/:id/resources`：账号、分组、代理的白名单元数据；停止态仍可读。
- `POST /api/v1/admin/plugins/:id/actions`：请求 `{action_id,name,payload}`，响应 `{action_id,accepted,status,result}`；仅运行态允许。
- `POST /api/v1/admin/plugins/:id/upgrade`：multipart 字段 `plugin` 上传升级包；不要先主动停用插件来模拟升级。
- `PUT /api/v1/admin/plugins/:id/config/secrets`：仅宿主可信弹窗调用，请求 `{values:{field:string}}`；省略字段保留、非空替换、空字符串清空，执行 step-up 验证。iframe 不能调用或传入 values。

UI Bridge 使用 `config.load` / `config.save`、`plugin.resources`、`plugin.status`、`plugin.action`。资源成功响应在 `resources` 字段，状态和动作成功响应在 `result` 字段。动作 `name` 为 `harvest` / `cancel`，`payload` 为 `{account_id,model}`；动作结果不是持续状态，后续状态仍通过 `plugin.status` 获取。

manifest 顶层声明 `config_secrets: ["harvest_proxy_url", "dial_proxy_url"]`。宿主读取 / 保存配置响应将这两个字符串替换为 `""`，附带 `_host_secrets: {field:boolean}`；普通 `config.save` 不能修改秘密，宿主合并已保存秘密并剔除 metadata 后再校验。运行时实际配置仍保持上面的固定 JSON，不保存 `_host_secrets`。

iframe 通过无业务参数的 `plugin.secrets.edit` 请求打开宿主弹窗，成功响应仅为 `result: {configured:{field:boolean}}`。宿主标签使用通用字段 key，密码输入默认空；取消、保留或单纯加载不写入。秘密原文，包括新输入值，不进入 iframe 的消息、草稿、状态或日志；`type=password` 本身不作为隔离机制。

iframe 使用 `sandbox="allow-scripts"`，不授予同源或网络访问，不注入管理员 token。Bridge 短期会话 token 不是管理员凭据。资源只含 ID、名称、平台、账号类型、分组、业务出口是否配置，以及代理协议 / host / port，不包含账号 `extra`、credentials 或代理密码。状态与日志仅投影已知字段和诊断码，未知错误不反射原始文本。

## 构建与打包

从 `ui/` 执行，使用锁文件安装：

```sh
pnpm install --frozen-lockfile
pnpm test
pnpm build
```

`ui/dist` 包含 `index.html`、经典 IIFE 脚本 `app.js` 和 `style.css`，无需宿主内部组件。然后从插件根目录执行：

```sh
go run ./tools/package --out dist
# 正式签名示例：私钥必须位于工作区与输出目录之外。
go run ./tools/package --out dist --signing-key /secure/path/publisher.pkcs8.pem --key-id publisher-id
```

签名密钥格式为 Ed25519 PKCS8 PEM；指定 `--signing-key` 时必须提供 `--key-id`。不要提交、上传或打包私钥。无密钥时产出未签名包，不应为安装它而关闭生产签名校验；签名公钥的信任配置由宿主管理。

默认目标为 Linux `amd64,arm64`，可使用 `--arches amd64` 或 `--arches arm64` 限定。工具构建运行时并读取已有 `ui/dist`，不会代替 UI 构建。它还强制读取 `LICENSE`、`THIRD_PARTY_NOTICES.md`、`sources.lock.json`、`MAINTENANCE.md` 并放入包内；本 README 不在当前打包清单中。输出包括 `.s2plugin`、运行时二进制和 `SHA256SUMS`，manifest 包含文件 SHA256 与可选签名。

## 安装、升级与多实例

仓库维护者可使用 `python .agents/skills/sub2api-production-deploy/scripts/release.py plugin-deploy-follow --commit <40位完整SHA>` 自动完成签名构建、本地校验、VM 插件 Gate、生产安装/升级与逐实例验真。该入口不会自动保存秘密、启用插件、启用账号模型或触发真实采集；首次安装保持 disabled。

- 首次安装使用 `POST /api/v1/admin/plugins/upload`，multipart 字段为 `plugin`。上传前必须确认 `baiyu.codex-state` 尚未安装；不得在 disabled、error 或 incompatible 状态下用 upload 替代 upgrade。上传后的插件必须保持停用，先核验签名、兼容性和配置页面，再分别保存配置、秘密并显式启用。
- 已安装的 Scoped 插件使用 `POST /api/v1/admin/plugins/:id/upgrade` 升级。升级前不能主动停用来解除 strict；宿主会保留受管范围、排空在途请求，并在失败时恢复旧包或保持明确不可用状态。
- PostgreSQL 保存插件包原件、配置、受管范围和维护状态。每个宿主实例在 `${DATA_DIR}/plugins`（或 `plugins.data_dir`）维护自己的校验后副本；缺失或过期时从数据库原包重新校验恢复，不需要人工逐机复制。
- 数据库发布成功不等于全部实例运行成功。多实例部署必须逐实例核对插件版本、二进制 SHA、运行时健康和受管范围；任一实例无法恢复时，受管账号模型保持 strict，不能静默绕过。
- 生产默认拒绝未签名包。当前本地无密钥构建得到的是开发包，不能通过临时开启 `plugins.allow_unsigned` 安装到生产。
- 仅更新本插件目录且不改变宿主能力时，插件包版本独立发布，宿主仍可保持 `0.2.7-baiyu`。Host API、管理路由、migration 或宿主 UI 壳变化仍需完整应用发布。

## 来源与维护

固定源码基线见 [sources.lock.json](sources.lock.json)，维护流程见 [MAINTENANCE.md](MAINTENANCE.md)，许可文本与声明见 [LICENSE](LICENSE) 和 [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md)。`ccodex-sleep-state` 仅供设计参考，本插件独立实现，不引入其源码或运行依赖。

本记录只锁定源码基线并记录其许可证证据，不代表上游或本插件的发布包已审计。哈希、签名、源码核对和前端测试也不能替代发行产物的独立检查。
