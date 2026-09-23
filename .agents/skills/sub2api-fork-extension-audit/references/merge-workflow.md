# Upstream 合并审计工作流

## 合并前

1. 显式取得 `upstream/main` 的 40 位完整 SHA，记录来源，不把当前样本固化为未来目标。
2. 确认工作区无未授权改动和未解决冲突。
3. 执行 `pre-merge`，保存报告并处理所有 blocker 与目录更新要求。
   合并前允许尚未升级目标 VERSION，但必须同时匹配 merge-base 官方版本加后缀和当前 profile；此时记录 `version_upgrade_required`。post-merge 严格要求目标官方正式版本加后缀，不允许旧版本放行。通常读取目标源码 VERSION；正式 tag 仅在构建阶段改写 VERSION 时，必须按目标 commit 使用目录中的精确 release version 覆盖。Git 文件名使用未转义 UTF-8，中文公告等独立文件必须精确登记。
4. 阅读报告的 fork-only commits、路径分组、高风险文件和最低测试。
5. 对本地 bug fix、兼容层和 workaround，检查目标 upstream commit 中是否已有对应官方修复；开放 PR、Issue 建议和未进入目标 commit 的代码只能作为参考，不能据此删除现有实现。
6. 逐项处理 `semantic_overlap_candidate`：先核对共同入口、标识符和配置证据，再阅读两侧完整实现与测试；候选本身不得作为删除 fork 代码或宣称官方覆盖的依据。

## 官方修复覆盖判定

1. 先按错误文本、触发路径、协议约束和受影响转发路径划分故障域，再对照官方实现与测试，不以文件重叠或相似标题代替语义判断。
2. 官方修复完整覆盖本地故障域时，以官方实现为主，删除重复 workaround，只保留 fork 业务确实需要的最小适配；记录被替换的本地提交、对应官方 commit 和验证测试。
3. 官方修复仅覆盖部分路径或边界时，保留未被覆盖的最小 fork 增量，并明确剩余不变量；不得复制整段官方实现后继续并行维护另一套同义逻辑。
4. 官方修复相邻但独立的问题时，两项修改分别保留或替换。例如 Responses `custom_tool_call.id` 的 `fc_`/`ctc_` 前缀兼容，与 HTTP bridge 重复 replay 导致 call/output 不闭环，是两个故障域；采用官方 replay 修复不能自动成为回退 ID 清洗的理由。
5. 合并后的测试至少覆盖官方新增回归、fork 原有专项回归，以及证明“已替换 workaround 不再需要”和“独立修复未被误删”的定向用例。
6. 若目标 upstream 尚未包含修复，只能记录候选 PR 和预期采用方式；不得把未合并 PR 宣称为当前官方修复，也不得仅因其存在就提前删除 fork 保护。
7. 语义重叠候选的人工结论只允许记录为“完整覆盖”“部分覆盖”“相邻独立”或“无法确定”。完整/部分覆盖仍需绑定实现与测试证据；无法确定时保留现有 fork 行为并继续复核。

### 先 fork 后官方的重核

当 fork 提前引入的提交后来进入固定官方目标时，先确认官方 commit 是该目标祖先，再按模型识别、协议转换、路由、计费、前端入口等故障域逐项对照两侧实现、边界条件和测试。完整覆盖项转为官方维护；部分覆盖只保留最小 fork 增量；未覆盖项继续由精确扩展 ID、路径、不变量和测试登记。原 fork 提交及作者保留在 Git 历史中，不因维护归属变化删除或改写。patch-id、共同路径和标题只提供候选证据，不代替语义结论，也不得扩大 `registered_support_paths` 等路径豁免。

- 固定目标 `a3eb7ef302961cba716dc78b39b93b60c467db0e` 包含 PR #7509 的官方 merge `4318a63bd886b1a64c49015979c61bf34eca19ff`；本地提前引入的 `f9633c4f51c15cfc7460d610e899431d0a7c1aaa`、`6bf1ab9197e747ddbdd14798f36fef4a2a8561e1` 已由官方覆盖。以官方模型、协议转换、计费和前端测试为基线，不重复维护同义实现，保留两条本地 Git 历史。
- Grok 4.7 不能认定为“官方未覆盖”：目标包含官方 `935db68517f8beef407db3da016eaef1799fce25`，本地提前引入是 `c41574a3ab1eaa05b4d9b61bec97ca87d9c782d5`。官方已有 xAI 模型与别名、Grok 路由与推理、价格 JSON/回退计费和前端白名单；仍需逐故障域验证 Codex 图像输入、OpenCode Go、runtime build 映射和相关边界测试。只将实证完整覆盖项转官方维护，未覆盖增量留 fork。

## 未正式发布批次的保留与重核

