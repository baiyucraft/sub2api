# Python Review 标准

与通用标准同时使用，聚焦异常、类型、async、路径、资源、安全和测试。

## Blocking / P0

- 禁止裸 `except:`、空 pass 或过宽 try 吞掉关键失败；保留异常上下文。
- 外部输入、配置、环境、文件和序列化数据需要运行时校验，公共契约有类型说明。
- async 不直接执行阻塞 IO/`time.sleep`；正确处理 cancel、timeout、lock 和共享状态。
- 文件、连接、锁、临时文件用 context manager；路径使用结构化 API 并防逃逸。
- 避免可变默认参数、遍历时修改集合、不可信 pickle 和敏感信息日志。
- pytest 覆盖异常、边界、async、资源释放和外部依赖 mock。

## Non-blocking / P1/P2

- 过深嵌套、复杂推导式、隐式 API、日志上下文或依赖锁定风险。

## 工具与误报

- ruff/formatter、mypy/pyright、pytest、coverage、安全与依赖扫描优先自动化。
- 不因局部 `Any` 或未使用某个特定模型库机械阻塞。
