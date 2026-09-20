# Codex STATE 维护说明

## 维护边界

本插件在独立目录构建；宿主只保留通用插件管理能力。不要恢复内置 STATE 管理 API、账号编辑入口或账号用量专用展示，不把普通账号 `extra` / credentials 暴露到插件 UI。新安装所有开关关闭，不从旧内置设置或旧票据自动迁移。

配置的 `enabled` 是布尔总开关，状态中的 `active` / `ready` 是只读对象。配置 schema 版本 `1`、模型名与 Pro / Team 值不能静默改变。变更配置、状态、动作或 host SDK 契约时，先明确兼容策略，再同步 UI DTO、两种语言、测试和 manifest。

## 每次更新之前

无论是手动更新、合并参考实现还是发布插件，必须先检查以下三个项目，不自动跟随分支、不自动覆盖本地实现或锁文件：

| 项目 | 固定源码基线 | 用途 |
| --- | --- | --- |
| `happy-loki/sub2api-codex-turn-state` | `c8e5483e06ab9de199c3848d9d46fccfc063f88a` | 参考实现与行为核对 |
| `wangyunjeff/sub2api-state-kit` | `9f2d20ba7db0f76a558ce2c28eefb20128eae562` | 参考实现与行为核对 |
| `gylive/ccodex-sleep-state` | `b18fabf9ad8e9d7af7d9d0306b623ba6091a39d6` | 仅设计参考；禁止源码 / 依赖引入 |

1. 读取 `sources.lock.json`，保留基线完整 40 位 SHA、仓库与许可证证据。不要把一次查询到的 HEAD 称为永久最新版本。
2. 查询三个项目当次的默认分支 HEAD、releases 和 release 对应 tag / commit，记录检查时间和完整 SHA。release 列表为空也要记录，不用默认分支 HEAD 冒充 release。
3. 在隔离的审阅目录中，逐一比较固定基线到当前 HEAD，以及固定基线到拟采用 release 的 diff。检查源码、LICENSE / NOTICE、依赖、构建脚本和发行流程，不只阅读 release 摘要。
4. 重点审阅采集 / 取消幂等、并发状态、过期和 ready 切换、strikes / cooldown、账号禁用、模型边界、出口隔离与错误脱敏。遇到分叉、强推、无法解析的 tag 或不可用仓库，应记录未完成证据并停止该更新决策，不猜 SHA。
5. 逐项决定接受、拒绝或用独立实现重做。`ccodex-sleep-state` 只记录设计观察，不能复制源码、打包脚本、补丁或引入其模块来替代独立实现。其他参考项目若引入代码，也必须记录逐文件来源、版权和许可，不把本锁文件当作自动授权。
6. 只有维护者明确接受新基线且检查完成后，才修改锁文件、notice 和必要实现。保留旧 / 新完整 SHA、diff 结论和未解决事项；禁止定时任务自动覆盖工作树。

上述为后续每次更新的必做流程，不表示当前已完成未来 HEAD / releases 或任何发布包审计。

## 契约与回归

最低能力为 `plugin_protocol=1`、`transport_api=1`、`ui_bridge=1`、`host_service_api=2`。原七个必需 feature 为 `scoped-routing.v1`、`admission.v1`、`resources.v1`、`actions.v1`、`state-cas.v1`、`leases.v1`、`oauth-like.v1`；当前打包器另外要求 `request-completion.v1` 和 `config-secrets.v1`，共九项。宿主版本字符串不能取代能力协商，旧插件缺省可选能力字段的行为也须回归。

