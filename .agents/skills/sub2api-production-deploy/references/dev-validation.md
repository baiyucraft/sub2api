# `sub2api-dev` 开发验证

## 目录

- [适用范围](#适用范围)
- [VM 边界](#vm-边界)
- [进入 VM 前](#进入-vm-前)
- [VM 空间与扩容](#vm-空间与扩容)
- [连接隔离门禁](#连接隔离门禁)
- [数据库保护](#数据库保护)
- [候选启动](#候选启动)
- [验证清单](#验证清单)
- [Migration 语义](#migration-语义)
- [Gate 失败条件](#gate-失败条件)

## 适用范围

本文对 `dev-gated` 和 `build-chain` 强制适用。`frontend-direct` 不进入 VM 门禁，但仍需完整镜像构建、前端检查和生产后浏览器 smoke。

## VM 边界

固定资源：

- 源码：`/opt/sub2api-src`
- 部署：`/opt/sub2api-deploy`
- 应用数据：`/opt/sub2api-deploy/data-dev`
- 应用容器：`sub2api-dev`
- PostgreSQL：VM 本地 `postgres` 服务或 `127.0.0.1:5432`
- Redis：VM 本地 `redis` 服务或 `127.0.0.1:6379`

本地 VM 仅用于开发验证，不是生产三机。它不得成为生产数据库、生产 Redis 或生产隧道的隐式副本。

## 进入 VM 前

1. 根据 [change-classification-and-build.md](change-classification-and-build.md) 确定镜像来源和 `candidate_image_id`。
2. 读取 VM Docker Root Dir、containerd 根目录、源码目录和 `/tmp` 的可用空间。
3. 按候选镜像层、解压临时空间、归档文件、构建缓存、数据库恢复副本、migration 临时空间、回滚镜像预留和安全余量计算峰值，不只比较镜像大小加 `2 GiB`。
4. 如使用中间文件，单独检查其所在文件系统和 inode；导入前后都要重新读取空间。
5. 优先使用 `gzip -dc | docker load`，避免在 VM 留下完整压缩包。
6. 导入后要求 Docker Root Dir、containerd 和临时目录仍满足本次峰值计划，并至少保留 `2 GiB` 安全余量。
7. loaded image ID 必须等于构建端 `candidate_image_id`，之后才允许绑定 full-SHA tag。

空间不足时停止。只允许清理过期 `/tmp` candidate 或经过引用检查的单个旧 Sub2API image；禁止 prune、删卷、删数据库、删 Redis、删 data 或删 backup。清理只能执行一次。

## VM 空间与扩容

- 清理前后以 `df` 的真实可用块为准记录释放量；`du` 只用于分类占用，不能推断可回收物理空间。
- Snap 缓存可能与已安装 Snap 共享物理块；Docker `system df` 仅作观察，不是授权清理依据。
- 需要扩盘时先保存 `sfdisk --dump` 及 checksum，确认目标分区是最后分区且空闲空间连续；不能在分区边界不明时操作。
- 扩盘顺序固定为：扩展分区 -> `partprobe` -> 按文件系统执行 resize（本 VM 为 `resize2fs /dev/sda3`）-> 重新读取分区、文件系统和 `df`。
- 内核无法重新读取分区，或文件系统增长未被验证时立即停止；不能继续构建、导入或恢复。
- 扩盘完成后必须重新执行完整容量检查和 `doctor`，不能直接续跑中断的发布阶段。
- 任何清理或扩容都不得触碰 Docker volume、PostgreSQL、Redis、`data-dev`、正式备份和当前验证容器。

## 连接隔离门禁

只采集 allowlist 字段，不输出完整环境。必须从实际容器设置和网络目标确认：

- PostgreSQL 目标是 VM 本地服务，不是 RackNerd 地址。
- Redis 目标是 VM 本地服务，不是 RackNerd 地址。
- 没有生产数据库主机、生产 Redis、生产 SSH 隧道端口。
- 容器使用 `/opt/sub2api-deploy/data-dev` 或其明确的开发挂载。
- 没有挂载生产 `.env`、生产 data 或生产配置目录。
- `sub2api-dev` 是待验证容器，而不是生产 `sub2api` 的别名。

发现任何生产地址、生产 DSN、隧道端口或生产挂载时立即停止，不得“先跑一下看看”。

## 数据库保护

候选包含 migration 或数据库行为变化时，在启动候选前：

1. 生成 VM 开发数据库 dump。
2. 对 dump 计算 SHA-256。
3. 记录当前 dev image ID。
4. 记录 migration 文件名和 checksum 集。
5. 保留旧 dev image，直到验证和失败恢复都结束。

对于会改写历史数据的 migration，VM 必须执行与生产相同的只读 preflight/postflight；测试数据必须覆盖无法证明来源的历史行。匿名 fixture 通过不代表生产数据满足迁移前置条件。

若 migration 已经运行但验证失败：

- 恢复 VM 开发数据库。
- 恢复旧 dev image。
- 重新验证旧容器 health。
- 停止生产发布，禁止把失败状态带入 RackNerd。

## 候选启动

- 只 recreate `sub2api-dev`。
- PostgreSQL 和 Redis 默认保持运行。
- 只有批准的方案明确要求时才重启依赖服务。
- 启动前再次确认 full-SHA tag 指向 `candidate_image_id`。
- 启动后确认容器实际 image ID 等于 `candidate_image_id`。

## 验证清单

必须逐项记录 `pass / fail / not_checked`：

- 容器最终变为 healthy。
- migration 文件名和 checksum 符合候选预期。
- 登录和 auth 流程成功，禁止输出 token。
- 变更相关 endpoint 行为正确。
- 代表性后台任务启动且没有重复错误。
- 日志没有 panic、fatal、migration failure、Redis auth failure 或 DB connection loop。
- 关键请求不连接生产上游或生产数据库。
- 候选 image 保留到生产验证完成。

## Migration 语义

### `no-migration`

没有数据库 schema 变化时，允许 image-only rollback，但仍需验证旧 image、健康、认证和双路径生产状态。

### `backward-compatible`

在 VM migration 完成后的 schema 上，实际运行迁移前的旧 dev image 并完成 smoke。只有旧 image 兼容性验证通过，生产才允许 image-only rollback。

### `incompatible`

开发验证阶段必须提前形成生产停写、PostgreSQL、Redis、配置和旧 image 的联合恢复方案。生产发布时必须冻结写入；生产回滚必须恢复数据和配置，禁止只改 Compose image。

## Gate 失败条件

以下任一项失败就停止：

- transfer SHA-256 不一致。
- loaded image ID 不一致。
- Docker 空间不足。
- dev 数据库备份或 checksum 失败。
- VM 连接隔离断言失败。
- 容器、health、auth、endpoint、后台任务或日志验证失败。
- migration checksum 异常。
- `space_before_build`、`space_after_build`、`space_before_import`、`space_after_import` 或 `space_before_restore` 任一不是 `pass`。
- 迁移数据 preflight 存在 `unproven`、`conflict` 或 unexpected 行。
- 旧 image 兼容性验证失败。
- 无法证明回滚路径。

最终报告只保留旧 image ID、candidate image ID、dump 标识、checksum、验证状态和时间，不保留数据值、DSN、密码或完整日志。
