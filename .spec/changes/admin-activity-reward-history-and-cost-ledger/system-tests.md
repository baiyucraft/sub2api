# 用户领奖与成本系统测试

## 测试环境

- runtime/platform：Windows Go/Vitest 单元门禁，VM Linux/PostgreSQL 集成。
- fixture/data：合成用户、兑换、返利、四类奖励和人工成本；禁止复制生产数据。
- 外部依赖：单元使用 mock，数据库集成使用 VM 本地隔离资源。

## ST-01 混合历史与合计

- 类型：normal
- 操作：查询全部、活动筛选、不同页；切换用户和筛选。
- 断言：四类领奖可见、合计准确且不随分页筛选变化、充值口径不变；同时间/同 ID 无重复丢失。
- 证据：Go history tests、Vitest user history tests。

## ST-02 发奖成本事务

- 类型：failure
- 操作：四类领奖及批量抽奖，注入成本 SQL 失败、重复请求和余额更新失败。
- 断言：成功时一奖励一成本；失败时奖励、余额、机会和成本全部回滚；只有成功提交通知缓存。
- 证据：daily activity cost unit/integration tests。

## ST-03 历史补录与日界线

- 类型：boundary
- 操作：跨北京时间午夜的历史奖励、零金额、非 credited，重复执行迁移/补录，模拟旧实例尾部奖励。
- 断言：原领奖时间/金额保留、按上海自然日归属、补录幂等、既有人工行完全保持。
- 证据：VM PostgreSQL 合成 fixture 及逐日对账。

## ST-04 自动成本追溯和保护

- 类型：normal/failure
- 操作：活动类别筛选、查看用户/奖励关联；尝试人工创建或冲正自动记录。
- 断言：自动记录不可伪造或冲正，今日/累计/趋势更新；人工成本原流程通过。
- 证据：extra cost Go/Vitest tests、dashboard existing tests。

## 成功标准映射

| 成功标准 | ST | 证据 |
| --- | --- | --- |
| SC-01 | ST-01 | history Go/Vitest |
| SC-02 | ST-02 | transaction tests |
| SC-03 | ST-03 | migration/VM |
| SC-04 | ST-04 | ledger/aggregation tests |
| SC-05 | ST-01–04 | aggregate checks/test report |
