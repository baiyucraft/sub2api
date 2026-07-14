# 备份与灾备恢复

## 目录

- [恢复目标](#恢复目标)
- [三机分工](#三机分工)
- [三类恢复资产](#三类恢复资产)
- [每日备份](#每日备份)
- [不兼容迁移恢复点](#不兼容迁移恢复点)
- [受限接收与原子晋升](#受限接收与原子晋升)
- [备份 Unit 维护模式](#备份-unit-维护模式)
- [版本基线状态机](#版本基线状态机)
- [恢复演练](#恢复演练)
- [RPO、RTO 与保留](#rpo-rto-与保留)
- [密钥与告警](#密钥与告警)

## 恢复目标

保护以下内容：

- PostgreSQL 全量逻辑数据、globals、owner、权限、migration 和序列。
- Redis RDB、完整 AOF 目录及其 manifest。
- `.env`、Compose、`data/config.yaml`、模型价格文件、Nginx/证书配置和恢复 manifest。
- 应用 commit、不可变 image ID、镜像版本和所有 SHA-256。

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
  └─ 只做 HAProxy/ACME 线路转发，不接收、不保存、不恢复备份

本地 VM
  └─ 可做 sub2api-dev 验证或一次性隔离恢复，不是生产备份副本
```

生产三机不是三份热数据库副本。RackNerd 是生产数据源，备份机是加密 artifact 存储，DMIT 是线路机。

解密和真实恢复必须发生在本地 VM 或另一个批准的一次性隔离环境。解密私钥、明文配置、临时 PostgreSQL 和 Redis 都不得进入 47.85.205.94。

## 三类恢复资产

### 每日加密包

由 RackNerd 的 `sub2api-backup.service` 生成并上传备份机。用于日常数据保护和常态 RPO 观测。

### 发布恢复点

后端、数据库、migration、配置或 fork 发布前生成。它必须包含可协调恢复的 PostgreSQL、Redis、配置和镜像身份。不兼容 migration 必须在应用停机和写入冻结状态下生成。

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

旧的 `backups/latest` 流程已经废弃，不得重新引入。DMIT 不得参与任何备份步骤。

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
- 晋升 bundle 必须精确包含 artifact、artifact checksum、manifest 和 bundle checksum；拒绝额外文件、重复字段、symlink 和路径穿越。
- artifact、checksum 和 manifest 全部在同父目录 staging 中验证后，用一次目录 rename 原子提交；禁止逐个移动 artifact/checksum。
- 已存在目标只有在 exact-content 校验完全一致时才视为幂等成功；冲突目标立即停止。
- 原子提交后，stdout 断开等报告错误不能把已经提交的状态伪装成未提交；最终报告从现场重新核验 bundle。

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
11. 全部通过后原子晋升 candidate 并原子更新 `verified` pointer。
12. 晋升完成前保留旧 verified baseline。

任何步骤失败：旧 verified 不变，报告 `partial: production healthy, disaster-recovery baseline incomplete`，不得报告完整成功。

## 恢复演练

恢复演练至少覆盖：

- 解密 key 可用性。
- 镜像 load 后 image ID。
- 配置和 manifest checksum。
- PostgreSQL 空库恢复零错误。
- Redis RDB/AOF 启动和 keyspace 可读。
- 关键计数、migration 和版本一致性。
- 临时材料销毁。

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
```

“每日生成”不能自动推导为“已达成 24 小时 RPO”；RTO 必须来自真实恢复演练。

daily、release recovery point、candidate、verified 和 previous verified 的保留数量或时间必须以现场配置核验为准。没有核验时写 `unknown`，不得凭空填写保留天数。清理前必须确认没有回滚或 verified 指针引用目标 artifact。

## 密钥与告警

- 加密私钥不得与密文包同库存放，不得进入 Git、日志、manifest 明文或最终报告。
- 只记录密钥存储类别、owner、轮换/吊销状态和恢复演练可用性，不记录路径和值。
- 解密出的 secret 只存在于临时恢复环境，验证完成后销毁。
- Healthchecks 只记录 `configured / missing / last_success / last_failure / checked_at`，不记录 URL 或 token。
- 没有外部告警不能抹掉已完成备份，但必须降级为 `external alerting incomplete`。
