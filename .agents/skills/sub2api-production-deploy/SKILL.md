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
- 执行 `doctor`、`bootstrap-production` 或 `deploy` 前，必须用 `git rev-parse --verify <commit>^{commit}` 得到实际完整 SHA，并将同一个 40 位小写值传入所有阶段；禁止手工复制、截断或补写 SHA。
- 后端、数据库、迁移、fork/upstream、共享契约、配置语义、混合改动和不确定改动必须经过 VM Gate 的 `sub2api-dev`；本机可以运行跨平台静态/unit 门禁，但不得启动本地后端作为最终联调服务。
- 只有最终 diff 严格限定在 `frontend/`，且只包含 UI、样式、静态资源或前端测试时，才允许不导入新的 candidate 到 VM；浏览器 smoke 仍应把 API 代理到已验证的 VM Gate 服务。
- 所有会生成或切换应用产物的类别都走仓库根 `Dockerfile` 的完整后端发布流水线；只有严格的 `ops-readonly-assets` 或 `ops-control-assets` 可以按分类规则不构建应用镜像。
- 禁止 `docker system prune`、缺少缓存上限或保留量的 builder prune、删除卷、数据库、Redis、`data` 或备份目录。VM 空间低于 8 GiB 时只允许执行仓库版本化清理器中的一次容量有界 BuildKit GC：按 LRU 将可回收私有缓存压到 1 GB，并保留至少 1 GB 私有 BuildKit 缓存；不得手工扩大范围。
- 生产空间清理只能使用 `cleanup-production`：先 dry-run，再把原样 `plan_sha256` 传入 apply。只删所有 tag 均为 full-SHA 发布 tag、没有任何容器引用且不属于运行镜像、pre-switch 或恢复点的旧 Sub2API image；退出的 migration 容器及其镜像作为 reconciliation 证据保留。生产 BuildKit GC 固定为 `max-used-space=2gb,reserved-space=2gb`，禁止扩大范围。
- VM 空间必须按 Docker/containerd、`/tmp`、源码、构建/恢复副本和回滚预留计算峰值；清理最多一次，不能用 `du` 或 Snap 缓存推断可回收空间。
- VM 扩盘后必须完成分区、`partprobe`、文件系统 resize 和完整 `df`/inode 复核，并重新执行 `doctor`；不能续跑中断阶段。
- 本地开发机的 Go 全量门禁默认使用 `-p 2 -parallel 2`，并与 Vitest、typecheck、前端生产构建串行执行；该限制不改变 VM 的正式镜像构建并行度。
- signer 公钥、私钥派生公钥、RackNerd trust key、validator 和 Gate 中记录的 checksum 任一不一致，必须停止，不得通过 bootstrap 自动修复后继续发布。
- validator 与 signer helper 必须作为同一版本单元暂存、验签自测后一次激活；不得单独升级其中一个，也不得在 Compose 闭包未验证前删除旧容器或恢复数据库。
- 一个 release 只允许一个 runner、一个 active claim 和一个 candidate；runner 返回异常、SSH 超时或测试断言失败后，必须先按 committed marker 和结构化状态做 reconciliation，禁止并发或重复启动第二个 `deploy`。
- profile 级迁移状态不能代替单个迁移状态。生产 preflight 必须同时返回整体状态和每个关键迁移的 `absent`/`verified`/`unknown` 状态；混合状态按已提交迁移跳过、缺失迁移按顺序执行，任何 checksum 或语义不一致立即停止。
- 任何备份、checksum、image ID、迁移、磁盘、健康、认证或空间断言失败，立即停止并报告。
- 生产 deploy 的生命周期必须长于调用工具生命周期；调用端只负责启动和观察，不能拥有或终止 release runner。标准入口为 `deploy-start`、`status`、`wait`、`verify-result`，不得把裸前台 `deploy` 作为日常入口。

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
| `deploy/release.py` 一键发布或恢复 | 发布预检与状态恢复、发布 Runner 生命周期，并叠加该变更类别要求的全部 reference |

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