1. fork 已通过真实 merge commit 采用、但尚未进入正式 tag 的 upstream 批次，登记到 `extensions.yaml.adopted_upstream_tranches`；base、tip、merge commit、提交数和采用时 VERSION 都是不可省略的证据。
2. 批次 tip 的 `VERSION` 未变化时，fork VERSION 也不变化。例如 tip 仍为 `0.2.5` 时保持 `0.2.5-baiyu`，不得因预期下一版是 `0.2.6` 就新增版本或 release profile。
3. upstream 回退、force-push 或撤回不触发 fork 回滚，不删除提交，不重写 merge 历史；先保持现有实现和测试稳定。
4. 正式目标版本到达前，批次 `base..tip` 的变更路径临时按 upstream 所有权登记；只豁免该批次真实修改且 merge commit 后未再变化的路径，不扩大到相邻目录或后续 fork 修改。
5. 正式目标版本到达后，临时路径所有权立即失效，并对批次内每个非 merge 源提交执行三态重核：
   - 官方目标包含原提交或语义等价实现：标记为 `upstream_covered_source_commits`，后续维护跟随官方。
   - 官方目标未覆盖：标记为 `fork_retained_source_commits`，新增或绑定 fork 扩展 ID、路径、不变量和最低测试。
   - 官方只覆盖部分故障域：标记为 `partially_covered_source_commits`，采用官方实现并保留最小 fork 增量，同样绑定 fork 扩展 ID。
6. `git cherry`/patch-id 仅用于给出初步等价候选，最终归属必须结合错误文本、调用路径、协议边界和回归测试做语义确认。
7. 重核分类必须无遗漏、无重复地覆盖全部非 merge 源提交；保留/部分覆盖项绑定的扩展 ID 必须真实存在并覆盖对应提交路径。完成前 fork audit 返回 `catalog_update_required`，不得进入正式合并或发布。

## 合并中

1. 创建真实 merge commit，第二父提交必须是目标 upstream SHA。
2. 对 OpenAI gateway/Responses/WebSocket/failover/usage/billing、scheduler、Redis 并发、LoadFactor、健康排除、上游 handler/service/repository/schema、Accounts/Upstream UI、migration/profile/release 资产逐文件语义合并。
3. 若官方改动触及账号管理或账号编辑，必须同步检查上游管理/编辑流程：后端结构、编辑白名单与校验、前端 modal 与提交 payload、同步后端缓存和相关回归测试必须都完成对比。官方修正要合理应用，fork 专属字段、白名单和生命周期约束必须保留。
4. 禁止对高风险文件直接整文件选择 `ours` 或 `theirs`。确有必要时记录理由、受影响扩展 ID 和专项测试。
4. 先保留 Ent schema 与 wire 源定义的业务语义，再重新生成；生成文件不能反向覆盖源定义。
5. 官方 migration 编号与 fork 冲突时按 SQL 内容和 profile manifest 对照，不能按同名文件覆盖。
6. 本轮官方新增的 `238b_content_moderation_engine_meta.sql`、`239_channel_reasoning_effort_multipliers.sql`、`240_affiliate_ledger_operation_id.sql` 只在 fork 对应新文件重编号为 `281_content_moderation_engine_meta.sql`、`282_channel_reasoning_effort_multipliers.sql`、`283_affiliate_ledger_operation_id.sql`，SQL 字节不变。fork 已有历史 238、`239_reconcile_non_grok_video_pricing.sql`、`240_upstream_observation_preference.sql` 不回写；Go runner 和 release planner 均按完整 filename 排序并按 filename+checksum 校验已应用项，因此 282 的回填在 265 的列创建后执行。必须核对 profile、catalog 原始字节 checksum、迁移测试文件名和新库/升级库执行顺序。

## 合并后

1. 使用目标 SHA 和真实 merge commit 执行 `post-merge`。
2. 重新处理 `semantic_overlap_candidate`，确认冲突解决后的实现没有误删完整覆盖、部分覆盖或相邻独立的 fork 语义；warning 不会自动改变退出码。
3. 处理 `whole_file_resolution_suspected`，人工确认每个文件是否有明确理由和回归证据。
4. 若出现 `catalog_update_required`，先判断它是新官方内容、生成物还是新的 fork 扩展；确属扩展才更新目录。
5. 按 [regression-matrix.md](regression-matrix.md) 执行报告列出的最低测试。
6. 审计通过后，将构建、VM Gate、生产发布和恢复交给 `sub2api-production-deploy` skill。

## 失败处理

- 短 SHA、未知对象、错误第二父提交、历史 migration/profile 漂移：停止合并交付。
- 脏工作区：先辨认改动归属，不得自动丢弃。
- 未登记路径：不能仅扩大 `backend/**` 或 `frontend/**` 之类通配符掩盖差异。
- 生成文件漂移：先核对 schema/wiring 源，再按项目生成流程重生并提交。
