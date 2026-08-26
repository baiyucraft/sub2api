# channel-monitor-ttft-timeline-correction 系统测试

## 测试环境

- runtime/platform：Windows 本地 Go/Node 工具链；Linux VM profile 242、`sub2api-dev:8211`
- fixture/data：本地 `httptest` SSE server、sqlmock/隔离 PostgreSQL 测试数据、Vitest 组件 fixture
- 外部依赖：本地 PostgreSQL/Redis 或 mock；不访问真实 provider，不使用生产数据

## ST-01 TTFT 包含响应头前等待

- 类型：normal
- 前置：SSE fixture 在响应头前等待，再立即发送首个可见文本与完整终端事件。
- 操作：执行渠道监控 V1 流式探针。
- 断言：TTFT 包含等待时间、`>=1ms` 且不大于完整耗时；结果正常完成。
- 证据：focused Go test 与 VM 隔离 fixture 输出。

## ST-02 空事件与首字后断流

- 类型：failure
- 前置：fixture 先发送空 delta/协议事件，再发送可见文本并在终端事件前断流。
- 操作：执行流式探针。
- 断言：空事件不启动 TTFT；首字后断流保留正 TTFT，但结果为错误/失败。
- 证据：checker SSE focused Go test。

## ST-03 历史非正 TTFT 归一化

- 类型：boundary
- 前置：history/latest/timeline/立即探测 fixture 分别提供 `nil`、`0`、负数和正数 TTFT。
- 操作：调用 repository mapper、admin/user response mapper 与前端格式化。
- 断言：非正值统一为 `null`/`-`，正值原样展示；不修改数据库历史行。
- 证据：Go mapper/repository tests、Vitest。

## ST-04 时间线状态与列顺序

- 类型：normal
- 前置：同一监控存在 `operational`、`degraded`、`error` 三条历史，并含总耗时、TTFT、ping 和时间。
- 操作：批量读取时间线并渲染用户组件。
- 断言：SQL 投影和 Scan 严格按六列同序；API 返回三条真实记录；页面显示绿、黄、红而非全灰。
- 证据：repository SQL test、MonitorTimeline Vitest、VM 隔离测试数据。

## ST-05 TTFT 与 V1 降级解耦

- 类型：boundary
- 前置：一组结果 TTFT 大但整轮总耗时未达阈值；另一组 TTFT 空/小但整轮总耗时达到阈值。
- 操作：执行最终状态判定。
- 断言：前者仍为 `operational`，后者为 `degraded`；重试和换号规则保持不变。
- 证据：channel monitor retry/policy Go tests。

## ST-06 非流式 replace 与 V2 回归

- 类型：regression
- 前置：replace body 显式 `stream=false`；V2 使用被动聚合 fixture。
- 操作：运行相关 service tests。
- 断言：非流式 replace 的 TTFT 为空；V2 状态、阈值和聚合行为不变。
- 证据：focused Go tests 与全量门禁。

## ST-07 VM Gate 隔离验收

- 类型：boundary
- 前置：完整 40 位 commit SHA 构建唯一 candidate，VM 使用本地 PostgreSQL、Redis、`data-dev` 和 `sub2api-dev:8211`。
- 操作：运行 profile 242 VM Gate、隔离 SSE fixture 与测试数据页面/API smoke。
- 断言：Gate 身份和健康通过；TTFT 为正、时间线状态/耗时字段正确、总耗时阈值仍按 `latency_ms` 生效；真实 provider 流式能力记录为 `not_checked`。
- 证据：签名 Gate、candidate image ID、结构化 VM 验证输出。

## 成功标准映射

| 成功标准 | ST | 证据 |
| --- | --- | --- |
| TTFT 从请求开始计时且有首字为正 | ST-01、ST-02 | Go SSE fixture、VM fixture |
| 时间线恢复真实状态和颜色 | ST-04 | repository test、Vitest、VM smoke |
| 非正 TTFT 统一为空 | ST-03 | Go mapper/repository tests、Vitest |
| 降级继续按总耗时/重试/换号 | ST-05 | policy/retry tests |
| 无 schema/V2/replace 回归 | ST-06 | focused 与全量 tests |
| 完整门禁与生产未修改 | ST-07 | review reports、fork audit、VM Gate |
