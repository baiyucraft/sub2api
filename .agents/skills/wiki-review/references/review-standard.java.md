# Java Review 标准

与通用标准同时使用，聚焦异常、空值、并发、资源、分层和测试。

## Blocking / P0

- 禁止空 catch、吞异常、无理由捕获 `Throwable` 或宽泛 `Exception`。
- collection 返回和 null/Optional contract 清晰；包装类型比较、集合修改和 view 语义正确。
- 线程池有界、命名并有拒绝策略；共享可变状态同步。
- stream/connection/statement/result/transaction 可靠关闭或回滚。
- Controller/Service/Repository、DTO/Entity/Domain 和事务边界不破坏业务语义。
- 覆盖异常、事务、并发、集合边界和外部依赖 mock。

## Non-blocking / P1/P2

- 日志上下文、方法/类职责、复杂 Stream/Lambda、注入方式或依赖维护风险。

## 工具与误报

- formatter、SpotBugs、PMD、Error Prone、JUnit、coverage 和依赖扫描优先自动化。
- 不因框架注解或 Javadoc 偏好机械阻塞。
