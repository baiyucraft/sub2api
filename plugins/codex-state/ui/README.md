# Codex STATE UI

独立 Vue/Vite 包，不导入宿主内部组件，不访问管理 API、Cookie 或存储。

```sh
pnpm install --frozen-lockfile
pnpm test
pnpm build
```

将 `dist/index.html`、`dist/app.js`、`dist/style.css` 打入插件包的 `ui/`，并在插件 manifest.files 中声明哈希，入口为 `ui/index.html`。产物使用经典 IIFE 脚本，支持宿主 `sandbox="allow-scripts"` 的不透明来源；网络权限由宿主 CSP 禁止。

Bridge 使用 `config.load`、`config.save`、`plugin.resources`、`plugin.status`、`plugin.action` 和 `plugin.secrets.edit`。资源响应字段为 `resources`，配置响应为 `config`，状态和动作响应为 `result`。动作请求携带 `action_id/name/payload`；不确定失败后的重试复用 action_id。

加载与轮询仅请求只读接口。编辑草稿保留在当前页面内存中，刷新状态、切换语言与分组不会覆盖；不写入浏览器持久存储。保存才发送配置，采集/取消分别需要显式点击。首次读取无 version 的配置时，必须显式保存 version=1、enabled=false 的初始配置后才能编辑凭据，不自动保存。

代理 URL 完整字符串由 manifest 的 `config_secrets` 声明为秘密字段，插件 iframe 不再提供密码输入或回显。`config.load` / `config.save` 的两个 URL 字段始终为空，`_host_secrets` 仅携带 boolean configured 元数据；该元数据不进入运行时配置或普通保存载荷。采集可用性检查使用 configured 状态，而不是空 URL。

`plugin.secrets.edit` 不带业务参数，只请求打开宿主可信弹窗；宿主用通用 manifest key 作为字段标签，提供保留 / 替换 / 清空，输入默认空且仅存在宿主 DOM。秘密只由宿主通过 `PUT /api/v1/admin/plugins/:id/config/secrets` 写入。Bridge 成功返回 `result: {configured: {harvest_proxy_url: boolean, dial_proxy_url: boolean}}`，不返回 URL；取消返回 `ok:false, code:'cancelled'`。交互超时为 10 分钟，普通请求仍为 30 秒。保存失败或取消后只重新读取安全 flags，保留普通草稿。

状态与日志严格投影已知诊断码，未知错误统一隐藏。测试覆盖恶意回传原文、保存时剔除秘密、秘密操作与普通保存隔离、首次保存、取消和会话失效；测试通过状态以实际验证记录为准。

状态契约对应 `../core/status.go`：`status_json.running`、`accounts[].models`、`active/ready` 元数据、`cooldown_until` 及 `logs[].code`。诊断码新增时需同步 `src/contracts.ts` 与两种语言字典。
