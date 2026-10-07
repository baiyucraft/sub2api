---
verification-result: pass
scope: full
---

# 用户领奖与成本验证报告

## 环境与范围

2026-10-07，基线 `0898fe8c6289f87a1a4d90cf74a50e296689960e`，Windows / Go 自动工具链 1.27 / pnpm / Vitest；真实数据库验证在 VM 本地 PostgreSQL 中新建的 `codex_activity_cost_` 独立数据库执行。全部数据为合成用户与账本，未复制生产数据，未启动本机后端。前端产物通过 embed 编入后端验证构建。

## 命令与结果

| 检查 | 结果 | 证据 |
| --- | --- | --- |
| `go test -tags unit -p 2 -parallel 2 ./internal/... ./migrations` | pass | 首轮及最终修复后全量均通过，`.tmp/activity-ledger-backend-final.log`；最终 service 包 224.857 秒，其他包通过或命中有效缓存；focused v2 四包通过 |
| `go build -p 2 -tags embed -o ../.tmp/sub2api-validation.exe ./cmd/server` | pass | `.tmp/activity-ledger-backend-build.log`；只构建，未运行本机服务 |
| `pnpm test:run` | pass | 418 文件 / 3195 测试，`.tmp/activity-ledger-frontend-full.log` |
| `pnpm typecheck` / `pnpm lint:check` | pass | 对应 `.tmp/activity-ledger-typecheck.log` / `activity-ledger-lint.log`；Lint 有 1 个既有未使用导入 warning，无 error |
| `pnpm build` | pass | `.tmp/activity-ledger-build.log`；locale 检查、vue-tsc 与 Vite 通过，有既有大 chunk 提示 |
| Release focused / aggregate pytest | pass | 最终 763 pass / 3 既有环境 skip，432.56 秒；首轮 14 个旧 profile fixture 失败均已修复回归；`.tmp/admin-activity-reward-history-and-cost-ledger/release-pytest-final.log` |
| Release `bash -n` | pass | 61 个 Shell 资产；新增补录 CLI 6 项本地 Bash 测试通过 |
| Fork audit 单元测试 | pass | 9 项，profile 264 / 289 checksum / 历史 profile 保持原样 |
| VM `TestActivityRewardCostsPostgres` | pass | 7 个主场景，含 gift / draw 各 8 个同键并发请求；`.tmp/activity-cost-vm-result.json` |
| VM 补账 CLI `check` / `apply` / 再次 `apply` | pass | check 发现尾部缺账且退出 1；apply 补 1，重放补 0；人工行与余额不变 |
| CodeGraph impact / affected | pass，辅助范围证据 | `.tmp/activity-ledger-codegraph-impact.json` / `activity-ledger-codegraph-affected.json`；跨模块边较宽，按当前源码和实际入口确定必需回归 |
| `git diff --check` / change strict validate | pass | 新 change 无 issue；既有全局 Wiki 坏链接不属于本次修改 |

## ST / 成功标准映射

| 用例 | 成功标准 | 实际证据与断言 | 结果 |
| --- | --- | --- | --- |
| ST-01 | SC-01 | Go service/handler 混合分页、同时间/跨表同 ID、旧字段/旧类型、独立累计金额、零奖励；Vitest 同窗换用户/筛选/分页、UsageView 最后点击优先、关闭取消待完成结果 | pass |
| ST-02 | SC-02 | 四类真实发奖与批量、并发同键重放、成本 trigger 失败整笔回滚、软删除余额 0 行回滚、来源冲突拒绝；余额/机会/奖励/成本同时成立 | pass |
| ST-03 | SC-03 | 真实 260/261/262/289 SQL、重复补录、旧 writer 尾部、8 位 NUMERIC、上海午夜、零额/non-credited、原时间日期、人工记录不变；逐日数量金额相等 | pass |
| ST-04 | SC-04 | 服务/仓储/HTTP/Vue 防止人工伪造自动类别、关联或保留幂等键，以及冲正自动记录；提交后才调用余额和管理统计缓存失效，复用既有成本聚合 | pass |
| 聚合与治理 | SC-05 | 前后端、release、VM 证据、profile 264 与 fork catalog、长期 Wiki、本 change 报告与 strict validate | pass |

## TDD 证据

| 单元范围 | Red | Green / Refactor |
| --- | --- | --- |
| UT-01 历史读模型 | 缺少新模型/helper/返回形态的编译失败已在工具输出观察；未单独保留首次原始日志 | 新读模型、DTO、安全旧字段、service 与 handler 测试通过；摘要 `.tmp/admin-activity-reward-history-tdd.json` |
| UT-02 成本 | 人工关联原先 201、自动成本可冲正、缺迁移、余额零行仍提交、游标错误误判无机会、gift 并发同键误报未达标 | 四包 focused v2 通过；`.tmp/activity-cost-{red,service-red,recipient-red,cursor-red,gift-race-red}.log` 与 `activity-cost-final-green-v2.log` |
| UT-03 前端 | 首轮 7 failed / 34 passed；追加真实切用户和 UsageView 竞态 4 failed / 23 passed | 首轮 42 passed；追加 27 passed；最终全量 3195 passed；`.tmp/admin-activity-reward-history-and-cost-ledger-frontend-tdd.json` |
| UT-04 发布合同 | profile 264 / 289 / catch-up 缺失与 mismatch 未失败关闭 | 264 新合同、历史全字典保持原样、未知 265 拒绝、catch-up CLI Green；`.tmp/admin-activity-reward-history-and-cost-ledger/release-contract-tdd.md` |

## VM 对账与清理

真实 CLI fixture 的北京日期 `2026-10-06`：奖励/成本各 1 条，金额均 0；`2026-10-07`：各 2 条，金额均 0.87345678。重复执行不改变用户余额或人工成本。原 `sub2api-dev` 容器身份和健康状态保持不变，本次隔离数据库、随机 schema 与远端临时目录已清理。Linux 测试程序由当前树交叉编译，SHA-256 为 `b4149ddfa398daaed40833b9ad1cd305446e20315ea1e0e5d71827271061d864`；该测试程序不是签名生产 candidate。

## 未验证与边界

- 三个既有 release 环境 skip：Windows 不支持的插件配置 symlink、runner parent symlink 测试；缺 jq 的生产镜像身份 Bash preflight。未触及其运行代码，均不涉及本次 264 / 289 / 补账必要用例；未来正式 Linux Gate 必须执行适用检查，不能把 skip 当 pass。
- 生产候选镜像、旧镜像兼容 smoke、签名 VM/恢复 Gate、数据库备份门禁、生产发布、旧生产实例排空后的实际补账和缓存重建未执行。用户本次授权实现及 VM 数据库验证，生产仍需独立授权与最终 blob 恢复分类。
- 组件测试和真实 SQL/service 断言为界面/接口稳定证据；未执行候选页面浏览器 smoke。
- 全局 Wiki 三个既有坏链接未混入修复：发布与协调恢复页的 release-runner-lifecycle、Sol 探针页的 confidence baseline LICENSE/README。本 change strict validate 无此问题。
- 验证结束时尚未 commit，随后用户授权本地提交；本次不 push、发布或 archive。原 `daily-activity-rewards` change 身份保持不变。

## 结论

本 change 声明的实现与本地/VM 数据库验证全部完成，SC-01～05、ST-01～04、UT-01～04 有对应证据，完整范围验证通过。生产候选、恢复分类与发布执行继续按独立授权门禁进行。
