# RackNerd 一键发布

## Fork 版本命名

Fork 版本号默认使用官方源码 `VERSION` 并追加 `-baiyu`，不补写或重复 `0.1.` 前缀。例如官方源版本为 `0.176` 时，fork 版本必须是 `0.176-baiyu`。若目标 40 位 commit 被精确正式 SemVer tag（`vX.Y.Z`）指向而 tag 内源码 `VERSION` 明确滞后，可以该精确 tag 版本作为源版本覆盖；必须证明 tag 精确解析到目标 commit，禁止使用分支名、预期版本或移动 tag。release manifest 的 profile 版本通过 `--build-arg VERSION` 写入候选镜像并由 validator 复核。最终 fork 版本每变化一次，就新增下一个连续整数 profile，parent 指向上一个当前 profile；即使没有 migration 也必须创建新 profile并使用 `new_migrations=[]`。历史 profile 的版本、parent、Gate、candidate、migration 和 checksum 不回写。当前映射为 profile 242 → `0.1.183-baiyu`（历史）、243 → `0.1.184-baiyu`（历史）、244 → `0.1.185-baiyu`（历史）、245 → `0.2.0-baiyu`（历史，新增 migration 256–262，其中 260/261 为每日活动与充值来源事件，262 为额外成本审计流水）、246 → `0.2.1-baiyu`（历史，parent 245，官方 migration 232/233/234/234 原字节重编号为 263–266）、247 -> 0.2.2-baiyu (historical, parent 246, official migration 235 renumbered to 267); 248 -> 0.2.3-baiyu (historical, parent 247, official migration 236 renumbered to 268); 249 -> 0.2.4-baiyu (historical, parent 248, official MiniMax migration renumbered to 269); 250 -> 0.2.4-baiyu (historical, parent 249, account RPM migration 270); 251 -> 0.2.4-baiyu (historical, parent 250, migrations 271–273 for native Zhipu monitor mode, model-level upstream routes, and probe minimum input tokens); 252 -> 0.2.5-baiyu (historical, parent 251, migrations 274–276); 253 -> 0.2.5-baiyu (historical, parent 252, migration 277 for user group rate percentage persistence); 254 -> 0.2.7-baiyu (historical, parent 253, migrations 278–280 for fork group TTFT Guard policies, proxy IP groups, and plugin runtime state; official tag v0.2.7 overrides the lagging source VERSION for release identity); 255 -> 0.2.8-baiyu (historical, parent 254, upstream migrations 281_content_moderation_engine_meta.sql, 282_channel_reasoning_effort_multipliers.sql, 283_affiliate_ledger_operation_id.sql; official 238b/239/240 renumbered to the local sequence); 256 -> 0.2.9-baiyu (historical, parent 255, no new migrations); 257 -> 0.2.9-baiyu (retained, parent 256, migration 284_unified_proxy_bindings.sql). Profile 254–256 identity and checksums remain unchanged; the raw SHA-256 for migration 284 is de0adde07c5dd930aa1cccf9e68d6c55958fd7ea16323d746019c7c97481afe3.

历史保留 profile 261：版本 `0.2.13-baiyu`、parent 260、`new_migrations=[286_add_payment_order_bonus_amount.sql,287_add_typesafe_platform.sql]`，固定官方目标为 `b8dece9000c68815a5b867ca5a1e6f236e173905`。260 已 signed 且于 2026-10-01 生产 verified，其 `0.2.11-baiyu`、parent 259、`new_migrations=[285_upstream_null_rate_lifecycle.sql]` 及既有 Gate/candidate/checksum 保持不变；259 及以前合同不变。两个官方 `241_*` 与旧 local241 编号冲突，只将新文件原字节重编号为 286/287，旧 SQL 与数据库记录不回写。286 保存充值赠送金额，287 扩展用户平台额度与组合路由约束；TypeSafe 不进入 LLM channel monitor。catalog 使用原始字节 SHA-256，Gate catalog 继续使用 strip 后 SQL 文本 checksum。261 的本地登记和静态/unit 测试不证明 VM Gate、实际 PostgreSQL 升级、旧镜像兼容或生产发布；历史 profile 262 保持 `0.2.13-baiyu`、parent 261、仅新增 `288_upstream_confidence_distribution.sql`，用于 OpenAI Sol 单次探针的持久化 128 次分布窗口；历史 profile 263 为 `0.2.14-baiyu`、parent 262、`new_migrations=[]`；历史 profile 264 为 `0.2.14-baiyu`、parent 263、仅新增 `289_activity_reward_costs.sql`；历史 profile 265 为 `0.2.15-baiyu`、parent 264、仅新增 `290_drop_platform_check_constraints.sql`，固定官方目标为 `3a6fd1c9db07203ca308aaba69e502bc1f35b307`；265 与更早合同不变；历史保留 profile 266 为 `0.2.15-baiyu`、parent 265、仅新增 `291_user_invoice_annotations.sql`，用户级开票仅为站内金额标注，无币种、余额或充值账本写入。当前 profile 274 parent 273，同版本仅新增 301，必须 downtime 并使用 coordinated_restore；未登记275拒绝，历史269合同由目录保留。266 的新恢复敏感 blob 必须重新分类，未分类继续阻断，不新增豁免。后续 VM 合同恢复叠加后仍须按最终 blob/模式完成恢复分类审阅，未分类变更保持阻断，不新增豁免。

标准入口（启动后调用端可断开）：

```text
python .agents/skills/sub2api-production-deploy/scripts/release.py doctor --profile <profile> --commit <40位完整SHA>
python .agents/skills/sub2api-production-deploy/scripts/release.py bootstrap-production --profile <profile>
python .agents/skills/sub2api-production-deploy/scripts/release.py deploy-follow --profile <profile> --commit <40位完整SHA> --mode blue-green|downtime --lang zh-CN
python .agents/skills/sub2api-production-deploy/scripts/release.py follow <release_id> --lang zh-CN
python .agents/skills/sub2api-production-deploy/scripts/release.py deploy-start --profile <profile> --commit <40位完整SHA> --mode blue-green|downtime
python .agents/skills/sub2api-production-deploy/scripts/release.py deploy-follow --profile <profile> --commit <40位完整SHA> --mode blue-green|downtime --recovery-gate-mode full --lang zh-CN
python .agents/skills/sub2api-production-deploy/scripts/release.py status <release_id>
python .agents/skills/sub2api-production-deploy/scripts/release.py wait <release_id> --timeout 900
python .agents/skills/sub2api-production-deploy/scripts/release.py verify-result <release_id>
python .agents/skills/sub2api-production-deploy/scripts/release.py logs <release_id> --node all --tail 100
```

已消费生产发布的后置检查/VM 收口失败，使用 `finalize-consumed <release_id>`，然后 `verify-result <release_id>`。默认保持 schema 1、本次启动 VM 的正常 poweroff/vmrun 合同；原本运行的 VM 必须显式使用 `finalize-consumed <release_id> --preserve-running-vm`，以 schema 2、`mode=preserve-running` 独立收口，要求原生命周期 initially_running=true、vm_started_by_release=false、retained_failure/retained。入口持全局发布/VM 身份锁，核验原 worker 已退出、原签名 Gate 及 production result、远端 consumed/无 claim/同 candidate、双健康、全节点 doctor 和 retained owner/同 boot。独立证据绑定原事故文件字节 SHA256、当前已提交推送 helper 完整 SHA/资产；local doctor 使用 helper SHA，remote使用原应用 SHA。原 runner exit1及旧证据字节不改，不部署、迁移、重发、消费或恢复数据；只清本 release 隔离任务。显式分支核对同 boot并保留运行，checks 与 VM 完成证据写入验真后，released owner 作为生命周期最后标记，随后写 finalization-result 汇总；成功幂等只验真，保留分支重放包含 VM 的全节点 doctor。未知 schema/mode、跨模式、失败证据/身份漂移均拒绝。

