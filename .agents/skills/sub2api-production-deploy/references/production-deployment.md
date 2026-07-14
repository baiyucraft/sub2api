# 生产部署、验收与回滚

## 目录

- [文档用途](#文档用途)
- [生产前置检查](#生产前置检查)
- [运维资产变更](#运维资产变更)
- [备份门禁](#备份门禁)
- [镜像切换](#镜像切换)
- [双路径验收](#双路径验收)
- [回滚](#回滚)
- [版本基线](#版本基线)
- [停止条件](#停止条件)

## 文档用途

本文规定 production preflight、备份门禁、迁移处理、镜像切换、双链路验收和回滚。机器角色与端口只以 [architecture-and-current-state.md](architecture-and-current-state.md) 为准；备份细节以 [backup-and-restore.md](backup-and-restore.md) 为准。

## 生产前置检查

远程写操作前必须先给出计划并获得用户确认。确认后：

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

## 运维资产变更

`ops-readonly-assets` 只执行本地校验、review、提交推送和固定字段只读巡检。不得借该类别上传文件、控制服务或修改生产；无需应用镜像、Compose 备份或数据库恢复点。

`ops-control-assets` 不自动等于应用发布。若已证明资产不进入应用构建和运行时，可以不构建或切换应用镜像，但必须：

1. 在任何远程写前取得用户确认。
2. 根据影响面完成备份、停写、恢复点和回滚方案。
3. 保存目标文件旧 checksum 和恢复副本，不覆盖唯一可恢复版本。
4. 只更新获批资产或执行获批维护动作，不顺带 recreate 应用、PostgreSQL 或 Redis。
5. 验证目标 checksum、服务状态、备份行为和适用的健康路径。

任何 `deploy/`、build、install、Compose、Docker 或 systemd 自动化是否影响当前运行路径无法证明时，改按 `dev-gated` 或 `build-chain`，不得使用本节绕过镜像门禁。

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

旧的 `backups/latest` 工作流已经废弃，不能重新引入。DMIT 不参与备份。

### Incompatible migration

不兼容 migration 必须：

- 停止生产应用并确认 `writes_frozen=true`。
- 先建立协调的 PostgreSQL + Redis recovery point。
- 只使用能证明“完成后应用仍保持停止”的维护备份入口。
- 如果普通备份 service 会自动重启应用，使用受控等价流程完成创建、加密、上传和校验，排除 restart step。
- 完成后再次确认应用仍停止、没有业务写入恢复。
- 无法证明 no-restart 路径时停止发布。

不允许先让普通备份服务自动启动旧应用，再事后重新停机。

## 镜像切换

本节仅适用于应用产物类别。`ops-readonly-assets` 和不涉及应用镜像的 `ops-control-assets` 记录 `image_switch=not_applicable`。

切换前重新确认：

- full-SHA tag 指向已验证的 `candidate_image_id`。
- 普通 dev-gated 的 RackNerd candidate 与 VM loaded image ID 相同。
- build-chain 的 VM validated image 与 RackNerd loaded image ID 相同。
- Compose 备份 SHA-256 未变化。
- PostgreSQL 和 Redis 健康。

切换动作只更新 `sub2api` 的 image reference，并执行 targeted Compose update。除批准的 migration 方案外，不重启 PostgreSQL 或 Redis。

不兼容 migration 在生产验收前保持写入冻结。

## 双路径验收

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

只通过 direct 而未通过 DMIT，不能报告“生产完全健康”。

## 回滚

### 无 migration

1. 恢复保存的 Compose image reference 到 `pre_switch_image_id`。
2. 只 recreate `sub2api`。
3. 重新验证应用健康、认证、RackNerd/DMIT 双路径和日志。

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
