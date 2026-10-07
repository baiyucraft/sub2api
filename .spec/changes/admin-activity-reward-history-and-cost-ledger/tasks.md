# 用户领奖与成本任务计划

implementation-mode: tdd

## 任务总览

按用户流水、奖励成本、展示入口、迁移发布与长期契约拆分。每块记录 Red/Green 命令和结果。

## 1. 管理员流水（UT-01 / ST-01）

- [x] 1.1 Red：补充混合来源/稳定分页/累计金额测试并确认失败。
- [x] 1.2 Green：实现读模型、接口、筛选和独立累计金额。
- [x] 1.3 Refactor：旧类型与合计兼容，完善 handler 测试。

### CheckList

- [x] Red/Green、格式与局部质量检查通过，注释遵守仓库规范。

## 2. 奖励成本事务（UT-02 / ST-02/04）

- [x] 2.1 Red：四类奖励、失败回滚、幂等、保护和缓存测试。
- [x] 2.2 Green：同事务成本 SQL、审计字段及自动记录保护。
- [x] 2.3 Refactor：复用聚合与缓存清理路径。

### CheckList

- [x] focused tests、格式及失败关闭验证通过。

## 3. 前端（UT-03 / ST-01/04）

- [x] 3.1 Red：弹窗合计、领奖行、筛选、快捷入口和自动成本保护。
- [x] 3.2 Green：API 类型、用户/成本弹窗、菜单、双语翻译。
- [x] 3.3 Refactor：请求竞态和用量页 hideActions 回归。

### CheckList

- [x] focused Vitest、typecheck、lint:check、build 通过。

## 4. 迁移与契约（UT-04 / ST-03）

- [x] 4.1 新迁移及补录入口，Red/Green 覆盖幂等/原日期/人工行保护。
- [x] 4.2 登记下一个连续 profile/migration、发布和 fork catalog 及长期 Wiki。
- [x] 4.3 VM 本地隔离 PostgreSQL 集成与逐日对账。

### CheckList

- [x] Go 聚合、release focused/aggregate、diff、strict validate 和 VM 证据完成。

## 用例到任务映射

| 用例 | task | 验证 |
| --- | --- | --- |
| ST-01 | 1/3 | UT-01/03 |
| ST-02 | 2 | UT-02 |
| ST-03 | 4 | UT-04 |
| ST-04 | 2/3 | UT-02/03 |

## 执行顺序

已授权设计 -> strict validate -> TDD Red -> 各块 Green/Refactor -> 串行聚合门禁 -> VM 合成集成 -> review/证据报告。

focused：backend go test -tags unit -p 2 -parallel 2 ./internal/service ./internal/repository ./internal/handler/admin -run 'BalanceHistory|ActivityReward|DailyActivity|ExtraCost'；frontend pnpm exec vitest run 目标测试。

aggregate：backend go test -tags unit -p 2 -parallel 2 ./internal/... ./migrations；frontend pnpm test:run、pnpm typecheck、pnpm lint:check、pnpm build；release pytest；git diff --check；spec-wiki-lite validate 本 change --strict --json。

## 暂缓事项

生产发布和旧生产实例排空后的实际补录需独立授权。用户在验证后另行授权本地 commit；未请求 push/archive，change 保持 verification 并留存真实证据。