## Profile 268 精确恢复审阅

本批经用户明确授权，固定生产 BASE `9bf9801a40c4e6732fa81fedbf6ad52f2fa06c62` → 已审 TARGET `bd00d35ebc0b4182070510375ee6878101cfbc7a`。三个互不重叠的冻结路径组共 41 条；以下路径均相对 `.agents/skills/sub2api-production-deploy/scripts/`：

| 冻结集合 | 数量与范围 | 分类理由 |
| --- | --- | --- |
| `_PROFILE268_COMPATIBILITY_PATHS` | 35 项 profile 兼容接线；完整闭包见 [分类与构建](../../references/change-classification-and-build.md) | `reviewed_profile_compatibility_changed` |
| `_PROFILE268_POSTFLIGHT_PATHS` | 2 项：`maintenance/release/activity-reward-cost-postflight.sh`、`maintenance/release/switch.sh` | `reviewed_reward_cost_postflight_changed` |
| `_PROFILE268_FINALIZATION_REVIEW_PATHS` | 4 项：`release/cli.py`、`release/finalization.py`、`release/supervisor.py`、`release/vm_lifecycle.py` | `reviewed_consumed_finalization_changed` |

第三组是独立 consumed-release 后置 doctor/VM 收口审阅，不能归为 profile-only。三组匹配同路径 BASE/TARGET 的 Git blob 和文件模式；未来内容、模式、删除、改名、额外路径及生产基线漂移仍拒绝，不是目录、祖先或自动豁免。最终提交重新分类，未来批次仍需实际审阅及负向测试，不能自动学习候选。

PostgreSQL/Redis 协调恢复算法、备份格式和信任根不在本批改动范围。仍强制 `specialized` 当前 helper 的真实 PostgreSQL/Redis/Compose 隔离恢复、旧 image 兼容及正式签名 Gate；profile 268 保持 `0.2.16-baiyu`、parent 267、仅 `296_upstream_cache_guard.sql`，旧 profiles、SQL/checksum 不回写，未知 269 拒绝。最低登记回归为 `scripts/tests/release/test_profile268_recovery_review.py`，路径相对 skill 根；登记不代表回归、VM Gate 或生产部署完成。

## Profile 269 精确恢复审阅

本次经用户确认的实际生产 BASE `c4d857235cecdc3458c21b5eec9411f1fd115674` → 已审 TARGET `f14ca8f65e3526370bf24a2fa0bca3ec490448ab`，其 41 条恢复敏感净变更按同路径 Git blob 与模式冻结；权威对象身份在 `release/recovery_gate.py`，本地审阅材料另见 `.tmp/account-cache-guard/deployment-sensitive-review.md`，固定路径与祖先断言闭包见 [分类与构建](../../references/change-classification-and-build.md)。路径均相对 skill 的 `scripts/`：

| 冻结集合 | 范围 | 分类理由 |
| --- | --- | --- |
| `_PROFILE269_COMPATIBILITY_PATHS` | 35 条 profile 269 接线，18 条固定路径加 17 条祖先断言 | `reviewed_profile_compatibility_changed` |
| `_PROFILE269_POSTFLIGHT_PATHS` | 2 条 `maintenance/release/activity-reward-cost-postflight.sh`、`maintenance/release/switch.sh` | `reviewed_reward_cost_postflight_changed` |
| `_PROFILE269_FINALIZATION_REVIEW_PATHS` | 4 条 `release/cli.py`、`release/doctor.py`、`release/finalization.py`、`release/vm_lifecycle.py`，不含 `supervisor.py` | `reviewed_consumed_finalization_changed` |

第三组包含显式 preserve-running VM 后置收口及 doctor 谓词检查，不能作为 profile-only。第二组维持旧写者停止后执行与失败协调恢复。profile 268 既有冻结 BASE/TARGET 及包含 `supervisor.py` 的不同四路径组不变；269 不继承历史审阅。BASE、任一同路径 blob/mode、删除、改名或额外敏感路径漂移均拒绝；显式 full 不豁免未知变更。净差异未修改协调 restore、快照、备份格式或信任根，但仍强制 `specialized` 的真实 PostgreSQL/Redis/Compose 隔离恢复、旧镜像兼容及签名 Gate。最低回归 `scripts/tests/release/test_profile269_recovery_review.py`，路径相对 skill 根；登记不等于 VM Gate 或生产上线通过。

## 可选 VM 自动启停

Profile 265 精确恢复审阅绑定 `aeb410bccc524164bdafbd3db865d4f5f588aced` → `6f5daaaed6e0cbfae2011cfa55b47688f33634eb`：35 项接线与两项停机奖励 postflight 分别冻结，不包含未修改的 `production.py`。同路径 blob、mode 或基线漂移继续阻断；仍执行 specialized 当前 helper 真实隔离恢复，登记不代表 Gate 或部署完成。

Profile 265 候选构建与失败租约的第三批精确审阅绑定 `aeb410bccc524164bdafbd3db865d4f5f588aced` → `99d9fcf97125ef6efc6543692697e85e27412396`，理由为 `reviewed_vm_preflight_recovery_changed`，独立集合 `_PROFILE265_BUILD_RECOVERY_REVIEW_PATHS` 仅含 `release/cli.py`、`release/supervisor.py`、`release/vm-validate.sh`。审阅覆盖这三项完整净 blob：显式 candidate-build 收口、持锁 audit-only 前后核验及零正式状态写、失败证据保留与原 owner 最后提交；validator 同时包含 265 白名单/版本/parent/迁移 290 接线、奖励 postflight 的 264|265 条件及 Go 1.27.2 对齐，不能视为只有 Go pin。旧 35 项兼容和两项 postflight 审阅的 SHA 与路径集保持不变，三组取并集为 39 条净敏感路径。未来 blob、mode、删除、改名和基线漂移，以及未登记的 restore、ingress、信任根等变更仍阻断；显式 full 不能绕过。登记不代替真实专项恢复、完整门禁或生产验真。

Profile 265 镜像代理例外的第四批精确审阅绑定 `aeb410bccc524164bdafbd3db865d4f5f588aced` → `67545c6d6e02f5210574ced9ea5c366688d28b87`，理由为 `reviewed_vm_preflight_recovery_changed`。独立集合 `_PROFILE265_REGISTRY_PROXY_REVIEW_PATHS` 仅含 `scripts/release/vm-validate.sh`，锁定完整净 validator 的 `100755:49dbe1513aafe049a76c5ae7425ae5f8824d73eb` → `100755:242622e8aa9db7d3a911b47031540d4acdc6eef3`；审阅包含既有 265/290 接线、奖励条件、失败收口与 Go 1.27.2 合同，以及仅对构建子命令添加镜像／匿名令牌两个精确主机例外。旧 35+2 批与 `99d9fcf97125ef6efc6543692697e85e27412396` 三路径批完全不变，净敏感路径并集仍为 39。未来 blob、mode、删除、移动、基线漂移与未登记 restore、ingress、trust 均阻断，显式 full 不绕过；登记及本地 Bash 回归不证明镜像在线可达、新 VM Gate、真实专项恢复或生产发布通过。

在 `.ssh.local` 顶层、与 `servers` 并列的公共配置为：

```yaml
vm_lifecycle:
  enabled: true
  vmrun_path: 'C:/MProgram/VMware/vmrun.exe'
  vmx_path: 'D:/vmu/Ubuntu.vmx'
  startup_timeout_seconds: 180
  shutdown_timeout_seconds: 180
```

未配置保持旧行为；配置结构、类型、路径或 timeout 错误 fail-closed。后台应用/插件发布 worker 在本地检查和全局锁后、首次 VM SSH/API 连接前取得跨 checkout VM 身份锁并核对持久所有权，通过 `vmrun list` 判断电源；已停止时 `vmrun start <vmx> nogui` 并有界等待 SSH 就绪。原本运行的 VM 始终保留，不记为本 release 启动。

