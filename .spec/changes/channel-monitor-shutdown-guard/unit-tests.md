# channel-monitor-shutdown-guard 单元测试

implementation-mode: tdd

- **UT-001 RunnerQuiesce**（ST-001）：Red 验证 quiesce 后仍可能提交任务；Green 增加 quiescing 标志、取消任务和非阻塞入口；Refactor 验证 Stop 可重复调用并完成 drain。
- **UT-002 StartupProbeDelay**（ST-002）：Red 验证启动加载任务立即调用 RunCheck；Green 仅对 StartChecked 加 5 秒首轮延迟；Refactor 保证 CRUD Schedule 仍立即触发。
- **UT-003 CancelledPersistence**（ST-001）：Red 验证取消 scheduled probe 会插入错误历史；Green/Refactor 保持 runner task context 取消时跳过 InsertHistoryBatch 与 MarkChecked。
- **UT-004 V1SilentRefreshFailure**（ST-003）：Red 验证自动刷新失败触发红色错误提示或清空状态；Green/Refactor 保留 items，自动刷新静默，手动刷新提示错误。
- **UT-005 RealProbeError**（ST-004）：验证非停机的真实网络/HTTP 错误仍保留原有红色状态和历史。
