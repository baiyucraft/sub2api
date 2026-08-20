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
- profile 233–239、已发布 migration、checksum 和 compatibility identity 是不可变历史证据；profile 240 是当前 pending 合同。
- Fork 版本必须等于目标官方源码 `VERSION` 加 `-baiyu`。
- 报告只写入 `.tmp/fork-extension-audit/`，不得修改 tracked 文件。

## 强制读取

- 每次审计先读取 [extensions.yaml](references/extensions.yaml)。
- 准备或执行 upstream 合并时读取 [merge-workflow.md](references/merge-workflow.md)。
- post-merge 审计后读取 [regression-matrix.md](references/regression-matrix.md)，按报告列出的功能域执行最低测试。
- 进入构建、Gate、VM 或生产阶段时改用 `sub2api-production-deploy` skill；本技能不替代发布门禁。

## 官方修复优先

- 审计本地兼容性修复或 workaround 时，主动检查目标 upstream commit 是否已包含同一故障域的官方修复；只有进入目标 commit 的代码才视为官方事实，开放中的 Issue、PR 或未合并 commit 仅作为设计参考。
- 官方修复完整覆盖本地修复时，以官方实现和官方测试为基线，建议删除重复的 fork 实现；官方仅部分覆盖时，只保留可证明仍有必要的最小 fork 增量。
- 按故障域分别判断覆盖关系，不得因为官方修复了相邻问题，就回退仍在解决另一独立错误的本地修改。详细判定和测试要求见 [merge-workflow.md](references/merge-workflow.md)。

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
- `warning`：需要人工语义复核，例如高风险文件结果与某一父提交完全相同。
- `blocker`：合并身份、工作区、版本或不可变历史不可信。
- `catalog_update_required`：发现未登记 fork-only 路径或 migration。

报告中的 `required_tests` 只是最低清单。实际构建和发布仍服从 `sub2api-production-deploy` 的更严格门禁。