只有本次启动且持久所有权一致，才在 signed `verify-result` 与 post-deploy doctor（插件为 `plugin-verify-result` 和逐实例版本/binary SHA/Health/受管范围验证）及本 release VM 隔离临时任务清理全部成功后，通过 SSH 正常 `poweroff`，再以 `vmrun list` 核实停止。关机不依赖 VMware Tools，禁止 hard stop。失败、blocked、恢复旧版本或崩溃保留 VM，遗留所有权不自动接管。

`vm_power_status`、`vm_started_by_release`、`vm_cleanup_status` 是新增状态白名单；电源与 cleanup 不替代生产验真。关机或停止核实失败独立报告 cleanup 失败，不能回滚、重发、重跑迁移或重复插件升级。VMX 路径、所有权全文和原始回包不得进入状态输出。

普通 `doctor/status/wait/follow/verify-result` 与插件对应只读入口不自动开关 VM；worker 的 postdoctor 在关机前执行，VM 已停止后的 doctor Guest 检查会失败。`deploy-follow`/`plugin-deploy-follow` 关闭仅结束观察，不影响 worker。独立 `vm-validate`、VM-only 验证/展示不自动关闭 VM 或持久展示容器；临时任务清理不得删除持久数据、展示、Gate 或失败证据。锁与恢复细节见 [release-runner-lifecycle.md](../../references/release-runner-lifecycle.md)。

## 三层发布门禁

三层门禁叠加到现有变更分类，不替代 VM Gate、签名 Gate、备份或生产验收：

| 层级 | 预计额外时间 | 触发规则 |
| --- | --- | --- |
| `fast` | 0–2 分钟 | 每次实际发布；完整 SHA、分类、语法、引用、helper bundle 和定向快速检查 |
| `specialized` | 5–15 分钟 | migration、PG/Redis、Compose、backup、restore/cleanup/reconcile、ingress 或发布状态机变化 |
| `full` | 20–60 分钟 | 至少每 30 天完整演练；恢复算法、备份格式、信任链变化或真实恢复事故修复时发布前强制 |

`full` 逾期不阻塞普通发布，但必须报告 `overdue`；只有恢复算法、备份格式、信任链变化或真实恢复事故修复时，未通过 `full` 不得进入生产。精确审阅的 profile 白名单、版本和入口兼容扩展走 `specialized`。时间预算不是 timeout，不授权杀死仍在推进的 runner。

默认 `--recovery-gate-mode auto` 根据生产完整 commit、最终 diff 和 pending migration 分类。恢复关键文件的未知变化为 `full/recovery_change_requires_review` 并阻断；已审阅兼容批次必须逐路径匹配旧/新 Git blob 与文件模式，才降为 `specialized`，额外修改即失效。显式 `--recovery-gate-mode full` 会执行并签名完整门禁的 VM 恢复部分，但不会自动完成生产级故障注入、独立 `drill_id`、RTO 测量或灾备演练收口，也不能绕过自动阻断；这些仍按备份恢复运维步骤单独执行。

协调恢复命令链为：

```text
python .agents/skills/sub2api-production-deploy/scripts/release.py reconcile-inspect <release_id>
python .agents/skills/sub2api-production-deploy/scripts/release.py reconcile <release_id> --mode coordinated-recover
python .agents/skills/sub2api-production-deploy/scripts/release.py status <release_id>
python .agents/skills/sub2api-production-deploy/scripts/release.py wait <release_id> --timeout 900
python .agents/skills/sub2api-production-deploy/scripts/release.py verify-recovery-result <release_id>
```

`verify-result` 只验证候选上线；`verify-recovery-result` 只验证恢复旧版本。不得放宽或复用 `verify-result` 接受 recovered release。

恢复 orchestrator、restore、cleanup 和 reconcile 作为同一 helper bundle 记录完整 commit 与 SHA-256。恢复按 `postgres_restored`、`redis_restored`、`compose_restored`、`app_healthy`、`nginx_restored`、`backup_units_restored`、`claim_reconciled`、`state_cleanup` 幂等续跑；已完成阶段复核后跳过，不能因 cleanup 中断而重复恢复数据库。

独立 `.s2plugin` 发布入口：

```text
python .agents/skills/sub2api-production-deploy/scripts/release.py plugin-deploy-follow --commit <40位完整SHA>
python .agents/skills/sub2api-production-deploy/scripts/release.py plugin-deploy-start --commit <40位完整SHA>
python .agents/skills/sub2api-production-deploy/scripts/release.py plugin-status <plugin_release_id>
python .agents/skills/sub2api-production-deploy/scripts/release.py plugin-authorize <plugin_release_id>  # 仅兼容恢复旧检查点
python .agents/skills/sub2api-production-deploy/scripts/release.py plugin-follow <plugin_release_id>
python .agents/skills/sub2api-production-deploy/scripts/release.py plugin-wait <plugin_release_id> --timeout 900
python .agents/skills/sub2api-production-deploy/scripts/release.py plugin-verify-result <plugin_release_id>
python .agents/skills/sub2api-production-deploy/scripts/release.py plugin-rollback-start <plugin_release_id>
```

插件 runner 从 `.ssh.local` 的 `plugin_signing.private_key` 与 `plugin_signing.key_id` 读取签名身份；Codex STATE 的 key ID 固定为 `baiyu-codex-state-v1`，私钥及其派生公钥必须匹配宿主内置且只绑定该插件 ID 的信任根，并且私钥必须位于工作区和 `.tmp/plugin-releases` 之外。从 `plugin_admin.vm.api_key` 与 `plugin_admin.production.api_key` 读取管理员 API Key。后台 worker 自动完成 VM 和生产 loopback 管理 API 操作，API Key 仅通过不落 raw log 的 SSH stdin 传入；`plugin-authorize` 仅用于兼容恢复旧版停在写检查点的 release。生产端先校验 `/opt/sub2api/active-app` 并优先操作当前 active slot；VM 升级前按旧版本和 runtime binary SHA 精确匹配、重新验签回滚包。首次安装不启用插件，升级保持原状态和受管范围。

同版本不同二进制的目标包与回滚包通常同名，上传到同一临时目录时必须用 `candidate.s2plugin` 和按旧包 SHA-256 命名的 `rollback-*.s2plugin` 分开暂存。VM Gate 必须检验实际升级后的二进制 SHA，而非只比较插件版本。

VM 插件管理 API 地址从 `.ssh.local` 的 `servers.local_vm.host` 解析，固定访问该主机的 `8211/api/v1`；不能使用 VM 自身的 `127.0.0.1:8211` 假设，因为 `sub2api-dev` 可能只通过 LAN 监听而没有 Docker host port binding。生产仍在 RackNerd 内按 active/standby loopback 端口解析，不受该规则影响。

仅用于本地 VM `sub2api-dev:8211` 展示的隔离入口：

```text
python .agents/skills/sub2api-production-deploy/scripts/release.py vm-only-validate --profile 261 --commit <40位完整SHA>
python .agents/skills/sub2api-production-deploy/scripts/release.py vm-only-switch --profile 261 --gate .tmp/releases/<release-id>/vm-only-gate
```

该入口只连接 `local_vm`，不读取生产快照、不连接 DMIT、RackNerd 或备份机；
`vm-only-switch` 只允许替换 `sub2api-dev` 的 `8211` 监听和
`/opt/sub2api-deploy/data-dev` 数据目录。它生成的 Gate 不被生产发布入口接受。
独立验证和展示完成后保留 VM 与持久展示容器，不触发发布成功自动关机分支。

生产部署必须显式选择 `--mode blue-green` 或 `--mode downtime`。非交互执行缺少 mode 会在创建 release 前失败；交互终端会询问部署模式。

