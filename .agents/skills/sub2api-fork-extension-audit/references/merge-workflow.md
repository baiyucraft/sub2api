# Upstream 合并审计工作流

## 合并前

1. 显式取得 `upstream/main` 的 40 位完整 SHA，记录来源，不把当前样本固化为未来目标。
2. 确认工作区无未授权改动和未解决冲突。
3. 执行 `pre-merge`，保存报告并处理所有 blocker 与目录更新要求。
4. 阅读报告的 fork-only commits、路径分组、高风险文件和最低测试。

## 合并中

1. 创建真实 merge commit，第二父提交必须是目标 upstream SHA。
2. 对 OpenAI gateway/Responses/WebSocket/failover/usage/billing、scheduler、Redis 并发、LoadFactor、健康排除、上游 handler/service/repository/schema、Accounts/Upstream UI、migration/profile/release 资产逐文件语义合并。
3. 禁止对高风险文件直接整文件选择 `ours` 或 `theirs`。确有必要时记录理由、受影响扩展 ID 和专项测试。
4. 先保留 Ent schema 与 wire 源定义的业务语义，再重新生成；生成文件不能反向覆盖源定义。
5. 官方 migration 编号与 fork 冲突时按 SQL 内容和 profile manifest 对照，不能按同名文件覆盖。

## 合并后

1. 使用目标 SHA 和真实 merge commit 执行 `post-merge`。
2. 处理 `whole_file_resolution_suspected`，人工确认每个文件是否有明确理由和回归证据。
3. 若出现 `catalog_update_required`，先判断它是新官方内容、生成物还是新的 fork 扩展；确属扩展才更新目录。
4. 按 [regression-matrix.md](regression-matrix.md) 执行报告列出的最低测试。
5. 审计通过后，将构建、VM Gate、生产发布和恢复交给 `sub2api-production-deploy` skill。

## 失败处理

- 短 SHA、未知对象、错误第二父提交、历史 migration/profile 漂移：停止合并交付。
- 脏工作区：先辨认改动归属，不得自动丢弃。
- 未登记路径：不能仅扩大 `backend/**` 或 `frontend/**` 之类通配符掩盖差异。
- 生成文件漂移：先核对 schema/wiring 源，再按项目生成流程重生并提交。
