---
verification-result: pass
scope: full
---

# group-preferred-account-pool Test Report

## 环境

- runtime/platform：Windows workspace，Go module `backend`，Node/pnpm frontend
- package/tarball：当前工作区实现；尚未部署生产
- fixtures：现有 Go/Vitest mock、migration contract、scheduler cache、gateway/OpenAI scheduler fixtures
---

## 命令与结果

| 命令/验证动作 | 结果 | 证据摘要 |
| --- | --- | --- |
| `go test -p 2 -parallel 2 ./... -count=1` | pass | 全部 backend packages 通过 |
| `go test -tags=unit -p 2 -parallel 2 ./... -count=1` | pass | 全部 unit-tag backend packages 通过 |
| `pnpm test:run` | pass | 294 files / 2045 tests |
| `pnpm typecheck` | pass | `vue-tsc --noEmit` |
| `pnpm lint:check` | pass | ESLint 全量 |
| `pnpm build` | pass | `vue-tsc -b && vite build`，1091 modules |
| `python -m pytest .agents/skills/sub2api-production-deploy/scripts/tests/release` | pass | 337 passed, 1 skipped |
| `go generate ./ent` 两次 | pass | 两次 diff hash 均为 `fbd2ca0f612aa3fc96d5b5724f1edef1cea84de6` |
| migration 253 SHA-256 与 catalog | pass | `7a4582c35bb45d287edbbd5110fc9ae14d2f74349af4c1d58278e132212a38c2` |
| `git diff --check` | pass | 无 whitespace error |
| `spec-wiki-lite validate group-preferred-account-pool --strict --json` | pass | 当前 change strict valid |
---

## System Test 覆盖

| ST | 类型 | 结果 | 证据 |
| --- | --- | --- | --- |
| ST-001 | normal/failure | pass | preferred repository/API focused tests |
| ST-002 | failure/boundary | pass | 去重、绑定校验、事务回滚合同 |
| ST-003 | normal/boundary | pass | BindGroups、duplicate account/group 行为与测试 |
| ST-004 | normal/failure | pass | scheduler cache projection 与 outbox 合同 |
| ST-005 | normal/regression | pass | gateway preferred pool 与 OpenAI rate-neutral tests |
| ST-006 | failure/boundary | pass | 满载优先回退普通池、硬准入过滤 |
| ST-007 | normal/boundary | pass | weighted sticky、compact 和 image-cost 外层约束 |
| ST-008 | regression | pass | OpenAI Top-K、成本隔离、decision audit 字段 |
| ST-009 | normal/failure | pass | GroupsView/API Vitest、typecheck/lint/build |
---

## Unit Test 与 TDD 证据

| UT / suite | Red | Green/Refactor | 结果 |
| --- | --- | --- | --- |
| UT-001 | 字段缺失边界由 migration/cache contracts 约束 | Ent、service、DTO、scheduler cache 通过 | pass |
| UT-002 | 非绑定账号与事务失败边界由 repository contract 约束 | 去重、校验、原子替换、outbox 通过 | pass |
| UT-003 | API route/DTO focused coverage | handler/routes/API tests 通过 | pass |
| UT-004 | 复制/解绑标记泄漏边界 | focused duplicate tests 与 BindGroups 修复通过 | pass |
| UT-005 | 分池顺序边界 | unified preferred helpers 通过 | pass |
| UT-006 | 满载、sticky、硬准入边界 | gateway/OpenAI scheduler tests 通过 | pass |
| UT-007 | Top-K/rate-neutral/audit 边界 | OpenAI focused/full suites 通过 | pass |
| UT-008 | UI API、加载、保存、清空边界 | 5 focused Vitest + 2045 全量通过 | pass |
---

## 成功标准覆盖

| 成功标准 | ST/UT/命令 | 结果 |
| --- | --- | --- |
| 高倍率优先账号先于低倍率普通账号，优先池内部不受倍率压低 | ST-005/ST-008、OpenAI tests | pass |
| 硬准入失败和满载回退普通池 | ST-006、gateway/OpenAI tests | pass |
| 合法粘性优先且模型路由/复合分组不绕过 | ST-007、sticky/compact/image tests | pass |
| 未配置优先池行为不变 | ST-005、全量 Go regression | pass |
| API、生命周期、outbox、快照 | ST-001..004、repository/migration tests | pass |
| 前端加载、搜索、保存、清空、双语提示 | ST-009、Vitest/typecheck/lint/build | pass |
| 不自动部署生产 | release boundary、后续 VM Gate 仅开发环境 | pass |
---

## 失败、未验证与证据缺口

- 失败：无。
- 未验证：真实 upstream 模型调用未发送，按发布安全边界标记 `not_checked`；不影响本 change 的隔离调度与 UI 证据。
- 证据缺口：无 blocking 缺口。
---

## 结论

- required evidence 全部通过，scope full，可归档并进入 VM Gate。