日志保留清理先生成计划，再绑定同一个 checksum 执行：

```text
python .agents/skills/sub2api-production-deploy/scripts/release.py retention --mode dry-run
python .agents/skills/sub2api-production-deploy/scripts/release.py retention --mode apply --plan-sha256 <plan_sha256>
```

生产空间维护不进入应用发布流程。只对已经 terminal verified 的 release 使用以下两阶段入口；apply 必须绑定同一次 dry-run 输出的计划 checksum：

```text
python .agents/skills/sub2api-production-deploy/scripts/release.py cleanup-production <release_id> --mode dry-run
python .agents/skills/sub2api-production-deploy/scripts/release.py cleanup-production <release_id> --mode apply --plan-sha256 <plan_sha256>
```

生产恢复点使用独立的 retention 入口，不与镜像或 candidate 清理混用：

```text
python .agents/skills/sub2api-production-deploy/scripts/release.py cleanup-production-recovery-points --mode dry-run
python .agents/skills/sub2api-production-deploy/scripts/release.py cleanup-production-recovery-points --mode apply --plan-sha256 <plan_sha256> --cutoff-epoch <cutoff_epoch>
```

该入口只处理 `/opt/sub2api/backups/release-state` 中结构完整、checksum 通过、已进入
`consumed` 或 `recovered` 终态的历史恢复点。默认保留最近 3 天全部有效恢复点；窗口内不足
3 个时补足最近 3 个。当前/上一发布、reconciliation、状态不明以及活动 claim 始终保护。
dry-run 生成候选 ID、容量、`cutoff_epoch` 和 `plan_sha256`；apply 必须携带同一 cutoff 和 checksum，候选漂移即停止。
它不会删除 PostgreSQL、Redis、release 目录、candidate、Gate 或 Docker 镜像。

备份机历史 release bundle 清理也必须先 dry-run，再用同一 `plan_sha256` 和逐个明确批准的 release ID apply：

```text
.agents/skills/sub2api-production-deploy/scripts/release/backup-release-retention-clean.sh dry-run
.agents/skills/sub2api-production-deploy/scripts/release/backup-release-retention-clean.sh apply <plan_sha256> <release_id> [release_id...]
```

该脚本只处理 `/srv/sub2api-backups/releases` 中满足校验、无指针引用、无恢复/协调标记且 checksum 完整的历史 bundle。默认 `KEEP_LATEST_PROFILE_ONLY=true`、`KEEP_RECENT_RELEASES=1`：仅保留最高版本 profile 的最新一个 bundle，其余历史 profile 和同一 profile 的旧 bundle 进入候选，不受 30 天窗口限制；`candidates`、`promotion-input`、`verified-bundles` 等元数据目录会跳过，candidate/verified/recovery/baseline 等指针始终保护。需要恢复旧的按 profile 保留策略时，显式设置 `KEEP_LATEST_PROFILE_ONLY=false`。apply 不接受未出现在同一次 dry-run 候选清单中的 release ID。

该命令保留 current、pre-switch、所有容器引用和 recovery point 镜像；残留 migration 容器只报告不删除。除旧镜像外，生产清理器默认保留最近 3 天内全部 `candidate.tar.gz`，若不足 3 个则补足最近 3 个；当前 release、带 `.recovered`/`.reconciliation` 标记和非单链接归档不进入候选。镜像保护集合仍独立保护 current、pre-switch 和 recovery point 的 image。apply 只删除 dry-run 计划中的归档文件，不删除 release 目录、Gate、marker、volume、数据库或 Redis。旧镜像仍以容量边界 `max-used-space=2gb,reserved-space=2gb` 执行一次 BuildKit LRU GC。禁止 volume/image/system prune，实际释放只看 `df` 前后差值。

`deploy-follow` 是日常人工发布入口：它只创建一个隐藏 worker，并在同一控制台用中文显示阶段变化和长阶段心跳。关闭窗口或观察器超时不会中止 runner，重新连接必须使用 `follow <release_id>`。`deploy-start` 仍是机器接口，可用于脚本或只启动后台 runner。

发布过程只验证 direct 与 DMIT 两条 HTTPS `/health` 路由，以及候选容器自身健康；不读取账号池或 Canary 凭据，不发送模型或管理 API 请求，也不生成 usage attribution。流式响应与 usage attribution 明确记录为 `not_checked`。worker 在 doctor 到最终收口期间持有同一把 OS 锁；调用端关闭 stdout 或超时不会中止 runner。

发布恢复点仅在受限上传阶段明确失败时，才以新的 transport 名称重新生成并重试最多两次；数据库、Redis、配置、归档或加密阶段失败仍立即停止。每次失败的阶段、退出码和尝试次数进入 `production-result.json`，协调恢复不得抹去该诊断证据。

迁移与候选启动阶段使用不含凭据的原子 stage marker。`switch.sh` 失败时，runner 必须在协调恢复前把最后完成的阶段写入 `production-result.json`；恢复仍按原合同清理远端明文 marker，不因诊断而保留额外敏感状态。

`doctor` 和 `bootstrap-production` 可独立用于排查或首次初始化。日常只需执行 `deploy-start`：
它先检查本地、VM 与外部节点，再幂等执行生产 bootstrap，最后检查 RackNerd；任何
预检失败都不得进入 Gate、停写或迁移。

本次发布后的强制经验：

- VM 空间清理必须在目标 commit 已同步到 `/opt/sub2api-src` 后解析 profile 兼容镜像；源码落后时先同步 `origin/main`，无法证明兼容 tag 时停止清理。
- 历史兼容 Candidate 导入只用于 VM 回滚 smoke，必须独立验签并核对 archive、release asset、migration map、commit 和 image ID；不修改历史 Gate，不把它当作当前生产 Gate。
- SSH banner/EOF/transport 在建立连接阶段做有限重试；RackNerd 固定经“DMIT SSH -> 10.77.0.1:1080 HTTP CONNECT”访问并使用 10 秒 keepalive，不允许回退到本机 SOCKS、旧 8888 或直连。RackNerd 的命令、doctor、SFTP 镜像/恢复包传输均经过同一链路。纯只读 `doctor` 综合探针遇到连接中断或 `exit_code=-1` 时可有限重放。大文件上传和下载统一使用 8 个可续传分片、最多同时 8 条 SSH 连接；分片失败保留远端/本地检查点并按未完成分片恢复，RackNerd 下载仍使用每片有界预取。任何发布写命令已开始或状态不明时，仍必须先核对 release 目录、`.active-release`、committed marker 和 checksum，禁止自动重放。
- 所有 SFTP 传输重试、分片保留和清理事件必须带 `stage=transfer`，避免日志记录异常覆盖原始传输错误。
- claim 前失败只能在远端 release 目录与 `.active-release` 都明确不存在时使用新 commit、新 Gate、新 release ID 重试；失败 release 证据保留。
- 成功发布必须执行 `verify-result` 和 post-deploy `doctor`，确认运行 image、claim、备份 units、迁移和 direct/DMIT 双链路；收口后删除一次性诊断脚本，保留 release/Gate/checksum 证据。

该命令固定执行以下顺序：

```text
VM 唯一构建 candidate
  -> fast: VM 本地隔离 PostgreSQL/Redis/data-dev 候选检查
  -> specialized/full: 额外恢复真实生产 PostgreSQL/Redis 快照并验证旧镜像兼容
  -> VM 签名 Gate
  -> RackNerd 验签并导入同一镜像
  -> 在线保存 release state、持久 mask、协调恢复点和异地校验
  -> 唯一 migrate-only、备用端口候选启动
  -> Nginx upstream 原子切换与 graceful reload
  -> direct/DMIT 双链路 `/health`、旧 SSE/WebSocket 最长排空 60 分钟
  -> 旧 slot 停止后激活独占后台任务、恢复备份 units、消费 Gate
```

