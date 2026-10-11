# 任务

implementation-mode: tdd

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

## 已授权的财务展示补充（2026-10-11）

- [x] 核对收入、消费与渠道余额的记账口径，保留现有后端计算和原 change 身份。
- [x] 取得 UT-FIN-01/02/03 的有效 Red 证据（ST-FIN-01/02/03）：`pnpm test:run -- src/views/admin/__tests__/UpstreamDashboardView.financial.spec.ts`，7/7 用例失败，原因是原组件缺少独立财务区。首次缺少 Vitest shim 的环境失败不算 Red；随后 `pnpm install --frozen-lockfile` 完成且未修改依赖合同。
- [x] 将卡片和详情改为收入、消费、估算毛利、毛利率独立展示，完成两位小数、亏损、未知和零值处理（ST-FIN-01/02）。
- [x] 更新中英文文案、记账口径说明、长期 Wiki 和 fork 扩展不变量（ST-FIN-03）；扩展目录 JSON 解析通过。
- [x] 完成看板/充值/locale focused tests、后端既有相关测试、typecheck、lint、build、strict validate 与 diff check（ST-FIN-01/02/03/04）。

本轮局部补充保持 implementation 阶段；不以此签发整个历史 change 的 full/pass，不执行归档或生产发布。

### 本轮验证证据

- `pnpm test:run -- src/views/admin/__tests__/UpstreamDashboardView.financial.spec.ts src/views/admin/__tests__/UpstreamDashboardView.spec.ts src/views/admin/__tests__/UpstreamDashboardView.recharge.spec.ts src/i18n/__tests__/localeKeyCompleteness.spec.ts`：4 suites，37/37 通过（财务 7、合同 17、充值 9、locale 4），exit 0。
- `go test ./internal/service ./internal/repository -run "Test(Dashboard|UpstreamDashboard|NormalizeProviderBalanceExtra|NormalizeUpstreamActualRate)" -count=1`（backend）：两个包通过，exit 0；本轮后端源码无差异。
- `codegraph impact UpstreamDashboardView` 只返回目标组件；`codegraph affected` 结合三个前端文件返回 locale 汇总的宽依赖，实际验证优先看板、充值、语言完整性与编译检查。
- 文档只读复核已纠正 `actual_cost` 的表述：收入包含余额扣费或订阅用量，不宣称现金实收。
- 前端独立复核发现 BaseDialog Teleport 到 body 后详情财务区的暗色祖先选择器失效，已改为 `.dark .detail-modal-content`，保留亏损和未知值专属颜色；未修改共享弹窗组件。
- `pnpm typecheck`：通过，exit 0。
- `pnpm lint:check`：通过，exit 0。
- 暗色选择器修复后重新执行同一 4-suite focused 命令，37/37 再次通过，exit 0。
- `pnpm build`：locale 4/4、Vue 编译及 Vite 构建通过，exit 0；输出既有工具提示（Browserslist 数据陈旧、静态/动态 import 混用和大 chunk），未修改依赖或构建链。此处只验证前端编译，不是发布镜像、VM Gate 或生产发布证据。
- `spec-wiki-lite validate upstream-config-dashboard --strict --json` 与 `git diff --check` 通过；backend、package.json 与 pnpm-lock.yaml 无差异。没有运行真实浏览器或生产数据核验，已覆盖组件渲染和接口 mock 的行为边界。
