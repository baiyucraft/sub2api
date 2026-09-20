from pathlib import Path


SKILL_ROOT = Path(__file__).resolve().parents[3]
SKILL = SKILL_ROOT / "SKILL.md"
REFERENCES = SKILL_ROOT / "references"


def read_path(path: Path) -> str:
    data = path.read_bytes()
    for encoding in ("utf-8", "gb18030"):
        try:
            return data.decode(encoding)
        except UnicodeDecodeError:
            continue
    raise AssertionError(f"unsupported text encoding: {path}")


def read(name: str) -> str:
    return read_path(REFERENCES / name)


def test_plugin_package_is_a_distinct_release_class() -> None:
    skill = read_path(SKILL)
    classification = read("change-classification-and-build.md")
    report = read("final-report.md")

    for text in (skill, classification, report):
        assert "plugin-package" in text
    assert "不构建宿主镜像" in classification
    assert "不新增 release profile" in classification
    assert "release.py deploy-*" in skill
    assert "release.py deploy-*" in classification


def test_plugin_package_gate_requires_signature_and_multi_instance_restore() -> None:
    skill = read_path(SKILL)
    validation = read("dev-validation.md")
    production = read("production-deployment.md")

    for text in (skill, validation, production):
        assert "allow_unsigned" in text
        assert "Ed25519" in text
    assert "amd64" in validation and "arm64" in validation
    assert "PostgreSQL artifact" in validation
    assert "逐实例" in production
    assert "/api/v1/admin/plugins/upload" in production
    assert "/api/v1/admin/plugins/:id/upgrade" in production
    assert "同插件 ID 不存在" in production
    assert "upload 替换" in production
    assert "baiyu-codex-state-v1" in production


def test_plugin_upgrade_preserves_strict_and_uses_trusted_old_package_for_rollback() -> None:
    production = read("production-deployment.md")

    assert "禁止升级前主动停用" in production
    assert "保留 strict" in production
    assert "旧受信签名包" in production
    assert "再次调用同一 upgrade 接口" in production
    assert "手工替换实例目录" in production
    assert "config_secrets" in production


def test_plugin_report_has_package_identity_and_authorization_boundaries() -> None:
    report = read("final-report.md")

    for field in (
        "plugin_id",
        "plugin_source_commit_sha",
        "plugin_previous_version",
        "plugin_target_version",
        "package_arches",
        "package_sha256",
        "runtime_binary_sha256",
        "signature_key_id",
        "signature_status",
        "host_version",
        "host_service_api",
        "host_features_status",
        "compatibility_status",
        "installation_state",
        "runtime_health",
        "managed_scope_digest",
        "managed_scope_count",
        "multi_instance_expected",
        "multi_instance_restored",
        "multi_instance_restore_status",
        "upgrade_drain_status",
        "automatic_rollback_status",
        "manual_rollback_status",
        "plugin_enabled_before",
        "plugin_enabled_after",
        "real_collection_performed",
    ):
        assert field in report
    assert "plugin-install | plugin-upgrade | plugin-rollback" in report
    assert "plugin-package" in report
    assert "不能同时省略镜像和插件包证据" in report