正常发布不停止 Nginx。active slot 记录在 `/opt/sub2api/active-app`，端口在 `18080/18081` 之间交替；候选启动时主动探针、同步、cron、quota flusher 等任务由 activation marker 暂停，旧 slot 停止后再接管。具备 Redis fencing 和分布式清理锁的调度快照服务是唯一预激活例外，用于在候选公开前消费 migration outbox 并完成运行时一致性断言；它不会把候选的后台 ready 状态提前置为 true。排空超时或连接状态不确定时会优先 reload 回旧 upstream，不强杀旧连接。协调数据恢复仍是停机流程，蓝绿不能消除非兼容 migration 的锁和回滚风险；schema 变更应优先采用 expand/contract。

profile 194 会在 manifest 中固定记录 profile 192 的完整 migration 列表及新增的
`193-194`，并保存有序 checksum；已执行过的迁移允许原样跳过，缺失迁移必须逐项
应用并逐项校验。本 profile 继续使用协调恢复，不采用 image-only rollback。

profile 195 在此基础上加入 `195_upstream_scheduling_monitor_rates.sql`。停写后、迁移前
必须生成存量倍率重算计数和 migration plan SHA-256，并要求 `unproven/conflict/unexpected`
均为零；迁移后必须核验 Key、账号、优先级、负载、分组快照和 scheduler outbox。

profile 197 继承 profile 195 的倍率迁移语义和全部 Gate 证据，并追加
`196_ops_ingress_reject_aggregates.sql` 与 `197_auth_cache_invalidation_outbox.sql`。
两项新增迁移的 checksum 会进入同一 manifest；既有 profile 195 的版本和迁移合同不变。

profile 198 继续使用版本 `0.1.162-baiyu`，继承 profile 197 的全部迁移和 Gate 证据，追加
`198_normalize_managed_monitor_key_names.sql`。该迁移只更新未删除托管监控 Key 的显示名称为
`监控-渠道名称`，并将 Key 名称列扩展到可容纳 100 字渠道名称和前缀；不改变 Key 字符串、ID、额度、usage 或费用历史。已删除监控保留的 tombstone Key 不参与修正。
VM Gate 和生产切换都会核验列长度为 103，且所有存活托管 Key 与关联监控名称完全一致。

profile 199 使用版本 `0.1.163-baiyu`，继承 profile 198 的全部迁移和 Gate 证据，追加
`199_group_reasoning_effort_policy.sql`。该迁移只增加分组 reasoning effort 上限和精确映射
字段，使用 `ADD COLUMN IF NOT EXISTS` 与稳定默认值；VM Gate 还会用旧镜像启动 smoke
核验迁移后的 schema 兼容性，并验证两个字段的类型、非空约束和默认值。

profile 202 使用版本 `0.1.164-baiyu`，完整继承 profile 199 的 migration、旧镜像兼容和
Gate 合同，并依次追加 `200_alipay_mobile_precreate_deep_link.sql`、
`201_group_auth_cache_image_generation.sql` 与 `202_composite_model_routes.sql`。生产 preflight
分别记录 200/201/202 的 `absent` 或 `verified` 状态；VM 与生产 postflight 必须验证支付宝
移动端开关、分组生图权限的鉴权缓存失效，以及组合模型路由表的约束、索引和级联外键。
任一单项 checksum 或语义证据不一致都停止，已验证的历史迁移不得重跑。

profile 206 使用版本 `0.1.165-baiyu`，完整继承 profile 202 的 migration、旧镜像兼容和
Gate 合同，并依次追加 `203_add_usage_log_session_id.sql`、
`204_allow_live_usage_request_type.sql`、`205_add_group_allow_live.sql` 与
`206_add_users_email_alias_dedup_index_notx.sql`。生产 preflight 分别记录 203/204/205/206 的
`absent` 或 `verified` 状态；VM 与生产 postflight 必须验证两个 nullable `session_id` 列、
Live request type 约束、`groups.allow_live` 非空默认值，以及邮箱别名并发索引的 valid/ready、
表达式、predicate 和 `text_pattern_ops`。任一单项 checksum 或语义证据不一致都停止。

profile 207 使用版本 `0.1.166-baiyu`，是 profile 206 的纯版本继承：migration map、逐项
`migration_203_status` 至 `migration_206_status`、旧镜像兼容和全部 Gate 语义证据保持完全一致。
本 profile 不新增 migration SQL，也不存在 `migration_207_status`；DR evidence 仍绑定继承后的
完整有序 migration map。

profile 208 使用版本 `0.1.168-baiyu`，完整继承 profile 207 的 migration map、旧镜像兼容和
全部 Gate/DR 合同，并追加 `208_passkey_credentials.sql`。生产 preflight 独立记录
`migration_208_status` 的 `absent` 或 `verified` 状态；VM Gate 与生产 postflight 必须验证
`passkey_user_handles`、`passkey_credentials` 的关键列、主键、唯一约束、到 `users` 的级联外键，
以及 `passkey_credentials_user_id_idx`、`passkey_credentials_last_used_at_idx` 两个索引。
profile 207 的纯版本继承身份保持不变，不得把 208 migration 回填到 207。

profile 209 继续使用版本 `0.1.168-baiyu`，完整继承 profile 208 的 migration map、Passkey 证据、
旧镜像兼容和全部 Gate/DR 合同，并追加 `209_user_usage_aggregation.sql`。生产 preflight 独立记录
`migration_209_status` 的 `absent` 或 `verified` 状态；VM Gate 与生产 postflight 必须验证按用户
小时/日聚合表、永久日聚合、回填状态单例、到 `users` 的级联外键及用户时间索引。profile 208
的 migration map、版本和 checksum 身份保持不变。

profile 210 使用版本 `0.1.169-baiyu`，是 profile 209 的纯版本继承：完整保留 28 项 migration、
`migration_208_status`、`migration_209_status`、Passkey、用户永久用量聚合、旧镜像兼容和全部
Gate/DR 证据。本 profile 不新增 migration SQL，也不存在 `migration_210_status`。

profile 212 使用版本 `0.1.170-baiyu`，完整继承 profile 210 的 28 项 migration，并追加
`211_group_profit_control.sql` 与 `212_group_profit_control_auth_cache_invalidation.sql`。发布链单独
记录 `migration_211_status`、`migration_212_status`，验证利润字段 schema、包含生图权限的完整
auth-cache 触发器语义，以及 profile 210 旧镜像在迁移后 schema 上的 health/auth 兼容性。

profile 213 使用版本 `0.1.171-baiyu`，是 profile 212 的纯版本继承：完整保留 30 项 migration、
`migration_211_status`、`migration_212_status`、利润控制 schema、鉴权缓存触发器、旧镜像兼容及
全部 Gate/DR 证据。本 profile 不新增 migration SQL，也不存在 `migration_213_status`。

profile 215 使用版本 `0.1.172-baiyu`，完整继承 profile 213 的 30 项 migration，并追加
`214_add_usage_log_upstream_response_model.sql` 与 `215_add_usage_log_upstream_model_mismatch_index_notx.sql`。
发布链独立记录 `migration_214_status`、`migration_215_status`，验证两个 nullable Usage Log 字段、
并发 partial index 的 valid/ready 状态、列顺序和 predicate。旧镜像兼容 smoke 沿当前 commit 的
first-parent 找到本次 upstream merge，再固定使用该 merge 第一父对应的 `0.1.171-baiyu` full-SHA Candidate；该保留镜像缺失或版本不符时 Gate 立即停止，不能退回
当前 `sub2api-dev` 的任意历史镜像代替。

