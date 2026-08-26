# channel-monitor-ttft-timeline-correction TDD 单元测试

## UT-01 响应头前等待计入 TTFT

- Test：`backend/internal/service/channel_monitor_checker_body_test.go`
- Modify：`backend/internal/service/channel_monitor_checker.go`
- 映射：ST-01 / TTFT 请求起点成功标准
- Red：fixture 在响应头前延迟，旧实现于 `Do` 返回后才计时，结果远小于延迟或为 0。
- Green：在 `Do` 前记录起点并传给解析器；有首字最小保存 1ms。
- Refactor：保持所有 provider 共用 setter，不增加 OpenAI 特判。

## UT-02 空事件与首字后断流

- Test：`backend/internal/service/channel_monitor_checker_body_test.go`
- Modify：`backend/internal/service/channel_monitor_checker.go`
- 输入：空/协议事件、首个可见文本、无终端事件的断流。
- 断言：空事件不设置 TTFT；断流返回错误但 TTFT 为正。

## UT-03 时间线 SQL 六列契约

- Test：`backend/internal/repository/channel_monitor_repo_test.go` 或同包专项测试
- Modify：`backend/internal/repository/channel_monitor_repo.go`
- 映射：ST-04 / 时间线恢复成功标准
- Red：断言最终投影包含 `ttft_ms` 且六列与 Scan 同序；旧查询缺列而失败。
- Green：补齐 `ttft_ms` 投影，保持 CTE 与 Scan 一致。
- Refactor：若需抽取 SQL 常量，仅限测试稳定性，不改变 repository interface。

## UT-04 TTFT 正值归一化

- Test：service types、repository 和 admin/user handler 相关测试
- Modify：`channel_monitor_types.go`、repository/handler mapper
- 映射：ST-03 / API 兼容边界
- Red：`0` 或负数当前会被映射成非空指针并输出 JSON 数字。
- Green：统一 helper 让非正值返回 nil，正值保留。
- Refactor：repository 与两个 handler 复用同一语义，避免各自漂移。

## UT-05 前端颜色与非正值展示

- Test：`frontend/src/components/user/__tests__`、`frontend/src/components/admin/monitor/__tests__`
- Modify：`MonitorTimeline.vue`、监控格式化/展示组件
- 映射：ST-03、ST-04
- Red：时间线三状态/非正 TTFT 缺少断言，0 可能显示为 `0ms`。
- Green：三状态分别映射绿/黄/红，`null/0` 显示 `-`，总耗时保持显示。
- Refactor：集中使用现有格式化 composable，不重复拼接业务规则。

## UT-06 降级口径回归

- Test：`backend/internal/service/channel_monitor_retry_test.go`
- Modify：无或仅测试 fixture
- 映射：ST-05 / 状态语义边界
- 输入：大 TTFT+小总耗时；空/小 TTFT+达到阈值的总耗时。
- 断言：只按 `latency_ms`、retry、switch 判定，不读取 TTFT。

## 覆盖边界

- 不调用真实 provider、生产数据库或生产 API。
- 不测试 V2 内部评分细节，只运行既有回归确认未改变。
- 不通过修改历史行验证归一化；只验证读取和响应边界。
