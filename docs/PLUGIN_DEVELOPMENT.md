# Sub2API 插件开发教程

本文面向希望为 Sub2API 开发、打包和发布插件的团队。插件是独立进程和静态 UI 组成的 `.s2plugin` 包，宿主通过稳定的 gRPC 协议调用它。本文以当前宿主已经定义的 `openai.oauth.outbound_transport.v1` 能力作为协议示例，说明开发者需要准备什么、哪些职责属于插件、哪些职责仍由 Sub2API 负责。

本文描述公开协议、宿主边界、开发流程和独立插件包的安装升级合同。仓库中的 `plugins/codex-state/` 是一套可构建、可安装的 fork 插件实现，但不是官方通用示例，也不代表 Sub2API upstream 发布或维护该插件。公开协议始终以 `backend/pkg/pluginapi/` 为准。

## 1. 准备开发环境

建议使用以下环境：

- 使用与所依赖 SDK 版本兼容的 Go 工具链，版本要求以 `backend/go.mod` 为准；
- Node.js（仅在插件 UI 使用 JavaScript 时需要）；
- Git；
- 与目标部署环境一致的构建工具链。

协议定义和通用说明位于：

- `backend/pkg/pluginapi/v1/plugin.proto`：进程间消息和流式请求定义；
- `backend/pkg/pluginapi/v1/runtime.go`：插件进程启动入口；
- `backend/pkg/pluginapi/v1/manifest.schema.json`：包清单 JSON Schema；
- `backend/pkg/pluginapi/docs/`：开发、UI Bridge、包格式和安全边界说明。
- [通用宿主契约](../backend/pkg/pluginapi/docs/host-services.md)：Scoped 配置、准入、资源目录、动作、加密状态 CAS 和 lease。

目前仍未提供可直接复制的官方示例源码。开发者可以按照本文的目录和协议说明创建自己的插件工程；需要查看完整的 fork 实现时，可阅读 `plugins/codex-state/`，但不得把其中的 Codex STATE 业务常量当作通用 SDK 契约。

## 2. 创建插件工程

在示例仓库发布前，可以先创建一个独立的 Go 工程，目录建议如下：

```text
my-plugin/
├── cmd/<plugin>/main.go
├── internal/pluginconfig/
├── internal/transport/
├── ui/index.html
├── ui/assets/
├── tools/
├── manifest.source.json
└── build.sh
```

开发时至少准备以下部分：

1. `manifest.source.json`：插件 ID、名称、版本、作者、能力和兼容的 Sub2API 版本；
2. `cmd/<plugin>/main.go`：启动入口和运行时版本注入，并同步打包器中的构建目标和二进制名称；
3. `internal/pluginconfig/`：配置结构、默认值、严格校验和规范化；
4. `internal/transport/`：HTTP 客户端、代理、请求头、请求体、网络连接参数、响应流和资源回收；
5. `ui/index.html` 与 `ui/assets/`：插件自己的配置界面；
6. 单元测试、进程集成测试和目标平台构建配置。

入口文件应保持很小，只负责调用 `pluginv1.Serve`。实际逻辑放在可独立测试的包中，避免把配置解析、网络请求和协议组装全部写在 `main.go`。

## 3. 编写运行时

运行时实现 `TransportPlugin` 服务，必须满足以下约定：

| 方法 | 要求 |
| --- | --- |
| `GetInfo` | 返回的插件 ID、版本、协议版本、传输 API 版本和能力必须与清单一致。 |
| `Health` | 快速返回进程是否可以接收新请求，不执行长时间网络探测。 |
| `ValidateConfig` | 严格解析 JSON，拒绝未知字段和非法范围，并返回完整的规范化配置。 |
| `ApplyConfig` | 成功后原子切换配置；失败时保留旧配置和旧连接。 |
| `TestConfig` | 针对已保存配置进行快速诊断，返回简短、可展示的结果。 |
| `Forward` | 按协议接收请求流，发出上游请求，再按顺序返回响应流。 |
| `InitHostServices` | 通过 broker 连接宿主，核验 HostService API 版本和必需 features。 |
| `AdmitBatch` | 对账号、最终出站模型、身份版本和配置代次返回快速、无上游副作用的准入决策。 |
| `RunAction` | 接受管理员的通用动作，按 action_id 处理幂等并返回脱敏结果。 |