profile 232 使用版本 `0.1.173-baiyu`，完整继承 profile 215 的 32 项 migration，并按顺序追加
`216_channel_monitor_v2.sql` 至 `232_clear_non_grok_video_generation_config.sql`，共 49 项。发布身份显式固定
`compatibility_version=0.1.172-baiyu`、`compatibility_commit=74e47e67205084750ccd994c331ead328e4ce35b`
和兼容 image ID，不再通过 first-parent 猜测回滚镜像。Gate 与生产 preflight 独立记录
`migration_216_status` 至 `migration_232_status`，验证 Channel Monitor V2 的表、主键、约束、索引、权限、
隐私默认值及媒体价格 schema。迁移 232 必须在停写后生成数据计划，绑定协调恢复点，验证备份表行数/hash、
Grok/Composite 保护集、媒体鉴权缓存触发器和清理后零残留；回滚必须协调恢复 PostgreSQL、Redis、配置和旧镜像。

profile 233 保持版本 `0.1.173-baiyu`，完整继承 profile 232 的 49 项 migration，并追加唯一的
`233_upstream_management.sql`，共 50 项。该迁移合并尚未生产的上游账号 Key 唯一约束、健康观测历史、
运行角色权限和账号绑定触发器语义，保留 profile 232 的显式旧镜像兼容身份。迁移前必须确认未删除账号
不存在重复 `upstream_key_id`；迁移后必须验证部分唯一索引、健康观测表的 19 个关键列、
`(upstream_key_id, observed_at)` 索引以及触发器不再派生或覆盖 `load_factor`。Gate 同时要求
`migration_233_status`、`migration_233_preflight_verified` 与 `migration_233_postflight_verified`。

profile 234 使用版本 `0.1.175-baiyu`，是 profile 233 的纯版本继承：完整复用同一 50 项
migration map、checksum 与显式旧镜像兼容身份，不新增、重编号或改写任何 migration。
它用于记录合并官方 `0.1.175` 代码后的 fork 发布身份。

profile 235 使用版本 `0.1.176-baiyu`，完整继承 profile 234 的 50 项 migration map、checksum
和显式旧镜像兼容身份，并追加重编号后的 `234_group_model_pricing.sql`，共 51 项。该迁移来自
后续官方分组模型定价能力；由于本 fork 已占用 migration 221，不能保留官方冲突编号。
生产 preflight、VM Gate 和 postflight 必须独立验证 `groups.long_context_pricing_enabled` 与
`groups.model_pricing` 的类型、默认值和可空语义。profile 233/234 的历史 manifest 保持不可变。

profile 236 使用版本 `0.176-baiyu`，是 profile 235 的纯版本继承：完整复用同一 51 项
migration map、checksum 与显式旧镜像兼容身份，不新增、重编号或改写任何 migration。
它用于记录上游配置与上游管理拆分为两个一级侧栏入口的 UI 发布身份。

profile 237 使用版本 `0.1.177-baiyu`，完整继承 profile 236 的 51 项 migration，追加
`235_group_usage_daily_rollups.sql` 与 `236_group_usage_rollup_timezone.sql`，共 53 项。
这两个文件是官方 222/223 迁移按本地序列重编号后的版本，SQL 语义保持不变；profile 233–236
的历史 Gate、candidate、checksum 和兼容身份保持不可变。

profile 238 继续使用版本 `0.1.177-baiyu`，完整继承 profile 237 的 53 项 migration，追加
`237_image_cost_routing.sql`，共 54 项。该迁移只增加分组图片成本路由配置列和 auth-cache
失效触发器字段，不修改普通文本调度、用户侧图片售价或历史 profile 证据。

profile 239 继续使用版本 `0.1.177-baiyu`，完整继承 profile 238 的 54 项 migration，追加
`238_upstream_account_lifecycle.sql` 与 `239_reconcile_non_grok_video_pricing.sql`，共 56 项。
前者增加上游派生账号生命周期 owner、归档原因、约束和归档查找索引；后者备份并清理
migration 232 之后重新出现的非 Grok/Composite 视频价格，并增加数据库约束阻止再次漂移。
profile 233–238 的历史证据保持不可变。

VM Gate signer、DR signer、备份机 verifier/promoter 当前同时保留 legacy profile 195、199、202、206、207、208、209、210、212、213、215、232–241，以及 Gate v2 profile 242–269 合同；268 及以前的合同保留，269 是当前 pending profile，VM signer 自测和备份机目录 bootstrap 都必须覆盖它。未登记的 270 必须被拒绝。此处登记不代表 267 已完成 VM 验证或签名。发布资产定向回归至少执行：

```text
python -m pytest .agents/skills/sub2api-production-deploy/scripts/tests/release/test_profile260_release_contract.py .agents/skills/sub2api-production-deploy/scripts/tests/release/test_profile260_maintenance_contract.py .agents/skills/sub2api-production-deploy/scripts/tests/release/test_migration_planner_v2.py -q
python -m pytest deploy/tests/release/test_release_core.py deploy/tests/release/test_production_release.py deploy/tests/release/test_signer_assets.py
python deploy/tests/release/backup_dr_profile_199_integration.py
python deploy/tests/release/backup_dr_profile_202_integration.py
python deploy/tests/release/backup_dr_profile_206_integration.py
python deploy/tests/release/backup_dr_profile_207_integration.py
python deploy/tests/release/backup_dr_profile_208_integration.py
python deploy/tests/release/backup_dr_profile_209_integration.py
python deploy/tests/release/backup_dr_profile_210_integration.py
python deploy/tests/release/backup_dr_profile_212_integration.py
python deploy/tests/release/backup_dr_profile_213_integration.py
python deploy/tests/release/backup_dr_profile_215_integration.py
python deploy/tests/release/backup_dr_profile_232_integration.py
python deploy/tests/release/backup_dr_profile_233_integration.py
python deploy/tests/release/backup_dr_profile_234_integration.py
python deploy/tests/release/backup_dr_profile_235_integration.py
python deploy/tests/release/backup_dr_profile_236_integration.py
python .agents/skills/sub2api-production-deploy/scripts/tests/release/backup_dr_profile_237_integration.py
python .agents/skills/sub2api-production-deploy/scripts/tests/release/backup_dr_profile_238_integration.py
python .agents/skills/sub2api-production-deploy/scripts/tests/release/backup_dr_profile_239_integration.py
```

首次安装信任根使用：

```text
python .agents/skills/sub2api-production-deploy/scripts/release.py bootstrap-trust
```

首次执行会在 VM 创建 signer 并停止，要求人工核对公钥指纹后将公钥加入
`deploy/release/trust/vm-gate-ed25519.pub`。提交最终代码后再次执行 bootstrap，
只有仓库、VM 和 RackNerd 三方公钥完全一致才会完成安装。

`bootstrap-trust` 只用于首次建立或经人工确认的信任根轮换。日常
`bootstrap-production` 和 `deploy` 必须使用已有 signer 私钥、公钥和 validator，不能创建、替换或自动修复它们；validator 更新后必须重新生成 Gate，旧 Gate 失效。

VM 的 validator、Gate signer 和 DR evidence signer 是同一版本单元。更新时必须先在
暂存目录完成语法、正负路径签名和公钥验签，再在全局锁内一次激活；三者 checksum
都会进入发布 manifest 和 Gate。Gate signer 只接受固定 release Gate 路径，DR signer
只接受 `/opt/sub2api-deploy/dr-evidence/<release-id>/<drill-id>/evidence.json`，并严格验证
恢复结果 schema、候选绑定、全部恢复断言和 Redis TTL 对账等式。两者都复用既有 VM
Ed25519 私钥，但任何流程不得绕过 helper 直接调用该私钥。

异地备份机使用 `deploy/release/drverify` 由 Go 标准库构建的静态只读 verifier。Linux
amd64 二进制必须匹配仓库内 `linux-amd64.sha256`，并与仓库 trust 公钥一起在备份机
暂存；正确签名、篡改 evidence 和篡改 signature 三组自测通过后才能原子激活。晋升
`verified` 前必须在备份机本机完成验签和 candidate/evidence 字段绑定，不能只依赖
操作端验签。

