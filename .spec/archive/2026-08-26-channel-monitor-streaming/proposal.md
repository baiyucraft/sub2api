# channel-monitor-streaming

## 问题

渠道监控 V1 的主动探测仍整包读取响应，无法得到首字耗时，也无法可靠区分 HTTP 200 后的流中断。上游账号探针已有部分流式实现，但 Grok 和 Antigravity 主动测试路径仍存在整包读取，导致两类探针的流式语义不一致。

## 目标

- 渠道监控 V1 与上游账号主动探针统一使用流式请求和增量解析。
- 保存首字 TTFT 与完整响应总耗时，保持现有状态、取消和可信度统计语义。
- 覆盖 OpenAI、Grok、Anthropic、Gemini、Kimi、智谱、DeepSeek 及 Antigravity 主动探针路径。
- V2 继续只聚合被动业务流量，不新增主动模型请求。

## 非目标

- 不把监控页面改成 SSE/WebSocket 实时推送。
- 不改变真实业务流量、V2 聚合、Juice 分类公式或非探针网关流式行为。
- 不把旧历史的总耗时回填为 TTFT。

## 成功标准

- 流式请求能记录首个非空文本增量和终端事件。
- HTTP 200 后流中断、空流、超时和取消按计划分类；调度器取消不落历史。
- 渠道监控历史/API/UI 同时返回 TTFT 与总耗时，旧数据兼容为空。
- Grok 与 Antigravity 主动账号探针不再整包读取，仍进入 TTFT、错误率和可信度反馈。
- V2 和 Juice 统计口径保持不变。

## 影响范围

- `backend/internal/service/channel_monitor_checker.go`：渠道监控流式请求和解析。
- `backend/internal/service/upstream_health_probe_client.go`、`antigravity_gateway_service.go`：账号探针流式路径。
- `backend/ent/schema`、`backend/migrations`、repository/handler：TTFT 数据契约。
- `frontend/src/api`、监控组件：首字/总耗时展示。

## 交付形态

single-change

本 change 可独立验证和回滚，不改变 V2 被动聚合或生产配置。

## 风险

- 不同 provider 的终端事件不同，错误地把普通 JSON 当 SSE 会造成误报。
- 自定义 replace body 可能显式要求非流式，必须保留兼容路径。
- 新增 migration 需要更新 Ent、checksum 和 VM Gate。

## 参考资料

- 来源：现有 `upstream_health_probe_client.go` SSE 解析器
  - 目标落点：共享流式事件语义
  - 采用方式：rewrite
- 来源：`channel_monitor_checker.go`
  - 目标落点：渠道监控 V1 主动探针
  - 采用方式：rewrite
- 来源：`antigravity_gateway_service.go`
  - 目标落点：Antigravity 主动测试
  - 采用方式：rewrite
