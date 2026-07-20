---
name: sub2api-production-deploy
description: 面向 Sub2API fork 的构建、开发门禁、生产发布、备份恢复和在线状态核验。适用于前端发布、后端或数据库改动、迁移、fork 同步、构建链修改、生产回滚、备份检查和灾备演练。
---

# Sub2API 部署与运维

使用本技能时用中文沟通。它描述本项目当前的 RackNerd、DMIT、异地备份机和本地 `sub2api-dev` 链路。主文件只负责决策和路由，详细步骤必须读取对应 reference。

## 不可违反的边界

- 读取 `.ssh.local` 只能作为连接输入；禁止输出、提交或记录密码、私钥、token、`.env`、完整环境变量、展开后的 Compose 和原始连接资料。
- 任何远程写操作前先给出方案并获得用户确认；只读核验也必须采用字段白名单。
- 生产旧容器在 candidate 构建、传输和验证完成前保持运行。
- 应用发布身份必须是完整 40 位 commit SHA、唯一 full-SHA tag 和不可变 `candidate_image_id`；运维资产至少记录完整 commit SHA。禁止用 `latest`、`main` 或短版本作为应用身份。
- 后端、数据库、迁移、fork/upstream、共享契约、配置语义、混合改动和不确定改动必须经过 VM Gate 的 `sub2api-dev`；本机可以运行跨平台静态/unit 门禁，但不得启动本地后端作为最终联调服务。
- 只有最终 diff 严格限定在 `frontend/`，且只包含 UI、样式、静态资源或前端测试时，才允许不导入新的 candidate 到 VM；浏览器 smoke 仍应把 API 代理到已验证的 VM Gate 服务。
- 所有会生成或切换应用产物的类别都走仓库根 `Dockerfile` 的完整后端发布流水线；只有严格的 `ops-readonly-assets` 或 `ops-control-assets` 可以按分类规则不构建应用镜像。
- 禁止 `docker system prune`、`docker builder prune`、删除卷、数据库、Redis、`data` 或备份目录。
- VM 空间必须按 Docker/containerd、`/tmp`、源码、构建/恢复副本和回滚预留计算峰值；清理最多一次，不能用 `du` 或 Snap 缓存推断可回收空间。
- VM 扩盘后必须完成分区、`partprobe`、文件系统 resize 和完整 `df`/inode 复核，并重新执行 `doctor`；不能续跑中断阶段。
- 本地开发机的 Go 全量门禁默认使用 `-p 2 -parallel 2`，并与 Vitest、typecheck、前端生产构建串行执行；该限制不改变 VM 的正式镜像构建并行度。
- signer 公钥、私钥派生公钥、RackNerd trust key、validator 和 Gate 中记录的 checksum 任一不一致，必须停止，不得通过 bootstrap 自动修复后继续发布。
- validator 与 signer helper 必须作为同一版本单元暂存、验签自测后一次激活；不得单独升级其中一个，也不得在 Compose 闭包未验证前删除旧容器或恢复数据库。
- 任何备份、checksum、image ID、迁移、磁盘、健康、认证或空间断言失败，立即停止并报告。

## 当前链路摘要

```text
海外/默认 -> RackNerd:443 -> Nginx -> Sub2API:18080
国内      -> DMIT:443 -> HAProxy(PROXY v2)
                         -> RackNerd:18443 -> Nginx -> Sub2API:18080

RackNerd -> PostgreSQL + Redis + 加密备份源
         -> 47.85.205.94（只保存和校验密文）

本地 VM -> sub2api-dev -> VM 本地 PostgreSQL + Redis + data-dev
```

权威拓扑、固定路径和最近一次脱敏状态见 [architecture-and-current-state.md](references/architecture-and-current-state.md)。在线状态不是永久事实；每次任务必须重新核验。

## 强制加载矩阵

| 任务 | 必须读取 |
| --- | --- |
| 纯前端发布 | 架构现状、分类与构建、生产部署、最终报告 |
| 后端/数据库/fork/混合发布 | 架构现状、分类与构建、开发验证、备份恢复、生产部署、最终报告 |
| 构建链或依赖改动 | 架构现状、分类与构建、开发验证、备份恢复、生产部署、最终报告 |
| 备份、恢复或灾备演练 | 架构现状、备份恢复、运维状态、最终报告 |
| 在线状态或故障巡检 | 架构现状、运维状态 |
| Skill/只读巡检资产 | 分类与构建、运维状态、最终报告 |
| 维护/服务控制资产 | 分类与构建、备份恢复、生产部署、运维状态、最终报告 |
| `deploy/release.py` 一键发布或恢复 | 发布预检与状态恢复，并叠加该变更类别要求的全部 reference |

不得只读取生产部署文档而跳过开发验证或备份门禁。

## 总决策

