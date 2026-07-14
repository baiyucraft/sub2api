# 发布预检与状态恢复

## 标准入口

对已提供 profile 的 RackNerd 应用发布，固定使用：

```text
python deploy/release.py doctor --profile <profile> --commit <40位完整SHA>
python deploy/release.py bootstrap-production --profile <profile>
python deploy/release.py deploy --profile <profile> --commit <40位完整SHA>
```

- `doctor` 只读检查本地、VM、RackNerd、DMIT 和异地节点，输出字段白名单；失败时禁止进入发布。
- `bootstrap-production` 只创建缺失的状态目录和固定 Canary 文件，并核验信任根、Canary 与数据库、备份全局锁；不修改 systemd、不构建、不迁移、不切换应用。已有资产内容不一致时停止。
- `deploy` 是日常一键入口：先检查本地、VM 与外部节点，幂等 bootstrap RackNerd 后再检查 RackNerd，随后完成 VM Gate、生产恢复点、迁移、切换和分节点验收。
- 信任根首次安装仍单独使用 `bootstrap-trust`，人工核验公钥指纹；普通 bootstrap 和 deploy 不得创建或替换信任根。

## 故障预检表

| 症状 | 根因 | doctor 字段或检查 | 处理 |
| --- | --- | --- | --- |
| SSH 代理可用但发布连接失败 | 仓库私有 `known_hosts` 缺目标记录，用户可信文件已有记录 | `vm_hostkey_trusted`、`racknerd_hostkey_trusted`、`dmit_hostkey_trusted`、`backup_hostkey_trusted` | 只导入用户已信任的精确记录；禁止 `ssh-keyscan`、TOFU 或自动接受 |
| VM Gate 无法提交或生产拒绝 Gate | RackNerd 缺 VM Gate 公钥或三方信任根不一致 | `vm_gate_trust_ready` | 运行独立 `bootstrap-trust` 并人工核验指纹 |
| Canary 无法执行 | RackNerd 缺固定 Canary 凭据文件或权限错误 | `canary_key_ready` | bootstrap 从生产本机生成受限文件；凭据不得回显或进入命令行 |
| 发布与每日备份竞争 | 缺全局备份锁 wrapper 或 systemd drop-in | `backup_global_lock_ready` | bootstrap 只核验并停止；通过独立运维初始化补齐后重跑 doctor |
| claim 后立即失败且无法重试 | claim 文件格式与读取协议不一致 | `active_claim`、`claim_format_valid` | 按远端 committed marker reconciliation，禁止直接删除 claim |
| preflight 失败却要求 recovery point | 恢复逻辑未区分切换前和切换后状态 | `committed_phase`、`recovery_point_ready` | 按下方状态表恢复，不凭本地异常猜测 |
| freeze 最早期失败 | release-state 根目录未初始化 | `release_state_root_ready` | bootstrap 创建并核验 owner、权限和挂载边界 |
| Redis 备份认证失败 | 密码只存在于容器启动参数 `--requirepass`，没有环境变量 | `redis_auth_source`、`redis_ping`、`redis_info`、`redis_bgsave_precheck` | 从启动参数安全解析，通过 stdin 传给客户端；不得写入 argv、stdout 或状态文件 |
| Compose stop/start 被判失败 | 正常进度输出写入 stderr，严格 SSH 将其视为错误 | `compose_control_precheck` | 远端脚本静默正常进度，仅保留结构化白名单结果 |
| RackNerd 内访问 DMIT TLS 失败，外部访问正常 | RackNerd 到 DMIT 公网地址的 hairpin 路径不成立 | `direct_health`、`dmit_external_health` | direct 在 RackNerd 验；DMIT 必须从异地节点验 |
| VM Gate 前空间不足 | candidate、旧测试容器或归档累积 | `vm_free_bytes`、`vm_required_bytes`、`vm_cleanup_candidate_bytes` | 仅执行下方白名单清理一次，随后重新预检；仍不足则停止 |

doctor 还必须核验 Git clean、完整 SHA 与远端一致、应用/PG/Redis 健康、迁移期望状态、Nginx、HAProxy/PROXY v2、证书、备份 timer、磁盘、PG dump、Redis 预演和异地上传协议。所有输出仅允许固定布尔值、计数、大小、状态和 checksum；禁止宽泛日志及敏感值。

已应用 profile 迁移时，生产 preflight 仅在数据库 checksum 与 Gate 完全一致时允许继续；迁移不存在时执行正向迁移，checksum 不一致时必须在领取 Gate 前停止。

## Committed-State 恢复决策

远端 committed marker 是唯一事实源。SSH 超时或本地进程异常后先重新读取 marker，再选择动作：

| 远端状态 | 允许动作 |
| --- | --- |
| 无 committed state，迁移未应用 | 清理本次 preflight 临时资产，reconcile claim，保持旧应用运行 |
| 仅完成 units mask | 恢复原 units 状态，清理临时资产并 reconcile claim |
| 已记录 pre-image/SHA，迁移未应用 | 恢复旧应用和原 units 状态，清理临时资产并 reconcile claim |
| 已有协调恢复点，迁移已应用 | 继续 candidate，或在持续停写下协调恢复 PostgreSQL、Redis、配置和旧镜像；禁止 image-only rollback |
| candidate 已公开且健康 | 完成分节点验收；任一路径失败立即关闭公开入口并进入协调恢复 |

不得删除 `.active-release`、`.consumed`、`.recovered` 或 committed marker 来制造“干净状态”。恢复完成后应原子记录结果，并恢复自动同步和备份 units 的既定状态。

## 分节点验收

```text
RackNerd  -> direct /health、应用日志、数据库归因
异地节点 -> DMIT /health、DMIT 流式 Canary
两节点   -> 各自发送唯一 marker 的流式 Canary
RackNerd  -> 按 marker 核验 usage 记录和真实客户端 IP
```

- 禁止从 RackNerd 回连 DMIT 公网入口作为 DMIT 验收证据。
- Canary secret 通过 stdin 传入一次性进程，仅保存在内存或权限受限的临时文件，退出时清理。
- direct 与 DMIT 任一路径未通过，都不能报告生产完全健康。

## VM 白名单清理

空间不足时只能清理一次，并在清理前后重新计算空间：

- 允许删除本次 `sub2api-probe-*` 临时资源。
- 允许删除状态为 `exited` 的 `sub2api-dev-pre*` 历史容器；`docker rm` 禁止带 `-v`。
- 允许删除仓库为 `sub2api`、full-SHA tag、无任何容器引用、不是当前目标或当前 `sub2api-dev` 使用的旧镜像。
- 只有本地 Gate 已完整下载并校验后，才允许删除对应的旧 VM Gate candidate 归档。

始终禁止 `docker system prune`、`docker builder prune`、删除 volume，以及触碰 PostgreSQL、Redis、`data`、`data-dev`、正式备份目录、当前 dev 容器或其镜像。白名单清理后仍不满足 `vm_required_bytes` 时停止发布。
