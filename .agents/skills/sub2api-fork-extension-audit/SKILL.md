---
name: sub2api-fork-extension-audit
description: 审计 Sub2API fork 相对官方 upstream/main 的扩展合同。用于准备或复核 upstream 合并、检查 fork 专属业务语义、版本与 migration/profile 历史、识别未登记差异和高风险整文件冲突处理，并生成只读 JSON/Markdown 报告；不执行 merge、代码修改、构建、VM Gate 或生产部署。
---

# Sub2API Fork 扩展审计

使用本技能时用中文沟通。把 `references/extensions.yaml` 视为 fork 扩展的唯一机器可读事实源；不要仅凭 Git diff 推断产品语义。

## 不可违反的边界

- 只执行只读 Git 和文件检查；不得 fetch、merge、checkout、reset、代码生成、格式化、构建、VM Gate 或生产操作。
- 必须由操作者明确提供官方目标的 40 位完整 commit SHA；短 SHA、未知对象、脏工作区和未解决冲突均为 blocker。
- 不得用整文件 `ours` 或 `theirs` 解决高风险冲突。确需整文件选择时，在合并记录中说明理由并绑定专项回归测试。
- profile 233–256 的已有合同不回写；256 为 `0.2.9-baiyu`、parent 255、无新增 migration。已检查的本地、VM、生产与备份证据目录未见 256 资产，但不足以证明从未签名、发布或被引用，因此保守保留 256，以 257（parent 256）承载 migration 284。254 的三项 278–280 与 255 的三项 281–283 原合同不得改写。
- Fork 版本必须等于目标官方正式版本加 `-baiyu`。通常以目标源码 `VERSION` 为准；若正式 annotated tag 的发布流程只在构建阶段写入版本、标签源码文件滞后，则必须在目录中用目标 commit 精确固定 release version，禁止按 tag 名或预期版本猜测。
- 报告只写入 `.tmp/fork-extension-audit/`，不得修改 tracked 文件。

## 强制读取

- 每次审计先读取 [extensions.yaml](references/extensions.yaml)。
- 若 `adopted_upstream_tranches` 非空，必须验证其中 base、tip、merge commit、提交数、版本和当前 HEAD 祖先关系；这些批次不得因 upstream 回退、force-push 或撤回而删除。
- 准备或执行 upstream 合并时读取 [merge-workflow.md](references/merge-workflow.md)。
- post-merge 审计后读取 [regression-matrix.md](references/regression-matrix.md)，按报告列出的功能域执行最低测试。
- 进入构建、Gate、VM 或生产阶段时改用 `sub2api-production-deploy` skill；本技能不替代发布门禁。

## 插件宿主与插件包双层审计

- 通用插件协议、Host API、Manifest 解析、管理路由、原生管理页渲染器、包校验、PostgreSQL 权威安装记录、跨实例请求守卫、maintenance journal 和 migration 属于宿主扩展；插件私有采集、解析、状态机、模型名单、v1 iframe UI 或 v2 声明式页面定义属于独立插件包。不得把两层所有权合并成一个模糊的“STATE 功能”。
- 审计宿主时检查 `generic-plugin-runtime-v2`：旧 API 兼容、必需 feature 协商、上传/启停/升级路由、签名策略、受管范围、准入失败隔离、加密 CAS/lease、数据库原包恢复和跨实例升级回滚。
- 审计插件包时检查 `codex-state-plugin`：插件 ID/版本、来源锁、许可证、双架构运行时、manifest 文件哈希、Ed25519 签名、Host API 2 与九项 feature、默认停用、STATE 私有生命周期和负向边界。
- 当前 `codex-state-plugin` 默认是 Manifest v2 Native 页面：`ui/admin-ui.json` 属于插件私有声明式页面，宿主只负责通用渲染；页面必须有 `en` / `zh` 翻译，固定覆盖概览、账号表、筛选与跨页选择、模型批量明确按钮和脱敏日志表格。账号表只能绑定固定白名单字段，不能用整对象 `key_value` 展示；`accounts.bulk_update` 只覆盖用户明确点击的 `enabled` 或 `ticket_plan` 字段，返回的规范化 `result.config` 由宿主回填并保存。旧 `ui/index.html` / `ui/dist` 只作 iframe 回滚依据，双架构包中的 Native 定义必须语义和字节一致，不得把 Native 页面专用能力写进宿主业务判断。
- `POST /api/v1/admin/plugins/upload` 是首次安装；调用前必须证明同插件 ID 不存在，不能利用当前宿主对部分非运行状态同 ID upload 的兼容行为绕过 maintenance upgrade。`POST /api/v1/admin/plugins/:id/upgrade` 是已安装 Scoped 插件升级。上传、保存秘密、启用和真实采集是独立授权，审计不得把“包已安装”表述为“功能已启用”。
- PostgreSQL 的 artifact 和 installation 是跨实例权威状态，本地 `plugins.data_dir` 只是校验后的运行副本。审计必须要求逐实例恢复/版本/二进制 SHA/Health 证据，不能以单实例上传成功证明整个集群完成升级。
- 仅插件包变化可独立发版；Host API、管理壳、migration、部署配置或包校验变化仍是宿主应用发布。构建、VM 和生产证据由 `sub2api-production-deploy` 的 `plugin-package` 分类负责，本技能只校验扩展登记与最低回归映射。