```text
最终 diff
  ├─ 纯 skill/固定只读巡检 -> ops-readonly-assets -> 不构建、不切换
  ├─ 纯维护/服务控制资产   -> ops-control-assets -> 操作门禁，不切应用镜像
  ├─ 严格纯 frontend UI   -> 本地前端检查 + Vite 代理 VM Gate smoke -> RackNerd 完整构建 -> 生产切换
  ├─ build-chain          -> VM 完整构建与验证 -> 同一镜像 -> RackNerd
  └─ 其他/不确定           -> RackNerd 构建 -> 同一镜像 -> VM 验证 -> RackNerd
```

详细分类和构建规则见 [change-classification-and-build.md](references/change-classification-and-build.md)。VM 的连接隔离和测试清单见 [dev-validation.md](references/dev-validation.md)。

## 执行顺序

1. 读取强制 reference，检查 Git 状态并记录完整 commit SHA。
2. 按最终 diff 选择 `ops-readonly-assets`、`ops-control-assets`、`frontend-direct`、`dev-gated` 或 `build-chain`；任何 `deploy/` 脚本默认按 `ops-control-assets` 或更严格类别处理。
3. 应用产物类别复用固定源码目录和依赖缓存，构建并记录 `candidate_image_id`；运维资产类别明确记录镜像字段 `not_applicable`。
4. 对需要 VM 门禁的改动，导入同一镜像、验证 VM 本地数据边界和容器行为；前后端混合改动的真实浏览器联调直接使用 VM Gate，不启动本机后端。
5. 后端/数据库/fork/配置发布前执行生产备份门禁；不兼容迁移使用不会自动恢复写入的维护备份路径。
6. 应用发布只切换 `sub2api` 容器；运维资产只执行获批的目标操作。两者都按适用范围完成 RackNerd 直连和 DMIT PROXY v2 验收。
7. 适用的应用发布在生产通过后处理版本基线：`candidate -> 实际 PostgreSQL/Redis 隔离恢复 -> verified`。`ops-readonly-assets` 和不涉及应用/恢复点的 `ops-control-assets` 明确记录 `not_applicable`。
8. 按 [final-report.md](references/final-report.md) 输出脱敏结果；基线失败时只能报告 `partial`。

## 浏览器验收服务路由

浏览器验收必须先确认页面请求实际到达哪个后端，再判断页面结果：

| 变更类别 | 前端页面 | API/后端 | 真实验收位置 |
| --- | --- | --- | --- |
| 严格纯 frontend UI | 本机 Vite 可用 | `VITE_DEV_PROXY_TARGET` 指向已验证 VM Gate | 本机页面 + VM Gate API |
| 前后端混合、后端、数据库、迁移或 fork | 使用 VM Gate candidate 自带前端，或直接访问 VM Gate 页面 | VM Gate candidate 的 Compose/PG/Redis | VM Gate |

- 纯前端本地 smoke 不得启动本地后端；启动 Vite 前记录代理目标，并用脱敏的版本字段或 API 响应确认目标是 VM Gate。
- 前后端混合改动禁止用“本机 Vite + 本机没有后端”作验收。`3000` 只代表页面开发服务器，若 `8080` 或代理目标没有后端，登录缓存、旧版本号和空列表都不能作为功能结论。
- 浏览器检查范围切换、登录、关键 endpoint、console 和响应版本；验收结束关闭本地 Vite，并确认监听端口已释放。

灾备 candidate 在进入任何晋升写操作前，必须先通过 [backup-and-restore.md](references/backup-and-restore.md) 的 exact-content bundle 合同。测试自测签名只能证明 signer 可用，不能替代真实解密、PostgreSQL/Redis 恢复和临时材料销毁；缺少批准的解密身份或恢复 helper 时必须停在 `restore pending`，保留 `candidate`，不得伪造 `verified`。

不兼容 migration 还必须在生产停写后完成数据 preflight、migration plan checksum 和 postflight 语义校验。VM fixture 通过不能替代生产数据证据；没有可证明来源的历史行必须停止。

## RackNerd 一键发布入口

对于已经提供 `deploy/release.py` profile 的 `build-chain + incompatible migration`，正式入口固定为：

```text
python deploy/release.py doctor --profile <profile> --commit <40位完整SHA>
python deploy/release.py bootstrap-production --profile <profile>
python deploy/release.py deploy --profile <profile> --commit <40位完整SHA>
```

`doctor` 和 `bootstrap-production` 可独立用于诊断和首次初始化；日常只执行 `deploy`。它必须先检查本地、VM 与外部节点，再幂等 bootstrap RackNerd，最后检查 RackNerd；通过后才在 VM 唯一构建 candidate，并完成 VM 本地 PostgreSQL、Redis、`data-dev` 的正向迁移和真实恢复。只有 VM 签名 Gate 验证通过后，才允许向 RackNerd 传输同一 image ID。RackNerd 不得重新构建 candidate。

执行前必须读取 [release-doctor-and-recovery.md](references/release-doctor-and-recovery.md) 和 [deploy/release/README.md](../../../deploy/release/README.md)。首次使用的信任根 bootstrap 必须与普通发布分离，人工核验 VM 公钥指纹后再提交公钥；普通发布禁止创建或替换信任根。