生产 bootstrap 不得创建或替换信任根，也不得修改 systemd。它只创建缺失的发布状态
目录，并核验信任根、现有应用健康、Nginx 和备份全局锁；不检查账号池或 Canary 凭据；已有资产内容
不一致时必须停止。

`vm-validate` 会在 VM 缺少 `jq` 时通过 `apt-get` 安装该单一依赖，并更新仓库内版本对应的 validator；不会升级其他系统包。

发布不读取 Canary key，也不发送模型请求。流式响应与 usage attribution 明确记录为
`not_checked`，不会因为账号池没有可用账号而阻塞 Docker 镜像升级。

`.release.lock` 使用操作系统文件锁，文件本身会长期保留；只有实际持锁进程会阻止并发发布。`status` 是白名单投影，`wait` 超时不会 kill；runner 异常退出后先执行 `reconcile-inspect`，禁止手工删除 claim、marker 或重复 deploy。
禁止删除 `.active-release`、`.consumed` 或 `.recovered` 来强行重试；不兼容迁移禁止 image-only rollback。

SSH 超时后以远端 committed marker 重新判定阶段，不凭本地异常猜测执行结果。RackNerd
只验 direct，DMIT 必须从异地节点验；Redis `--requirepass` 只通过 stdin 传递。VM 空间
不足时（包括 Docker 所在文件系统可用空间低于 8 GiB 下限）先执行发布白名单对象清理；仍不足时，只允许版本化清理器执行一次
`--all`、`max-used-space=1gb`、`reserved-space=1gb` 的容量有界 BuildKit GC；`--all` 必须与后两项同时使用。禁止 `docker system prune`、
缺少缓存上限或保留量的 builder prune、删除 volume，或触碰数据库、Redis、data 和备份。

`migration_started` 只表示尝试开始；只有 `migration-committed` marker 与数据库迁移记录、目标 checksum 同时吻合才算提交。SSH 超时、迁移容器存在或本地布尔值异常都不能直接选择恢复分支。

恢复不是只替换一个 Compose 文件：必须恢复 `.env` 与 `COMPOSE_FILE` 引用的完整 Compose 文件集合，显式渲染 `docker compose config --format json` 并核对 image ID、挂载、端口和关键环境摘要。override 状态不明时停止，不能先启动旧应用再判断。

完整故障映射和恢复决策见
`.agents/skills/sub2api-production-deploy/references/release-doctor-and-recovery.md`。

## 官方 0.2.16 迁移与发布合同

官方 0.2.16 合并固定目标为 `4d9ac3e482106d2da3a39fe3e546d8d6801f1cc1`，按目标源码 VERSION 使用 `0.2.16-baiyu`。新增连续 pending profile 267，parent 266，`new_migrations=[292_group_i18n.sql,293_gemini_cached_contents.sql,294_gemini_cached_contents_channel_usage.sql,295_gemini_cached_contents_mutation_lease.sql]`。四项官方 SQL 仅按原字节重编号，293 建表先于 294/295；历史 266 保留 `0.2.15-baiyu`、parent 265 和迁移 291，全部旧 profile、SQL 和 checksum 不回写。Python/Shell、VM/Gate/DR signer、bootstrap、生产、祖先断言、奖励成本 postflight、备份和清理入口共同支持 267，未知 269 拒绝。恢复敏感最终 blob/模式继续分类，未分类变更保持阻断，不新增审阅豁免；本地合并与测试不代表真实 PostgreSQL 升级、VM Gate、旧镜像兼容或生产部署完成。

最低本地验证为 `test_profile267_release_contract.py`、`backend/migrations/profile_267_migrations_test.go`、关联 Group i18n/Gemini 缓存计费与租约回归、完整 release suite、修改 Shell 的语法检查及 `git diff --check`；真实新库/升级库/部分应用和旧镜像兼容另在适用 VM 阶段验证。

## 上游缓存保护发布合同

当前连续 pending profile 270 保持 `0.2.16-baiyu`，parent 269，仅新增 `298_group_scheduling_protection_policies.sql`。分组缓存模式与两保护优先策略保存于 fork 独立表；旧非空 group_ids 白名单一次性投影为 inherit/disabled，自定义重放不覆盖，原 settings 保留旧镜像回滚依据。269 及所有历史 SQL/profile/checksum 不回写。

298 assertion helper 绑定 source/catalog checksum，生产 preflight/postflight/verified replay 只读，VM 写 fixture 仅在精确确认的 `sub2api_v2_*` 隔离库内事务回滚；签名 Gate 必须具有四个语义 phase 及精确 checksum。最低回归为 `test_profile270_release_contract.py`、`test_migration_298.py` 和真实 PostgreSQL 的 `TestGroupSchedulingProtectionPostgres`。Python/Shell 所有执行入口支持 270，未知 271 拒绝；未分类敏感 blob/模式继续阻断，不扩展历史审阅豁免。dirty 未提交工作树不能用旧 HEAD 代替新 candidate，正式 VM-only Gate 仍要求干净、完整且远端精确匹配的提交。

历史 profile 268，parent 267，版本 `0.2.16-baiyu`，仅新增 `296_upstream_cache_guard.sql`。历史保留 profile 269，parent 268，沿用 `0.2.16-baiyu`，仅新增 `297_upstream_cache_guard_account_scope.sql`。267/268 及全部历史 profile、SQL 和 checksum 不回写。Python/Shell、VM validator、Gate/DR signer、bootstrap 自测、生产 context/prepare、祖先断言、奖励 postflight、备份和清理入口共同支持当前 270，未知 271 拒绝。新迁移为事实补当前 Key 身份、建立跨组查询索引与账号级状态，不写余额、计费或错误重试配置，不保存原始请求正文、认证头或凭据。

本地最低验证包括 `test_profile269_release_contract.py`、缓存核心与调度/API 回归、前端设置/状态测试、双语键完整性、typecheck/lint/build、完整 release suite、修改 Shell 语法及 `git diff --check`。本地登记不证明 PostgreSQL 实际升级、旧镜像兼容、VM Gate 或生产上线；恢复敏感最终 blob/模式重新精确分类，未分类继续阻断，不增加新的审阅豁免。长期产品合同见 `.wiki/03-模块指南/14-上游缓存保护.md`。


## 上游账号表性能迁移（profile 273）

历史 profile 为 `273`，版本 `0.2.17-baiyu`、parent `272`，仅新增 `300_upstream_health_history_index.sql`。300 以非唯一可重复创建索引 `(upstream_key_id, observed_at DESC, id DESC)` 有界读取每 Key 最近历史，旧读写协议不变。272 的版本、parent `271` 和 `new_migrations=[]` 及全部旧 SQL/checksum 保持不可变；当时下一未登记 profile `274` 拒绝；当前 274 见末节。原始文件 checksum 与 Gate 的 strip 后 SQL checksum分别登记。

本地运行 profile273、全部历史 profile、release suite 和变更 Shell 的语法检查；真实 PostgreSQL 的索引定义/幂等/旧 SQL 读写兼容、健康与质量差分另行验证。上述本地/隔离 fixture 验证不代表 signed VM Gate、旧生产镜像兼容或生产发布。新增 helper blob/模式仍按最终候选分类，不新增恢复目录豁免。长期接口与刷新规则见 `.wiki/03-模块指南/15-上游账号表刷新与质量统计.md`。


## Profile 274 的精确恢复审阅

历史 profile 274 保持 `0.2.17-baiyu`，parent 为 273，仅新增 `301_upstream_cache_guard_admissions.sql`；历史 273 及全部旧 SQL/checksum 保持不可变，登记时未登记的 profile 275 拒绝；当前 275 见末节。