### 原生管理页合同

- Manifest v1 的 `ui.entrypoint` 必须继续按 sandbox iframe + Bridge 解释；升级宿主不能要求旧包改写 manifest，也不能把旧入口隐式视为 native。
- Manifest v2 只允许 `ui.type=native|iframe|none`。`native` 必须声明 `ui.definition` 和 `requires.admin_ui=1`；`iframe` 使用 `entrypoint`；`none` 不暴露页面入口。非法组合在安装阶段拒绝。
- Native 定义必须位于包内 `ui/`、列入签名文件哈希并在安装时完整解析。宿主只渲染允许的声明式组件、JSON Pointer 和有限条件；任意 JavaScript、HTML、远程资源、自由表达式、动态模块和宿主 DOM 访问均为 blocker。
- 公共路由固定为 `/admin/plugins/:pluginKey`。插件不能注册任意 Vue 路由或孙页面；复杂布局在单页内部使用 Tabs、折叠区和局部导航。`pluginKey` 是稳定 URL 身份，数据库数字 ID 只用于受权管理 API。
- `GET /api/v1/admin/plugins/by-key/:pluginKey` 和 `GET /api/v1/admin/plugins/:id/admin-ui` 属于宿主管理接口。Native 页面不创建 UI Session；定义缓存必须绑定安装 ID 与 binary SHA，并在升级、卸载或 artifact 身份变化时失效。
- Manifest schema/parser、Admin UI API、宿主路由、原生渲染器、安全校验或宿主插件壳变化均按宿主应用发布；只有当前生产宿主已经支持所需 `admin_ui` 版本时，插件包内声明式定义变化才允许 `plugin-package`。

## 发布恢复合同审计

历史保留 profile 261：版本 `0.2.13-baiyu`、parent 260、`new_migrations=[286_add_payment_order_bonus_amount.sql,287_add_typesafe_platform.sql]`，固定官方目标为 `b8dece9000c68815a5b867ca5a1e6f236e173905`。260 已 signed 且于 2026-10-01 生产 verified，其 `0.2.11-baiyu`、parent 259、`new_migrations=[285_upstream_null_rate_lifecycle.sql]` 及既有 Gate/candidate/checksum 保持不变；259 及以前合同不变。两个官方 `241_*` 与旧 local241 编号冲突，只将新文件原字节重编号为 286/287，旧 SQL 与数据库记录不回写。286 保存充值赠送金额，287 扩展用户平台额度与组合路由约束；TypeSafe 不进入 LLM channel monitor。catalog 使用原始字节 SHA-256，Gate catalog 继续使用 strip 后 SQL 文本 checksum。261 的本地登记和静态/unit 测试不证明 VM Gate、实际 PostgreSQL 升级、旧镜像兼容或生产发布；历史 profile 262 保持 `0.2.13-baiyu`、parent 261、仅新增 `288_upstream_confidence_distribution.sql`，用于 OpenAI Sol 单次探针的持久化 128 次分布窗口；历史 profile 263 为 `0.2.14-baiyu`、parent 262、`new_migrations=[]`；历史 profile 264 为 `0.2.14-baiyu`、parent 263、仅新增 `289_activity_reward_costs.sql`；历史 profile 265 为 `0.2.15-baiyu`、parent 264、仅新增 `290_drop_platform_check_constraints.sql`，固定官方目标为 `3a6fd1c9db07203ca308aaba69e502bc1f35b307`；265 与更早合同不变；历史保留 profile 266 为 `0.2.15-baiyu`、parent 265、仅新增 `291_user_invoice_annotations.sql`，用户级开票仅为站内金额标注，无币种、余额或充值账本写入。该历史登记时未知269拒绝；当前273/274边界见末节。266 的新恢复敏感 blob 必须重新分类，未分类继续阻断，不新增豁免。后续 VM 合同恢复叠加后仍须按最终 blob/模式完成恢复分类审阅，未分类变更保持阻断，不新增豁免。