请求帧顺序为 `start`、零到多个 `body_chunk`、`body_end`；响应帧顺序为 `start`、零到多个 `body_chunk`、`end`。不能继续处理时发送 `error` 帧。

`ForwardResponseError.request_sent` 必须准确：只有在能够确认尚未调用上游 HTTP Transport 时才返回 `false`；一旦已经调用，或无法确认上游是否收到请求，就返回 `true`。宿主会据此决定是否允许切换账号重试，避免重复执行同一个请求。

资源管理也属于运行时契约：复用 HTTP Transport 和连接池，配置切换时关闭旧空闲连接，沿用 gRPC stream 的 context 取消 DNS、连接、上传和响应读取，并始终关闭上游响应体。日志和错误消息不能包含 Token、代理凭据、完整请求体或敏感响应头。

旧插件默认协商 HostService API 1；新插件通过 `requires.host_service_api=2` 和 `requires.host_features` 声明必需设施。必需设施缺失、握手失败或 `ready=false` 会阻止启用；未声明这些要求的旧插件继续允许可选握手。进程协议、传输 API 和 UI Bridge 仍是 v1。

## 4. 设计插件配置

插件配置由插件定义，由 Sub2API 加密保存。推荐流程是：

1. 在 `internal/pluginconfig.Config` 中定义字段和默认值；
2. 使用 `json.Decoder.DisallowUnknownFields` 等严格方式解析；
3. 将空对象规范化为完整默认配置；
4. 在 `ValidateConfig` 和 `ApplyConfig` 中复用同一套校验；
5. 对 Scoped 插件返回 `scoped_routing=true` 和精确的 `managed_targets=[{account_id,models}]`；草稿校验不启动后台工作；
6. 宿主原子保存加密配置、范围和新 `config_revision` 后调用 `ApplyConfig`，仅在 `runtime_active=true` 时激活。

空受管范围表示不接管任何请求。保存成功但激活失败时，已经提交的范围继续失败关闭，等待恢复；不能使用旧配置绕过新范围。旧非 Scoped 插件保留先应用再保存、失败恢复旧配置的原有流程。

JSON 字段统一使用 `snake_case`。敏感配置不要放入 URL、UI 通知、诊断结果或日志。插件不应从 UI 读取、刷新或持久化账号 Token；需要认证材料时由获授权的插件进程调用宿主出站身份 RPC。

Scoped 路由按账号 ID 与最终出站模型精确匹配。准入候选和实际 Forward 都携带配置代次及身份版本，插件必须在发送上游前再核验，防止候选选择后配置或身份已经变化。身份 hash 只使用账号 ID、账号类型和 ChatGPT account/user ID，不随 token 或代理轮换变化。

## 5. 实现插件自己的配置 UI

UI 是插件包内的静态页面，不需要修改 Sub2API 前端源码。宿主会在受限 iframe 中加载 `ui/index.html`，并通过 UI Bridge 提供配置读写和测试能力。

页面初始化流程：

1. 加载包内 HTML、CSS 和 JavaScript；
2. 创建 Bridge 并注册 `message` 监听；
3. 发送 `sub2api.plugin.ready`；
4. 调用 `config.load` 渲染表单；
5. 编辑后调用 `config.save`；
6. 测试前先保存，再调用 `config.test`；
7. 页面卸载时调用 `dispose()`。

当前 Bridge 支持：

| 消息 | 用途 |
| --- | --- |
| `config.load` | 读取当前配置。 |
| `config.save` | 提交配置，由运行时校验、应用并加密保存。 |
| `config.test` | 运行已保存配置的诊断。 |
| `plugin.status` | 被动读取 Health 状态，不启动任务或上游探测。 |
| `plugin.resources` | 读取无凭据的账号、分组和代理目录；停用时也可读取。 |
| `plugin.action` | 经 step-up 对已启用且运行中的插件提交通用动作。 |
| `ui.resize` | 调整配置 iframe 高度。 |
| `ui.notify` | 显示成功、错误或提示消息。 |

每条消息都必须带 `request_id`，并校验 `event.source`、消息来源标识和 Bridge Token。不要依赖 CDN、远程脚本、Cookie 或本地存储。页面需要兼容窄屏和明暗主题，并正确处理加载、保存、测试、超时和未保存状态。

