# RackNerd 一键发布

标准入口（启动后调用端可断开）：

```text
python deploy/release.py doctor --profile <profile> --commit <40位完整SHA>
python deploy/release.py bootstrap-production --profile <profile>
python deploy/release.py deploy-start --profile <profile> --commit <40位完整SHA>
python deploy/release.py status <release_id>
python deploy/release.py wait <release_id> --timeout 900
python deploy/release.py verify-result <release_id>
```

生产空间维护不进入应用发布流程。只对已经 terminal verified 的 release 使用以下两阶段入口；apply 必须绑定同一次 dry-run 输出的计划 checksum：

```text
python deploy/release.py cleanup-production <release_id> --mode dry-run
python deploy/release.py cleanup-production <release_id> --mode apply --plan-sha256 <plan_sha256>
```

该命令保留 current、pre-switch、所有容器引用和 recovery point 镜像；残留 migration 容器只报告不删除。它只删除 full-SHA tag 的零引用旧 Sub2API image，并以容量边界 `max-used-space=2gb,reserved-space=2gb` 执行一次 BuildKit LRU GC。禁止 volume/image/system prune，实际释放只看 `df` 前后差值。

`deploy-start` 会在停写前使用当前生产版本完成 RackNerd direct 与 DMIT 两条流式基线 Canary，避免把既有上游或链路故障带入切换阶段；该请求会像普通请求一样产生 usage 记录，但不会使用候选容器。候选公开后使用相同合同复验；只有 `curl 28` 和 `502/503/504` 会以新 marker 最多尝试三次，所有实际落库的尝试都会核验 API Key、endpoint 和真实 IP，其他错误立即停止。worker 在 doctor 到最终收口期间持有同一把 OS 锁；调用端关闭 stdout 或超时不会中止 runner。

`doctor` 和 `bootstrap-production` 可独立用于排查或首次初始化。日常只需执行 `deploy-start`：
它先检查本地、VM 与外部节点，再幂等执行生产 bootstrap，最后检查 RackNerd；任何
预检失败都不得进入 Gate、停写或迁移。

该命令固定执行以下顺序：

```text
VM 唯一构建 candidate
  -> VM 本地 PostgreSQL/Redis/data-dev 迁移与恢复验证
  -> VM 签名 Gate
  -> RackNerd 验签并导入同一镜像
  -> 停写、持久 mask、协调恢复点和异地校验
  -> 唯一 migrate-only、候选启动和双链路 canary
  -> 开启自动同步、恢复备份 units、消费 Gate
```

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

VM Gate signer、DR signer、备份机 verifier/promoter 当前同时保留 profile 195、199、202、206、207、208、209、210 和 212
合同。发布资产定向回归至少执行：

```text
python -m pytest deploy/tests/release/test_release_core.py deploy/tests/release/test_production_release.py deploy/tests/release/test_signer_assets.py
python deploy/tests/release/backup_dr_profile_199_integration.py
python deploy/tests/release/backup_dr_profile_202_integration.py
python deploy/tests/release/backup_dr_profile_206_integration.py
python deploy/tests/release/backup_dr_profile_207_integration.py
python deploy/tests/release/backup_dr_profile_208_integration.py
python deploy/tests/release/backup_dr_profile_209_integration.py
python deploy/tests/release/backup_dr_profile_210_integration.py
python deploy/tests/release/backup_dr_profile_212_integration.py
```

首次安装信任根使用：

```text
python deploy/release.py bootstrap-trust
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
目录和固定 Canary 文件，并核验信任根、Canary 与数据库、备份全局锁；已有资产内容
不一致时必须停止。

`vm-validate` 会在 VM 缺少 `jq` 时通过 `apt-get` 安装该单一依赖，并更新仓库内版本对应的 validator；不会升级其他系统包。

发布要求 RackNerd 已存在权限为 `0600` 的
`/root/.config/sub2api-release/canary-api-key`。该文件不由仓库保存，也不会写入
命令行、stdout、Gate 或状态文件。

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