- `GET /api/v1/admin/plugins/:id/resources` 在停止态可读；严格投影目录元数据，不携带账号 extra、凭据、代理用户或密码。
- `POST /api/v1/admin/plugins/:id/actions` 请求 `{action_id,name,payload}`，响应 `{action_id,accepted,status,result}`。`harvest` / `cancel` 使用 `{account_id,model}`；停止态禁止动作，step-up 后仍需重查运行态。
- 持续状态经现有 `plugin.status` 返回。`Health.healthy` 仅表示 RPC 健康，UI 还应检查 `status_json.running`，不要将未启用的引擎当作运行中。
- `status_json` 包含 version、running、enabled、config_revision、accounts、logs。模型状态包含 active / ready 元数据、strikes、attempts、refreshing、cooldown_until 和脱敏诊断码；以 `core/status.go` 为源码契约。
- `queued` 表示待采集且可取消，不能重复派发采集。active / ready 可为 null，不使用票据原文作为 UI 状态。新增诊断码需同步 UI 白名单和中英文文案。
- Bridge 必须检查 iframe source、会话 token、request_id、消息类型。关闭 / 切换 UI 后旧响应不能写入新会话，不能在旧 step-up 返回后执行 mutation。
- iframe 不得增加 `allow-same-origin`、管理员 token 或网络权限；不使用 localStorage 保存代理 URL 或配置。状态、日志、错误文本中不得出现完整 URL、authorization、Cookie、STATE envelope 或指纹。
- manifest 的 `config_secrets` 声明顶层秘密字符串。读取及保存响应必须将其置空，只返回 `_host_secrets` boolean flags；普通保存保留已存秘密并去掉 metadata。不要把掩码、空字符串占位或 iframe 提交值当作修改秘密的指令。
- 秘密输入只在宿主可信弹窗中，插件 UI 没有秘密输入框。无参数 `plugin.secrets.edit` 仅请求打开弹窗；宿主拒绝 iframe 携带 values / config 等参数。成功仅回传 `result.configured`，不得回传保存后的原始 config 或 API 错误文本。
- 宿主弹窗以通用 manifest key 标识字段，默认保留。只有显式替换 / 清空才经 step-up 调用 `PUT /config/secrets`；取消和全部保留不发写请求。关闭会话、iframe 导航或 10 分钟超时须销毁输入，迟到 step-up 不能继续 mutation；普通配置保存与秘密弹窗不得重叠。
- 首次缺少 version 的配置必须由用户显式保存全关闭初始配置，再编辑凭据，不增加自动保存或后端隐式默认化。秘密操作后的 flags 刷新不能覆盖模型 / 总开关草稿；不确定保存结果要重新读 flags，失败时禁止依赖未知凭据状态发起采集。
- 加载、筛选、切换语言和轮询无写入副作用。保存进行时继续编辑的草稿不得被迟到响应覆盖；不确定动作重试复用 action_id。

## 本地验证

宿主在 `frontend/` 中顺序执行，避免与其他 agent 的重型检查争用资源：

```sh
pnpm exec vitest run
pnpm exec vue-tsc -b
pnpm run lint:check
pnpm run build --outDir .tmp/codex-state-host-dist
```

显式 outDir 避免验证构建改写默认的 `backend/internal/web/dist`。不要用带 `--fix` 的 lint 顺手改无关文件。记录已有失败和警告，不把未检查的路径写成通过。

在 `plugins/codex-state/ui/` 中执行：

```sh
pnpm install --frozen-lockfile
pnpm build
pnpm test
```

还需在真实 `sandbox="allow-scripts"` 且 `connect-src 'none'` 的本地测试宿主中检查桌面与移动布局、初始关闭、筛选、草稿、保存、取消及敏感错误投影。凭据回归必须断言 config.load / config.save / plugin.secrets.edit 的全部 iframe 响应、DOM 和草稿都没有秘密原文，同时验证普通保存留密、明确清空、无值回显、旧插件兼容及首次保存。测试使用合成目录 / 状态，不连接生产、真实账号或上游采集服务。Go 测试由运行时负责人单独安排；上述前端命令不代替 Go 回归。

## 打包与升级

1. 先完成 UI 构建，检查 `ui/dist/index.html` 和引用的 JS / CSS 均存在，不含 symlink、源码映射秘密或配置草稿。
2. 确认插件根目录的 `LICENSE`、`THIRD_PARTY_NOTICES.md`、`sources.lock.json`、`MAINTENANCE.md` 已更新；工具会把它们放入 `.s2plugin`，缺失则失败。
3. 从插件根运行 `go run ./tools/package --out dist`；需要签名时追加 `--signing-key /secure/path/publisher.pkcs8.pem --key-id publisher-id`。密钥为 Ed25519 PKCS8 PEM，必须位于源码和输出目录之外。默认构建 Linux amd64 / arm64，可通过 `--arches` 限定。
4. 对实际产物单独检查 manifest、逐文件 SHA256、签名与受信公钥、架构、能力要求、依赖许可和对应源码。检查私钥、账号凭据、代理 URL、真实 STATE 与调试文件未进入包。不要把 `sources.lock.json` 的源码基线结论当成此步骤已完成。
5. 保留前一已验证包和对应配置 / 状态的恢复方案，通过通用 `POST /api/v1/admin/plugins/:id/upgrade` 上传字段 `plugin`。不要在升级前主动停用来绕过升级流程；由宿主负责升级事务、运行态保留与失败处理。
6. 发布、部署、生产操作及回滚必须遵守仓库相应门禁和单独授权。测试、生成包或签名本身不构成部署授权。对未签名本地包，不以关闭生产签名校验作为安装办法。

## 许可证与证据范围

两个参考实现的固定基线 LICENSE 为 GNU LGPL v3；ccodex 的固定基线 LICENSE 为 GNU GPL v3，仅设计参考。LICENSE 标题不单独证明上游授权是 only 还是 or-later，本记录不补写该结论。第三方依赖沿用各自许可。

本目录 LICENSE 包含 LGPL v3 和其引用的 GPL v3 文本。分发前必须核对实际纳入的代码、依赖和对应源码 / 构建信息，并保留适用版权和许可声明。当前 notice 不是发行 SBOM，也不是对任一上游二进制、安装包或本插件发布包的审计证明。
