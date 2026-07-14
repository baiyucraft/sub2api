# RackNerd 一键发布

正式入口：

```text
python deploy/release.py deploy --profile 182 --commit <40位完整SHA>
```

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

首次安装信任根使用：

```text
python deploy/release.py bootstrap-trust
```

首次执行会在 VM 创建 signer 并停止，要求人工核对公钥指纹后将公钥加入
`deploy/release/trust/vm-gate-ed25519.pub`。提交最终代码后再次执行 bootstrap，
只有仓库、VM 和 RackNerd 三方公钥完全一致才会完成安装。

`vm-validate` 会在 VM 缺少 `jq` 时通过 `apt-get` 安装该单一依赖，并更新仓库内版本对应的 validator；不会升级其他系统包。

发布要求 RackNerd 已存在权限为 `0600` 的
`/root/.config/sub2api-release/canary-api-key`。该文件不由仓库保存，也不会写入
命令行、stdout、Gate 或状态文件。

`.release.lock` 使用操作系统文件锁，文件本身会长期保留；只有实际持锁进程会阻止并发发布。
禁止删除 `.active-release`、`.consumed` 或 `.recovered` 来强行重试；不兼容迁移禁止 image-only rollback。