当目标差异触及 `.agents/skills/sub2api-production-deploy/**`、备份格式、migration/profile、Compose、ingress 事务或恢复状态机时，审计必须核对 `release-operations-isolation`：

- 三层门禁名称和预算固定为 `fast=0-2min`、`specialized=5-15min`、`full=20-60min`；`full` 不阻塞普通发布，仅恢复算法、备份格式、信任链变化或真实恢复事故修复为硬门禁。profile 白名单、版本、自测目录和入口兼容扩展须按同路径旧/新 blob 与模式精确审阅，走真实隔离恢复 `specialized`；未知关键变化先阻断，显式 VM Full 不解除。
- 触发矩阵必须覆盖 migration、PostgreSQL/Redis、Compose、backup、restore/cleanup/reconcile、ingress、发布状态机和真实恢复事故修复。
- Redis 恢复使用总键、TTL 键和永久键单调不等式，禁止恢复旧的精确差值等式。
- 完整灾备的新 Redis 证据在原精确字段集外仅增加 `source_observation` 与 `boundary_probe`，host/Guest 共用严格验证器：真实恢复源用同一原子 Lua 记录 SCAN 前后总键与 TTL 键，最终计数取同刻 after，永久键与备份精确相等；允许真实源无 TTL 或无自然删除，顶层观测如实记录。独占随机 canary 必须标记 `production_source=false`，绑定同 Guest/engine/rootfs/drill/release 与真实 Redis image，以无网络、只读根、tmpfs 和无端口/volume 容器真实等待后证明 `2/1 -> 1/0`、永久 sentinel 摘要不变及实际清理。旧精确字段分支保留原严格语义，postrelease legacy schema 1 的 TTL 差值等式和 checkpoint 身份不变；未知/缺失字段、bool 冒充整数、来源或清理漂移均拒绝。最低回归为 `test_full_recovery_drill.py`、`test_recovery_evidence.py`、`test_postrelease_baseline.py` 与 `test_baseline_candidate_load.py`；独立 canary 通过不代表完整 DR、VM Gate 或生产部署完成。
- orchestrator、restore、cleanup、reconcile 作为同一 helper bundle 绑定完整 commit 和 SHA-256；禁止新旧 helper 混用。
- 协调恢复按 PostgreSQL、Redis、Compose、应用、Nginx、backup units、claim 和 state cleanup checkpoint 幂等续跑。
- `verify-result` 与 `verify-recovery-result` 分离，不得放宽或互相替代。

本技能仍只检查登记、路径、文档和最低测试映射，不执行门禁、恢复或演练。

## 官方修复优先

- 审计本地兼容性修复或 workaround 时，主动检查目标 upstream commit 是否已包含同一故障域的官方修复；只有进入目标 commit 的代码才视为官方事实，开放中的 Issue、PR 或未合并 commit 仅作为设计参考。
- 官方修复完整覆盖本地修复时，以官方实现和官方测试为基线，建议删除重复的 fork 实现；官方仅部分覆盖时，只保留可证明仍有必要的最小 fork 增量。
- 按故障域分别判断覆盖关系，不得因为官方修复了相邻问题，就回退仍在解决另一独立错误的本地修改。详细判定和测试要求见 [merge-workflow.md](references/merge-workflow.md)。
- 对先 fork 后官方的提交，核对固定官方目标与官方修复 commit 的祖先关系，再逐故障域对照实现和测试；完整覆盖归官方维护，未覆盖/部分覆盖的最小增量继续绑定精确 fork 扩展。保留原 fork 提交和作者历史，不以 patch-id、文件同名或目录豁免代替语义重核。
- 本轮固定目标 `a3eb7ef302961cba716dc78b39b93b60c467db0e` 中 PR #7509 merge `4318a63bd886b1a64c49015979c61bf34eca19ff` 已覆盖本地 `f9633c4f51c15cfc7460d610e899431d0a7c1aaa` 和 `6bf1ab9197e747ddbdd14798f36fef4a2a8561e1`。Grok 4.7 目标已有官方 `935db68517f8beef407db3da016eaef1799fce25`，本地 `c41574a3ab1eaa05b4d9b61bec97ca87d9c782d5` 只可逐域裁决，不能写成官方未覆盖。
- 目标新增的官方 `238b_content_moderation_engine_meta.sql`、`239_channel_reasoning_effort_multipliers.sql`、`240_affiliate_ledger_operation_id.sql` 在 fork 只重编号为 `281_content_moderation_engine_meta.sql`、`282_channel_reasoning_effort_multipliers.sql`、`283_affiliate_ledger_operation_id.sql`，SQL 字节不变；profile 255、catalog 原始 checksum 和测试引用使用新名。历史 238/239/240 原文件与数据库记录不可改写；发布前仍需对新库及升级库验证执行顺序和 Gate。

