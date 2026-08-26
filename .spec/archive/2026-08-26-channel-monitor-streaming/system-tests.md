# channel-monitor-streaming 系统测试

## 测试环境

- runtime/platform：Go、PostgreSQL、Vue/Vitest；provider 请求使用 httptest/mock。
- fixture/data：SSE 首字、终端、空流、断流和旧历史 NULL TTFT。
- 外部依赖：无真实模型请求。

## ST-01 正常流式探针

- 类型：normal
- 前置：provider 返回合法 SSE delta 与终端事件。
- 操作：执行渠道监控和上游账号探针。
- 断言：请求包含 `stream=true`；TTFT、总耗时、文本和状态正确。
- 证据：Go focused tests。

## ST-02 HTTP 200 后流中断

- 类型：failure
- 前置：先发送非空 delta，随后关闭连接且无终端事件。
- 操作：执行探针。
- 断言：保留 TTFT，状态为失败/错误；账号探针不计成功，渠道监控写入可观测失败历史。
- 证据：Go service tests。

## ST-03 调度取消

- 类型：boundary
- 前置：runner 已提交请求。
- 操作：Stop/Unschedule 取消 context。
- 断言：HTTP 请求取消；渠道监控不插入历史、不更新 last_checked_at。
- 证据：runner/service tests。

## ST-04 兼容与 UI

- 类型：normal
- 前置：旧历史 `ttft_ms=NULL` 与新历史有值。
- 操作：查询 API 并打开管理员/用户监控页面。
- 断言：旧数据显示 `-`，新数据显示首字和总耗时；可用率不变。
- 证据：repository/handler/Vitest。

## 成功标准映射

| 成功标准 | ST | 证据 |
| --- | --- | --- |
| 流式首字与终端 | ST-01 | focused tests |
| 中断与取消语义 | ST-02/ST-03 | service tests |
| TTFT API/UI 兼容 | ST-04 | repository/Vitest |
