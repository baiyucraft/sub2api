# Go Review 标准

与通用标准同时使用，聚焦 Go 错误、context、并发、资源和测试。

## Blocking / P0

- 关键 error 不得丢弃，必须保留上下文和可判断语义。
- 跨 IO/RPC/DB/长任务正确传递 `context.Context`，cancel/timeout 后释放资源。
- goroutine 有 wait/cancel/ownership；共享 map/slice/struct 有同步；channel 关闭责任明确。
- 文件、body、rows/tx、锁和临时资源在所有路径关闭/回滚。
- 覆盖 error、cancel、并发、资源释放和外部依赖 mock。

## Non-blocking / P1/P2

- 过大接口、泛化 any、包边界模糊、明显分配/拼接浪费或日志上下文不足。

## 工具与误报

- gofmt、go vet、staticcheck、race、coverage、依赖扫描优先自动化。
- 不机械要求所有依赖抽象接口，不因小切片未预分配阻塞。
