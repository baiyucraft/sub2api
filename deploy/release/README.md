# RackNerd 一键发布

标准入口：

```text
python deploy/release.py doctor --profile 187 --commit <40位完整SHA>
python deploy/release.py bootstrap-production --profile 187
python deploy/release.py deploy --profile 187 --commit <40位完整SHA>
```

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

profile 187 会在 manifest 中固定记录 `182-187` 的有序 migration 列表及 checksum；
已执行过的迁移允许原样跳过，缺失迁移必须逐项应用并逐项校验。profile 187 是
不兼容且会改写持久余额语义的发布，禁止 image-only rollback。

首次安装信任根使用：

```text
python deploy/release.py bootstrap-trust
```

首次执行会在 VM 创建 signer 并停止，要求人工核对公钥指纹后将公钥加入
`deploy/release/trust/vm-gate-ed25519.pub`。提交最终代码后再次执行 bootstrap，
只有仓库、VM 和 RackNerd 三方公钥完全一致才会完成安装。

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

完整故障映射和恢复决策见
`.agents/skills/sub2api-production-deploy/references/release-doctor-and-recovery.md`。
