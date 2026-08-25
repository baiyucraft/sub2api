# Upstream 合并审计工作流

## 合并前

1. 显式取得 `upstream/main` 的 40 位完整 SHA，记录来源，不把当前样本固化为未来目标。
2. 确认工作区无未授权改动和未解决冲突。
3. 执行 `pre-merge`，保存报告并处理所有 blocker 与目录更新要求。
4. 阅读报告的 fork-only commits、路径分组、高风险文件和最低测试。
5. 对本地 bug fix、兼容层和 workaround，检查目标 upstream commit 中是否已有对应官方修复；开放 PR、Issue 建议和未进入目标 commit 的代码只能作为参考，不能据此删除现有实现。

## 官方修复覆盖判定

1. 先按错误文本、触发路径、协议约束和受影响转发路径划分故障域，再对照官方实现与测试，不以文件重叠或相似标题代替语义判断。
2. 官方修复完整覆盖本地故障域时，以官方实现为主，删除重复 workaround，只保留 fork 业务确实需要的最小适配；记录被替换的本地提交、对应官方 commit 和验证测试。
3. 官方修复仅覆盖部分路径或边界时，保留未被覆盖的最小 fork 增量，并明确剩余不变量；不得复制整段官方实现后继续并行维护另一套同义逻辑。
4. 官方修复相邻但独立的问题时，两项修改分别保留或替换。例如 Responses `custom_tool_call.id` 的 `fc_`/`ctc_` 前缀兼容，与 HTTP bridge 重复 replay 导致 call/output 不闭环，是两个故障域；采用官方 replay 修复不能自动成为回退 ID 清洗的理由。
5. 合并后的测试至少覆盖官方新增回归、fork 原有专项回归，以及证明“已替换 workaround 不再需要”和“独立修复未被误删”的定向用例。
6. 若目标 upstream 尚未包含修复，只能记录候选 PR 和预期采用方式；不得把未合并 PR 宣称为当前官方修复，也不得仅因其存在就提前删除 fork 保护。

## 合并中

1. 创建真实 merge commit，第二父提交必须是目标 upstream SHA。
2. 对 OpenAI gateway/Responses/WebSocket/failover/usage/billing、scheduler、Redis 并发、LoadFactor、健康排除、上游 handler/service/repository/schema、Accounts/Upstream UI、migration/profile/release 资产逐文件语义合并。
3. 若官方改动触及账号管理或账号编辑，必须同步检查上游管理/编辑流程：后端结构、编辑白名单与校验、前端 modal 与提交 payload、同步后端缓存和相关回归测试必须都完成对比。官方修正要合理应用，fork 专属字段、白名单和生命周期约束必须保留。
4. 禁止对高风险文件直接整文件选择 `ours` 或 `theirs`。确有必要时记录理由、受影响扩展 ID 和专项测试。
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
