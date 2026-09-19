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
- profile 233–253、已发布 migration、checksum 和 compatibility identity 是不可变历史证据；profile 254 是当前 pending 合同。
- Fork 版本必须等于目标官方正式版本加 `-baiyu`。通常以目标源码 `VERSION` 为准；若正式 annotated tag 的发布流程只在构建阶段写入版本、标签源码文件滞后，则必须在目录中用目标 commit 精确固定 release version，禁止按 tag 名或预期版本猜测。
- 报告只写入 `.tmp/fork-extension-audit/`，不得修改 tracked 文件。

## 强制读取

- 每次审计先读取 [extensions.yaml](references/extensions.yaml)。
- 若 `adopted_upstream_tranches` 非空，必须验证其中 base、tip、merge commit、提交数、版本和当前 HEAD 祖先关系；这些批次不得因 upstream 回退、force-push 或撤回而删除。
- 准备或执行 upstream 合并时读取 [merge-workflow.md](references/merge-workflow.md)。
- post-merge 审计后读取 [regression-matrix.md](references/regression-matrix.md)，按报告列出的功能域执行最低测试。
- 进入构建、Gate、VM 或生产阶段时改用 `sub2api-production-deploy` skill；本技能不替代发布门禁。

## 官方修复优先

- 审计本地兼容性修复或 workaround 时，主动检查目标 upstream commit 是否已包含同一故障域的官方修复；只有进入目标 commit 的代码才视为官方事实，开放中的 Issue、PR 或未合并 commit 仅作为设计参考。
- 官方修复完整覆盖本地修复时，以官方实现和官方测试为基线，建议删除重复的 fork 实现；官方仅部分覆盖时，只保留可证明仍有必要的最小 fork 增量。
- 按故障域分别判断覆盖关系，不得因为官方修复了相邻问题，就回退仍在解决另一独立错误的本地修改。详细判定和测试要求见 [merge-workflow.md](references/merge-workflow.md)。

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
