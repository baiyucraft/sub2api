from pathlib import Path


SCRIPT = Path(__file__).parents[2] / "release" / "backup-release-retention-clean.sh"
REFERENCE = Path(__file__).parents[3] / "references" / "backup-and-restore.md"
README = Path(__file__).parents[2] / "release" / "README.md"


def test_backup_release_retention_is_explicit_and_fail_closed() -> None:
    text = SCRIPT.read_text(encoding="utf-8")
    assert "[[ $backup_root == /srv/sub2api-backups ]]" in text
    assert "[[ $release_root == /srv/sub2api-backups/releases ]]" in text
    assert "retention_days=${RELEASE_RETENTION_DAYS:-30}" in text
    assert "failed_retention_days=${FAILED_RELEASE_RETENTION_DAYS:-7}" in text
    assert "[[ $retention_days =~ ^[0-9]+$ && $retention_days -ge 30 ]]" in text
    assert "keep_recent=${KEEP_RECENT_RELEASES:-1}" in text
    assert "[[ $keep_recent =~ ^[0-9]+$ && $keep_recent -ge 1 ]]" in text
    assert "keep_recent_per_profile=${KEEP_RECENT_PER_PROFILE:-$keep_recent}" in text
    assert "keep_latest_profile_only=${KEEP_LATEST_PROFILE_ONLY:-true}" in text
    assert "[[ $keep_latest_profile_only == true || $keep_latest_profile_only == false ]]" in text
    assert "release_is_explicitly_failed" in text
    assert "verify_bundle" in text
    assert "candidate_release_ids" in text
    assert "declare -A approved=()" in text
    assert "grep -Fxq \"$release_id\" \"$candidate_ids\"" in text
    assert 'rm -rf -- "$backup_root' not in text
    assert 'rm -rf -- "$release_root' not in text
    assert 'rm -rf -- "$bundle' not in text
    assert "flock -n 9" in text
    assert "flock -n 8" in text


def test_backup_release_retention_skips_metadata_and_keeps_only_latest_profile_by_default() -> None:
    text = SCRIPT.read_text(encoding="utf-8")
    for metadata_dir in ("candidates", "promotion-input", "verified-bundles"):
        assert metadata_dir in text
    assert "latest_profile=$(cut -f1 \"$all\" | LC_ALL=C sort -n | tail -n 1)" in text
    assert "if [[ $keep_latest_profile_only == true ]]" in text
    assert '$1 == profile && count++ < keep {print $4}' in text
    # The former per-profile policy remains an explicit opt-out fallback.
    assert 'count[$1]++ < keep {print $4}' in text
    assert "keep_latest_profile_only != true" in text
    assert 'release_is_explicitly_failed "$profile" "$release_id"' in text


def test_backup_release_retention_protects_recovery_and_pointer_markers() -> None:
    text = SCRIPT.read_text(encoding="utf-8")
    for marker in (
        "candidate",
        "verified",
        "current",
        "previous",
        "recovery",
        "baseline",
        ".prepared",
        ".consumed",
        ".recovered",
        ".reconciliation",
        "recovery-point.*",
        "production-result.json",
    ):
        assert marker in text


def test_backup_release_retention_is_documented() -> None:
    reference = REFERENCE.read_text(encoding="utf-8")
    readme = README.read_text(encoding="utf-8")
    assert "备份机 release 证据清理合同" in reference
    assert "backup-release-retention-clean.sh" in reference
    assert "显式批准的 release ID" in reference
    assert "只保留最高版本 profile 的最新 1 个恢复包" in reference
    assert "KEEP_LATEST_PROFILE_ONLY=true" in reference
    assert "明确失败且超过 7 天" in reference
    assert "元数据目录跳过扫描" in reference
    assert "backup-release-retention-clean.sh" in readme