详细信封格式见 `backend/pkg/pluginapi/docs/ui-bridge.md`。如果后续示例仓库提供可复用的 Bridge SDK，本文会在示例仓库章节补充对应路径和使用方式。

资源目录仅包含账号的 `id/name/platform/account_type/group_ids/business_egress_configured`、分组的 `id/name` 和代理的 `id/name/protocol/host/port`。token、代理认证 URL、lease owner、持久状态原文不经过 iframe。不要通过 `config.save`、`plugin.status` 或 `plugin.resources` 触发实际工作。

动作请求使用 `{action_id,name,payload}`；宿主填写配置代次，动作 ID 与 Bridge 的 request_id 分开使用。结果为 `{action_id,accepted,status,result}`，其中 result 是 JSON 对象，不是 Base64。插件负责动作幂等；超时后重试应复用原 action_id。

## 6. 编写包清单

只维护 `manifest.source.json`，不要手工编辑构建目录中的 `manifest.json`。至少需要填写：

```json
{
  "schema_version": 1,
  "id": "example.openai.transport",
  "name": "Example OpenAI Transport",
  "version": "0.1.0",
  "requires": {
    "sub2api": ">=0.1.179 <0.2.0",
    "recommended_sub2api_version": "0.1.179",
    "tested_sub2api_versions": ["0.1.179"],
    "plugin_protocol": 1,
    "transport_api": 1,
    "ui_bridge": 1,
    "host_service_api": 2,
    "host_features": ["scoped-routing.v1", "admission.v1", "resources.v1", "actions.v1", "state-cas.v1", "leases.v1", "oauth-like.v1"]
  },
  "capabilities": [
    {
      "id": "openai.oauth.outbound_transport.v1",
      "platform": "openai",
      "account_type": "oauth"
    }
  ],
  "runtimes": {},
  "ui": { "entrypoint": "ui/index.html" },
  "files": {}
}
```

打包器会自动填充目标平台运行时、UI 和运行时文件的 SHA-256。清单中的 `requires.sub2api` 是硬兼容范围；`tested_sub2api_versions` 应只填写真实验证过的版本；`recommended_sub2api_version` 用于管理页面展示。当前宿主仅处理 `openai.oauth.outbound_transport.v1`，声明其他能力不会自动产生新路由。后续增加 Provider 支持时，会在协议、能力清单和宿主路由完成适配后，再补充对应的清单示例。

示例版本范围需按实际验证结果调整。`host_service_api` 和 `host_features` 对旧清单可省略；新插件只声明实际依赖的 features。`account_type=setup-token` 必须同时声明 `oauth-like.v1`，旧 OAuth-only capability 不会因宿主升级而自动获得 Setup Token 身份。API Key 仍不在此 capability 中。

## 通用状态与跨实例协调

有持久任务的插件可以使用 `StateGet`、`StateCompareAndSwap`、`StateList`。宿主将不透明 value 加密保存到共享数据库，并把 namespace/key 绑定到当前插件身份。索引元数据不应包含秘密，UI 只显示插件主动生成的脱敏摘要。

首次创建使用 `expected_version=0`，更新提交当前版本。删除产生保留版本的 tombstone，恢复同名值时也必须使用当前版本，不能再按首次创建覆盖。CAS 冲突后重新读取并计算更新；状态分页使用 `next_key`/`after_key`，当前 state 的 ttl_seconds 必须为 0。

`AcquireLease` 为同一状态槽位签发随机 owner 与递增 fence，TTL 为 1 到 300 秒。续租和释放必须匹配响应中的 owner/fence；CAS 和删除携带 `lease_owner/lease_fence`。lease 过期或被替换后旧持有者不能再写，插件应停止相应工作。旧 Redis KV 不提供这套加密持久 CAS 和 lease 语义。

完整消息、大小限制、错误分类和恢复语义见[通用宿主契约](../backend/pkg/pluginapi/docs/host-services.md)。这些接口不解释插件私有状态的业务内容。

## 7. 生成密钥并签名

生产包应始终签名，宿主默认拒绝未签名包。可以使用插件工程中的密钥生成工具生成一对 Ed25519 密钥；示例仓库发布后会提供标准工具和完整命令：

