# 备份与灾备恢复

## 目录

- [恢复目标](#恢复目标)
- [三机分工](#三机分工)
- [三类恢复资产](#三类恢复资产)
- [每日备份](#每日备份)
- [不兼容迁移恢复点](#不兼容迁移恢复点)
- [Redis 认证与 TTL 对账](#redis-认证与-ttl-对账)
- [受限接收与原子晋升](#受限接收与原子晋升)
- [备份 Unit 维护模式](#备份-unit-维护模式)
- [版本基线状态机](#版本基线状态机)
- [恢复演练](#恢复演练)
- [RPO、RTO 与保留](#rpo-rto-与保留)
- [密钥与告警](#密钥与告警)
- [备份密钥自动准备](#备份密钥自动准备)

## 恢复目标

保护以下内容：

- PostgreSQL 全量逻辑数据、globals、owner、权限、migration 和序列。
- Redis RDB、完整 AOF 目录及其 manifest。
- `.env`、Compose、`data/config.yaml`、模型价格文件、Nginx/证书配置和恢复 manifest。
- 应用 commit、不可变 image ID、镜像版本和所有 SHA-256。

Compose 备份必须保存完整的文件引用闭包：基础 Compose、`.env` 中 `COMPOSE_FILE` 引用的所有 override（包括存在时的 `docker-compose.release-active.yml`）、`include`、`env_file`、本地 config/secret 引用、路径映射、override 缺失标记、选择状态摘要和渲染后的 config checksum。不能只备份一个基础 Compose 文件。

发布恢复点还必须保存 Nginx 受管配置闭包：主配置与 sites-enabled、`sub2api-release-*.conf`、ingress snippet、专用 upstream logrotate，以及 `/etc/nginx/sub2api-release-backups` 中的历史站点快照。缺失的可选资产必须用明确 absent marker 表示，不能把缺失与备份遗漏混为一谈。

备份成功只表示 artifact 已生成并复制；只有在隔离环境中真实恢复 PostgreSQL 和 Redis，才能把版本基线标为 `verified`。

## 三机分工

```text
RackNerd
  ├─ 生产 PostgreSQL / Redis / Sub2API
  ├─ 生成本地加密 daily 包
  ├─ 生成发布 recovery point 和版本基线 candidate
  └─ 受限上传 artifact + checksum
             |
             v
47.85.205.94
  ├─ 接收加密 daily 包和版本基线
  ├─ 校验文件名、checksum、空间和保留策略
  ├─ 保存 candidate / verified 指针
  └─ 只向外部隔离恢复环境提供密文，不解密、不承载生产

DMIT
  └─ 做 HAProxy/ACME 线路转发，并可作为发布 SSH/加密产物的受限传输中继；不接收、不保存、不恢复备份

本地 VM
  └─ 可做 sub2api-dev 验证或一次性隔离恢复，不是生产备份副本
```

生产三机不是三份热数据库副本。RackNerd 是生产数据源，备份机是加密 artifact 存储，DMIT 是线路机。

解密和真实恢复必须发生在本地 VM 或另一个批准的一次性隔离环境。解密私钥、明文配置、临时 PostgreSQL 和 Redis 都不得进入 47.85.205.94。

## 三类恢复资产

### 每日加密包

由 RackNerd 的 `sub2api-backup.service` 生成并上传备份机。用于日常数据保护和常态 RPO 观测。

### 发布恢复点

后端、数据库、migration、配置或 fork 发布前生成。它必须包含可协调恢复的 PostgreSQL、Redis、配置和镜像身份。不兼容且不能 expand/contract 的 migration 必须在应用停机和写入冻结状态下生成。正常蓝绿发布使用在线 PostgreSQL `pg_dump` 与 Redis `redis-cli --rdb`，不得为备份重启 Redis、Nginx 或 active 应用。

### 版本基线

生产验收后生成 image/config 与 recovery point 引用的加密 candidate。candidate 只有完成实际 PostgreSQL/Redis 隔离恢复后，才可原子晋升为 `verified`。

纯前端发布可以免除新的数据库协调恢复点，但不能免除 Compose 备份、candidate image ID 和回滚镜像记录。

## 每日备份

非不兼容 migration 的发布或日常备份按以下顺序：

1. 记录 gate 起始时间。
2. 确认 `sub2api-backup.service` 未运行，timer 未正在激活。
3. 启动服务并要求 exit code 为 0。
4. 要求最新 RackNerd 加密包 mtime 晚于 gate 起始时间。
5. 要求备份机存在同名 `.age` 包和 checksum 文件。
6. 比较本地和远端加密包 SHA-256。
7. 要求备份机至少保留 `5 GiB` 可用空间。
8. 记录包文件名、大小、mtime、checksum 和状态，不记录明文内容。
9. 如果 `/etc/sub2api-backup.env` 没有 Healthchecks URL，报告：`backup completed, external alerting incomplete`。

旧的 `backups/latest` 流程已经废弃，不得重新引入。DMIT 不参与备份生成、保存、校验或恢复；它只可作为到 RackNerd 的加密传输中继。

## 不兼容迁移恢复点

不兼容 migration 的数据一致性比在线时间优先：

1. 只读检查 backup unit/entrypoint 的 allowlist 字段，确认是否会自动重启应用。
2. 停止 Sub2API，确认 `writes_frozen=true`，并确认没有业务写事务。
3. 使用能证明“完成后应用仍停止”的 maintenance mode 入口。
4. 如果普通 service 会 restart，使用受控等价流程生成、加密、上传和校验 artifact，排除 restart step。
5. 验证本地和远端文件名、checksum、空间和 artifact 可读性。
6. 再次确认 Sub2API 仍停止、没有业务写入恢复。
7. 无法证明 no-restart 路径时停止发布，不得用普通 service 碰运气。

回滚必须同时恢复 PostgreSQL、Redis、配置和 `pre_switch_image_id`，禁止只回退 Compose。

生产机只保存加密 recipient，不保存解密私钥。为保证迁移后的即时协调恢复，停写备份阶段必须同时在 root-only release state 中保留一份临时明文恢复 tar，并完成 checksum 与 tar 可读性校验；runner 只有收到 `local_restore_point_ready=true` 才能迁移。该临时 tar 不上传异地、不进入 Git 或日志，发布成功清理明文状态时必须销毁；长期保留的恢复资产仍为加密包。禁止让生产 `restore.sh` 依赖不存在的解密私钥。

### Compose 恢复合同

恢复启动前必须在临时目录恢复 `.env` 和完整 Compose 文件集合，不继承宿主机的 `COMPOSE_FILE`，也不依赖部署目录残留文件。显式渲染：

```text
docker compose -f docker-compose.yml [-f docker-compose.release-active.yml] config --format json
```

必须满足：

1. `COMPOSE_FILE`、override 存在/缺失状态和恢复点摘要一致。
2. override 不存在时有经过校验的 `no-release-active-override` 标记。
3. 渲染后的 `sub2api` image、volumes、ports、network 和关键环境摘要与恢复点一致。
4. Compose 渲染 image、`docker inspect` 运行 image 和恢复点 image 三者一致。

`.env` 恢复后不得无条件删除它引用的 override；状态不明时停止，不得先启动后判断。

## Redis 认证与 TTL 对账

Redis 恢复时不能假设密码位于环境变量。先从目标 Redis 容器的受限启动参数中解析 `--requirepass`，通过一次性进程的 stdin 传给 `redis-cli` 或恢复 helper；密码不得进入命令行参数、stdout、状态 JSON、日志或报告。解析不到密码、出现多个冲突参数或认证失败时立即停止。

恢复验收必须同时记录以下脱敏计数：

```text
redis_backup_dbsize
redis_backup_expiring_keys
redis_restored_dbsize
redis_restored_expiring_keys
```

Redis 会惰性清理已经逻辑过期的键，恢复后 `DBSIZE` 与 TTL 键数不保证同步下降。恢复验收必须满足以下单调不等式：

```text
redis_restored_dbsize <= redis_backup_dbsize
redis_restored_expiring_keys <= redis_backup_expiring_keys
(redis_restored_dbsize - redis_restored_expiring_keys)
>=
(redis_backup_dbsize - redis_backup_expiring_keys)
```

第三条保证备份中的永久键没有丢失；前两条允许备份生成与隔离恢复期间自然过期及惰性清理。该判定不能替代 artifact checksum、`redis-check-rdb`/AOF 完整性、认证来源、Redis 启动、`PING` 和 keyspace 可读性。任一计数缺失、为负数、类型错误、永久键减少或单调不等式不成立时，`redis_ttl_reconciliation` 必须为 `fail`，旧 `verified` 基线保持不变。

## 受限接收与原子晋升

受限上传 receiver 的 transport 命名规则与发布恢复点的语义名称是两层协议，不能混为一个字段：

```text
生产生成 encrypted artifact
  -> receiver 接受标准 transport name
  -> 校验 transport checksum
  -> 备份机映射 immutable release name
  -> exact-content bundle 原子晋升
```

执行要求：

- 上传前先核验 receiver 实际允许的 transport class 和文件名；不得反复用不被接受的 release 名碰撞接口。
- maintenance 输出同时记录 transport name、release name 和 SHA-256，但不得记录 secret。
- maintenance/release recovery point bundle 必须精确包含 `artifact.tar.age`、`artifact.tar.age.sha256`、`manifest` 和 `bundle.sha256`；其中 `bundle.sha256` 固定三行，按该顺序逐项校验。
- 版本基线 candidate 使用独立合同：bundle 必须精确包含 `artifact.tar.age`、`candidate.tar.gz`、`gate.json`、`gate.sig`、`manifest`、`SHA256SUMS` 和 `bundle.sha256`；其中 `bundle.sha256` 固定六行，按前六个文件的顺序逐项校验。
- 两类合同都必须逐行校验精确 basename、64 位小写十六进制和实际文件 SHA-256，并拒绝额外文件、重复字段、symlink 和路径穿越；禁止把三行与六行合同互换，也不能简化为只校验 artifact 的单行 sidecar。
- artifact、checksum 和 manifest 全部在同父目录 staging 中验证后，用一次目录 rename 原子提交；禁止逐个移动 artifact/checksum。
- 已存在目标只有在 exact-content 校验完全一致时才视为幂等成功；冲突目标立即停止。
- 原子提交后，stdout 断开等报告错误不能把已经提交的状态伪装成未提交；最终报告从现场重新核验 bundle。

### 晋升测试与诊断合同

- 测试 candidate 必须用 `install` 显式设置 root owner、目录 `0700`、敏感文件 `0600/0400`，并保留真实六行 bundle；`cp -a` 产生的权限或 link count 不得作为生产合同。
- 集成测试必须同时覆盖所测资产对应的三行或六行 bundle 逐项匹配、锁 symlink 拒绝、candidate/verified 冲突、失败保留旧 verified、成功重放和临时文件清理。文件集合排序必须固定 `LC_ALL=C`（二进制安全路径使用 `LC_ALL=C sort -z`），避免不同 locale 造成真实晋升成功后的测试误报。
- 结构化 SSH runner 的 stderr、stdout 和字段集合是接口合同。失败诊断只能在临时测试根启用，不能把原始远端日志、环境或 secret 带入报告；不得因 stderr 被隐藏就重复执行可能已提交的晋升。

## 备份 Unit 维护模式

当 unit 文件位于 `/etc/systemd/system` 时，`systemctl mask --runtime` 只写 `/run`，不能可靠覆盖 persistent unit。停写维护必须使用经过审计的 persistent mask/restore 流程：

1. 修改前严格校验固定 state root、直接子目录、canonical path，并拒绝 symlink 和 `..`。
2. 先快照全部 unit 文件及 `is-enabled/is-active` 状态，生成并验证 checksum，再修改 systemd。
3. 同时 stop 所有相关 unit，把 persistent unit 替换为 `/dev/null`，`daemon-reload` 后验证 inactive + masked。
4. mask 任一步失败时恢复全部原文件和原状态；恢复失败时必须 fail-closed，重新 mask，不能留下混合状态。
5. restore 前验证 snapshot、marker 和全部文件，任一步失败时重新 mask，使同一 state 目录可重试。
6. committed/restored marker 只在全部验收后原子写入；重复调用必须做现场幂等验收。

禁止手工分步删除两个 unit、禁止无快照 unmask、禁止在备份完成前恢复 timer。

## 版本基线状态机

当前完整恢复审阅发布使用独立的每次 release recipient：签名 Gate 中
`recovery_gate.full_recovery_evidence.payload.source.approved_recipient_sha256`
约束公开 recipient，`approved_identity_sha256` 约束本机持久 identity。
执行器自动取回匹配 identity 并自检，只把公开 recipient 写入该 release 的
`backup-recipient.txt`；正式 `backup.sh` 再次核对指纹，显式用于本次备份。
这不改变 daily recipient，也不把解密 identity 复制到生产或备份机。

停机发布的四文件恢复点晋升，与版本基线的七文件 candidate 是两个合同。
`full-recovery-capture.sh` 在停机切换完成、candidate 已成为运行镜像后，以
`CAPTURE_PURPOSE=postrelease`、同一正式 `RELEASE_ID` 和
`EXPECTED_PRE_IMAGE_ID=<candidate_image_id>` 新建加密恢复点；其 receipt 必须记录
`capture_purpose=postrelease`、`captured_image_id`、artifact 字节数和
`cross_store_writes_frozen=false`。将该 artifact、receipt、已验签 Gate、Gate 签名和
candidate 归档放进私有 staging 后执行
`stage-dr-baseline-candidate.sh <release-id> <private-input-dir> <artifact-sha256>`。
脚本拒绝旧四文件 pre-switch 恢复点、image/Release 不匹配或 receipt 缺失，只组成新的
candidate；不会更新 `verified`。随后使用 `full_recovery_drill.py` 在隔离 Guest 中真正
解密和恢复本次 release 资产，并在同一 Guest 加载 Gate 绑定的 candidate 镜像。恢复旧
镜像成功不能替代 candidate 镜像加载观察。

`python -m release.postrelease_baseline build-evidence` 验证本次独立签名完整恢复
报告、精确 candidate bundle、同 Guest 的实际 image-load receipt 及审阅摘要，
再输出旧 signer 要求的 schema 1 证据；不能使用 Gate 前的另一个 capture 冒充
发布后的资产恢复。若旧晋升器要求的 Redis TTL 差值等式不成立，保留旧 verified，
不得把当前永久键下界不等式强行映射成旧合同成功。完成后用该模块的
`sign-promote` 子命令调用既有 signer 和 `promote-dr-baseline.sh`；签名存在时先验真
续跑。生成 key、candidate staging、单元测试或来源解密本身均不是基线晋升证明。

```text
candidate-created
  -> encrypted-and-checksummed
  -> uploaded-as-candidate
  -> isolated-restore-started
  -> PostgreSQL-restored
  -> Redis-restored
  -> manifest-and-counts-verified
  -> temporary-material-destroyed
  -> atomically-promoted
  -> verified
```

执行要求：

1. 在 RackNerd 创建唯一命名的加密 candidate。
2. manifest 必须包含 `candidate_image_id`、恢复点引用、版本和 SHA-256。
3. 上传到备份机的 `candidate` 名称，不覆盖当前 `verified`。
4. 在本地 VM 或另一个批准的非生产、非 DMIT、非备份机的一次性隔离环境拉取并解密 candidate。
5. 加载镜像，loaded image ID 必须等于 `candidate_image_id`。
6. 把配置恢复到临时目录，逐项验证 manifest checksum。
7. 使用版本匹配的临时 PostgreSQL 创建空库，执行 `pg_restore --exit-on-error`，按需恢复 globals/owners，并要求零错误。
8. 使用版本匹配的临时 Redis 加载 RDB 和完整 AOF 目录，要求 Redis 启动、`PING` 成功、keyspace 可读，但禁止输出值。
9. 对比 PostgreSQL/Redis key count、migration/checksum 和 manifest 关键断言。
10. 销毁临时数据库、Redis、解出的 secret、配置目录和测试镜像，只保留白名单结果。
11. VM 把白名单结果写入固定 `dr-evidence/<release-id>/<drill-id>/evidence.json`；文件必须包含严格 schema、候选与迁移 checksum、全部恢复断言、时间字段以及 Redis TTL 对账等式。只允许通过 `/usr/local/libexec/sub2api-sign-dr-evidence` 使用既有 signer 身份签名，禁止直接调用私钥。
12. 操作端使用仓库 trust key验签，备份机再次核对 evidence/signature checksum、candidate pointer 和全部绑定字段；任一不一致停止。

postrelease collector 的执行串必须保持为：

```text
停机切换完成
  -> CAPTURE_PURPOSE=postrelease fresh full-recovery-capture
  -> stage-dr-baseline-candidate (artifact + receipt + Gate + candidate archive)
  -> full_recovery_drill optional postrelease_candidate
       -> Guest 内真实 docker load/inspect
       -> Guest 销毁前保留 candidate-load.json
  -> same-Guest/rootfs/engine/time 校验
  -> full-review findings 绑定 candidate-load receipt
  -> build-evidence -> sign-promote
```

没有 `postrelease_candidate` 时只能输出 `candidate_load_status=not_required`，该路径不
得声称已完成版本基线；pre-Gate full DR 不得复用为 postrelease candidate load。
13. 全部通过后原子晋升 candidate 并原子更新 `verified` pointer。
14. 晋升完成前保留旧 verified baseline。

任何步骤失败：旧 verified 不变，报告 `partial: production healthy, disaster-recovery baseline incomplete`，不得报告完整成功。

### 真实恢复的前置阻断

在开始下载或解密前，先验证隔离环境具有按 [备份密钥自动准备](#备份密钥自动准备) 规则复用或自动生成的 age 解密身份、恢复 helper，并能在不接触备份机明文的前提下使用。用户已授权发布或演练即包含新备份专用 identity 的自动准备，不要求用户另行提供 key 或存放路径。已有密文必须与原 identity 匹配；缺失时保留旧资产，明确记录本次未取得匹配 identity、旧资产恢复验证未完成，并为当前验证自动准备新 identity 与新备份。只有 `candidate`、对应解密身份可用、镜像和恢复点来源一致时，才能进入 PostgreSQL/Redis 恢复。只有 signer 自测证据、VM fixture、合成计数或人工填写的 `pass` 字段时，必须保持 `restore pending`；不能把 bootstrap 自测证据复制成真实 drill，也不能向备份机晋升 `verified`。

## 恢复演练

恢复演练分为 `specialized` 与 `full`：前者在恢复相关变更时执行受影响链路，正常预算 5–15 分钟；后者使用真实加密恢复资产完成端到端灾备验证，正常预算 20–60 分钟。预算仅用于计划和报告，不是强制超时。

`full` 不阻塞普通发布，建议至少每 30 天一次；只有恢复算法、备份格式、信任链变化或真实恢复事故修复时，`full` 升级为生产前硬门禁。仅 profile 白名单、版本、自测目录及入口兼容扩展经精确内容审阅后执行 `specialized`，不重复完整演练。未知恢复关键变化先阻断审阅，不能用显式 VM Full 参数解除。每次演练必须绑定完整 commit、恢复 helper bundle SHA-256、恢复资产 checksum 和唯一 `drill_id`。

发布入口的 `--recovery-gate-mode full` 只完成完整门禁的 VM 恢复部分：绑定 `full` 分类、传输真实生产恢复包和旧镜像，并执行隔离 PostgreSQL/Redis 恢复与兼容检查。它不自动生成独立灾备 `drill_id`，也不替代下述生产级故障注入、RTO 测量和最终演练验真。

恢复演练至少覆盖：

- 解密 key 可用性。
- 镜像 load 后 image ID。
- 配置和 manifest checksum。
- PostgreSQL 空库恢复零错误。
- Redis RDB/AOF 启动和 keyspace 可读。
- Redis 密码来源可证明且未泄露，备份/恢复 `DBSIZE` 与 TTL key 数严格对账。
- 关键计数、migration 和版本一致性。
- 临时材料销毁。
- PostgreSQL、Redis、Compose、应用、Nginx、备份 units、claim 与 state cleanup checkpoint 均可幂等恢复；已完成阶段复核后跳过。
- orchestrator、restore、cleanup 和 reconcile 来自同一原子 helper bundle，不调用历史 release 内的旧版本。

演练只能记录版本、时间、大小、checksum、计数、状态和耗时，不记录数据库值、token、DSN 或解密后的配置。

## RPO、RTO 与保留

技能不擅自制定业务 SLA。每次备份或演练记录以下字段：

```text
target_rpo: 已确认数值或 not_defined
measured_rpo: 本次实际测量或 not_measured
target_rto: 已确认数值或 not_defined
measured_rto: 本次实际测量或 not_measured
checked_at: ISO-8601 with timezone
drill_id: 恢复演练编号
gate_tier: specialized | full
gate_trigger: 脱敏触发原因
estimated_minutes: 5-15 | 20-60
actual_minutes: 本次实测
recovery_helper_bundle_sha256: 小写 64 位 SHA-256
last_full_drill_at: ISO-8601 with timezone | not_available
next_full_drill_due_at: ISO-8601 with timezone | not_defined
```

“每日生成”不能自动推导为“已达成 24 小时 RPO”；RTO 必须来自真实恢复演练。

daily、release recovery point、candidate、verified 和 previous verified 的保留数量或时间必须以现场配置核验为准。没有核验时写 `unknown`，不得凭空填写保留天数。清理前必须确认没有回滚或 verified 指针引用目标 artifact。

### 生产恢复点 retention

生产 `/opt/sub2api/backups/release-state` 的恢复点不再无限期保留。只能使用版本化入口：

```text
python .agents/skills/sub2api-production-deploy/scripts/release.py cleanup-production-recovery-points --mode dry-run
python .agents/skills/sub2api-production-deploy/scripts/release.py cleanup-production-recovery-points --mode apply --plan-sha256 <plan_sha256>
```

固定策略：

- 最近 3 天内的全部有效恢复点保留；若窗口内不足 3 个，补足最近 3 个有效恢复点；
- 当前发布、上一发布、活动 claim、reconciliation、未进入 `consumed`/`recovered` 终态以及任何状态不明的目录始终保护；
- 只接受非 symlink、单硬链接、恢复包 checksum 通过且目录结构完整的恢复点；
- dry-run 输出候选 release ID、容量和 `plan_sha256`；apply 必须携带同一 checksum，候选漂移立即停止；
- apply 只删除恢复点目录内的恢复包及其发布元数据，不删除 release 目录、Gate、candidate、镜像、PostgreSQL、Redis 或其他备份资产；
- 清理前后必须记录根文件系统可用空间差值，不能把逻辑文件大小当作实际释放量。

恢复点 retention 不能替代 daily 备份或异地基线。生产恢复点没有异地同名副本时，仍按上述时间/数量规则清理，但必须在报告中标明历史恢复能力已缩短。

### 备份机容量清理合同

备份机空间不足时只允许使用版本化脚本：

```text
.agents/skills/sub2api-production-deploy/scripts/release/backup-retention-clean.sh
```

固定策略：

- 只检查 `/srv/sub2api-backups/daily`；默认清理文件名时间早于 15 天的 daily 包，同时至少保留最新 1 组完整 daily 包及其 `.sha256` 文件。策略可通过 `RETENTION_DAYS` 调整，但不得低于 1 天；`MINIMUM_KEEP_DAILY` 不得低于 1 组。
- 不删除 `baseline`、`releases`、`candidate`、`verified`、`recovery`、`release-logs` 或备份机上的 profile 恢复包。生产机恢复点使用上方独立 retention 入口。
- 只接受 root 所有、非 symlink、单硬链接且 checksum 匹配的成对 daily 文件。
- dry-run 生成候选文件、大小、mtime、checksum 和 `plan_sha256`；apply 必须携带同一 checksum，候选漂移即停止。
- apply 使用独立锁，逐文件复核后删除；删除后必须重新核对可用空间达到 5 GiB。
- 禁止 `rm -rf`、`docker/system prune`、手工删除 profile 235，或把 VM 清理器套到备份机。

标准流程：

```text
backup-retention-clean.sh dry-run
  -> 核对候选与保护集合
  -> 记录 plan_sha256
  -> backup-retention-clean.sh apply <同一 plan_sha256>
  -> doctor --node backup
```

如果没有超过 retention 窗口且超过保护下限的候选，或执行后仍无法达到 5 GiB，必须扩容备份机或进入人工 retention 审核，不得扩大自动删除范围。

### 备份机宿主日志清理合同

当 daily 已没有超过 15 天的自动清理候选且至少保留最新 1 组，但备份机根文件系统仍不足以承接下一个上传时，先检查与备份证据无关的宿主机日志占用。只有 `/var/log/journal` 明确异常增长时，允许使用：

```text
.agents/skills/sub2api-production-deploy/scripts/release/backup-host-space-clean.sh
```

固定策略：

- 只执行 `journalctl --rotate` 和容量有界的 `journalctl --vacuum-size=1G`。
- `journalctl` 的 stdout/stderr 原文只追加到备份机 root-only `release-logs/maintenance/backup-host-space-clean.raw.log`，结构化 stdout 不混入进度文本。
- 不读取日志内容，不删除 `/var/log` 其他文件，不触碰 `/srv/sub2api-backups` 中的 daily、baseline、release、candidate、verified、恢复包或 release 日志。
- dry-run 绑定根文件系统设备、1 GiB journal 上限、5 GiB 最低余量和 512 MiB 上传预留，生成 `plan_sha256`；apply 必须使用同一 checksum。
- apply 同时获取备份晋升、daily retention 和宿主清理锁；任一锁忙、路径为 symlink、设备不一致或计划漂移均停止。
- 清理后必须同时满足 journal 容量上限容差，以及备份文件系统至少 `5 GiB + 512 MiB` 可用空间。

标准流程：

```text
backup-host-space-clean.sh dry-run
  -> 核对只包含 journal vacuum
  -> 记录 plan_sha256
  -> backup-host-space-clean.sh apply <同一 plan_sha256>
  -> doctor --node backup
```

宿主日志清理不能替代备份 retention 或磁盘扩容。若 journal 已在 1 GiB 左右且空间仍不足，停止并扩容或单独审核 release retention；禁止降低 5 GiB 门槛。

### 备份机 release 证据清理合同

`/srv/sub2api-backups/releases` 保存发布 bundle、manifest、checksum 和恢复证据，不能使用 `rm -rf` 或按目录大小直接删除。需要整理历史 release 时，只能使用版本化脚本：

```text
.agents/skills/sub2api-production-deploy/scripts/release/backup-release-retention-clean.sh dry-run
.agents/skills/sub2api-production-deploy/scripts/release/backup-release-retention-clean.sh apply <plan_sha256> <显式批准的 release ID> ...
```

固定规则：

- 当前策略只保留最高版本 profile 的最新 1 个恢复包；不再保留历史版本或同一版本的旧恢复包。脚本默认以 `KEEP_LATEST_PROFILE_ONLY=true`、`KEEP_RECENT_RELEASES=1` 执行，历史包不受 30 天窗口限制而直接进入候选；如需临时恢复按 profile 保留策略，必须显式设置 `KEEP_LATEST_PROFILE_ONLY=false`，且不得低于每个 profile 1 个包；
- 该策略只作用于可验证且未被指针/恢复标记保护的 release bundle；`candidate`、`verified`、`current`、`previous`、`recovery`、`baseline` 等指针仍优先保护；
- 每次清理前仍须 dry-run、核对候选集合并使用同一 `plan_sha256` apply；
- 明确失败且超过 7 天的 release，在没有任何保护标记时可进入候选；
- 失败状态无法从脱敏发布日志明确证明，或日志同时包含成功与失败阶段时，继续保留；
- `candidates`、`promotion-input`、`verified-bundles` 等 profile 元数据目录跳过扫描，不当作 release bundle；
- `candidate`、`verified`、`current`、`previous`、`recovery`、`baseline` 指针指向的 bundle 永不进入候选；
- 含 `.prepared`、`.consumed`、`.recovered`、`.reconciliation`、`recovery-point.*` 或 `production-result.json` 的 bundle 永不进入候选；
- 只接受结构完整、manifest 与 bundle checksum 一致、非 symlink、单硬链接的 bundle；
- dry-run 输出候选 release ID、容量和 `plan_sha256`；apply 必须同时提供同一 checksum 和逐个明确批准的 release ID；
- apply 使用独立 retention 锁和 release promotion 锁，逐个复核 mtime、大小、checksum 后只删除 bundle 内四个文件，再删除空目录；
- 脚本不得遍历或删除 `/srv/sub2api-backups` 的其他目录，不触碰 daily、release-logs、candidate、verified、恢复包或项目外文件。

如果 dry-run 没有候选，或候选不足以满足发布上传余量，应扩容备份机或进行人工审核，不得扩大自动删除范围。

## 密钥与告警

- 加密私钥不得与密文包同库存放，不得进入 Git、日志、manifest 明文或最终报告。
- 公开报告、日志和演练证据只记录密钥存储类别、owner、版本/指纹、轮换/吊销状态和恢复演练可用性，不记录私钥路径和值；工作区外的受保护执行注册可保存私有位置引用，供后续自动定位。
- 解密出的 secret 只存在于临时恢复环境，验证完成后销毁。
- Healthchecks 只记录 `configured / missing / last_success / last_failure / checked_at`，不记录 URL 或 token。
- 没有外部告警不能抹掉已完成备份，但必须降级为 `external alerting incomplete`。

## 备份密钥自动准备

### 授权与用途

用户明确授权发布、停机部署或恢复演练后，执行器自行处理本次所需的备份加密密钥，不再询问“是否生成”、要求用户提供私钥或选择密钥路径。优先复用已登记、权限合格且 recipient 指纹匹配的专用 identity；没有可用 identity 时，使用 `age-keygen` 自动生成唯一版本的 age X25519 identity。执行器选择并建立符合下述权限要求的持久存储，不把常规密钥准备交回用户处理。

普通停机部署的即时协调回退使用停写阶段生成并校验的 root-only 临时明文恢复 tar，因此不依赖用户提供解密私钥。真实加密备份灾备演练和版本基线晋升另须证明新加密资产可被对应 identity 解密并完整恢复。备份解密 identity 与 VM Gate/DR 的 Ed25519 签名信任根是不同用途：自动生成只适用于新备份加密，不创建、替换或轮换既有签名信任根，也不生成虚假演练签名。

### 生成、保管与新资产绑定

1. 私钥在工作区外、当前用户独占的持久安全目录生成并保存；Linux 目录 `0700`、私钥文件 `0600` 且 owner 为执行身份，Windows 使用仅当前用户和必要系统管理主体可访问的显式 ACL，不能把 `chmod` 当作 Windows ACL 验证。拒绝 symlink、reparse point、硬链接及不安全父目录，采用独占创建，禁止覆盖现有 key。
2. 生成前选定与一次性 Guest、Gate 临时目录及单台 VM 生命周期分离的安全保管位置；需要向隔离恢复环境传入私钥时使用受控副本并单独验证其权限。私钥不得进入 Git、工作区 `.tmp`、日志、终端输出、生产机、DMIT 或异地备份机，也不得作为命令行参数或完整环境输出。
3. 登记 key 版本、生成时间、recipient 与 identity 的 SHA-256、权限检查结果和私有存储引用。公开报告和演练证据只保留版本、指纹、布尔状态及时间，不暴露私钥或存储连接资料；私有引用用于后续自动定位，不能只依靠当前进程记忆。
4. 使用新 key 做一次小型临时加密/解密自检并核对字节，随后销毁测试明文和隔离环境中的临时私钥副本。自检只证明 key 可用，不能作为真实 PostgreSQL/Redis 恢复、full DR 或版本基线通过的证据。
5. 只将公开 recipient 传给新建备份的受控加密步骤，使用唯一资产身份，并将 key 版本与 recipient 指纹绑定到新资产来源证据；现有已签 manifest 不回写。新资产须绑定当前生产 image、完整 Compose/config 闭包与一致的 PostgreSQL/Redis 快照，再进入真实隔离恢复、独立审阅、受信签名和原子晋升。生成 key 不代表这些入口已实现或门禁已通过。
6. 新 identity 无法解密旧密文。旧 key 缺失时，保留旧密文及原证据，明确记录历史恢复能力缺口；为当前发布新建并验证备份，不能把新 key 指纹写入旧 manifest 或将旧资产标为已恢复。更换既有 daily/生产备份 recipient 属于单独的密钥轮换，不能借一次演练静默修改。
7. 新 key 的持久主副本不属于 Gate/Guest 临时清理范围，至少保留到所有引用资产按既有 retention 合同退役。新基线真实恢复、独立签名和原子晋升完成前保留旧 verified 指针与旧 key；旧 key 的后续保留仍由其引用资产决定。

工具、权限、持久存储、备份创建或真实恢复失败时，执行器先处理其授权范围内的环境和实现问题，保留失败证据并报告具体阻塞；不能以“请用户提供密钥”代替可自行完成的新备份密钥准备，也不能因此放宽恢复或签名门禁。必须恢复某一旧密文且其原 key 确实缺失时，应说明该历史资产无法通过新生成 key 解密。

### 自动准备入口

在仓库根目录运行以下入口。它自动选择工作区外的持久存储，复用通过权限、registry、工具版本、recipient 和加解密自检的专用身份；没有专用身份时独占创建新版本。默认工具安装使用固定 `filippo.io/age` `v1.2.1` 与 Go checksum database，已有经验证同字节工具可通过 `--tool-dir` 指定。Windows 使用显式 ACL 并核验 owner，Unix 使用实际 owner/mode 核验。终端只输出版本、时间、SHA-256 与验证状态。

```powershell
$env:PYTHONPATH='.agents/skills/sub2api-production-deploy/scripts'
python -m release.backup_keys
```

入口不更改 daily recipient 或签名信任根。私有 registry 持久保存公开 recipient 和 key 引用；`PreparedIdentity.identity_path` 仅供执行器受控传输到隔离 Guest，不写入公开报告。`--expected-recipient-sha256` 用于查找特定历史资产的身份；未找到时明确拒绝，不能生成新 key 冒充匹配身份。新资产使用不带该历史指纹约束的自动准备流程。

`maintenance/release/full-recovery-capture.sh` 是 Gate 前的新基线捕获入口：绑定已核验的 active container/旧 image、持有备份全局锁、通过显式公开 recipient 加密新资产，保持应用和既有 timer 运行。PostgreSQL dump、counts、content digests 和迁移观察使用同一 export snapshot；globals 独立读取，Redis 使用独立 RDB，明确记录 `cross_store_writes_frozen=false`。这是灾备验证来源，不替代正式停写阶段的即时协调恢复点，也不发行 Gate 或恢复通过证据。

### Full DR 与发布后基线的可重跑调度串

以下顺序只记录入口和依赖，私有路径以执行器实际 root-only staging 为准；命令不会把 key、identity 或原始请求写入终端：

```text
backup_keys
  -> full-recovery-capture.sh (Gate 前，真实生产 source)
  -> full_recovery_guest stage (Guest manifest/images/units)
  -> full_recovery_authorization authorize-drill
  -> full_recovery_drill probe -> plan -> run
  -> full_recovery_authorization request (collector receipt + 独立 approval)
  -> full_recovery_authorization sign / sign-full-recovery-review.sh (同一签发入口二选一)
  -> deploy-follow --full-recovery-evidence (VM Gate / downtime deploy)
  -> fresh CAPTURE_PURPOSE=postrelease full-recovery-capture.sh
  -> stage-dr-baseline-candidate.sh <release-id> <private-stage> <artifact-sha256>
  -> full_recovery_drill run (配置含 postrelease_candidate；同 Guest docker load/inspect)
  -> postrelease_baseline build-evidence -> sign-promote
```

`full_recovery_drill` 的真实 Guest runtime 前置必须同时满足 Docker `29.1.3`、`containerd-snapshotter`/`overlayfs`、daemon 配置 SHA-256 `b19f3cd112d1066390ec91f16034398a19d767869019862c48d5d8c9fdcfcc10`、Compose 插件 SHA-256 `5633cb21e06a7c88c7ca48a9334d3d0f7f892e9605ae9e9a45f9a095d4ffceb8`，并显式校验 `/usr/share/zoneinfo/UTC`。这些是 provisioning/runtime 前置观察，不能单独证明恢复通过；失败必须保留诊断并保持 `verified` 不变。

Guest 服务固定 `UMask=0077`，Guest 入口在处理私钥/明文前设置进程掩码，`age` 解密子进程也显式采用 `0077`；输出从创建时即为 `0600`，不依赖 systemd 默认掩码，也不以事后修改权限或放宽 `0400/0600` 来源校验补救。权限缺陷修复会改变 helper 字节，必须重新生成提交、闭包、授权和独立 drill；旧失败现场保持不可变。

Guest Docker unit 使用 system containerd 的 `/run/containerd/containerd.sock`，离线镜像引用重建必须先核对 socket 的 Unix 类型、root owner、`0660` 权限及 Docker `ExecStart` 的 `--containerd` 地址，再连接同一 socket 和 `moby` namespace。`ctr images inspect` 是人类可读树形输出，不能当作 JSON；执行器直接 `ctr content get` 读取精确目标 OCI index、linux/amd64 manifest 和 config，逐项校验内容 SHA-256。只从与捕获摘要一致的现有引用重建 digest 引用，再以 `docker image inspect` 精确核验原引用的 image ID。禁止用当前 tag 猜测历史 descriptor、拉取、改写 Compose 或忽略摘要不匹配。当前 Guest 工具版本支持的 CLI 必须现场核验，不能以宿主工具的接口推断 Guest。

Guest provisioning 还须从已批准 rootfs 的 Nginx 运行数据提供 `/etc/nginx/mime.types`，保持 root `0644`、普通单链接文件，并绑定 3957 bytes 与 SHA-256 `632f37cafbdce8ebfba27f7d8264da1ea462880c4961c32354455c8ab722f7c3`；该摘要证明与批准来源同字节，不声称未取得的 dpkg 软件包来源。`stage` 与 Guest 系统准备都在服务写入前核验这一运行依赖。清空旧 Nginx 配置时不能连同运行数据一起遗漏，也不能修改捕获的生产主配置来隐藏缺失。正式 `nginx -t` 和恢复验真仍须执行；前置依赖通过不等于恢复通过。

发布后 candidate 必须来自同一 release 的 fresh postrelease capture，且在同一 Guest 产生实际 `docker load/inspect` receipt；`postrelease_baseline` 还要绑定独立 findings、candidate bundle checksum、engine/rootfs identity 与时间窗口。旧 Redis TTL 等式或任一恢复断言失败时，保留旧 `verified`，不得把健康发布报告成灾备基线完成。
