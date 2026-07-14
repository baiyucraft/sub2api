# 发布与运维报告模板

## 目录

- [报告元数据](#报告元数据)
- [变更与门禁](#变更与门禁)
- [代码与镜像](#代码与镜像)
- [运维资产](#运维资产)
- [VM 验证](#vm-验证)
- [生产备份与恢复点](#生产备份与恢复点)
- [生产切换与验收](#生产切换与验收)
- [版本基线](#版本基线)
- [RPO、RTO 与遗留项](#rpo-rto-与遗留项)
- [脱敏声明](#脱敏声明)

只填写本任务重新核验的白名单字段，不复制原始命令输出。所有在线结论带 `checked_at`；没有证据写 `unknown` 或 `not_checked`。

## 报告元数据

```text
task_id:
task_type: release | rollback | backup | restore | drill | status | ops-change
started_at:
finished_at:
reported_at:
overall_status: success | partial | failed | stopped
state_rechecked_in_this_task: true | false
```

## 变更与门禁

```text
change_class: ops-readonly-assets | ops-control-assets | frontend-direct | dev-gated | build-chain
vm_gate_required: true | false
vm_gate_status: pass | fail | not_required | not_checked
classification_basis: 最终 diff 的脱敏摘要
migration_class: no-migration | backward-compatible | incompatible
rollback_mode: image-only | coordinated-data-restore | not_applicable
```

## 代码与镜像

```text
commit_sha: 完整 40 位 SHA
image_tag: full-SHA tag | not_applicable
candidate_image_id: sha256:... | not_applicable
pre_switch_image_id: sha256:... | not_applicable
older_fallback_image_id: sha256:... | not_available | not_applicable
build_host: racknerd | vm | not_applicable
source_worktree: 固定逻辑路径 | not_applicable
build_started_at: value | not_applicable
build_finished_at: value | not_applicable
image_size: value | not_applicable
transfer_sha256: value | not_applicable
source_load_tag_id_equal: pass | fail | not_checked | not_applicable
```

`not_applicable` 仅允许用于经过证明的 `ops-readonly-assets` 和不涉及应用镜像的 `ops-control-assets`。其他类别不得用它跳过镜像身份。

## 运维资产

适用时填写：

```text
remote_write_performed: true | false
user_confirmation: pass | not_required | missing
asset_test_status: pass | fail | not_checked
strict_review_status: pass | fail | not_checked
target_asset_checksum_before: value | not_applicable | not_checked
target_asset_checksum_after: value | not_applicable | not_checked
rollback_asset_status: pass | fail | not_applicable | not_checked
affected_service_status: pass | fail | not_applicable | not_checked
readonly_status_check: pass | fail | not_checked
```

## VM 验证

适用时填写：

```text
dev_database_boundary: pass | fail | not_checked
dev_redis_boundary: pass | fail | not_checked
data_dev_mount: pass | fail | not_checked
production_address_absent: pass | fail | not_checked
dev_db_dump_id:
dev_db_dump_sha256:
old_dev_image_id:
migration_checksum_status: pass | fail | not_checked
container_health: pass | fail | not_checked
auth_status: pass | fail | not_checked
changed_endpoint_status: pass | fail | not_checked
background_job_status: pass | fail | not_checked
log_gate_status: pass | fail | not_checked
```

禁止填写数据库值、DSN、token、密码或完整日志。

## 生产备份与恢复点

适用时填写：

```text
backup_kind: daily | release-recovery-point | version-baseline
gate_started_at:
artifact_filename:
artifact_size:
artifact_mtime_local:
artifact_mtime_remote:
local_remote_sha256_equal: pass | fail | not_checked
remote_free_space:
healthchecks: configured | external alerting incomplete | unknown
writes_frozen: true | false | not_applicable | unknown
no_restart_path_proven: true | false | not_applicable | unknown
```

## 生产切换与验收

```text
compose_backup_path: value | not_applicable
compose_backup_sha256: value | not_applicable
running_image_after_switch: value | not_applicable
application_health: pass | fail | not_checked
racknerd_direct_health: pass | fail | not_checked
dmit_health: pass | fail | not_checked
auth: pass | fail | not_checked
streaming: pass | fail | not_checked
underscore_headers: pass | fail | not_checked
real_client_ip_direct: pass | fail | not_checked
real_client_ip_proxy_v2: pass | fail | not_checked
two_mib_request: pass | fail | not_checked
startup_log_gate: pass | fail | not_checked
frontend_browser_smoke: pass | fail | not_required | not_checked
```

如果 direct 通过但 DMIT 未通过，整体不能写 `success`。

## 版本基线

```text
baseline_candidate:
baseline_candidate_sha256:
recovery_point_reference:
image_load_id_check: pass | fail | not_checked
config_manifest_check: pass | fail | not_checked
postgres_restore: pass | fail | not_checked
redis_restore: pass | fail | not_checked
counts_and_migrations: pass | fail | not_checked
temporary_material_destroyed: pass | fail | not_checked
verified_pointer_promoted: pass | fail | not_checked
baseline_status: verified | partial | not_required | not_checked
```

如果生产健康但基线创建、上传或实际恢复失败，必须写：

```text
partial: production healthy, disaster-recovery baseline incomplete
```

旧 verified 在原子晋升完成前必须保持不变。

## RPO、RTO 与遗留项

```text
target_rpo: 已确认数值 | not_defined
measured_rpo: 实测数值 | not_measured
target_rto: 已确认数值 | not_defined
measured_rto: 实测数值 | not_measured
drill_id:
retention_policy_verified: pass | fail | unknown
capacity_risk: none | degraded | unknown
open_items:
```

不能把“每日备份”自动写成 24 小时 RPO，不能把估算恢复时间写成 measured RTO。

## 脱敏声明

报告不得包含：`.ssh.local`、密码、私钥、token、Healthchecks URL、`.env`、数据库 DSN、Redis 密码、响应 token、完整环境、展开后的 Compose、解密后的配置或数据值。
