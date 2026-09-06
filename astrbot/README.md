# Sub2API AstrBot 官方 QQ / Telegram 插件

这个目录是 AstrBot 插件源码，使用 AstrBot 的统一事件 API，不依赖 OneBot。

## 功能

- QQ 官方机器人和 Telegram 群聊中使用 /status 查询 Sub2API 渠道状态。
- 按配置间隔向 QQ 群、Telegram 群或频道发送状态，默认每 3600 秒一次。
- 预留 QQ 官方机器人入群欢迎监听，当前是否生效取决于适配器是否透传成员加入事件。
- V2 监控快照接口可用时先探测，渠道明细使用 V1 管理监控接口，并自动读取历史数据。
- 状态接口超时、鉴权失败、空数据或服务不可用时返回可读文本，不会泄漏管理 API Key。

## 安装

将整个 astrbot 目录复制到 AstrBot 的插件目录，例如 data/plugins/sub2api_astrbot/，确保 main.py、client.py、renderer.py、metadata.yaml 和 _conf_schema.json 位于同一层。重启 AstrBot 后在插件配置中填写参数。

## 配置重点

- base_url：Sub2API 根地址，例如 http://127.0.0.1:8000，不要填写 /api/v1。
- SUB2API_ADMIN_KEY：Sub2API 管理 API Key，只写入 AstrBot 插件配置。
- qq_status_group_ids：优先填写从 QQ 官方机器人实际消息事件中看到的完整 unified_msg_origin；也支持群 OpenID。
- telegram_status_chat_ids：优先填写完整 unified_msg_origin；也支持 Telegram chat ID。
- status_interval_seconds：默认 3600，最小 300。
- welcome_enabled 和 welcome_group_ids：启用后仅处理 QQ 官方适配器实际透传的成员加入事件。

## unified_msg_origin 示例

QQ 官方群目标通常形如 qq_official:GroupMessage:群OpenID。

Telegram 群目标通常形如 telegram:GroupMessage:-1001234567890。

完整来源字符串比裸 ID 更可靠，尤其是机器人刚重启、还没有缓存会话时。

## 兼容性边界

当前 AstrBot QQ 官方适配器主要透传消息事件，未稳定提供 group_member_add 或 group_increase 事件。若适配器没有透传成员加入事件，欢迎功能会静默跳过，/status 和定时状态仍然可用；本插件不会改用 OneBot。

## 资源评估

插件自身主要是一个轻量 aiohttp 客户端、30 秒状态缓存和一个休眠定时任务。常态内存通常远低于 AstrBot 本体，状态刷新时的 CPU 和网络开销取决于渠道数量；历史请求最多并发 8 个。建议首次部署观察容器内存、请求日志和重启次数。
