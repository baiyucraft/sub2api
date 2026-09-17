# `sub2新站` 合并阶段 0：基线冻结与差异清单

> 执行日期：2026-09-17
> 当前阶段：阶段 0 已完成；未进入业务代码实现、迁移、构建、VM Gate 或生产发布。
> 分析源：`E:\project\!byAI\sub2api\.upstream\sub2新站`

## 1. 冻结结果

### 1.1 当前 fork

| 项目 | 冻结值 |
|---|---|
| 分支 | `main` |
| HEAD | `7c11b04fc5a7eb8ce9b388eac8f1d19a350b829e` |
| HEAD 标题 | `实现会话级切号与 API Key 调度偏好` |
| 版本 | `0.2.5-baiyu` |
| 官方 upstream | `881f3202694c6bc932446931a30c27d9675178b9` |
| merge-base | `881f3202694c6bc932446931a30c27d9675178b9` |
| 相对官方提交 | fork 多 `626` 个提交，官方侧多 `0` 个提交 |
| 相对官方差异 | `1422` 个路径，约 `245159` 行新增、`15686` 行删除 |
| 当前 release profile | `252` |
| 当前最高 migration | `276_api_key_scheduling_mode.sql` |

目标官方对象已存在本地对象库，类型为 `commit`。本阶段没有执行 `fetch`、`merge`、`rebase` 或分支切换。

### 1.2 `sub2新站` 源快照

`sub2新站` 内部 `.git` 是没有任何提交的空仓库，全部源码均为未跟踪文件，因此不能用 Git SHA 作为来源身份。阶段 0 改用“现有发布清单 + 当前目录树摘要”双重冻结。

| 项目 | 冻结值 |
|---|---|
| 源版本 | `0.2.4` |
| `SOURCE_MANIFEST.json` SHA-256 | `4450ce723de5094695f7df6b2e27826a5dd9afe39b55e16d06175cfc3b11445b` |
| 清单条目数 | `4008` |
| 清单缺失文件 | `1`：`澄川新站说明.md` |
| 与清单不一致文件 | `157`：backend 116、frontend 37、`.github` 2、docs 1、其他 1 |

由于随包清单已经漂移，后续实施不能仅以 `SOURCE_MANIFEST.json` 判断源文件身份。以下目录树摘要才是本阶段读取到的实际源码快照：

| 范围 | 文件数 | 树 SHA-256 |
|---|---:|---|
| `backend/` | 3278 | `44f2269966302697207898923dbbf3523ec43600f52508cd360fc2873b8bd53a` |
| `frontend/`（排除 `node_modules`、`dist`、`coverage`） | 890 | `6f47af22a6d7d0eaa274315223cc23ea6fa19c3f4dbf2d358478b43c6d38b760` |
| `deploy/` | 43 | `ed2a01a5b23986a5aae6c95d3862887d882e1de6a0e18648c0d6bb9a831e5fc3` |
| `docs/` | 21 | `2754d362a48c545aecb3cdebd698415af37e7bb3ea3ead77acf2ef761c082c9f` |
| `openspec/` | 23 | `a86e696498e706157b375c676b6cea6c193d595d805a34316abc310d541b2540` |
| `tools/` | 8 | `de2ea104d04bb7bcfdf895e6800e526ee0baa1c1805fc8705844105385e85796` |
| `verification/` | 95 | `f338bd5c57db8df23c2b74f4364e152412033a6cf3e0dbf9d00e19e5080a6e16` |

树摘要算法：按仓库相对路径排序，对每个文件生成 `路径<TAB>文件 SHA-256`，以 LF 连接后再次计算 SHA-256。

## 2. 工作区边界

阶段开始前已经存在以下未提交改动，本阶段未覆盖、回退或暂存它们：

- `frontend/src/i18n/locales/en/dashboard.ts`
- `frontend/src/i18n/locales/zh/dashboard.ts`

本任务新增的阶段性文档位于 `.docs/`。因此正式 pre-merge 审计当前仍被 `dirty_worktree` 阻断。该阻断只影响进入后续真实合并/实施门禁，不否定本阶段的只读差异结论。

## 3. Fork 扩展审计结果

已执行 `snapshot` 和 `pre-merge`，目标均为完整 SHA `881f3202694c6bc932446931a30c27d9675178b9`。

报告：

