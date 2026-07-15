# 运维巡检与在线状态

## 目录

- [运维原则](#运维原则)
- [状态记录格式](#状态记录格式)
- [白名单采集](#白名单采集)
- [Windows SOCKS SSH](#windows-socks-ssh)
- [日常检查矩阵](#日常检查矩阵)
- [发布前巡检](#发布前巡检)
- [发布中巡检](#发布中巡检)
- [发布后验收](#发布后验收)
- [备份状态](#备份状态)
- [故障分层](#故障分层)
- [证书、入口和磁盘](#证书入口和磁盘)

## 运维原则

本文规定如何检查状态，不重新定义机器角色、IP、端口或目录。拓扑和最近快照以 [architecture-and-current-state.md](architecture-and-current-state.md) 为准。

“当前在线”“健康”“空间足够”“备份最新”都只能表示任务时刻的观测结果。每次发布、回滚、恢复、备份核验或故障排查都必须重新采集，不得复制上一次任务的绿色结论。

## 状态记录格式

每条结果至少包含：

```text
component: 组件或节点角色
environment: racknerd | dmit | backup | vm
assertion: 具体断言
result: pass | fail | not_checked
status: healthy | degraded | failed | unknown | not_checked
checked_at: ISO-8601 with timezone
method: 只读命令、HTTP、容器 health 或 checksum
evidence_summary: 脱敏的版本、布尔、计数、大小或错误摘要
freshness: fresh | stale | unknown
```

开始新任务时，历史状态自动变为 `stale`，直到本任务重新核验。无法连接或没有证据时写 `unknown`，不要推断。

## 白名单采集

允许记录：

- 节点角色、主机名、公开 IP、服务名和容器名。
- commit SHA、full-SHA tag、image ID、版本、镜像大小。
- HTTP 状态码、health 状态、迁移名/checksum、计数。
- 文件名、文件大小、mtime、SHA-256、剩余空间。
- service active/enabled、监听端口、证书到期时间。
- `pass/fail/not_checked`、`healthy/degraded/unknown`、时间和脱敏错误摘要。

禁止记录：

- `.ssh.local` 内容、密码、私钥、token、Healthchecks URL/token。
- `.env`、数据库 DSN、Redis 密码、解密后的配置或数据值。
- 完整容器 environment、完整 `docker inspect`、展开后的 Compose。
- `systemctl cat` 原始内容、宽泛日志和可能含凭据的请求体。

## Windows SOCKS SSH

RackNerd 的 Windows 巡检优先使用 skill 自带的固定脚本：

```text
python .agents/skills/sub2api-production-deploy/scripts/racknerd_readonly_status.py --config .ssh.local
```

运行约束：

- 依赖必须与 `scripts/requirements-readonly-status.txt` 精确一致；缺失或不一致时停止。只允许在获批的本地受控环境预装，生产巡检期间禁止临时联网安装。
- 脚本只接受当前本机 `connect.exe -S ... %h %p` SOCKS5 形式，只解析 endpoint，绝不执行 `proxy_command`。
- 不把 OpenSSH `ProxyCommand` 原样传给 Windows Paramiko。Paramiko 的 subprocess pipe 不能按 Unix socket 使用 `select()`，且不会替 OpenSSH 展开 `%h/%p`。
- 使用 PySocks socket 后，SSH host key 仍按 RackNerd 原始 `host:port` 从用户 `known_hosts` 校验；未知或不匹配立即停止，禁止 `AutoAddPolicy`。
- 禁止直连 fallback。代理失败写 `unknown`；直连超时不能证明应用故障。
- 脚本只输出固定枚举、HTTP code 和 image ID。任何 stderr、异常详情、host、user、代理 endpoint、密钥路径或原始远端输出都不得进入报告。

代理连接、SSH 认证、内部应用检查和公网 HTTPS 是不同层。某层失败只报告该层，不能用公网 200 掩盖内部未知，也不能用 SSH 客户端错误宣告业务下线。

采集应从源头使用字段级 allowlist，不要先抓完整输出再事后打码。

## 日常检查矩阵

### RackNerd

- Sub2API 容器 health 和实际 image ID。
- PostgreSQL、Redis health 和可连接性。
- Nginx active、证书有效期、`443` 和内部 `18443` 监听。
- 根文件系统、Docker Root Dir、构建缓存和生产 data 空间。
- `sub2api-backup.service` 与 timer 状态。
- 最近本地加密 artifact 的文件名、mtime、大小和 checksum。

### DMIT

- HAProxy active/enabled 和版本。
- `80/443/1030` 监听。
- 从外部或允许的检查点验证 DMIT HTTPS `/health`。
- 确认它没有数据库、Sub2API 容器或备份 artifact。
- 正常发布不修改 DMIT 配置。

### 47.85.205.94

- 最近加密 artifact 和 checksum 是否成对存在。
- 本地/远端 SHA-256 是否一致。
- 可用空间是否达到当前现场阈值。
- `candidate` 和 `verified` 指针是否存在且状态可解释。
- 最近一次真实 PostgreSQL/Redis 隔离恢复演练时间和结果。
- 不执行生产 health 检查，不把备份机当业务节点。

### 本地 VM

- `sub2api-dev` health、实际 image ID 和版本。
- VM 本地 PostgreSQL/Redis 健康。
- `/opt/sub2api-deploy/data-dev` 存在且没有生产挂载。
- 没有 RackNerd 地址、生产 DSN 或生产 SSH 隧道端口。
- Docker Root Dir 在构建/导入前后满足空间门禁。
- 同时检查 Docker Root Dir、containerd 根目录、`/tmp`、源码目录的 `df` 可用空间和 inode；按构建峰值而不是单一镜像大小判断。
- 记录构建缓存、镜像、容器和临时 archive 的分类大小；`du` 和 `docker system df` 仅用于观察。
- 记录 `vm_expansion_status=not_required|required|completed|failed`。扩盘后必须重新执行完整 `doctor`，不能续跑中断阶段。

## 发布前巡检

发布前重新检查：

1. Git 状态、完整 SHA、fork 推送和最终 diff 分类。
2. 应用产物发布检查 RackNerd/VM fixed worktree marker、origin、clean 状态；运维资产按分类写 `not_applicable`。
3. 应用产物发布记录当前生产 `pre_switch_image_id` 和可用的 `older_fallback_image_id`；运维资产不伪造镜像字段。
4. PostgreSQL、Redis、应用和入口 health。
5. 备份 service/timer、远端 checksum 和空间。
6. 需要 VM/image 的类别检查 Docker Root Dir、containerd、`/tmp` 和源码文件系统的峰值空间、inode 和回滚预留；导入前后重新采集，不以 image size 加 `2 GiB` 作为唯一门禁。
7. 当前状态均带本次 `checked_at`，旧快照不直接继承。

## 发布中巡检

- 旧生产容器保持运行。
- 记录 `candidate_image_id`、tag-to-ID、transfer SHA-256 和 loaded ID equality。
- 只更新应用 image，除获批迁移外不重启 PostgreSQL/Redis。
- 不兼容 migration 记录 `writes_frozen=true`，直到验收或恢复结束。
- 任一 gate、checksum、空间、health 或 auth 失败即停止。

## 发布后验收

逐项记录：

- 应用运行 image ID 和 health。
- RackNerd direct `/health`。
- DMIT `/health`。
- login/auth 不泄露 token。
- Codex streaming 未被 Nginx buffer。
- underscore headers。
- direct/PROXY v2 real client IP。
- 安全 `2 MiB` 未认证请求没有被 Nginx 413 拦截。
- 启动日志没有 panic、fatal、migration、Redis auth 或 DB connection loop。

纯前端发布还需浏览器登录、变更页面、静态资源和 console smoke。

## 备份状态

daily 备份状态至少区分：

- 生成成功或失败。
- 本地/远端同名和 SHA-256 是否匹配。
- 远端空间是否足够。
- Healthchecks 是否配置。
- 最近一次真实恢复演练是否通过。

版本基线必须区分：

```text
candidate -> restore pending -> restore passed -> verified
candidate -> restore failed -> partial，旧 verified 保留
```

candidate 已上传不等于 verified。

## 故障分层

| 现象 | 状态表达 | 处理原则 |
| --- | --- | --- |
| RackNerd direct 通过，DMIT 未测或失败 | 线路降级或未知 | 不报告生产完全健康，先修复/核验 DMIT |
| 生产健康，baseline 恢复失败 | `partial` | 保留旧 verified，停止灾备晋升 |
| 备份成功但无 Healthchecks | 外部告警不完整 | 如实报告，不伪装成完整监控 |
| 旧快照健康但本次无法连接 | `unknown/stale` | 重新建立观测，不继承旧结论 |
| VM 磁盘不足 | `degraded/failed` | 停止构建或导入，不执行 prune 破坏性清理 |

## 证书、入口和磁盘

- 证书检查只记录域名、SAN、到期时间和链验证结果，不记录私钥。
- Nginx 检查 `underscores_in_headers on;`、SSE 不缓冲和长超时是否生效。
- DMIT 检查 HAProxy PROXY v2 到 RackNerd `18443` 的连通性。
- 生产、VM 和备份机磁盘检查都记录文件系统、总量、可用量和时间。
- 同时记录 Docker Root Dir、containerd 根目录、`/tmp` 的总量、可用量、inode、清理前后 `df` 差值和扩盘状态。
- Docker cache 只观测大小和是否可复用；Snap 共享块可能使缓存看似可回收但实际不释放，发布流程不主动 prune。
- VM 扩盘必须保存分区表 checksum，确认分区连续后按“扩分区 -> `partprobe` -> 文件系统 resize -> `df` 复核”执行；任一步无法验证即停止。
