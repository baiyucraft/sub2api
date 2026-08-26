# channel-monitor-ttft-timeline-correction

## 问题

渠道监控 V1 已保存 `ttft_ms`，但当前流式请求在收到 HTTP 响应头后才启动首字计时，遗漏了连接、TLS、上游排队和响应头等待，首个 SSE 文本紧随响应头到达时还会因毫秒截断保存为 `0ms`。同时批量时间线 SQL 的外层投影漏掉 `ttft_ms`，却仍按六列扫描，查询失败后用户端只能补灰色占位条，无法展示真实的正常、降级和错误颜色。

## 目标

- 将 V1 主动流式探针的 TTFT 统一定义为“请求开始发送到首个非空可见文本”，并保证有首字时保存为正值。
- 修复时间线查询投影与扫描顺序，恢复真实状态、总耗时和 TTFT 数据。
- 在读取/API 展示边界将历史 `ttft_ms <= 0` 归一为空值，不修改既有数据库记录。
- 明确 V1 降级继续依据整轮 `latency_ms`、重试和换号次数，TTFT 只用于展示。

## 非目标

- 不修改已归档的 `channel-monitor-streaming` change。
- 不新增数据库字段、migration、设置项或 API endpoint。
- 不回填、删除或更新现有 `0/NULL` 历史记录。
- 不改变 V2 被动监控、上游账号 TTFT Guard、可信度、调度或真实 provider 请求行为。
- 不部署生产环境，不发送真实模型请求。

## 成功标准

- 响应头前的等待被计入 TTFT；首个非空文本到达后 `ttft_ms >= 1`。
- 空事件、reasoning、心跳和协议事件不触发 TTFT；无可见文本保持空值，首字后断流保留 TTFT但结果仍失败。
- 时间线查询稳定返回 `status`、`latency_ms`、`ttft_ms`、`ping_latency_ms` 和 `checked_at`，前端按状态显示绿、黄、红。
- history、latest、timeline、立即探测和用户/管理视图均不输出非正 TTFT；前端对 `null/0` 显示 `-`。
- V1 降级仍只由整轮总耗时、重试和换号阈值决定，TTFT 不参与可用率或降级判定。
- focused、全量门禁、SpecWiki full/pass review、fork audit 和完整 SHA VM Gate 全部通过；生产保持未修改。

## 影响范围

- `backend/internal/service/channel_monitor_checker.go`：请求起点与 SSE 首字计时。
- `backend/internal/repository/channel_monitor_repo.go`：时间线 SQL 投影、扫描和历史 TTFT 归一化。
- `backend/internal/handler/admin/channel_monitor_handler.go`、`backend/internal/handler/channel_monitor_user_handler.go`、DTO/service 聚合：API 边界归一化。
- `frontend/src/components/admin/monitor`、`frontend/src/components/user/monitor`：非正 TTFT 防御展示与状态颜色回归。
- `.wiki/03-模块指南/05-分叉扩展与兼容性.md` 与 fork extension catalog：长期合同和必测项。

## 交付形态

single-change

本 change 是对已归档流式化能力的独立正确性修复，可在不改变 schema、V2 和生产配置的前提下单独 review、归档与回滚。

## 风险

- 计时起点传递遗漏某个 provider 分支会形成口径不一致。
- 只修 SQL 不修读取归一化，会继续向 API 暴露历史 `0ms`。
- 错把 TTFT 接入降级判定会改变现有状态语义和可用率。
- VM fixture 若依赖真实 provider，会引入外部副作用和不稳定证据。

## 参考资料

- 来源：已归档 `channel-monitor-streaming` change 与当前流式解析实现
  - 目标落点：V1 TTFT 计时和兼容边界
  - 采用方式：rewrite
- 来源：当前 `ListRecentHistoryForMonitors` SQL 与用户时间线组件
  - 目标落点：时间线数据链路和状态颜色
  - 采用方式：direct migration
- 来源：CodeGraph 当前调用/影响分析
  - 目标落点：service、repository、handler、frontend 和测试范围
  - 采用方式：inspiration only
