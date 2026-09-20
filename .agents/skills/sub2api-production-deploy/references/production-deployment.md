# 生产部署、验收与回滚

## 目录

- [文档用途](#文档用途)
- [生产前置检查](#生产前置检查)
- [运维资产变更](#运维资产变更)
- [插件包部署](#插件包部署)
- [备份门禁](#备份门禁)
- [镜像切换](#镜像切换)
- [双路径验收](#双路径验收)
- [业务语义验收](#业务语义验收)
- [回滚](#回滚)
- [版本基线](#版本基线)
- [停止条件](#停止条件)

## 文档用途

本文规定 production preflight、备份门禁、迁移处理、镜像切换、双链路验收和回滚。机器角色与端口只以 [architecture-and-current-state.md](architecture-and-current-state.md) 为准；备份细节以 [backup-and-restore.md](backup-and-restore.md) 为准。

## 生产前置检查

远程写操作前必须先给出计划并获得用户确认。发布计划应明确包含失败时的自动路由回切、恢复 backup units 和既定 rollback/recovery；用户确认该计划后，这些同一 release 内的自动安全动作无需逐项重新确认。runner 已退出后的人工 reconciliation、改变恢复分支或计划外写操作仍须再次确认。确认后：

以下清单用于应用产物发布。运维资产先按下一节分流，只执行适用项，不得伪造 candidate 或 VM 结果。

1. 检查本地 Git 状态，保留无关改动。
2. 记录完整 40 位 commit SHA，并推送到用户 fork。
3. 记录 change class、是否需要 VM gate；应用产物记录构建主机和 `candidate_image_id`，运维资产明确记录 `not_applicable`。
4. 检查 RackNerd 与 VM 的固定 worktree、marker、origin 和 clean 状态。
5. 记录当前生产 image 为 `pre_switch_image_id`。
6. 记录可用的更早稳定镜像为 `older_fallback_image_id`。
7. 保存当前 Compose 文件并计算 SHA-256。
8. 确认生产 PostgreSQL、Redis 和当前应用健康。

应用 candidate 构建、传输和验证期间，生产旧容器继续运行。

已接入签名 Gate 的不兼容迁移不得手工拼接生产命令。使用仓库一键入口，并要求 Gate 未过期、未 claim/consume、VM validator 与发布资产 checksum 完全匹配；任一 SSH 回包不确定状态必须从远端 committed marker 重新核验，不能凭本地异常猜测是否执行。

首次修复 Nginx ingress 的发布不要先运行独立 strict `doctor`：pre-Gate doctor 允许返回
`nginx_ingress_policy=needs_update`，由 `deploy-start`/`deploy-follow` 内部的宽松检查继续完成
签名 Gate、停写、migration preflight 和正式 recovery point，随后才由 release runner 应用
ingress。发布完成后再运行同 profile、同完整 SHA 的 strict `doctor` 和 `verify-result`。只有
不存在待修复 ingress policy 的普通发布，才适合把 strict `doctor` 放在发布前。

## 运维资产变更

`ops-readonly-assets` 只执行本地校验、review、提交推送和固定字段只读巡检。不得借该类别上传文件、控制服务或修改生产；无需应用镜像、Compose 备份或数据库恢复点。

`ops-control-assets` 不自动等于应用发布。若已证明资产不进入应用构建和运行时，可以不构建或切换应用镜像，但必须：

1. 在任何远程写前取得用户确认。
2. 根据影响面完成备份、停写、恢复点和回滚方案。
3. 保存目标文件旧 checksum 和恢复副本，不覆盖唯一可恢复版本。
4. 只更新获批资产或执行获批维护动作，不顺带 recreate 应用、PostgreSQL 或 Redis。
5. 验证目标 checksum、服务状态、备份行为和适用的健康路径。

任何 `deploy/`、build、install、Compose、Docker 或 systemd 自动化是否影响当前运行路径无法证明时，改按 `dev-gated` 或 `build-chain`，不得使用本节绕过镜像门禁。

## 插件包部署

本节只适用于已证明为 `plugin-package` 的独立插件包。它不构建或切换宿主应用镜像、不新增 release profile、不创建应用 DR baseline；若同时改变宿主协议、管理壳、migration、部署配置或包校验，先按完整应用发布更新宿主。

自动化入口为 `plugin-deploy-follow --commit <40位完整SHA>`。机器调用可改用 `plugin-deploy-start`，后台 worker 在包验证完成后自动执行 VM Gate 和生产安装或升级；`plugin-authorize` 只用于恢复旧版已停在授权检查点的 release，不是普通发布步骤。发布状态位于 `.tmp/plugin-releases/<plugin-id>-*/`，与宿主 `.tmp/releases/` 隔离，但两类生产写共用全局发布锁。签名和管理员 API Key 配置只从未提交的 `.ssh.local` 中读取：

```yaml
plugin_signing:
  private_key: /工作区之外的/ed25519-pkcs8.pem
  key_id: baiyu-codex-state-v1
plugin_admin:
  vm:
    api_key: VM 接受的管理员 API Key
  production:
    api_key: 生产接受的管理员 API Key
```

插件自动发布不接受命令行密码、JWT、TOTP 或 API Key 参数。管理员 API Key 只从未提交的 `.ssh.local` 读取并经 SSH stdin 传给 loopback helper；宿主只为插件包 `upload`、`upgrade` 和 VM 首次安装回收所需 `delete` 放行该机器凭据，其他插件写接口继续要求真人会话 step-up。生产 loopback 必须先从 `/opt/sub2api/active-app` 读取并校验当前 `18080/18081` active port，再把另一个槽位仅作为连接失败后的只读核验回退；禁止固定命中 `18080` 或优先操作正在排空的旧槽位。任何上传或升级响应丢失都先进入 `blocked_reconciliation`，不得直接重传。

### 首次安装

1. 记录插件源码完整 SHA、插件 ID/版本、amd64/arm64 包 SHA256、manifest、签名 `key_id`、当前宿主版本和 Host API/features。
2. 确认生产 `plugins.allow_unsigned=false`，并核验插件包使用受信的 Ed25519 签名。`baiyu.codex-state` 使用宿主内置且仅绑定该插件 ID 的 `baiyu-codex-state-v1` 公钥；私钥只保留在本机工作区外。更换 key ID 或公钥属于宿主信任根轮换，必须先发布并验证宿主，再发布新签名包。其他第三方插件仍使用 `trusted_publishers`。
3. 先通过插件列表或详情证明同插件 ID 不存在，再由已确认发布计划中的管理员 API Key 调用 `POST /api/v1/admin/plugins/upload`，multipart 字段 `plugin`；只上传生产实际架构对应的包。JWT 管理会话仍走 step-up。当前宿主对部分 disabled、error 或 incompatible 安装仍可能接受同 ID upload 替换，但该路径没有升级维护事务，运维流程必须拒绝并改用 upgrade。
4. 安装结果必须是同一插件 ID/版本、`signature_status=trusted`、兼容且 disabled。安装授权不包含保存秘密、enable、启用账号模型或真实采集。
5. 核验 PostgreSQL 权威 installation/artifact 与当前实例本地恢复出的 binary SHA；再逐个当前服务实例核验相同版本、SHA 和 Health。不得只验证负载均衡随机命中的实例。

### 在线升级

1. 保留上一受信包、包 SHA、插件版本、runtime binary SHA、配置 revision、启用状态和受管范围摘要；受管范围只记录数量和规范化 digest，不记录账号名称或 ID 清单。VM 写入前必须按“版本 + runtime binary SHA”找到本地重新验签通过的精确旧包；只有同版本但 binary SHA 不同的包不能作为恢复证据。
2. 禁止升级前主动停用 Scoped 插件。调用 `POST /api/v1/admin/plugins/:id/upgrade`，由宿主进入维护态、保留 strict、阻断新受管准入并按 PostgreSQL 请求守卫排空在途请求。
3. 目标包必须同插件 ID、受信签名、宿主兼容、能力集合不变且不能移除既有 secret 保护；升级不能隐式改变 managed scope。
4. 发布后逐实例核验目标版本、binary SHA、Health、配置 revision、启用状态和受管范围。数据库 artifact 发布成功但任一实例恢复失败时，整体最多为 partial/blocked，不能报告集群升级成功。
5. 升级事务失败时核验 maintenance journal 已恢复旧 installation、旧 runtime 或明确的 recovery pending 状态；不得手工覆盖 `plugins.data_dir`、删除数据库记录或把 strict 改成放行。

### 成功后的回退

升级已经成功、随后发现业务回归时，使用保留的旧受信签名包再次调用同一 upgrade 接口，形成新的受控维护事务。禁止删除数据库、回写表、手工替换实例目录或复用主动停用绕过准入。若旧 manifest 会移除当前受保护的 `config_secrets` 字段，或新版本已经写入旧版本无法读取的插件私有状态格式，普通包回退必须停止，先制定秘密字段兼容、状态迁移或恢复方案。

插件包首次安装、升级和回退仍必须由发布计划明确授权；确认后自动 runner 可连续完成 VM Gate 与生产包写入，不再在中途重复询问。上传完成后不自动执行真实采集；需要配置生产秘密、enable 或触发动作时分别明确授权，并在报告中记录 `real_collection_performed=false` 或本次获批结果。

## 备份门禁

### 纯前端

纯前端发布不要求新建数据库协调恢复点，但必须保留 Compose 备份、candidate image ID、`pre_switch_image_id` 和生产验证记录。不得因跳过数据库备份而跳过镜像或回滚门禁。

### 后端、数据库、配置和 fork

对非 incompatible migration 的发布，读取 [backup-and-restore.md](backup-and-restore.md) 并完成：

1. 记录 release gate 起始时间。
2. 确认 `sub2api-backup.service` 未运行，timer 未正在激活。
3. 启动已安装备份服务并要求 exit code 为 0。
4. 要求 RackNerd 新加密包 mtime 晚于 gate 起始时间。
5. 要求备份机存在同名 artifact 和 checksum 文件。
6. 比较本地和远端加密包 SHA-256。
7. 要求备份机至少有 `5 GiB` 可用空间。
8. 若 `/etc/sub2api-backup.env` 没有 Healthchecks 配置，报告 `backup completed, external alerting incomplete`。

旧的 `backups/latest` 工作流已经废弃，不能重新引入。DMIT 不参与备份数据处理；本地到 RackNerd 的发布控制和加密产物传输经 DMIT 1080 中继。

## 生产迁移前置校验

VM migration 通过不代表生产数据满足迁移合同。对不兼容 migration，生产停写后必须先执行只读 preflight，生成脱敏的 migration plan checksum，并核对：

- affected row、expected recomputed/preserved/skipped row 数量；
- `unproven_count=0`、`conflict_count=0`、`unexpected_count=0`；
- 关键外键/绑定完整性、历史数据不变量和 scheduler outbox 预期；
- 每个历史转换值都有可证明来源。以 `187` 为例，历史余额使用同一 `sync_run_id` 的 `upstream_key_rate_snapshots.recharge_rate` 作为证据；没有证据必须失败，不能使用当前配置值猜测。

执行顺序固定为：

```text
停写 -> 生产 preflight -> migration plan checksum -> recovery point
     -> 再次确认无写入 -> migration -> postflight 语义校验 -> 启动 candidate
```

preflight 或 postflight 任一不通过，禁止部分迁移和继续启动；必须恢复到迁移前 recovery point。

### Incompatible migration

不兼容 migration 必须：

- 停止生产应用并确认 `writes_frozen=true`。
- 在执行 migration 前完成上一节定义的生产数据 preflight，并保存 plan checksum。
- 先建立协调的 PostgreSQL + Redis recovery point。
- 只使用能证明“完成后应用仍保持停止”的维护备份入口。
- 如果普通备份 service 会自动重启应用，使用受控等价流程完成创建、加密、上传和校验，排除 restart step。
- 完成后再次确认应用仍停止、没有业务写入恢复。
- migration 完成后执行 postflight，核对实际受影响行、关联完整性、边界约束和事件/outbox 数量。
- 无法证明 no-restart 路径时停止发布。

不允许先让普通备份服务自动启动旧应用，再事后重新停机。

## 镜像切换

本节仅适用于应用产物类别。`ops-readonly-assets`、`plugin-package` 和不涉及应用镜像的 `ops-control-assets` 记录 `image_switch=not_applicable`。

切换前重新确认：

- full-SHA tag 指向已验证的 `candidate_image_id`。
- 普通 dev-gated 的 RackNerd candidate 与 VM loaded image ID 相同。
- build-chain 的 VM validated image 与 RackNerd loaded image ID 相同。
- Compose 备份 SHA-256 未变化。
- PostgreSQL 和 Redis 健康。

正常应用发布使用两个固定 loopback 端口 `18080/18081`。当前 active slot 从 `/opt/sub2api/active-app` 读取，candidate 总是在相反端口启动。候选通过内部 health、实例身份和 migration postflight 后，发布器原子替换受管 Nginx upstream，执行 `nginx -t && systemctl reload nginx`。Nginx graceful reload 保留旧 worker 的既有 SSE/WebSocket 连接，新请求进入 candidate。

切流后旧容器最长排空 3600 秒；只有能确认旧容器 ESTABLISHED 连接为零时才停止并删除。超时或连接状态无法读取时不强杀旧容器，而是尝试 reload 回旧 upstream，并保持 release 为待 reconciliation。候选成为 active 后不进行第二次容器重建，只持久化 image override、active slot 与后台 activation marker。除批准的 migration 方案外，不重启 PostgreSQL 或 Redis。

候选预热期间，请求面服务立即可用；主动探针、同步、cron、quota flusher 等非多实例安全后台任务保持休眠。旧容器排空并停止后才写 activation marker 接管这些任务。蓝绿 candidate 永久跳过启动时的全局 stale slot 清理，避免误删旧实例仍在使用的 Redis 并发槽位。

不兼容 migration 在生产验收前保持写入冻结。

## 双路径验收

### Nginx ingress 缓冲与观测合同

生产受管 Sub2API location 必须通过独立 include 同时满足：

```text
proxy_request_buffering on
proxy_buffering off
专用 upstream 状态与耗时 access log
```

请求缓冲与响应缓冲是两个独立合同。请求缓冲开启后，Nginx 先完整接收客户端上传，再把请求交给应用，避免应用提前返回或关闭连接时由 Nginx 生成 HTML 502；响应缓冲保持关闭以保留 SSE 流式语义。专用日志只记录 URI path、状态、长度和 upstream 耗时，不记录 query、请求体或认证信息。

该配置只能在生产领取并验签 Gate、停写、migration preflight 和正式恢复点完成后由
release runner 应用。pre-Gate doctor 允许只报告 `nginx_ingress_policy=needs_update`；
`verify.sh` 与 post-deploy doctor 必须严格验证受管 include、文件 owner/mode、logrotate 和
`nginx -T` 的实际加载来源。专用 access log 也由 release runner 安装和校验，bootstrap 只
核验已有资产，不承担首次安装。Gate 消费前失败时恢复本 release 的 Nginx 事务快照；即使
ingress 回滚失败，也必须继续执行适用的路由回退或协调数据恢复，并将 ingress 状态保留为
`blocked_reconciliation`。

### Ingress transaction 崩溃恢复

runner 退出、SSH 超时或本地协调器崩溃后，必须从远端重新读取 transaction，而不是依据退出码
判断 Nginx 是否变更。transaction 必须同时通过目标文件集合和 checksum 校验；symlink、缺失
checksum、实际文件集合漂移或校验失败统一归类为 `unsafe`。

| 状态 | 允许的自动动作 |
| --- | --- |
| `applied` | 不允许 claim-only 清理；进入协调恢复或保持 `blocked_reconciliation` |
| `rolled_back` | 在其他 committed-state 条件全部满足时允许继续切换前清理 |
| `rollback_failed` | 保留 blocker 和失败证据，禁止清理或伪造回滚完成 |
| `recovery_restored` | 仅在恢复 marker、checksum、Nginx reload 和健康检查均通过后继续收口 |
| `unsafe` | fail-closed，停止自动恢复，转人工现场核验 |

因此，`applied`、`rollback_failed` 和 `unsafe` 不能被“runner 已退出”“旧容器健康”或
“Nginx active”替代解释；只有 `rolled_back` 和 `recovery_restored` 具备进入清理分支的资格，
仍需满足 active claim、migration、route、backup 和应用健康条件。

必须逐项记录 `pass / fail / not_checked`：

- 运行容器 image ID 等于 `candidate_image_id`。
- 应用容器 healthy。
- RackNerd direct HTTPS `/health` 返回 200。
- DMIT HTTPS `/health` 返回 200。
- 登录和 auth 成功，不暴露 token。
- Codex streaming 正常，Nginx 没有缓冲响应。
- 下划线请求头可用，Nginx `http {}` 中保持 `underscores_in_headers on;`。
- direct 和 PROXY v2 两条路径的真实客户端 IP 正确。
- 安全的 `2 MiB` 未认证请求能到达应用，而不是 Nginx 413。
- 启动日志没有 panic、fatal、migration、Redis auth 或 DB connection loop。

纯前端发布额外检查：

- 登录页面和变更页面。
- 静态资源加载。
- 浏览器 console 无新增错误。

运维资产额外检查：

- `ops-readonly-assets` 只报告本次重新采集的白名单状态。
- `ops-control-assets` 验证受影响的 unit、timer、备份、checksum、回滚入口和适用的双路径健康。

插件包额外检查：

- 管理 API 显示目标插件版本、trusted 签名、兼容状态和预期 enabled/disabled 状态。
- 所有当前应用实例均恢复相同 binary SHA 并报告健康；不能只验证一条公网请求。
- 首次安装没有自动保存秘密、扩大受管范围、启用账号模型或触发采集。
- 升级期间 strict、在途排空和失败恢复状态符合预期；已可能发送到上游的请求没有自动重放。
- 不执行真实账号或模型探针，除非发布计划另行明确授权；默认写 `real_collection_performed=false`。

只通过 direct 而未通过 DMIT，不能报告“生产完全健康”。

当前 production runner 不执行、也不得把任何账号池、Canary、模型调用或 upstream 探针作为升级条件；流式响应、usage attribution 和真实 IP 归因必须明确记录 `not_checked`，没有可用账号不得阻塞 Docker 镜像升级。direct/DMIT `/health` 与 candidate container health 只确认容器已启动、公开路由可达，不是模型探针，也不能被表述为流式能力通过。迁移、备份、配置不变量和恢复合同仍然适用。

## 业务语义验收

基础健康和双路径验收不能替代业务合同验证。每次后端、migration、scheduler、上游同步或计费发布，都应从本次变更中选择 1 至 3 条最关键的不变量，并按是否已恢复公开流量分阶段执行只读 SQL、Redis 或管理 API 断言：

- migration：目标记录和 checksum 正确，新增列、索引或约束存在。
- 上游倍率或金额：Key 权威值、绑定账号派生值、优先级和负载因子符合本次公式，异常计数为零。
- scheduler：本次涉及的 namespace、账号快照或 outbox 与数据库一致，不用 Redis key 总数代替具体不变量。
- 同步：检查发布后的最新一批 `upstream_sync_runs` 及其成功、partial、failed 数；最近 24 小时等历史聚合只能作为趋势，不能用旧 partial 误判当前发布。
- 新增管理功能：验证对应 API 或持久化合同；仅确认路由存在不能证明事务和幂等语义正确。

断言必须输出字段白名单中的布尔值、计数、状态或 checksum，不输出账号名、Key、凭据、原始行或完整响应。只有发布计划已包含写操作并获得确认时，才允许为验收主动触发同步或写入；否则报告 `not_checked`，不能暗中制造测试数据。

不兼容 migration 的 schema、数据重算、绑定、倍率、scheduler 和 outbox 不变量必须在保持停写、公开流量尚未恢复时验证；失败可直接进入既定协调恢复。恢复流量后的断言只覆盖 direct/DMIT `/health`、后台任务和最新同步等运行态行为；模型与 upstream 能力保持 `not_checked`。

恢复流量后的业务断言失败时，先立即重新停写并统计恢复流量后产生的新写入，再判断是当前发布回归、既有数据异常还是验收查询错误。此时禁止直接恢复迁移前数据库；必须明确选择前向修复，或制定包含新增写入核对、保留或补偿的数据恢复方案。无法证明来源时保持停写并报告 `blocked_reconciliation`。全部业务断言通过后再次运行同 profile、同完整 commit 的 `doctor`，要求 migration 状态、备份协议、VM/RackNerd/DMIT 和外部路径仍通过。

公开后进入 `blocked_reconciliation` 时禁止重新执行完整 `deploy`。只允许在同一 release ID、signed candidate 和 active claim 下继续剩余验收，或执行协调恢复；必须恢复 backup timer/自动同步并原子 consume/reconcile claim。当前仓库提供 `reconcile-inspect` 和受限的 `reconcile --mode recover`：后者只接受切换前 claim-only 条件，迁移、停写或公开流量已开始时保持 blocked，不用一次性脚本或手工编辑状态文件冒充正式收口。

## 回滚

### Compose 恢复前置检查

回滚前必须记录基础 Compose checksum、`docker-compose.release-active.yml` checksum 或 absent、缺失标记、`.env` 的 `COMPOSE_FILE` 状态和渲染 checksum。恢复顺序固定为：

```text
恢复 .env 与 Compose 文件集合
  -> 清除不在恢复点内的残留 override
  -> 显式设置 COMPOSE_FILE
  -> docker compose config --format json
  -> 校验渲染 image ID、挂载、端口和关键环境摘要
  -> 仅 recreate sub2api
  -> 双链路验收
```

SSH 中断或脚本报错时，先从远端重新读取 committed marker、`.env` 的 `COMPOSE_FILE`、override 状态、渲染 checksum 和当前容器 image；不能只依据退出码判断恢复结果。

### 无 migration

1. 若旧 slot 尚存且健康，原子恢复 pre-release Nginx upstream 并 graceful reload，写回 active slot；不停止 Nginx。
2. 停止未使用的 candidate，重新验证应用健康、认证、RackNerd/DMIT 双路径和日志。
3. 只有旧 slot 已不可用时，才恢复 Compose image reference 到 `pre_switch_image_id` 并 targeted recreate 稳定 `sub2api:18080`。

### Backward-compatible migration

只有 [dev-validation.md](dev-validation.md) 已证明旧 image 兼容迁移后 schema，才允许 image-only rollback。回滚后仍须完成全部生产验收。

### Incompatible migration

1. 继续保持写入冻结。
2. 恢复批准的 PostgreSQL recovery point。
3. 恢复 Redis RDB/AOF recovery point。
4. 恢复配置和 `pre_switch_image_id`。
5. 确认数据恢复完成后再启动旧应用。
6. 重新完成双链路、认证和日志验收。

不兼容 migration 禁止 Compose-only rollback。容器能启动不等于回滚安全。

## 版本基线

生产验收完成后，按 [backup-and-restore.md](backup-and-restore.md) 执行：

```text
candidate -> 上传 -> 隔离环境真实恢复 PostgreSQL/Redis
          -> manifest/count/checksum 验证 -> 原子晋升 verified
```

candidate 创建、上传、解密、镜像加载、PG/Redis 恢复、计数校验或原子晋升任一步失败，都保留旧 verified，并报告：

```text
partial: production healthy, disaster-recovery baseline incomplete
```

## 停止条件

备份、checksum、image ID、迁移、磁盘、健康、认证、流式、真实 IP、TLS 或恢复断言任一失败，立即停止。不得输出 secrets、原始连接资料、token、完整环境、展开后的 Compose 或宽泛日志。
