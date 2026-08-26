# channel-monitor-ttft-timeline-correction 设计方案

## 方案概述

在渠道监控 V1 的共享流式发送函数中，把单一请求起点固定在 `monitorHTTPClient.Do` 之前，并继续把该时间传给 OpenAI Chat/Responses、Anthropic、Gemini 和兼容 SSE 解析器。首个非空可见文本调用统一 TTFT setter，setter 将正数不足 1ms 的值夹到 1ms。repository 使用正值专用解包函数读取 TTFT，时间线查询补齐缺失投影；handler mapper 再做一次非正值防御，覆盖立即探测和任何未经过 repository 的响应。

V1 `latency_ms` 与降级策略不改：service 仍从整轮 `roundStart` 计算完整耗时，并只按总耗时阈值、额外重试和累计换号判定 `degraded`。TTFT 不进入该函数、可用率 SQL或 V2。

## 接口与稳定合同

- `postRawMonitorStream(...)`：对外函数签名不变；内部 `requestStartedAt` 在 `Do` 前创建。
- `setMonitorTTFT(...)`：仅首个非空可见文本生效；结果最小为 `1ms`。
- `NormalizeChannelMonitorTTFT(*int) *int`：`nil` 或 `<=0` 返回 `nil`，正值原样返回，用于 repository/handler 读取边界。
- `ListRecentHistoryForMonitors` SQL 投影固定为：

```text
monitor_id, status, latency_ms, ttft_ms, ping_latency_ms, checked_at
```

- JSON 字段和 endpoint 保持不变；合法 TTFT 仍为可空整数毫秒。
- 前端格式化将任何 `null/undefined/<=0` 显示为 `-`，不把总耗时替代为首字。

## Ownership 与数据/文件流

```text
请求开始时间
  -> monitorHTTPClient.Do + SSE 增量解析
  -> CheckResult.TTFTMs (nil 或 >=1)
  -> channel_monitor_histories.ttft_ms
  -> history/latest/timeline repository 正值归一
  -> admin/user response mapper 防御归一
  -> 管理端与用户端格式化展示

roundStart
  -> 失败请求 + 等待 + 重试 + 最终完成
  -> latency_ms
  -> 总耗时/重试/换号降级策略
```

## 正常流程

1. 请求发送前记录 `requestStartedAt`。
2. HTTP 客户端完成连接、TLS、排队和响应头等待，SSE scanner 开始读取事件。
3. 空 delta、reasoning、心跳和协议事件不调用 TTFT setter。
4. 首个非空可见文本调用 setter，保存 `max(1, elapsed_ms)`；后续文本不覆盖。
5. 完整终端事件到达后以现有逻辑成功/降级并持久化 TTFT 与整轮总耗时。
6. 查询端只向上层返回正 TTFT；时间线状态被完整扫描，前端按 `status` 显示绿、黄、红。

## 失败、边界与回滚

- 无效输入：历史 `ttft_ms <= 0` 视为未知，API 返回 `null`，不报错、不写库。
- 重复/并发：每个请求使用局部起点和局部结果；setter 仅写一次，不共享状态。
- 部分失败：首字后断流保留已记录 TTFT，但沿用现有流中断错误状态；首字前失败保持空 TTFT。
- 用户内容：不更新历史数据库行，不删除时间线，不改变配置。
- path safety：无新路径、文件上传或外部命令输入。
- replace 非流式：显式 `stream=false` 继续走 JSON 路径，TTFT 为空。
- 回滚：回退本 change 代码和文档即可；没有 schema 或数据回滚动作。

## 验证设计

- SSE fixture 在写响应头前延迟，随后立即发送可见文本和终端事件；断言 TTFT 包含延迟且为正。
- SSE fixture 先发送空/协议事件，再发送文本；断言首字不被提前触发。
- 首字后关闭流；断言错误保留 TTFT。
- repository 使用 SQL mock 验证投影/扫描六列同序，并覆盖 TTFT `0/NULL/正数`。
- service/handler mapper 覆盖立即探测、latest、timeline 的非正归一化。
- 现有降级策略测试补充 TTFT 与 `latency_ms` 独立性。
- Vitest 直接挂载时间线和管理/用户显示组件，断言三种颜色及 `null/0` 的 `-`。
- VM 只使用隔离 SSE fixture 与测试数据，不访问真实 provider。

## Wiki 与长期合同落点

- `.wiki/03-模块指南/05-分叉扩展与兼容性.md`：补充请求起点、最小正值、读取归一化、时间线列顺序及 TTFT 不参与 V1 降级。
- `.agents/skills/sub2api-fork-extension-audit/references/extensions.yaml`：更新 `channel-monitor-v2` 描述、invariants 和 required tests；不登记 `.wiki/`、`.spec/` 路径。

## 参考边界

- 来源：当前 `channel-monitor-streaming` 实现和归档合同
- 目标落点：V1 TTFT 和 timeline 数据链路
- 采用方式：rewrite

## 回滚

- 代码回滚恢复原计时与查询行为，但不需要数据库操作。
- VM Gate 失败时保留旧 `sub2api-dev` candidate、PostgreSQL、Redis 和 `data-dev`；不启动生产发布。

## CodeGraph-derived design constraints

- entry points and call paths: `RunCheck -> runCheckForModelWithRetry -> postRawMonitor -> postRawMonitorStream -> parseMonitor*Event -> setMonitorTTFT`；`ListRecentHistoryForMonitors -> UserMonitorView -> user handler -> MonitorTimeline`。
- ownership and dependency boundaries: service 拥有计时与状态语义，repository 拥有 SQL/数据库读取，handler 只做 API 防御归一，frontend 只做展示防御。
- impact radius: channel monitor V1 service/repository/handler/frontend；不进入 V2 或 upstream account probe。
- affected tests: checker body/SSE、retry degradation、repository query/mapping、admin/user handler mapper、MonitorTimeline/管理显示组件。
- rollback boundary: 无 migration/数据写回；按 commit 回滚。
- graph evidence vs source verification: CodeGraph 确认流式解析与 UI 依赖；源码进一步确认外层 SELECT 漏 `ttft_ms` 而 Scan 仍为六目标，以及前端颜色表本身完整。
- unresolved items: repository 是否已有可复用 sqlmock seam 在 Red 阶段确定；若无则仅抽取查询常量，不改变 repository public interface。
