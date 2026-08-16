from pathlib import Path


SCRIPT = Path(__file__).parents[2] / "release" / "backup-host-space-clean.sh"
REFERENCE = Path(__file__).parents[3] / "references" / "backup-and-restore.md"
PITFALLS = Path(__file__).parents[3] / "references" / "known-pitfalls.md"


def test_backup_host_space_clean_has_bounded_journal_contract() -> None:
    text = SCRIPT.read_text(encoding="utf-8")
    assert "[[ $backup_root == /srv/sub2api-backups ]]" in text
    assert "[[ $journal_root == /var/log/journal ]]" in text
    assert "journal_max_bytes=${JOURNAL_MAX_BYTES:-1073741824}" in text
    assert "upload_reserve_bytes=${UPLOAD_RESERVE_BYTES:-536870912}" in text
    assert "[[ $backup_device == \"$journal_device\" ]]" in text
    assert "journalctl --rotate" in text
    assert 'journalctl --vacuum-size="$journal_max_bytes"' in text
    assert 'raw_log="$log_dir/backup-host-space-clean.raw.log"' in text
    assert '} >>"$raw_log" 2>&1' in text
    assert '[[ $(stat -c \'%u:%g:%a\' "$raw_log") == 0:0:600 ]]' in text
    assert "(( free_after >= required_free_bytes ))" in text
    assert "rm -rf" not in text
    assert "find \"$backup_root\"" not in text


def test_backup_host_space_clean_binds_apply_to_plan() -> None:
    text = SCRIPT.read_text(encoding="utf-8")
    assert "schema=backup-host-space-clean-v1" in text
    assert '[[ $plan_sha256 =~ ^[0-9a-f]{64}$ && $plan_sha256 == "$plan_sha" ]]' in text
    assert 'flock -n 8' in text
    assert 'flock -n 7' in text
    assert 'flock -n 9' in text


def test_backup_host_cleanup_is_documented() -> None:
    reference = REFERENCE.read_text(encoding="utf-8")
    pitfalls = PITFALLS.read_text(encoding="utf-8")
    assert "备份机宿主日志清理合同" in reference
    assert "backup-host-space-clean.sh" in reference
    assert "5 GiB + 512 MiB" in reference
    assert "上传前空间只贴着 5 GiB" in pitfalls
