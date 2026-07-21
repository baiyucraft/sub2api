# RackNerd 一键发布

标准入口：

```text
python deploy/release.py doctor --profile 197 --commit <40位完整SHA>
python deploy/release.py bootstrap-production --profile 197
python deploy/release.py deploy --profile 197 --commit <40位完整SHA>
```

`deploy` 会在停写前使用当前生产版本完成 RackNerd direct 与 DMIT 两条流式基线 Canary，避免把既有上游或链路故障带入切换阶段；该请求会像普通请求一样产生 usage 记录，但不会使用候选容器。候选公开后使用相同合同复验；只有 `curl 28` 和 `502/503/504` 会以新 marker 最多尝试三次，所有实际落库的尝试都会核验 API Key、endpoint 和真实 IP，其他错误立即停止。每个长阶段会实时输出 release ID 和阶段，并在结构化状态中记录开始时间与截止时间；调用端关闭 stdout 不会中止 runner。

`doctor` 和 `bootstrap-production` 可独立用于排查或首次初始化。日常只需执行 `deploy`：
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

`.release.lock` 使用操作系统文件锁，文件本身会长期保留；只有实际持锁进程会阻止并发发布。
禁止删除 `.active-release`、`.consumed` 或 `.recovered` 来强行重试；不兼容迁移禁止 image-only rollback。

SSH 超时后以远端 committed marker 重新判定阶段，不凭本地异常猜测执行结果。RackNerd
只验 direct，DMIT 必须从异地节点验；Redis `--requirepass` 只通过 stdin 传递。VM 空间
不足时只允许发布白名单清理，禁止任何 prune、删除 volume 或触碰数据库、Redis、data 和备份。

`migration_started` 只表示尝试开始；只有 `migration-committed` marker 与数据库迁移记录、目标 checksum 同时吻合才算提交。SSH 超时、迁移容器存在或本地布尔值异常都不能直接选择恢复分支。

恢复不是只替换一个 Compose 文件：必须恢复 `.env` 与 `COMPOSE_FILE` 引用的完整 Compose 文件集合，显式渲染 `docker compose config --format json` 并核对 image ID、挂载、端口和关键环境摘要。override 状态不明时停止，不能先启动旧应用再判断。

完整故障映射和恢复决策见
`.agents/skills/sub2api-production-deploy/references/release-doctor-and-recovery.md`。