- `.tmp/fork-extension-audit/snapshot-7c11b04fc5a7-881f3202694c/report.json`
- `.tmp/fork-extension-audit/snapshot-7c11b04fc5a7-881f3202694c/report.md`
- `.tmp/fork-extension-audit/pre-merge-7c11b04fc5a7-881f3202694c/report.json`
- `.tmp/fork-extension-audit/pre-merge-7c11b04fc5a7-881f3202694c/report.md`

结果为 `74` 项 finding：`73 pass`、`1 blocker`。

通过项包括：

- `1422` 个 fork-only 路径均已落入扩展目录登记范围；
- 当前 profile `252` 合同通过；
- `60` 个登记 migration checksum 通过；
- 历史 profile 合同通过；
- fork 版本 `0.2.5-baiyu` 与官方 `0.2.5` 的版本合同通过；
- 已登记 fork 扩展的路径、语义标记和最低测试均已发现。

唯一 blocker：工作区存在未提交文件。未发现 catalog 漏登记、profile 漂移、migration checksum 漂移或目标 Git 对象缺失。

## 4. 能力处置矩阵

| 能力 | 判定 | 阶段 0 结论 |
|---|---|---|
| `X-Codex-Turn-State` 捕获、透传、同账号复用、防跨账号污染 | 当前已有 | 核心文件哈希相同，不迁移 |
| 账号保护策略、预览、原子应用/还原、字段 ownership | 新站独有 | 进入阶段 1，按领域模型增量实现 |
| HTTP/WSS/账号测试身份统一 | 双方不同 | 进入阶段 2；以当前 fork 调度和身份隔离为底稿逐函数合并 |
| Mode1 请求语义完整性守卫 | 新站独有 | 进入阶段 3；先 observe-only，再考虑 enforce |
| 连接池按身份/代理/TLS 隔离 | 双方不同 | 进入阶段 4；保留当前 WS reader/ping/保活实现 |
| Node.js 24 风格 TLS 兼容传输 | 双方不同 | 进入阶段 4，作为可选账号策略，不改变默认行为 |
| 动态 RPM、burst、动态并发、429/5xx 反馈 | 新站独有但与 fork RPM 重叠 | 进入阶段 5；只能扩展现有统一准入，禁止并存两套权威计数 |
| 智能能力测试、队列、评估、重评和 UI | 新站独有 | 进入阶段 6/7；独立迁移并重新分配 migration |
| 保护感知账号编辑和运行态诊断 | 双方不同 | 进入阶段 7；不能整块覆盖 `EditAccountModal.vue` |
| 多地区 IP 智商测试 + 优质 Turn State 池 + 40–45 分钟轮换 | 两边均无完整实现 | 不属于本次“从新站合并”；如需实施必须单独立项实验 |

## 5. 关键文件矩阵

| 路径 | 状态 | 处理方式 |
|---|---|---|
| `backend/internal/service/openai_codex_turn_state.go` | 相同 | 不迁移 |
| `backend/internal/service/account_mode1_protection.go` | 仅新站 | 阶段 1 参考实现 |
| `backend/internal/service/account_protection.go` | 仅新站 | 阶段 1 参考实现 |
| `backend/internal/service/anti_degrade_strategies.go` | 仅新站 | 阶段 1 参考实现 |
| `backend/internal/handler/admin/anti_degrade_handler.go` | 仅新站 | 阶段 1 参考实现 |
| `backend/internal/service/openai_agent_identity.go` | 双方不同 | 阶段 2 逐函数合并 |
| `backend/internal/service/openai_mode1_integrity.go` | 仅新站 | 阶段 3 参考实现 |
| `backend/internal/service/openai_mode1_semantics.go` | 仅新站 | 阶段 3 参考实现 |
| `backend/internal/service/openai_ws_pool.go` | 双方不同 | 禁止整文件覆盖；阶段 4 增量合并 |
| `backend/internal/pkg/tlsfingerprint/dialer.go` | 双方不同 | 阶段 4 增量合并 |
| `backend/internal/service/account_traffic_service.go` | 仅新站 | 阶段 5 参考实现 |
| `backend/internal/service/account_traffic_policy.go` | 仅新站 | 阶段 5 参考实现 |
| `backend/internal/service/account_traffic_events.go` | 仅新站 | 阶段 5 参考实现 |
| `backend/internal/service/account_traffic_ws.go` | 仅新站 | 阶段 5 参考实现 |
| `backend/internal/repository/account_traffic_cache.go` | 仅新站 | 阶段 5 参考实现 |
| `backend/internal/service/intelligent_test_service.go` | 仅新站 | 阶段 6 参考实现 |
| `backend/internal/handler/admin/intelligent_test_handler.go` | 仅新站 | 阶段 6 参考实现 |
| `frontend/src/components/account/EditAccountModal.vue` | 双方不同 | 禁止整文件覆盖；阶段 7 拆分交互 |
| `frontend/src/views/admin/IntelligentTestsView.vue` | 仅新站 | 阶段 7 参考实现 |