```bash
go run ./tools/keygen -out build/keys/my-publisher
```

生成的 `my-publisher.private` 只保存在受控的开发机或 CI Secret 中，不能提交到源码仓库、插件包或部署服务器。公钥是 Base64 文本，可以提供给部署者。

插件工程的 `build.sh` 应调用标准打包器。自定义发布者密钥时必须同时提供 `-signing-key` 和 `-key-id`：

```bash
./build.sh \
  -signing-key /安全目录/my-publisher.private \
  -key-id my-publisher-v1 \
  -output dist/my-openai-plugin.s2plugin
```

签名覆盖最终 `manifest.json` 的精确字节；清单中的文件哈希再覆盖运行时和 UI 文件。签名完成后不要重新格式化 `manifest.json`。

部署者在 Sub2API 配置文件中追加公钥：

```yaml
plugins:
  allow_unsigned: false
  trusted_publishers:
    my-publisher-v1: "BASE64_ED25519_PUBLIC_KEY"
```

`trusted_publishers` 是在宿主内置公钥之外追加的信任来源，不能覆盖内置公钥。当前 fork 还内置了仅绑定 `baiyu.codex-state` 的 `baiyu-codex-state-v1` 公钥；它不信任其他插件。`signature.json` 中的 `key_id` 必须与配置键或内置 key ID 完全一致。密钥轮换时先发布包含新公钥的宿主配置或版本，再发布新签名包，最后再停用旧密钥。

开发阶段如需使用未签名包，只应在隔离的本地环境临时设置 `plugins.allow_unsigned: true`，测试完成后立即恢复为 `false`。

## 8. 构建、测试和安装

在插件目录执行：

```bash
go test ./... -count=1
node --check ui/assets/bridge-v1.js
node --check ui/assets/app.js
./build.sh
unzip -t dist/*.s2plugin
```

在 Sub2API 的 `backend` Go 模块目录中，再使用真实构建包运行宿主集成测试：

```bash
cd backend
SUB2API_TEST_PLUGIN_PACKAGE=/absolute/path/my-openai-plugin.s2plugin \
  go test ./internal/service -run '^TestPluginRuntimeIntegration$' -count=1
```

最低测试集应覆盖配置默认值和边界值、插件身份、请求和响应分块、流式响应、上下文取消、插件退出、代理开关、包哈希、签名、路径安全、目标平台运行时以及 UI Bridge 的加载、保存、测试、错误和超时。

新设施还需覆盖旧 API 1 协商、缺失 features、Scoped 保存和激活失败、准入版本不一致、资源脱敏、动作幂等与取消、CAS/tombstone、跨插件隔离和 lease 过期后的旧 fence 写入。

首次安装使用 `POST /api/v1/admin/plugins/upload`，multipart 字段固定为 `plugin`。JWT 管理会话需要 step-up；自动发布器可使用已认证的管理员 API Key，但该例外只覆盖插件包 upload/upgrade/delete 生命周期，不覆盖启停、配置、秘密、动作或测试。调用前必须先通过插件列表或详情确认同一插件 ID 不存在；当前宿主对部分 disabled、error 或 incompatible 安装仍可能接受同 ID upload 替换，但该路径没有在线升级的维护 journal、在途排空和回滚合同，禁止把它当作升级入口。上传成功只表示包完成签名、清单、文件哈希、路径、大小和宿主兼容性校验；新安装保持停用，不能把上传成功写成已经启用或开始执行业务任务。

插件包原件和安装元数据以 PostgreSQL 为跨实例权威状态，本实例将运行文件恢复到 `plugins.data_dir`；该配置留空时使用 `${DATA_DIR}/plugins`，`DATA_DIR` 未设置时使用 `./data/plugins`。其他实例在需要启动插件时从数据库读取原包、重新执行完整包校验并恢复自己的本地运行目录，因此不要求操作者手工向每台实例复制 `.s2plugin`。数据库中存在包也不等于全部实例已经成功恢复；多实例发布必须逐实例核验恢复、进程健康、版本和二进制 SHA。