## 语义重叠候选

- `pre-merge` 和 `post-merge` 会检查 fork 与 upstream 从共同基线起共同修改的用户界面和高风险文件；`snapshot` 不执行该检查。
- `semantic_overlap_candidate` 只基于新增代码中的共享稳定入口、共享非噪声标识符，或 `extensions.yaml.semantic_overlap.evidence_rules` 中显式登记的共同证据生成。
- 该 finding 始终是 `warning`，只要求人工复核。它不表示两侧实现等价、官方完整覆盖 fork，也不授权自动删除、回退或改写任何代码。
- 显式证据规则用于补充已知业务入口、提高候选召回率，不能替代故障域、调用路径、边界行为和回归测试的人工裁决。

- 如果目标 `upstream/main` 变更了账号管理或账号编辑，必须单独建立“上游管理/编辑同步”审计项：逐块对比官方的 handler/service/repository、账号编辑 modal、字段白名单、模型能力同步和相关测试，并把官方新增或修正合理合入当前的上游管理/编辑实现。不得仅合并后端变更而遗漏前端编辑流程，也不得以整文件覆盖丢失 fork 专属白名单、运行时字段或上游账号生命周期语义。

## 已采用但未正式发布的 upstream 批次

- upstream `main` 的提交可以在正式 tag 前被 fork 采用。采用时在 `extensions.yaml.adopted_upstream_tranches` 记录完整 base、tip、真实 merge commit、提交数和采用时的官方/Fork VERSION。
- 如果该批次的官方 `VERSION` 仍为当前正式版本，Fork VERSION 保持该正式版本加 `-baiyu`，不得按预期版本名提前升级。例如采用 `0.2.5` 之后、正式 `0.2.6` 之前的提交时仍使用 `0.2.5-baiyu`。
- upstream 后续回退、force-push、撤回 PR 或删除远端引用，不构成删除 fork 已采用代码或改写 Git 历史的理由。受保护批次必须继续可由 merge commit 到达。
- 在目标正式版本到达前，批次 `base..tip` 的变更路径临时按 upstream 所有权登记，不要求伪装成 fork 扩展；该临时登记不得覆盖批次之外的路径，也不得掩盖 merge commit 之后对同一路径新增的 fork 修改。
- 当目录声明的目标正式版本到达时，审计返回 `adopted_upstream_tranche_reconciliation_required`，必须逐源提交判断：官方完整或等价覆盖的归回 upstream；官方未覆盖的登记为 fork 扩展；部分覆盖的只保留最小 fork 增量。
- 目标正式版本一旦到达，临时路径登记立即失效；仍未被官方覆盖的路径必须绑定正常的 fork 扩展合同，不能继续借用临时 upstream 所有权。
- “归回 upstream”只改变维护归属并在后续语义合并中消除重复实现，不删除既有提交历史。重核完成后将 `reconciliation.status` 设为 `complete`，精确分类全部非 merge 源提交，并把保留/部分覆盖项绑定到真实存在且覆盖相应路径的 `fork_extension_ids`；目标正式版本到达前不得提前完成重核。

## 审计命令

在仓库根执行：

```text
python .agents/skills/sub2api-fork-extension-audit/scripts/audit_fork_extensions.py snapshot --upstream-ref <40位SHA>
python .agents/skills/sub2api-fork-extension-audit/scripts/audit_fork_extensions.py pre-merge --upstream-ref <40位SHA>
python .agents/skills/sub2api-fork-extension-audit/scripts/audit_fork_extensions.py post-merge --upstream-ref <40位SHA> --merge-commit <40位SHA>
```

