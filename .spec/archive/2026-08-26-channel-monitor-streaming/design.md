# channel-monitor-streaming 设计方案

## 方案概述

以 provider-specific SSE 事件处理器为边界，把“请求发送、首字、终端事件、流中断、完整文本”统一纳入探针结果。渠道监控保留 `latency_ms` 为完整响应耗时，新增可空 `ttft_ms`；上游账号沿用既有 `ttft_ms`/`duration_ms` 字段。

## 接口与稳定合同

- `channel_monitor_histories.ttft_ms BIGINT NULL`。
- `CheckResult`、历史 row/entry、latest/timeline 和 admin/user JSON 增加 `ttft_ms`。
- `stream=true` 是默认主动探针协议；replace body 明确 `stream=false` 时走 JSON 兼容解析。
- 首字仅由首个非空文本增量触发。

## Ownership 与数据/文件流

```text
provider request -> SSE parser -> CheckResult/UpstreamHealthProbeResult
  -> history/observation repository -> admin/user API -> monitor UI
```

## 正常流程

1. 构造流式请求并设置 `Accept: text/event-stream`。
2. 解析 provider 事件，首个有效文本记录 TTFT。
3. 收到终端事件后校验 challenge/Juice/完整性并记录总耗时。
4. 请求失败或调度取消按既有状态机处理。

## 失败、边界与回滚

- HTTP 非 2xx：读取受限错误体，保留原错误分类。
- HTTP 2xx 无终端事件或流中断：记录 error/failed，已产生 TTFT 则保留。
- 调度器取消：沿用 runner context，不插入历史或更新 `last_checked_at`。
- replace + `stream=false`：保留现有 JSON 解析。
- migration 幂等；代码回滚不回填旧 TTFT。

## 验证设计

- checker 单测断言请求体、SSE 首字、终端、空流和中断。
- upstream probe 单测断言 Grok/Antigravity 流式请求和 TTFT。
- repository/handler/UI 单测断言 `ttft_ms` 可空透传。
- Go、Vitest、typecheck、build、diff-check 和 VM Gate。

## Wiki 与长期合同落点

- `.wiki/03-模块指南/05-分叉扩展与兼容性.md`：更新健康探针条目，说明渠道监控 V1 TTFT 与流式终端语义。
- `extensions.yaml`：登记迁移和渠道监控流式扩展。

## 参考边界

- 上游健康探针 SSE：rewrite/复用已有 parser。
- 渠道监控 checker：rewrite。

## 回滚

- 应用回滚到旧 image 时保持新 nullable 列；旧代码忽略该列。
- 不删除 migration 或历史 TTFT 数据。

## CodeGraph-derived design constraints

- 入口：`runCheckForModel -> callProvider -> postRawJSON`；`RunUpstreamHealthProbe -> executeUpstreamHealthProbe`。
- 数据落点：`persistCheckResults -> InsertHistoryBatch`；`RecordCheckResult -> UpstreamHealthObservation`。
- 影响范围：checker、upstream probe client、Antigravity test path、Ent/repository/handlers/frontend monitor components。
- 图证据与源码核验一致；V2 只读聚合路径不进入主动请求改造。
