# CodeGraph 研究摘要

## 查询

- `Channel monitor TTFT postRawMonitorStream timeline ListTimeline channel_monitor_checker.go channel_monitor_repo.go API normalization frontend timeline status colors affected tests`
- `ListTimeline channel_monitor_repo.go ChannelMonitorHistory TTFTMs Normalize TTFT API admin user timeline MonitorTimeline statusClass MonitorMetricPair run check tests`

## 已确认入口与路径

- `ChannelMonitorService.RunCheck` 经 provider 请求构造进入 `postRawMonitorStream`；该函数调用 `monitorHTTPClient.Do` 后才创建 `started`，再把时间传给四类 SSE 事件解析器。
- `setMonitorTTFT` 仅在首个非空文本写值，但直接使用 `Milliseconds()`，不足 1ms 会得到 0。
- repository 的 `ListRecentHistoryForMonitors` CTE 已选择 `h.ttft_ms`，外层 SELECT 漏列，而 Scan 仍包含 TTFT 目标。
- 用户聚合把 repository timeline 映射到 `UserMonitorTimelinePoint`，再经 user handler 输出；查询失败时聚合层得到空 timeline，`MonitorTimeline.vue` 用灰色占位补齐。
- `MonitorTimeline.vue` 已为 `operational/degraded/failed/error` 定义绿/黄/红颜色，颜色缺失是后端时间线为空，不是前端色表缺失。

## 影响半径

- 后端：流式请求计时、history/latest/timeline 查询映射、admin/user response mapper。
- 前端：管理立即探测/列表、用户卡片/时间线的 TTFT 格式化防御。
- 不触及 V2 聚合、上游账号探针、数据库 schema、真实业务网关。

## 测试线索

- service SSE fixture：响应头前延迟、空事件、首字、断流。
- repository：SQL 投影与六个扫描目标同序；历史 `0/NULL/正数` 映射。
- handler/service mapper：立即探测、latest、timeline 的非正 TTFT 归一化。
- Vitest：时间线状态颜色及 `null/0` 展示，管理端与用户端格式一致。

## 源码核验差异与残余风险

- CodeGraph 未找到 `postRawMonitorStream` 的现成直接覆盖测试，需在既有 checker body 测试中补充 fixture。
- 前端已有状态色映射，但缺少对真实 timeline 三状态和非正 TTFT 的专项断言。
- repository SQL 测试 seam 需优先复用现有 sqlmock/查询测试约定；若当前仓库无 seam，最小抽取稳定 SQL 常量供静态契约测试。
