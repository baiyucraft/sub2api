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
- profile 233–254、已发布 migration、checksum 和 compatibility identity 是不可变历史证据；profile 255 是当前 pending 合同，版本 `0.2.8-baiyu`、parent 254。254 的三项 278–280 原合同不得改写。
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

当目标差异触及 `.agents/skills/sub2api-production-deploy/**`、备份格式、migration/profile、Compose、ingress 事务或恢复状态机时，审计必须核对 `release-operations-isolation`：

- 三层门禁名称和预算固定为 `fast=0-2min`、`specialized=5-15min`、`full=20-60min`；`full` 不阻塞普通发布，但恢复链自身变化时是硬门禁。
- 触发矩阵必须覆盖 migration、PostgreSQL/Redis、Compose、backup、restore/cleanup/reconcile、ingress、发布状态机和真实恢复事故修复。
- Redis 恢复使用总键、TTL 键和永久键单调不等式，禁止恢复旧的精确差值等式。
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

报告中的 `required_tests` 只是最低清单。实际构建和发布仍服从 `sub2api-production-deploy` 的更严格门禁。