统一输出：

```text
.tmp/fork-extension-audit/<mode>-<head12>-<upstream12>/report.json
.tmp/fork-extension-audit/<mode>-<head12>-<upstream12>/report.md
```

`blocker` 或 `catalog_update_required` 使进程非零退出。`warning` 要求人工确认，但不会单独改变退出码。

## 固定工作流

```text
解析官方目标完整 SHA
  -> pre-merge 生成扩展快照
  -> 执行真实 merge commit
  -> 按扩展目录语义解决冲突
  -> post-merge 审计
  -> 执行 regression matrix
  -> 调用 sub2api-production-deploy 完成 Gate/发布
```

若报告出现 `catalog_update_required`，先确认差异确属新的 fork 产品或运维扩展，再更新 `extensions.yaml`、不变量和最低测试；不得只扩大通配符来消除告警。

## 结果解释

- `pass`：机器合同满足。
- `warning`：需要人工语义复核，例如高风险文件结果与某一父提交完全相同，或两侧可能修改同一语义入口。
- `blocker`：合并身份、工作区、版本或不可变历史不可信。
- `catalog_update_required`：发现未登记 fork-only 路径或 migration。

### 统一代理绑定迁移登记

`279_proxy_ip_groups.sql` 已属于不可变历史合同。统一代理绑定迁移 `284_unified_proxy_bindings.sql` 已纳入当前 pending profile 257 的 `new_migrations`，SQL 原始字节 SHA-256 已登记为 `de0adde07c5dd930aa1cccf9e68d6c55958fd7ea16323d746019c7c97481afe3`；修改 SQL 必须同步更新 checksum。不得伪造 `migration_contracts`、不得误列为已发布 migration，也不得因文档已更新而报告后端能力已落地。284 的旧 `accounts.proxy_id` FK 与互斥 CHECK 必须在回填代理组账号前移除；同时检查历史 279 不被改写，并按 `accounts.proxy_id -> proxy_bindings.id`、正数共享序列、旧输入归一和删除后不复用 ID 逐项生成审计证据。

报告中的 `required_tests` 只是最低清单。实际构建和发布仍服从 `sub2api-production-deploy` 的更严格门禁。

## 官方 0.2.16 迁移与发布合同

官方 0.2.16 合并固定目标为 `4d9ac3e482106d2da3a39fe3e546d8d6801f1cc1`，按目标源码 VERSION 使用 `0.2.16-baiyu`。新增连续 pending profile 267，parent 266，`new_migrations=[292_group_i18n.sql,293_gemini_cached_contents.sql,294_gemini_cached_contents_channel_usage.sql,295_gemini_cached_contents_mutation_lease.sql]`。四项官方 SQL 仅按原字节重编号，293 建表先于 294/295；历史 266 保留 `0.2.15-baiyu`、parent 265 和迁移 291，全部旧 profile、SQL 和 checksum 不回写。Python/Shell、VM/Gate/DR signer、bootstrap、生产、祖先断言、奖励成本 postflight、备份和清理入口共同支持 267，未知 269 拒绝。恢复敏感最终 blob/模式继续分类，未分类变更保持阻断，不新增审阅豁免；本地合并与测试不代表真实 PostgreSQL 升级、VM Gate、旧镜像兼容或生产部署完成。

最低本地验证为 `test_profile267_release_contract.py`、`backend/migrations/profile_267_migrations_test.go`、关联 Group i18n/Gemini 缓存计费与租约回归、完整 release suite、修改 Shell 的语法检查及 `git diff --check`；真实新库/升级库/部分应用和旧镜像兼容另在适用 VM 阶段验证。

## 上游缓存保护发布合同

历史 profile 268，parent 267，版本 `0.2.16-baiyu`，仅新增 `296_upstream_cache_guard.sql`。当前连续 pending profile 269，parent 268，同版本仅新增 `297_upstream_cache_guard_account_scope.sql`；全部历史 profile、SQL 和 checksum 不回写。Python/Shell、VM validator、Gate/DR signer、bootstrap 自测、生产 context/prepare、祖先断言、奖励 postflight、备份和清理入口共同支持 269，未知 270 拒绝。新迁移持久化账号与当前 Key 的统一状态、跨组事实索引，不写余额、计费或错误重试配置，不保存原始请求正文、认证头或凭据。

