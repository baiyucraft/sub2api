from pathlib import Path


SCRIPT = Path(__file__).parents[2] / "release" / "backup-retention-clean.sh"
REFERENCE = Path(__file__).parents[3] / "references" / "backup-and-restore.md"


def test_backup_retention_script_has_fail_closed_contract() -> None:
    text = SCRIPT.read_text(encoding="utf-8")
    assert "[[ $backup_root == /srv/sub2api-backups ]]" in text
    assert "[[ $keep_daily =~ ^[0-9]+$ && $keep_daily -ge 2 ]]" in text
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
    assert "默认保留最新 2 组" in text
    assert "candidate" in text and "verified" in text and "profile 235" in text
    assert "plan_sha256" in text