另外，当前 fork 相对官方的高风险共同路径已明确包含 OpenAI gateway、scheduler、账号 service/repository、`wire_gen.go`、账号创建/编辑组件、`AccountsView.vue`、平台 badge、sidebar 和渠道状态页面。后续任何阶段不得使用整文件 `ours`/`theirs` 或直接复制新站旧文件。

## 6. 测试矩阵

| 能力 | 新站证据 | 当前 fork 必须保留/新增的门禁 |
|---|---|---|
| Turn State | `openai_codex_turn_state_test.go` | 保持现有测试全绿；failover 不跨账号复用 |
| 账号保护 | `account_mode1_protection_test.go`、`account_protection_test.go`、`anti_degrade_strategies_test.go` | 新增事务原子性、旧快照覆盖、字段 ownership 失败测试 |
| 身份统一 | `openai_agent_identity_test.go`、`openai_agent_identity_compat_test.go` | 覆盖 HTTP、透传、HTTP→WS、原生 WS、账号测试一致性 |
| 语义守卫 | `openai_mode1_integrity_test.go` | 覆盖合法等价转换与真实字段丢失；observe-only 不拦截 |
| WS/TLS | `openai_ws_pool_test.go`、TLS dialer 测试组 | 保持 fork 的 `openai_ws_pool_reader_loop_test.go`，新增连接兼容键测试 |
| 动态流量 | `account_traffic_*_test.go` | 保持 `account_rpm_test.go`、`account_rpm_gate_test.go`；验证一次真实尝试只计一次 |
| 智能测试 | `intelligent_test_*_test.go`、repository integration 测试 | 新增不刷新 OAuth、不改代理、不污染健康/调度、容量共享测试 |
| 管理后台 | 新站 traffic UI verification | 增加草稿、并行保存、乱序刷新、嵌套弹窗和中英文文案测试 |
| Fork 回归 | 无 | RPM、探针、公平轮询、上游生命周期、分组优先、国产平台、OpenCode、渠道监控全部保留 |

## 7. Migration / Profile 计划

当前 fork 已占用 `246`、`247`、`248`，且最高 migration 已到 `276`。新站的以下编号禁止原样复制：

| 新站 migration | 含义 | 阶段 0 候选重编号 |
|---|---|---|
| `246_account_protection_roles.sql` | 账号保护角色 | `277` |
| `247_intelligent_tests.sql` | 智能测试基础表 | `278` |
| `248_intelligent_assessment_v2.sql` | 智能评估 v2 | `279` |

`277–279` 只是基于 2026-09-17 当前最高编号的候选，不提前占号。真正实现对应阶段前必须重新读取 migration 尾部；若期间已有新 migration，则顺延。当前 profile 为 `252`，只有实际新增迁移并完成实现后才创建新的 profile，阶段 0 不修改 profile、checksum 或版本。

## 8. 阶段 0 结论与停点

阶段 0 的四项交付物已经形成：

- 能力矩阵：见第 4 节；
- 文件矩阵：见第 5 节；
- 测试矩阵：见第 6 节；
- migration/profile 计划：见第 7 节。

扩展目录不需要在本阶段增加“尚未实现的新站能力”；现有 fork 合同已经通过审计。稳定能力真正落地后，再登记 `sub2api-fork-extension-audit` 并沉淀到 `.wiki`，避免把一次性调研或未实现设计写成长期合同。

进入阶段 1 前的硬条件：

1. 处理或隔离当前两个 dashboard 国际化未提交改动；
2. 明确 `.docs/` 阶段文档的提交/保留方式；
3. 重新运行 pre-merge 审计并达到无 blocker；
4. 重新确认 `sub2新站` 目录树摘要未变化；
5. 再按阶段 1 的逐符号方案实施，不能直接复制整文件。