本地最低验证包括 `test_profile269_release_contract.py`、缓存核心与调度/API 回归、前端设置/状态测试、双语键完整性、typecheck/lint/build、完整 release suite、修改 Shell 语法及 `git diff --check`。本地登记不证明 PostgreSQL 实际升级、旧镜像兼容、VM Gate 或生产上线；恢复敏感最终 blob/模式重新精确分类，未分类继续阻断，不增加新的审阅豁免。长期产品合同见 `.wiki/03-模块指南/14-上游缓存保护.md`。


## 调度保护性能优化合同

历史登记的 profile271，parent270，版本 `0.2.16-baiyu`，仅新增 `299_upstream_cache_guard_revisions.sql`；历史270/298和此前profile、SQL、checksum原样保留，当时未知272拒绝；现行273/274边界见末节。核验 `scheduling-protection-performance` 扩展：不可变策略 revision、按身份轻量版本刷新、兼容旧写入的事实/pending trigger、活跃会话冷加载、精确复用失效边界、100ms 保护预算、账号→会话→配额锁序、最多4个账号完成 worker 及批量管理读取。5s/30s、TTFT15m、试用与恢复口径和发送前硬约束不得因优化改变。

新增最低本地验证包括 profile271/迁移299合同、冻结原算法差分、时间边界/并发/冷加载故障、SQL锁序、singleflight授权与取消、前端刷新竞态，以及同机微基准。微基准须标明只覆盖的热点，不能冒充数据库扫描行数、端到端延迟或完整叠加载荷；实际 PostgreSQL升级/旧镜像兼容/正式Gate仍按 production-deploy 执行，dirty本地实现不作为发布证据。


当前 pending profile 273 为 `0.2.17-baiyu`、parent 272，仅新增 `300_upstream_health_history_index.sql`。272 及更早 profile、SQL、checksum 原合同不变，未知274拒绝。审计新增 `upstream-account-table-refresh-performance` 扩展：健康每Key最新24条、精确confidence分流、列表内联质量、失败隔离及请求取消/结构共享，不得以TTL结果缓存、省略判断或缩短统计窗口实现性能目标。300仅增加非唯一索引，实际数据库验证和签名Gate另行证明。

## 官方 0.2.18 与 profile 275

官方目标为 `5c185c5c5d4f5e409e2139806e0964ae944e03e8`，fork 版本为 `0.2.18-baiyu`。当前 profile `275` 的 parent 为 `274`，仅新增 `302_enable_gemini_image_generation_groups.sql`；官方 `246_enable_gemini_image_generation_groups.sql` 只重编号，原始字节不变。该迁移开启存量 Gemini / Antigravity 分组的生图权限，Composite / OpenAI 不在回填范围内。274 及更早 profile、SQL 和 checksum 保持冻结，未知 276 拒绝。

275 仍要求停机并继承 migration 301 的旧写者退出与协调恢复约束；297 / 298 / 299 / 301 的祖先断言逐项校验当前 parent、新增列表和原 catalog checksum。新 profile 和 Go `1.27.3` 构建依赖必须在 Python、Shell、VM、signer、备份及清理入口一致。最终恢复敏感 blob 仍须重新分类，不扩大任何历史精确审阅豁免。

调度快照采用官方内存母本与写时复制，新增 revision 键继续使用 `sched:v2:meta:rev` / `sched:v2:meta:rev:`，保留 fork 上游元数据、优先池与最终账号 hydration。官方流式断连结算只改变已观测 usage 的记账；fork 缓存保护仍要求成功终止、完整读取和客户端输出，读错、断连及缺失终止证据不能据 usage 推定有效 warm 样本。Cloudflare 1010 的非认证阻断不惩罚账号或绑定的上游 Key。

账号编辑采用官方 Vertex service_account 配额，继续通过 fork 配置白名单过滤运行态字段。普通账号与上游管理分别使用 `account-filter-values` / `upstream-account-filter-values`，切换 scope 后重载筛选并丢弃旧请求响应。最低验证包含 profile 275 / migration 302 合同、调度快照隔离、Anthropic / Gemini 断连缓存证据、绑定 Key 的 1010、Vertex 配额和账号页筛选隔离。以上登记不代表 VM Gate 或生产部署完成。