以下远端标记任一存在冲突时停止并人工 reconciliation，禁止删除后重试：

- RackNerd `/opt/sub2api/releases/.active-release`
- release 目录中的 `.consumed` 或 `.recovered`

`migration_started`、迁移容器存在或 SSH 异常都不能证明迁移已提交；只能以远端 committed marker 与数据库 migration 记录共同确认。恢复时不得继承调用者的 `COMPOSE_FILE`、`COMPOSE_PROFILES` 等环境覆盖。

本地 `.tmp/releases/.release.lock` 使用操作系统文件锁，锁文件可长期存在；只有实际持锁进程阻止并发发布。

`deploy` 长时间没有控制台输出时不得重复启动或凭沉默判定卡死。先按 [release-doctor-and-recovery.md](references/release-doctor-and-recovery.md) 的“长时间无输出诊断”只读检查进程和结构化状态文件。最终成功必须同时满足签名 Gate 已验证，`production-result.json` 的 `stage` 为 `production_verified` 或 `production_verified_after_reconciliation` 且顶层 `status=verified`，并且原 deploy 返回 `release=verified` 或经过审计的 reconciliation 入口返回 verified。当前 commit 没有正式 reconciliation 入口时，runner 失败后必须保持 `blocked_reconciliation`。

前台工具超时不等于 runner 退出。宿主工具存在短超时时，使用唯一隐藏后台进程启动发布，并在 `.tmp/` 记录 PID、release ID、stdout/stderr 路径；后续只跟踪该 PID 和结构化状态，禁止再次执行 `deploy`。Canary `curl exit 28`、公开后失败或 `blocked_reconciliation` 必须按恢复 reference 分层诊断，不能直接认定候选故障或跳过双链路验收。

Gate 必须绑定 commit、origin、VM identity、validator、runner、发布资产、migration checksum、candidate archive checksum 和 image ID。生产端必须再次验签并从 Gate 派生镜像身份，禁止用环境变量覆盖。

## 失败即停止

构建、空间、镜像 ID、传输 SHA-256、VM 连接边界、迁移恢复、备份复制、TLS、健康、认证、流式响应或真实 IP 任一断言失败，都不得继续生产切换。容器能启动不等于发布或回滚成功。

### 灾备发布硬门禁

- 先按资产类型选择 checksum 合同，禁止混用：maintenance/release recovery point 的 `bundle.sha256` 固定三行 `artifact.tar.age`、`artifact.tar.age.sha256`、`manifest`；只有版本基线 candidate 的 `bundle.sha256` 固定六行 `artifact.tar.age`、`candidate.tar.gz`、`gate.json`、`gate.sig`、`manifest`、`SHA256SUMS`。两者每行都必须是小写 64 位 SHA-256 加精确 basename，并拒绝额外行、重复名、路径穿越和 symlink。
- 候选目录和晋升 staging 必须用显式 `install` 建立 owner、mode、link count；不要用 `cp -a` 作为安全合同。锁文件、candidate pointer、verified pointer、临时目录和旧 verified 目标都要分别检查 canonical path、权限和 checksum。
- 远端 runner 隐藏 stderr 时，不能凭空重试写操作，也不能为了诊断放宽生产 allowlist。只允许在一次性测试环境启用受控诊断，先从 committed marker、pointer、checksum 和进程状态重建事实，再决定是否继续。
- 测试中的文件集合排序固定使用 `LC_ALL=C sort`（含 `sort -z`），文件集合、字段 allowlist 必须与真实输出顺序和合同一致；测试误报也必须修复后重跑完整集成，不能把“晋升已成功但断言失败”当作未提交而重复晋升。
- bootstrap 的 synthetic evidence 只能验证 verifier、trust 和 signer 安装；真实基线必须使用独立 drill ID、真实 candidate checksum，并在 VM/隔离环境完成解密、镜像 ID、Compose、PostgreSQL、Redis、migration、计数和临时材料销毁。任一缺失都只能报告 `partial`。

## 相关文档

- [architecture-and-current-state.md](references/architecture-and-current-state.md)：机器职责、链路、固定路径和带时间的状态快照。
- [change-classification-and-build.md](references/change-classification-and-build.md)：变更分流、完整构建、缓存和镜像身份。
- [dev-validation.md](references/dev-validation.md)：`sub2api-dev` 隔离、迁移前保护和验证。
- [production-deployment.md](references/production-deployment.md)：备份门禁、生产切换、双路径验收和回滚。
- [backup-and-restore.md](references/backup-and-restore.md)：三机备份、恢复点、基线晋升和灾备演练。
- [operations-and-online-status.md](references/operations-and-online-status.md)：只读巡检、状态时效和故障分层。
- [release-doctor-and-recovery.md](references/release-doctor-and-recovery.md)：一键发布预检、生产 bootstrap、committed-state 恢复、分节点验收和 VM 清理边界。
- [final-report.md](references/final-report.md)：发布、备份、恢复和状态报告模板。