1. 读取强制 reference，执行 `git rev-parse --verify` 校验目标 commit，检查 Git 状态并记录完整 commit SHA。
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
python deploy/release.py deploy-start --profile <profile> --commit <40位完整SHA>
python deploy/release.py status <release_id>
python deploy/release.py wait <release_id> --timeout 900
python deploy/release.py verify-result <release_id>
```

`doctor` 和 `bootstrap-production` 可独立用于诊断和首次初始化；日常只执行 `deploy-start`。它启动的 worker 必须先检查本地、VM 与外部节点，再幂等 bootstrap RackNerd，最后检查 RackNerd；通过后才在 VM 唯一构建 candidate，并完成 VM 本地 PostgreSQL、Redis、`data-dev` 的正向迁移和真实恢复。只有 VM 签名 Gate 验证通过后，才允许向 RackNerd 传输同一 image ID。RackNerd 不得重新构建 candidate。

执行前必须读取 [release-doctor-and-recovery.md](references/release-doctor-and-recovery.md) 和 [deploy/release/README.md](../../../deploy/release/README.md)。首次使用的信任根 bootstrap 必须与普通发布分离，人工核验 VM 公钥指纹后再提交公钥；普通发布禁止创建或替换信任根。

以下远端标记任一存在冲突时停止并人工 reconciliation，禁止删除后重试：

- RackNerd `/opt/sub2api/releases/.active-release`
- release 目录中的 `.consumed` 或 `.recovered`

`migration_started`、迁移容器存在或 SSH 异常都不能证明迁移已提交；只能以远端 committed marker 与数据库 migration 记录共同确认。恢复时不得继承调用者的 `COMPOSE_FILE`、`COMPOSE_PROFILES` 等环境覆盖。

本地 `.tmp/releases/.release.lock` 使用操作系统文件锁，锁文件可长期存在；只有实际持锁进程阻止并发发布。

runner 长时间没有控制台输出时不得重复启动或凭沉默判定卡死。先按 [release-doctor-and-recovery.md](references/release-doctor-and-recovery.md) 的“长时间无输出诊断”只读检查进程和结构化状态文件。最终成功必须同时满足签名 Gate 已验证，`production-result.json` 的 `stage` 为 `production_verified` 或 `production_verified_after_reconciliation` 且顶层 `status=verified`，并且 `verify-result` 通过。正式 `reconcile --mode recover` 当前只覆盖切换前 claim-only 恢复；其他分支必须保持 `blocked_reconciliation`。

前台工具超时不等于 runner 退出。`deploy-start` 使用独立后台进程，在 `.tmp/` 原子记录 PID 启动 token、release ID、stdout/stderr 相对路径和 runner checksum；后续只跟踪该 PID 和结构化状态，禁止再次执行 `deploy`。Canary `curl exit 28`、公开后失败或 `blocked_reconciliation` 必须按恢复 reference 分层诊断，不能直接认定候选故障或跳过双链路验收。完整生命周期见 [release-runner-lifecycle.md](references/release-runner-lifecycle.md)。

Gate 必须绑定 commit、origin、VM identity、validator、runner、发布资产、migration checksum、candidate archive checksum 和 image ID。生产端必须再次验签并从 Gate 派生镜像身份，禁止用环境变量覆盖。

## 本次发布经验的硬化规则

- VM Gate 重放时，数据库已经存在的 migration 必须走幂等语义断言，状态记为 `verified`；只有数据库没有该 migration 记录且 checksum 预检通过时才记为 `absent`。禁止为了让 Gate 通过而删除 migration 记录、重建数据库或修改 marker。
- 生产 profile 可能处于混合状态，例如旧 migration 已记录而新 migration 缺失。先独立读取整体 migration 状态和关键 migration 状态，按有序 manifest 逐项处理；不能用“profile 不完整”推断所有 migration 都需要重跑。
- 生产恢复使用正式 recovery/reconciliation 入口。即使远端动作可能已经提交、但调用端只收到非零退出码或测试断言失败，也必须先读取 committed marker、数据库迁移记录、运行 image、active claim 和 backup unit，再决定继续候选或协调恢复。
- Redis 恢复必须证明认证来源、RDB/AOF 可读性和 TTL 对账；仅 `PING`、`DBSIZE` 或容器健康不能单独把恢复点晋升为 `verified`。恢复后必须重新执行 `doctor`，并以 Gate、production-result、Git 和线上运行 image 四类证据共同收口。

### VM 构建缓存与磁盘

- Docker 使用 containerd image store 时，`docker system df` 的 image/cache 数字包含共享逻辑大小，不能与 `/var/lib/containerd` 的物理占用相加。空间判断必须同时记录 `df`、containerd snapshots、Docker volumes、BuildKit records 和 release-gates 归档。
- VM Gate 构建前安装构建阶段失败 trap；构建失败、构建后空间断言失败或中断时，移除本次新 tag/image 并恢复构建前同名 tag。失败清理不得触碰原 candidate、当前 dev image 或 BuildKit cachemount。
- VM Gate 正常构建持续复用 Go module/build cache；清理器要求 Docker 所在文件系统至少保留 8 GiB 可用空间。低于该下限时，版本化 `vm-space-clean.sh` 可在清理旧无引用 Sub2API 镜像后执行一次 `docker buildx prune --all --max-used-space 1gb --reserved-space 1gb`，由 BuildKit 按 LRU 回收未被运行时镜像占用的缓存。`--all` 只有同时具备 1 GB 缓存上限和 1 GB 私有缓存保留量时才允许；禁止缺少任一容量边界的扩大清理。
- `release-gates` 保留当前待生产 candidate、当前 commit、失败证据和仍被本地 release 引用的归档；旧成功归档只有在本地 Gate 完整下载并校验后才能删除。匿名 PostgreSQL 卷即使 dangling 也不能自动删除。

### 生产镜像与 BuildKit 清理

- `cleanup-production` 属于 `ops-control-assets`，不构建、不切换应用镜像。它要求本地签名 Gate 和 terminal verified `production-result.json`，并在生产重新绑定 `.consumed` marker、运行镜像及同 release 的 `pre-image-id`；任一证据不唯一或不一致立即停止。
- 清理与 `prepare.sh` 共用 `/run/lock/sub2api-production-release.lock`，同时独占备份全局锁。存在 active claim、异常 `.prepared`、正在构建、活动备份或服务不健康时整体停止。
- 保护集合包含 current、pre-switch、所有状态的容器引用镜像及全部 release recovery point 的 `pre-image-id`。`sub2api-migrate-*` 只计数、不删除；`.consumed/.recovered`、candidate archive、Gate、volume、PostgreSQL、Redis、data 和备份均不在清理范围。
- dry-run 生成候选集 `plan_sha256`；apply 必须携带同一 checksum，候选漂移即停止。每张镜像删除前重新核验保护集合和 full-SHA tag，删除不使用 `-f`。逻辑 image size 只作观察，实际释放量只用清理前后同一文件系统的 `df -PB1` 差值报告。

### Release workspace 与 runner 恢复

- 将 `deploy-start` 预创建的 release 目录视为 workspace 合同，worker 只能安全复用。复用前确认它是普通目录且不是 symlink，并核对 `manifest.json`、`state.json` 中的 schema、release ID、profile 和完整 commit；启动 VM Gate 前要求 `gate/` 完全不存在。
- 遇到 release 目录 `FileExistsError` 且生产阶段仍为 `not_started` 时停止当前 runner，不重复启动同一 release。修复发布资产后必须使用新 commit、新 release ID 和新签名 Gate。
- `wait` 超时或 runner 非零退出只触发只读诊断，不代表可以重试。先执行 `status`；Gate 或 `production-result.json` 尚未生成时不得执行 `reconcile-inspect`，只核对 `runner.json`、`state.json`、committed marker 和受限错误摘要。

### 签名资产与 profile 兼容

- validator、Gate signer 或 DR signer checksum 变化时，先在隔离目录完成配套自测，再原子激活同一版本单元。日常更新固定使用 `REQUIRE_EXISTING_SIGNER_KEYS=true`，禁止创建、替换或轮换既有信任根。
- 在备份机分别 bootstrap verifier 与 promoter，并预置当前支持的 profile 目录。profile 195 保留历史单 migration checksum；profile 199 的 migration checksum 固定按以下方式生成：

```text
jq -cS '.manifest.migration_sha256' | sha256sum
```

- signer 必须同时自测 profile 195 和 199；集成测试至少覆盖 195 回归、199 成功和错误 checksum 拒绝。测试 Gate 必须由版本化 fixture 或测试过程生成，禁止依赖未版本化 `.tmp` 资产。

### 生产成功与灾备基线

- `verify-result=verified` 只证明生产切换成功。完整发布还需要真实隔离恢复、PostgreSQL/Redis 与 TTL 对账、签名 evidence 和 verified pointer 晋升。
- 缺少仓库化恢复 helper 或已批准的解密身份时，将版本基线报告为 `partial` 并保留 candidate；禁止用 synthetic evidence 冒充真实恢复或 verified 基线。

### 一次性收口清单

按顺序完成并留存脱敏结果：release pytest、VM signer integration、profile 195/199 DR integrations、`git diff --check`、完整 SHA commit/push、`doctor`、`deploy-start`、`status`/`wait`、`verify-result`、post-deploy `doctor`。任一步失败都先按结构化状态恢复事实，不得跳步或复用失败 release。

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
- [release-runner-lifecycle.md](references/release-runner-lifecycle.md)：持久 runner、状态观察、成功验真和 claim-only 恢复边界。
- [final-report.md](references/final-report.md)：发布、备份、恢复和状态报告模板。
