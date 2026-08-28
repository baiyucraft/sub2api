# 任务

- [x] 创建并校验 change 元数据（`spec-wiki-lite validate upstream-config-dashboard --strict --json`）。
- [x] 增加上游配置看板聚合 DTO、仓储查询和管理 API。
- [x] 增加看板前端页面、详情抽屉和 API 类型。
- [x] 将渠道/账号迁移到新路由并更新导航、App 路由 key 与 i18n。
- [x] 补充 focused 测试、长期 Wiki 和 fork audit 记录。
- [x] 修复详情查询、空 request_id 去重、使用记录/渠道/账号筛选和前端请求竞态。
- [x] 执行局部验证；本阶段不部署生产和 VM Gate。
- [x] 增强看板筛选交互：时间窗口改为五档分段按钮，Provider/状态改用自定义 Select。
- [x] 增加余额快照、余额新鲜度、未解决事件和最近倍率变更聚合，详情按配置返回明细。
- [x] 增加关注优先排序、运营信号卡片、详情运营区块及中英文文案。
- [x] 完成本轮增强的 focused 测试、类型检查、Lint 和差异检查。

## implementation 证据

- `go test ./... -run 'TestNonexistent' -count=0`（backend）通过，覆盖新增 handler/repository/service 编译。
- `pnpm exec vitest run src/components/layout/__tests__/AppSidebar.spec.ts src/views/admin/__tests__/UpstreamDashboardView.spec.ts src/router/__tests__/upstreamRoutes.spec.ts` 通过。
- `pnpm typecheck` 通过。
- `pnpm lint:check` 通过。
- `git diff --check` 通过。
- `go test ./internal/service ./internal/repository ./internal/handler/admin -run 'TestNonexistent' -count=0` 通过（变更包编译）。
- `pnpm test:run -- src/views/admin/__tests__/UpstreamDashboardView.spec.ts` 通过（2 tests）。
- `pnpm typecheck` 已启动并完成，无错误输出；完整门禁仍按后续授权执行。
- 本轮 focused dashboard tests、Vitest、typecheck、Lint 和 diff check 已通过；本轮未执行全量门禁、review、audit、VM Gate 或生产部署。
