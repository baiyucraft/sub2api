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
- 后端、数据库、迁移、fork/upstream、共享契约、配置语义、混合改动和不确定改动必须经过本地 `sub2api-dev`。
- 只有最终 diff 严格限定在 `frontend/`，且只包含 UI、样式、静态资源或前端测试时，才允许跳过 VM 门禁。
- 所有会生成或切换应用产物的类别都走仓库根 `Dockerfile` 的完整后端发布流水线；只有严格的 `ops-readonly-assets` 或 `ops-control-assets` 可以按分类规则不构建应用镜像。
- 禁止 `docker system prune`、`docker builder prune`、删除卷、数据库、Redis、`data` 或备份目录。
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

不得只读取生产部署文档而跳过开发验证或备份门禁。

## 总决策

```text
最终 diff
  ├─ 纯 skill/固定只读巡检 -> ops-readonly-assets -> 不构建、不切换
  ├─ 纯维护/服务控制资产   -> ops-control-assets -> 操作门禁，不切应用镜像
  ├─ 严格纯 frontend UI   -> RackNerd 完整构建 -> 前端检查 -> 生产切换
  ├─ build-chain          -> VM 完整构建与验证 -> 同一镜像 -> RackNerd
  └─ 其他/不确定           -> RackNerd 构建 -> 同一镜像 -> VM 验证 -> RackNerd
```

详细分类和构建规则见 [change-classification-and-build.md](references/change-classification-and-build.md)。VM 的连接隔离和测试清单见 [dev-validation.md](references/dev-validation.md)。

## 执行顺序

1. 读取强制 reference，检查 Git 状态并记录完整 commit SHA。
2. 按最终 diff 选择 `ops-readonly-assets`、`ops-control-assets`、`frontend-direct`、`dev-gated` 或 `build-chain`；任何 `deploy/` 脚本默认按 `ops-control-assets` 或更严格类别处理。
3. 应用产物类别复用固定源码目录和依赖缓存，构建并记录 `candidate_image_id`；运维资产类别明确记录镜像字段 `not_applicable`。
4. 对需要 VM 门禁的改动，导入同一镜像、验证 VM 本地数据边界和容器行为。
5. 后端/数据库/fork/配置发布前执行生产备份门禁；不兼容迁移使用不会自动恢复写入的维护备份路径。
6. 应用发布只切换 `sub2api` 容器；运维资产只执行获批的目标操作。两者都按适用范围完成 RackNerd 直连和 DMIT PROXY v2 验收。
7. 适用的应用发布在生产通过后处理版本基线：`candidate -> 实际 PostgreSQL/Redis 隔离恢复 -> verified`。`ops-readonly-assets` 和不涉及应用/恢复点的 `ops-control-assets` 明确记录 `not_applicable`。
8. 按 [final-report.md](references/final-report.md) 输出脱敏结果；基线失败时只能报告 `partial`。

## 失败即停止

构建、空间、镜像 ID、传输 SHA-256、VM 连接边界、迁移恢复、备份复制、TLS、健康、认证、流式响应或真实 IP 任一断言失败，都不得继续生产切换。容器能启动不等于发布或回滚成功。

## 相关文档

- [architecture-and-current-state.md](references/architecture-and-current-state.md)：机器职责、链路、固定路径和带时间的状态快照。
- [change-classification-and-build.md](references/change-classification-and-build.md)：变更分流、完整构建、缓存和镜像身份。
- [dev-validation.md](references/dev-validation.md)：`sub2api-dev` 隔离、迁移前保护和验证。
- [production-deployment.md](references/production-deployment.md)：备份门禁、生产切换、双路径验收和回滚。
- [backup-and-restore.md](references/backup-and-restore.md)：三机备份、恢复点、基线晋升和灾备演练。
- [operations-and-online-status.md](references/operations-and-online-status.md)：只读巡检、状态时效和故障分层。
- [final-report.md](references/final-report.md)：发布、备份、恢复和状态报告模板。
