# channel-monitor-shutdown-guard 设计方案

## 方案概述

将 runner 生命周期拆为“静默停止提交”和“最终停止并 drain”两个阶段。`Quiesce` 只取消父任务上下文并阻止后续调度，不等待 worker；现有 `Stop` 在 cleanup 阶段继续等待所有 goroutine 和 pond worker。启动加载的存量任务采用 5 秒首探测缓冲，直接 `Schedule` 的 CRUD 路径保持立即执行。

## 接口与数据流

- `ChannelMonitorRunner.Quiesce()`：幂等、非阻塞、仅供应用停机前调用。
- `ChannelMonitorRunner.Stop()`：保持现有最终清理语义，可在 `Quiesce` 后安全调用。
- `Application.PrepareShutdown func()`：由 wire 注入 runner quiesce，`main` 在 `Server.Shutdown` 前调用。
- `ChannelStatusV1View.reload(silent)`：silent 请求失败只保留已有 `items`；手动请求仍走现有错误提示。

## 生命周期

```text
SIGTERM/SIGINT
  -> PrepareShutdown -> runner.Quiesce (取消任务/禁止新提交)
  -> http.Server.Shutdown (停止接入)
  -> app.Cleanup -> runner.Stop (等待并关闭 worker)
```

scheduled probe 的 task context 取消仍由 `persistCheckResultsIfAllowed` 识别；普通请求超时使用 child context，不会被误吞。

## 兼容与回滚

不新增表、字段或 API。回滚应用代码即可恢复旧生命周期；历史数据不回填、不删除。
