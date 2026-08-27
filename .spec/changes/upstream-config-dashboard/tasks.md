# 任务

- [x] 创建并校验 change 元数据（`spec-wiki-lite validate upstream-config-dashboard --strict --json`）。
- [x] 增加上游配置看板聚合 DTO、仓储查询和管理 API。
- [x] 增加看板前端页面、详情抽屉和 API 类型。
- [x] 将渠道/账号迁移到新路由并更新导航、App 路由 key 与 i18n。
- [x] 补充 focused 测试、长期 Wiki 和 fork audit 记录。
- [x] 执行局部验证；本阶段不部署生产和 VM Gate。

## implementation 证据

- `go test ./... -run 'TestNonexistent' -count=0`（backend）通过，覆盖新增 handler/repository/service 编译。
- `pnpm exec vitest run src/components/layout/__tests__/AppSidebar.spec.ts src/views/admin/__tests__/UpstreamDashboardView.spec.ts src/router/__tests__/upstreamRoutes.spec.ts` 通过。
- `pnpm typecheck` 通过。
- `pnpm lint:check` 通过。
- `git diff --check` 通过。