本批实际生产 BASE `5deebb2929c2a694145cc2726e895f01c43d28c1` 到已独审 TARGET `aca670e896ffa0e0e54204f1e6c33c205d5160f5` 的 48 条敏感净差异，按三组显式路径和同路径旧/新 Git blob、文件模式冻结：41 条 profile 兼容接线、5 条 migration 301 Gate 接线、2 条停机奖励 postflight 接线。权威登记为 `recovery_gate.py` 的 `_PROFILE274_COMPATIBILITY_PATHS`、`_PROFILE274_MIGRATION_GATE_PATHS`、`_PROFILE274_POSTFLIGHT_PATHS`；新增 301 helper 的旧状态必须不存在。任何未来字节、模式、删除、改名、额外敏感路径或基线漂移继续阻断，历史登记保持不变。

本批不改变恢复算法、备份格式或信任链，按 `specialized` 执行当前 helper 的真实隔离恢复与新的签名 VM Gate。301 将会话 JSON 的逐请求 admissions 迁出，必须先停止旧写者，只允许 `downtime`，失败采用 `coordinated_restore`；旧镜像健康 smoke 不代表旧镜像可继续写新结构，也不代表可直接回退镜像。VM 的旧镜像健康步骤不豁免，301 仍须有 checksum、preflight、postflight、vm_semantics、verified_replay 证据。

回归入口 `test_profile274_recovery_review.py` 固定 48 条 literal 对象、互斥分组及全部历史登记，并验证每条路径的 blob、mode、delete、rename、BASE 漂移和额外恢复/信任/传输文件拒绝。登记与本地测试不能替代最终 SHA 的完整 release suite、真实专项恢复、生产验真或灾备基线晋升。


### 隔离 probe 所有权与失败保留的精确审阅

独立登记 `_VM_FAST_FIXTURE_PRESERVE_REVIEW_PATHS` 固定实际生产 BASE `c88f06e57a829438595263d524e02219aa2f5a2e` → 实施 TARGET `85d7274703ad07570403e501ecb9b66c8f0218ea`，仅三条同路径 Git blob 与模式：

| 相对 skill scripts 路径 | BASE 对象 | TARGET 对象 |
| --- | --- | --- |
| `release/cli.py` | `100644:c7b1c9b4bb633a7474bc9761b3d9b4f3a710558e` | `100644:0a28894545be615437e07bc94ae1c4b83cb993ff` |
| `release/vm-validate.sh` | `100755:e45e1bbd9d972f16823b64054646500c91c5ea90` | `100755:840a1eda0a603c13fa9d941edfddbfee46e81d00` |
| `release/vm_fast_fixture_preserve.py` | 不存在 | `100644:b651bbeb4020a740afb504dddb13e606cefeb3f8` |

分类理由为 `reviewed_vm_fixture_ownership_preserve_changed`。完整净 validator 同时包含应用角色恢复对象、配置角色核验和同族失败退出码/输出先持久化修复；新增 helper 是专事故保留收口，不修改生产 PostgreSQL/Redis 协调恢复算法、备份格式或信任根。该 helper 已明确列入恢复敏感文件，不按目录、祖先或事故类型豁免未来变化。

历史各路径集合和登记前缀保持冻结；任何未来字节、模式、删除、改名、BASE、额外敏感路径及新增 helper 旧状态不为空均拒绝，显式 full 不能解除未知变化阻断。`test_vm_fast_fixture_recovery_review.py` 独立固定 literal 对象、逐项负例和当前分类 AST 的唯一登记增量；旧 raw-index AST 在其固定登记提交验真，不把新登记写回历史。最终仍须新完整 SHA、干净推送、新候选、新签名 VM Gate 及生产验真；精确登记与本地/隔离回归不等于发布完成。

### VM probe 配置与资源归属

`vm-validate.sh` 的所有恢复探针入口均先调用 `vm-probe-config.py`：它从 VM 有效环境生成 probe-only `connection.env`/`postgres.env`，完成 PostgreSQL 身份预检后才创建隔离数据库。`DATABASE_*` 的非空环境值遵循 Viper 优先级，生产配置不会被改写；恢复使用 `pg_restore --no-owner --role`，不要求跨环境 user/owner 相等。资源 cleanup 只针对本次成功创建并有 owned 标记的容器、网络、连接和数据库；同名已有资源不会被删除。成功或失败均清理临时连接，仅保留 0700/0600 私有有界失败诊断。

### Probe 连接与失败保留的精确审阅

`_VM_PROBE_CONFIG_PRESERVE_REVIEW_PATHS` 仅追加生产 BASE `c88f06e57a829438595263d524e02219aa2f5a2e` → 实现 TARGET `91a13c6c64558f5d78ae8b75979af0e6a24b60b2`，理由为 `reviewed_vm_probe_config_preserve_changed`。固定对象如下，两个新 helper 的旧状态必须不存在：

| 相对 scripts 路径 | BASE 对象 | TARGET 对象 |
| --- | --- | --- |
| `release/cli.py` | `100644:c7b1c9b4bb633a7474bc9761b3d9b4f3a710558e` | `100644:65ba379d893258d40266799ae4a35b1b2d82b068` |
| `release/vm-validate.sh` | `100755:e45e1bbd9d972f16823b64054646500c91c5ea90` | `100755:d48b6dc2ab97e2b470b8299318121f212a4f6e93` |
| `release/vm-probe-config.py` | 不存在 | `100644:2957c1eb31c48e11b73d707bdf553fec15b9809c` |
| `release/vm_restore_probe_preserve.py` | 不存在 | `100644:d39f66afb85783263aafc75747bf03493a190c72` |

本批统一 probe-only 连接权限、应用角色恢复和容器所有权；容器先 create、记录不可变 ID 和 owned，再 start，失败按 ID 清理。专事故 preserve helper 只收口冻结的 `274-12d4cf67d1c6-1792572302-7b82c271`，不重跑、不清理远端资源、不关机。生产恢复算法、备份格式和信任根未改，仍要求 `specialized` 真实隔离恢复、新候选及新签名 Gate。

历史 tranche 不回写。`test_vm_probe_config_recovery_review.py` 独立固定 literal 对象、旧登记 AST 和实现的两项敏感标记；逐路径拒绝 blob/mode/删除/改名/BASE、额外敏感路径、缺失审阅来源，以及新增 helper 旧路径已是文件、目录、symlink 或 gitlink。同名异目录不能借用登记，既有敏感路径迁移仍阻断。显式 full 不能解除未审变化阻断；本地测试不代表生产发布。


## 官方 0.2.18 / profile 275

当前 profile `275` 使用 `0.2.18-baiyu`、parent `274`，仅新增 `302_enable_gemini_image_generation_groups.sql`。官方目标 `5c185c5c5d4f5e409e2139806e0964ae944e03e8` 的 246 生图开关回填只按原字节重编号，不改历史 fork 246、301 或任何旧 profile/checksum；原始 SQL SHA-256 为 `f49b458cfbd52bd17f5bb02e30e3813c8e192e4ae38bce6358713ef65f3874b5`，Gate catalog strip 后 checksum 为 `87267be7ab657173768989e2b0b105c5569c5930a7f1ed124f4cf81e5198297c`。旧章节的 current 274/未知275是当时历史边界，现行未知276拒绝。

275 继续只允许 downtime；累计升级仍可能执行301，须保留停旧写者、四项祖先迁移 assertion 的精确身份及301 coordinated_restore。候选 Go 构建 pin 对齐官方 go.mod 的 `1.27.3`。最低验证包含 `test_profile275_release_contract.py`、新旧迁移 Go 测试、全部历史 profile、完整 release suite 和变更 Shell 语法检查。恢复敏感最终 blob 仍须重新精确分类，旧审阅不扩展；本地验证不等于真实数据库升级、VM Gate、旧镜像兼容或生产发布。