安装后先保持停用，确认清单兼容性、签名、九项必需 host feature 和诊断结果。保存配置、写入秘密、启用插件及真实外部动作分别属于独立管理操作；安装授权不能隐含授权启用或采集。旧插件继续按账号灰度；Scoped 插件按已提交的账号/模型范围启用，受管请求不可用时失败关闭。API Key 账号仍使用原有路径。

管理员 JWT 会话可经 step-up 使用 `POST /api/v1/admin/plugins/:id/upgrade`；自动发布器也可使用管理员 API Key 调用该包生命周期接口。升级保留配置、能力绑定和持久状态身份，维护期间阻止受管新请求并等待在途请求完成；失败时恢复或保持明确不可用状态。旧非 Scoped 插件仍需显式停用后安装。

管理扩展通过 `GET /api/v1/admin/plugins/:id/resources` 提供只读脱敏目录，通过
`POST /api/v1/admin/plugins/:id/actions` 执行带幂等 ID 的显式操作。声明 `config_secrets`
的包只允许宿主可信表单调用 `PUT /api/v1/admin/plugins/:id/config/secrets` 修改秘密；
普通配置 Bridge 返回空值和 configured 布尔标志，不能把代理凭据传入 iframe。

插件业务代码、插件 UI 或私有状态机在现有 Host API 能力范围内变化时，可以只发布新的签名插件包，不需要重建宿主镜像或修改宿主版本。若变更涉及 Host API、必需 feature、管理路由、宿主前端壳、数据库 migration、运行目录或包校验逻辑，则属于宿主发布，必须先完成对应的应用 VM Gate 和正式发布门禁。

## 9. 发布前检查清单

- 插件版本与 `GetInfo` 返回值一致；
- `requires.sub2api` 覆盖范围经过验证，没有未经测试的破坏性版本；
- `tested_sub2api_versions` 与实际测试记录一致；
- 每个支持的平台和架构都有运行时文件；
- 生产包存在有效 `signature.json`，公钥已交付部署者；
- 包中没有私钥、源映射、测试数据、日志和临时文件；
- UI 不依赖外部资源，也不保存宿主会话信息；
- 配置切换、请求取消、响应关闭和错误重试语义经过测试；
- 发布说明包含升级、停用、回滚和兼容版本信息。

## 10. 常见问题

| 现象 | 排查方向 |
| --- | --- |
| 安装提示签名不受信任 | 检查 `signature.json.key_id`、Base64 公钥和配置键是否完全一致。 |
| 插件显示不兼容 | 检查版本范围、三个 v1 协议版本、`host_service_api` 和所有必需 `host_features`。 |
| 插件进程无法启动 | 检查目标系统和架构对应的运行时路径、可执行权限和运行用户权限。 |
| 配置页无法加载 | 检查 `ui.entrypoint`、UI 文件哈希、Bridge Token 校验和 iframe 消息来源。 |
| 保存后配置未生效 | 查看 `ValidateConfig`、`ApplyConfig` 返回的规范化配置和诊断信息。 |
| 请求失败后重复执行 | 检查 `ForwardResponseError.request_sent` 是否准确反映请求是否可能已发出。 |
| 受管请求暂时不可用 | 检查当前配置代次、AdmitBatch 决策、身份版本及维护状态。 |
| 动作超时 | 使用原 action_id 查询或重试；超时不能证明动作未被接受。 |
| 状态写入冲突 | 重新读取版本并核验 lease owner/fence，不能跳过 CAS。 |

## 11. 需要扩展能力时

如果新插件需要支持其他 Provider、其他账号类型或新的消息字段，应先扩展并版本化公开协议，再由宿主增加能力匹配和生命周期处理。不要仅通过清单声明一个宿主尚未实现的能力。这样可以让旧插件继续运行，也能让新宿主明确拒绝不兼容的插件。

Sub2API 后续会持续补充更多 Provider 的插件适配说明，包括能力标识、请求和响应契约、配置字段、UI Bridge 使用方式、版本兼容要求以及测试清单。本文会随着这些能力的落地继续更新，Provider 专属章节会放在本节之后。

## 12. 示例仓库预留

后续计划提供独立的插件示例仓库，用于存放可复用的运行时骨架、UI 组件、打包工具和各 Provider 的最小实现。目前示例仓库尚未准备完成，因此暂不提供地址；正式发布后会在这里补充仓库地址、适用的 Sub2API 版本、示例插件版本和构建说明。
