# channel-monitor-shutdown-guard 系统测试

## ST-001 停机探测静默

- 前置：启用一个 V1 scheduled monitor，并让探测处于在途状态。
- 动作：调用停机前 quiesce，再执行 HTTP server shutdown。
- 断言：不再提交新探测；在途请求收到取消；历史行数量和 `last_checked_at` 不因取消增加。
- 失败关闭：若发现新增 `error/failed` 历史，停止后续验证并保留日志。

## ST-002 启动首探测缓冲

- 前置：ListEnabledMonitors 返回存量启用监控。
- 动作：启动 runner 并观察首个探测时间。
- 断言：5 秒缓冲前无首探测，缓冲后执行；直接 Schedule 的新建/编辑监控仍在 2 秒内执行。

## ST-003 用户端刷新失败

- 前置：V1 页面已加载 operational 卡片。
- 动作：让自动刷新请求失败，再执行手动刷新失败。
- 断言：两种失败均保留卡片和原状态颜色；自动刷新不弹红色错误，手动刷新显示错误提示。

## ST-004 真实错误不被隐藏

- 前置：上游真实返回错误或超时。
- 动作：执行正常 scheduled probe。
- 断言：仍写入原有 `error/failed` 状态并显示红色；quiesce 只影响生命周期取消。
