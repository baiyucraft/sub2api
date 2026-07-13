# 架构与当前状态

## 目录

- [文档用途](#文档用途)
- [生产拓扑](#生产拓扑)
- [节点职责](#节点职责)
- [固定路径与容器](#固定路径与容器)
- [网络与数据边界](#网络与数据边界)
- [观测态记录格式](#观测态记录格式)
- [最近一次脱敏快照](#最近一次脱敏快照)
- [状态时效规则](#状态时效规则)

## 文档用途

本文是机器角色、网络链路、固定路径和数据归属的唯一事实源。其他 reference 只引用角色名和逻辑名称，不重复维护 IP、端口和目录。

本文分为两类内容：

- **设计态**：应该保持稳定的职责边界和流量拓扑。
- **观测态**：某次只读检查得到的状态，必须带 `checked_at`；新任务开始后必须重新核验。

不要把历史快照中的 `healthy` 当作当前在线保证。

## 生产拓扑

```text
海外/默认访问
  -> RackNerd 173.254.217.135:443
  -> Nginx
  -> Sub2API 127.0.0.1:18080

国内访问
  -> DMIT 179.255.148.240:443
  -> HAProxy TCP + PROXY v2
  -> RackNerd:18443
  -> Nginx proxy_protocol
  -> Sub2API 127.0.0.1:18080

生产数据
  RackNerd PostgreSQL + Redis
       |
       +-> 加密每日包、发布恢复点、版本基线 candidate
       +-> 受限上传 -> 47.85.205.94

开发验证
  本地 VM -> sub2api-dev
           -> VM 本地 PostgreSQL:5432
           -> VM 本地 Redis:6379
           -> /opt/sub2api-deploy/data-dev
```

正式业务域名是 `sub.baiyuapi.xyz`。公网业务只应使用 HTTPS；DMIT 是国内线路入口，不承载 Sub2API、数据库或备份。

## 节点职责

| 节点 | 角色 | 允许做什么 | 明确禁止 |
| --- | --- | --- | --- |
| RackNerd | 生产应用和生产数据 | 构建 candidate、运行 Sub2API/Nginx/PostgreSQL/Redis、生成加密备份 | 把生产数据复制到本地 VM；用浮动 tag 发布 |
| DMIT | 国内线路机 | HAProxy、ACME/HTTP 转发、PROXY v2 | 构建镜像、运行数据库、保存备份、承载业务应用 |
| 47.85.205.94 | 异地加密备份机 | 接收和校验加密 artifact、保存 `candidate/verified`、向隔离恢复环境提供密文 | 运行生产 Sub2API、保存解密私钥、接收明文 secret、在本机解密或恢复生产数据 |
| 本地 VM | `sub2api-dev` 开发门禁 | 用 VM 本地 PG/Redis 验证 candidate、执行临时恢复演练 | 连接 RackNerd 生产 PG/Redis、使用生产 `.env`、作为生产副本 |

生产三机只指 RackNerd、DMIT 和 47.85.205.94；本地 VM 不计入生产三机。

## 固定路径与容器

| 环境 | 源码目录 | 部署目录 | 关键容器/服务 |
| --- | --- | --- | --- |
| RackNerd | `/opt/sub2api-src` | `/opt/sub2api` | `sub2api`、PostgreSQL、Redis、Nginx、`sub2api-backup.service` |
| 本地 VM | `/opt/sub2api-src` | `/opt/sub2api-deploy` | `sub2api-dev`、VM 本地 PostgreSQL、VM 本地 Redis |
| DMIT | 不适用 | 系统服务目录 | HAProxy、ACME 转发 |
| 备份机 | 不适用 | `/srv/sub2api-backups`（以现场核验为准） | 受限上传入口；不运行生产应用 |

两个构建环境都复用 `/opt/sub2api-src`，不按 commit 创建新源码目录。构建缓存留在实际构建主机，发布过程不得 prune。

## 网络与数据边界

- RackNerd 的 PostgreSQL 和 Redis 是生产唯一数据源。
- VM 的 PostgreSQL、Redis、`data-dev` 必须是本地资源；SSH 隧道端口、RackNerd 地址、生产 DSN 一旦出现在 dev 容器配置中，立即停止验证。
- DMIT 的 `80/443/1030` 是线路或管理入口；DMIT 不接收备份流量。
- RackNerd 的 `18443` 只接受 DMIT 转发；生产应用监听 `127.0.0.1:18080`，不直接暴露应用端口。
- Nginx `http {}` 必须保持 `underscores_in_headers on;`，双链路都要验证。

## 观测态记录格式

所有“在线、健康、最新、空间足够、正在运行”的陈述必须记录：

```text
component: RackNerd / DMIT / backup / VM
assertion: 具体断言
status: healthy | degraded | failed | unknown | not_checked
checked_at: ISO-8601 with timezone
method: 只读命令、HTTP 检查或备份校验类别
evidence_summary: 脱敏后的版本、计数、布尔结果或错误摘要
freshness: fresh | stale | unknown
```

不得记录 `.ssh.local`、密码、私钥、token、`.env`、完整环境、数据库 DSN 或解密后的配置值。

## 最近一次脱敏快照

以下快照由本次任务的白名单只读检查产生。它不是当前状态保证，统一标记为 `freshness=stale`；新发布、备份、恢复或故障任务必须重新核验。

| 环境 | checked_at | method | freshness | 观测结果 | 状态 | 说明 |
| --- | --- | --- | --- | --- | --- | --- |
| 本地 VM | `2026-07-12T11:05:31Z` | SSH allowlist：容器 health、监听端口、磁盘、固定路径 | `stale` | `sub2api-dev` healthy；VM 本地 PG/Redis 监听；根文件系统约 90% 使用 | `degraded` | 磁盘余量约 4.9 GiB，构建或导入前必须重新检查空间 |
| DMIT | `2026-07-12T11:05:33Z` | SSH allowlist：service active/enabled、端口、版本、磁盘 | `stale` | HAProxy active/enabled；监听 `80/443/1030`；Nginx inactive | `degraded` | 未将 Nginx inactive 自动解释为故障；必须通过 DMIT HTTPS 实测确认线路 |
| RackNerd | `2026-07-12T11:05Z` | 配置的 Windows SOCKS ProxyCommand 连接尝试 | `stale` | 代理连接失败，未取得服务器字段 | `unknown` | 客户端没有记录秒级时间；不得沿用旧快照推断生产健康 |
| 47.85.205.94 | `2026-07-12T11:05:41Z` | SSH allowlist：artifact 元数据、端口、磁盘、容器摘要 | `stale` | 可读取近期 `.age` 与 `.sha256` artifact；磁盘约 64% 使用 | `degraded` | 本次未做完整 PG/Redis 恢复演练，也未记录外部告警 URL |

## 状态时效规则

1. 文档中的设计态可以长期引用；观测态必须有 `checked_at`。
2. 新任务一开始，旧观测态自动视为 `stale`，直到重新核验。
3. 只检查到一条路径时，不得汇总为“生产健康”；例如 direct 通过而 DMIT 未测，应报告线路状态未知。
4. RackNerd、DMIT、备份机和 VM 的连接失败都应记录为 `unknown` 或 `failed`，不能用“应该正常”替代证据。
