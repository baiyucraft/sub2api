from pathlib import Path


SCRIPT = Path(__file__).parents[2] / "release" / "backup-retention-clean.sh"
REFERENCE = Path(__file__).parents[3] / "references" / "backup-and-restore.md"


def test_backup_retention_script_has_fail_closed_contract() -> None:
    text = SCRIPT.read_text(encoding="utf-8")
    assert "[[ $backup_root == /srv/sub2api-backups ]]" in text
    assert "retention_days=${RETENTION_DAYS:-15}" in text
    assert "minimum_keep_daily=${MINIMUM_KEEP_DAILY:-2}" in text
    assert "[[ $retention_days =~ ^[0-9]+$ && $retention_days -ge 1 ]]" in text
    assert "[[ $minimum_keep_daily =~ ^[0-9]+$ && $minimum_keep_daily -ge 1 ]]" in text
    assert "cutoff_epoch=$((now_epoch - retention_days * 86400))" in text
    assert 'date_input="${stamp:0:8} ${stamp:9:2}:${stamp:11:2}:${stamp:13:2}"' in text
    assert "printf '%s\\t%s\\t%s\\t%s\\t%s\\n'" in text
    assert "if (( index > minimum_keep_daily && artifact_epoch < cutoff_epoch )); then" in text
    assert "[[ -d $backup_root && ! -L $backup_root ]]" in text
    assert "[[ -f $promotion_lock && ! -L $promotion_lock" in text
    assert "[[ $plan_sha256 =~ ^[0-9a-f]{64}$ && $plan_sha256 == \"$plan_sha\" ]]" in text
    assert "[[ -f $path && ! -L $path && $(stat -c '%h' \"$path\") == 1 ]]" in text
    assert "(( free_after >= minimum_free_bytes ))" in text
    assert 'rm -rf -- "$backup_root' not in text
    assert 'rm -rf -- "$daily' not in text


def test_backup_retention_reference_documents_protected_sets() -> None:
    text = REFERENCE.read_text(encoding="utf-8")
    assert "备份机容量清理合同" in text
    assert "默认清理文件名时间早于 15 天" in text
    assert "至少保留最新 1 组" in text
    assert "candidate" in text and "verified" in text and "profile 235" in text
    assert "plan_sha256" in text
