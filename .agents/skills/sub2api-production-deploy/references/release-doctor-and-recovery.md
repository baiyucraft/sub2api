# 发布预检与状态恢复

## 目录

- [标准入口](#标准入口)
- [迁移状态与重复 Gate](#迁移状态与重复-gate)
- [长时间无输出诊断](#长时间无输出诊断)
- [Canary 超时分层诊断](#canary-超时分层诊断)
- [公开后的 Reconciliation](#公开后的-reconciliation)
- [故障预检表](#故障预检表)
- [Signer 身份一致性门禁](#signer-身份一致性门禁)
- [Committed-State 恢复决策](#committed-state-恢复决策)
- [分节点验收](#分节点验收)
- [VM 白名单清理](#vm-白名单清理)

## 标准入口

对已提供 profile 的 RackNerd 应用发布，固定使用：

```text
python deploy/release.py doctor --profile <profile> --commit <40位完整SHA>
python deploy/release.py bootstrap-production --profile <profile>
python deploy/release.py deploy-start --profile <profile> --commit <40位完整SHA>
python deploy/release.py status <release_id>
python deploy/release.py wait <release_id> --timeout 900
python deploy/release.py verify-result <release_id>
```

- `doctor` 只读检查本地、VM、RackNerd、DMIT 和异地节点，输出字段白名单；失败时禁止进入发布。
- `bootstrap-production` 只创建缺失的状态目录和固定 Canary 文件，并核验信任根、Canary 与数据库、备份全局锁；不修改 systemd、不构建、不迁移、不切换应用。已有资产内容不一致时停止。
- `deploy-start` 是日常一键入口：预分配 release ID 后启动独立 worker；worker 先检查本地、VM 与外部节点，幂等 bootstrap RackNerd 后再检查 RackNerd，随后完成 VM Gate、生产恢复点、迁移、切换和分节点验收。
- worker 在停写前先用当前生产版本执行 direct/DMIT 流式基线 Canary；它会产生带唯一 marker 的正常 usage 记录，但不验证候选容器。该检查失败时释放本次 claim 并保持旧应用运行。候选公开后仅对 `curl 28` 和 `502/503/504` 使用新 marker 最多尝试三次，确定性 4xx、协议或 SSE 错误不重试。
- 信任根首次安装仍单独使用 `bootstrap-trust`，人工核验公钥指纹；普通 bootstrap 和 deploy 不得创建或替换信任根。

## 迁移状态与重复 Gate

每次生产 preflight 必须同时输出 profile 的整体 `migration_status` 和关键迁移的独立状态，例如 `migration_195_status`、`migration_196_status`、`migration_197_status`。独立状态只允许：

- `absent`：数据库中没有该迁移记录，且目标 checksum 与 manifest 已匹配，可以按顺序执行；
- `verified`：数据库中已有该迁移记录，记录 checksum、schema/数据语义和目标 checksum 全部匹配，可以幂等跳过；
- `unknown` 或 `conflict`：无法证明状态，立即停止，不能进入停写、迁移或切换。

profile 可能是混合状态，例如 195 已存在而 196/197 缺失。此时只对缺失迁移生成执行计划，已 `verified` 的迁移不得重跑；所有迁移仍按 manifest 顺序校验 checksum。VM Gate 重放遇到已存在的迁移时，应重复执行幂等断言并记录 `verified`，禁止删除数据库记录、marker 或 Gate 目录来制造 `absent`。

profile 197 的预检/执行矩阵固定为：

| 195 | 196 | 197 | 结论 |
| --- | --- | --- | --- |
| `verified` | `absent` | `absent` | 允许按 196 -> 197 前向执行，禁止重跑 195 |
| `verified` | `verified` | `verified` | 全部只读复核后幂等跳过 |
| `absent` | 任意 | 任意 | 按 manifest 先执行 195，再继续后续迁移 |
| `unknown`/`conflict` 或任一 checksum 不匹配 | 任意 | 任意 | 立即停止并进入恢复/新 Gate |

最终 committed marker 和 Gate evidence 必须覆盖 195、196、197 三项目标 checksum，不能只记录 profile 总体为 `verified`。

profile 198 在上述矩阵后追加 `198_normalize_managed_monitor_key_names.sql`：当 195/196/197 均为 `verified` 且 198 为 `absent` 时，只执行 198；若 198 已 `verified`，只读复核后幂等跳过。最终 marker 和 Gate evidence 还必须包含 198 的目标 checksum。

`migration_started`、迁移容器存在、SSH 超时或调用端断言失败，都不能证明迁移已经提交。只有 committed marker、数据库迁移记录和目标 checksum 三者吻合，才可将迁移判定为已提交。

## 长时间无输出诊断

`release.py` 的父进程和远程子阶段可能因 Python 或管道缓冲而长时间不刷新控制台。无新输出不等于卡死，也不授权重复执行、删除锁文件或手工进入生产阶段。

诊断顺序固定为：

1. 确认原 `deploy-start` worker 仍存在；只读取进程名、PID、父 PID 和 Python 模块名，不输出完整命令行。
2. 使用原进程对应的 profile、完整 commit SHA 和 release ID 精确定位 `.tmp/releases/<release-id>/`。目录名中的短 SHA 只用于定位，身份判断仍使用 manifest 的完整 SHA；不得仅按目录时间猜测其他发布。
3. 从以下 JSON 仅投影白名单字段，不原样输出文件：
   - `state.json`：VM Gate 状态；`stage=vm_validate,status=verified` 只证明 VM Gate 完成。
   - `release-state.json`：本地编排状态；`stage=production_release,status=running` 表示已进入生产 runner，不表示已切换成功。
   - `gate/production-result.json`：生产阶段事实；按 `history[].stage` 判断 `stage_assets`、`production_preflight`、`freeze`、`backup`、`migration_and_switch` 和最终验收。`history[]` 只表示阶段推进，最终结论读取顶层 `stage/status`。
4. 子进程为 `release.vm_validate` 时，只能报告 VM Gate 进行中；子进程为 `release.production` 时，只能报告生产 runner 进行中。具体是否停写、迁移或公开流量必须由 `production-result.json` 的阶段证据确认。
5. 状态文件持续推进时继续等待原进程。完整 VM build 可以持续十余分钟，远程命令执行期间状态文件可能只在阶段边界更新；只要父子进程仍存活且没有明确错误，就不能根据状态文件时间判定卡死。
6. 诊断轮询保持低频，只投影阶段、状态、错误码、候选镜像 ID 和状态文件时间等固定字段。禁止输出完整命令行、原始 JSON 或远端日志，也禁止在原进程存活时并发运行第二个 `deploy` 或 `doctor`。
7. 当前状态文件没有统一的 `stage_deadline_at`。需要判断超时时，只能读取当前 commit 中 release runner/validator 为正在执行的子命令声明的 timeout，并等待原进程返回对应超时错误；不能自定义更短的人工超时。只有进程退出、状态明确失败或原子命令已明确超时，才复核远端 committed marker 并进入恢复决策。

调用工具会在远短于 runner timeout 的时间内终止前台会话时，使用唯一隐藏后台 runner（标准实现即 `deploy-start`）：

1. stdout、stderr、PID 和 release ID 只写入仓库 `.tmp/`，不记录 secret 或完整命令行。
2. 启动后确认只有一个 `_deploy-worker` 父进程和对应 validator/production 子进程。
3. 只低频读取 PID 存活、日志大小和三个状态 JSON 的白名单字段。
4. 工具层超时后先确认原进程是否仍存活；存活时不得重启、杀进程或并发运行 doctor/deploy。
5. runner 退出后才读取脱敏异常类型，并按远端 committed state 决定下一步。`stage_assets_verified` 后没有 `production_preflight` 时，优先归类为 caller/runner interruption，并运行 `reconcile-inspect`；不得重跑 `deploy`。

成功判定必须同时满足：

```text
`verify-result` 输出 status=verified，或正式 reconciliation 入口输出 recovered
  + state.json: vm_validate / verified
  + production-result.json: stage=production_verified 或 production_verified_after_reconciliation
                            AND status=verified
  + production running_image_id == signed candidate_image_id
```

进程退出码 `0`、VM Gate `verified`、健康接口单独返回 `200`，均不能独立证明生产发布完成。诊断期间禁止再次运行 `deploy`；`.release.lock` 是否存在不代表是否持锁。

## Canary 超时分层诊断

`curl exit 28` 只表示 Canary 在自身时间窗内未完成，不能单独证明候选回归，也不能忽略。候选已经公开时，先执行 `emergency-close` 并确认 Nginx inactive，再依次验证：

```text
signed image/迁移/业务不变量
  -> 127.0.0.1 /health 与短时鉴权
  -> 127.0.0.1 内部流式 Canary
  -> RackNerd direct HTTPS 流式 Canary
  -> 异地节点经 DMIT 流式 Canary
  -> unique marker 的 usage、API Key、endpoint、真实 IP 归因
```

- 每次 Canary 使用唯一 marker/User-Agent；secret 仅从 stdin 传入。
- 只记录 `curl_exit`、HTTP code、SSE detected、耗时、记录数和状态，不输出请求体、响应体、账号或错误原文。
- 只有 image、迁移、配置不变量、内部 health 和短时鉴权均通过时，才允许流式 Canary 最多重试 3 次；每次使用新 marker。
- 内部流式也失败、日志出现关键错误、迁移不变量失败或状态无法证明时，不重试公开链路，进入协调恢复。
- direct 与 DMIT 都通过后仍须核验 usage 和真实 IP；仅 `/health=200` 或一次内部 SSE 不能完成发布。
- 不得临时切换测试模型、账号或参数规避既定 Gate；修改验收合同必须重新审核。

## 公开后的 Reconciliation

公开后失败会产生真实写入风险，不能直接恢复迁移前数据库：

1. 关闭公开入口，确认 active claim 精确匹配 release ID。
2. 核验当前 image、应用/PG/Redis、迁移、Nginx、备份 units、恢复点和流量恢复后的新增写入。
3. 候选内部健康、持久状态一致且失败仅为可重复链路验收时，允许在同一 signed candidate 和 active claim 下继续剩余验收；禁止重跑完整 `deploy`、重新迁移或创建第二个 claim。runner 已退出后的续验属于新的远程写操作，必须确认。
4. 候选或持久状态失败时，使用 `local_restore_point_ready=true` 对应的 root-only 临时 tar 协调恢复 PostgreSQL、Redis、完整 Compose/config/data 和旧 image。
5. 状态查询失败必须 fail-closed；`systemctl`、`docker info`、`docker inspect`、`docker ps` 查询失败不能解释为服务已停止或容器不存在。
6. 无论继续候选还是恢复旧版，都要恢复 backup units；成功候选还要恢复自动同步。
7. 最终必须由当前 commit 中已实现、经过测试并有明确命令/参数的 runner/reconciliation 入口原子 consume/reconcile active claim 并更新结构化状态。禁止手工改 JSON、删除 marker 或伪造 `verified`。不满足 claim-only 条件时保持 `blocked_reconciliation`，先完成现场审计，不用一次性脚本代替。

人工 reconciliation 完成发布至少要求：运行 image 等于 signed candidate、迁移与业务不变量通过、生产开关符合计划、direct/DMIT health 和 streaming 通过、usage/真实 IP 归因通过、backup timer 与自动同步恢复、claim 已消费且 post-deploy doctor 通过。若真实隔离恢复未完成，整体仍报告 `partial: production healthy, disaster-recovery baseline incomplete`。

## 故障预检表

| 症状 | 根因 | doctor 字段或检查 | 处理 |
| --- | --- | --- | --- |
| SSH 代理可用但发布连接失败 | 仓库私有 `known_hosts` 缺目标记录，用户可信文件已有记录 | `vm_hostkey_trusted`、`racknerd_hostkey_trusted`、`dmit_hostkey_trusted`、`backup_hostkey_trusted` | 只导入用户已信任的精确记录；禁止 `ssh-keyscan`、TOFU 或自动接受 |
| VM Gate 无法提交或生产拒绝 Gate | RackNerd 缺 VM Gate 公钥或三方信任根不一致 | `vm_gate_trust_ready` | 运行独立 `bootstrap-trust` 并人工核验指纹 |
| Canary 无法执行 | RackNerd 缺固定 Canary 凭据文件或权限错误 | `canary_key_ready` | bootstrap 从生产本机生成受限文件；凭据不得回显或进入命令行 |
| 流式 Canary 返回 `curl 28` | 上游/调度抖动、首字或完整流超时、公开链路异常 | `internal_streaming`、`direct_streaming`、`dmit_streaming`、marker usage | 先关公开入口并分层诊断；满足前提时最多重试 3 次，不直接判候选故障 |
| 迁移后协调恢复无法解密 | 生产只配置 recipient，`restore.sh` 却依赖本机 age identity | `local_restore_point_ready` | 迁移前生成并校验 root-only 临时恢复 tar；缺失时停止迁移，禁止把解密私钥补到生产机 |
| 远端动作成功但本地报 undeclared/missing field | SSH allowlist 与脚本 stdout 合同不同步 | committed marker、目标状态、输出字段集合 | 先核验动作是否已提交；同步 allowlist 与测试后再继续，不重复远端写操作 |
| 发布与每日备份竞争 | 缺全局备份锁 wrapper 或 systemd drop-in | `backup_global_lock_ready` | bootstrap 只核验并停止；通过独立运维初始化补齐后重跑 doctor |
| claim 后立即失败且无法重试 | claim 文件格式与读取协议不一致 | `active_claim`、`claim_format_valid` | 按远端 committed marker reconciliation，禁止直接删除 claim |
| preflight 失败却要求 recovery point | 恢复逻辑未区分切换前和切换后状态 | `committed_phase`、`recovery_point_ready` | 按下方状态表恢复，不凭本地异常猜测 |
| freeze 最早期失败 | release-state 根目录未初始化 | `release_state_root_ready` | bootstrap 创建并核验 owner、权限和挂载边界 |
| Redis 备份认证失败 | 密码只存在于容器启动参数 `--requirepass`，没有环境变量 | `redis_auth_source`、`redis_ping`、`redis_info`、`redis_bgsave_precheck` | 从启动参数安全解析，通过 stdin 传给客户端；不得写入 argv、stdout 或状态文件 |
| Compose stop/start 被判失败 | 正常进度输出写入 stderr，严格 SSH 将其视为错误 | `compose_control_precheck` | 远端脚本静默正常进度，仅保留结构化白名单结果 |
| RackNerd 内访问 DMIT TLS 失败，外部访问正常 | RackNerd 到 DMIT 公网地址的 hairpin 路径不成立 | `direct_health`、`dmit_external_health` | direct 在 RackNerd 验；DMIT 必须从异地节点验 |
| VM Gate 前空间不足 | candidate、旧测试容器或归档累积 | `vm_free_bytes`、`vm_required_bytes`、`vm_cleanup_candidate_bytes` | 仅执行下方白名单清理一次，随后重新预检；仍不足则停止 |
| signer/validator 漂移 | VM signer helper 与 validator 版本不一致，或信任公钥不一致 | `signer_identity_status`、`vm_validator_sha256`、`gate_signer_binding_status` | 停止；普通 bootstrap 不得自动生成密钥或替换信任根 |
| 生产迁移前置不满足 | VM fixture 通过但生产历史数据缺少可证明来源 | `migration_preflight_status`、`migration_unproven_rows` | 停止停写/迁移，先修订数据计划和 Gate |
| Compose 恢复状态不明 | `.env` 的 `COMPOSE_FILE` 引用缺失或 override 被误删 | `compose_restore_contract_status`、`compose_override_state` | 停止启动旧应用，恢复完整 Compose 文件集合并重新渲染 |

doctor 还必须核验 Git clean、完整 SHA 与远端一致、应用/PG/Redis 健康、迁移期望状态、Nginx、HAProxy/PROXY v2、证书、备份 timer、磁盘、PG dump、Redis 预演和异地上传协议。所有输出仅允许固定布尔值、计数、大小、状态和 checksum；禁止宽泛日志及敏感值。

已应用 profile 迁移时，生产 preflight 仅在数据库 checksum 与 Gate 完全一致时允许继续；迁移不存在时执行正向迁移，checksum 不一致时必须在领取 Gate 前停止。

## Signer 身份一致性门禁

VM validator 和 signer helper 是同一版本化发布单元。更新时必须先暂存两者，执行 shell/profile 兼容性检查和临时 Gate 签名验签自测，再一次激活；中途失败不得留下混合版本。普通 `bootstrap-production` 和 `deploy` 必须设置 `REQUIRE_EXISTING_SIGNER_KEYS=true`，要求已有 signer 私钥/公钥，不得创建、替换或自动修复信任根。发布前必须验证：

1. signer 私钥派生公钥等于 VM signer 公钥。
2. VM signer 公钥等于仓库信任公钥，且 RackNerd trust key 也相等。
3. signer 文件不是 symlink，owner 和权限符合固定要求。
4. validator 支持当前 profile，validator、runner、signer public key checksum 都绑定进 Gate。

任一 checksum 或 profile 能力不一致即停止。更新 validator 时保留原密钥、重新计算 checksum 并生成新 Gate；旧 Gate 不得继续使用，不得盲目重建。

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

`migration_started=true` 只表示尝试开始，不表示事务已提交。失败后必须重新读取 committed marker、migration 记录、active claim、应用、Nginx、PostgreSQL、Redis、备份 unit 和 Compose 恢复状态。

恢复决策矩阵：

```text
switch failure
  |
  +-- migration 未记录且持久 schema/data 未变化
  |     -> resume-old
  |
  +-- migration 已记录或持久 data 已变化
  |     -> 协调恢复 PostgreSQL + Redis + 配置 + 旧 image
  |
  +-- 状态无法证明
        -> fail-closed，manual reconciliation
```

不得通过删除 `.active-release`、`.consumed`、`.recovered` 或 marker 来重试；只有完成字段级现场核验后才能 reconcile。

恢复顺序的硬门禁是：先恢复并校验完整 Compose 闭包和渲染结果，再删除旧容器或恢复数据库，最后才允许启动旧应用。调用环境中的 `COMPOSE_FILE`、`COMPOSE_PROFILES` 等覆盖变量必须显式清除或固定，不得继承。

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
