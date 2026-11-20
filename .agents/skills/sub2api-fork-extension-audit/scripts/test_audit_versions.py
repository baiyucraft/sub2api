import importlib.util
import json
import shutil
import subprocess
from pathlib import Path

import pytest

spec = importlib.util.spec_from_file_location("audit_fork_extensions", Path(__file__).with_name("audit_fork_extensions.py"))
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


@pytest.mark.parametrize("mode,fork,profile,base,expected", [
    ("pre-merge", "0.2.0-baiyu", "0.2.0-baiyu", "0.2.0", "version_upgrade_required"),
    ("post-merge", "0.2.0-baiyu", "0.2.0-baiyu", "0.2.0", "fork_version_mismatch"),
    ("snapshot", "0.2.0-baiyu", "0.2.0-baiyu", "0.2.0", "fork_version_mismatch"),
    ("pre-merge", "0.2.0-baiyu", "0.1.9-baiyu", "0.2.0", "fork_version_mismatch"),
    ("pre-merge", "0.2.0-baiyu", "0.2.0-baiyu", "0.1.9", "fork_version_mismatch"),
    ("pre-merge", "0.2.0-baiyu", "0.2.0-baiyu", "", "fork_version_mismatch"),
    ("post-merge", "0.2.2-baiyu", "0.2.2-baiyu", "0.2.1", "version_contract"),
])
def test_version_validation_respects_merge_phase(tmp_path, mode, fork, profile, base, expected):
    audit = module.Audit(tmp_path, mode, "a" * 40, None, tmp_path / "catalog.json")
    audit.head = "b" * 40
    audit.merge_base = "c" * 40
    audit.catalog = {"current_profile": {"version": profile}}
    versions = {audit.upstream_ref: "0.2.2", audit.head: fork, audit.merge_base: base}
    audit.show = lambda ref, path: versions[ref]
    audit.check_versions()
    assert audit.findings[0]["code"] == expected
    assert audit.findings[0]["level"] == ("blocker" if expected == "fork_version_mismatch" else "warning" if expected == "version_upgrade_required" else "pass")


def test_version_validation_uses_pinned_release_version_when_source_file_lags(tmp_path):
    upstream = "a" * 40
    audit = module.Audit(tmp_path, "post-merge", upstream, None, tmp_path / "catalog.json")
    audit.head = "b" * 40
    audit.merge_base = "c" * 40
    audit.catalog = {
        "version_contract": {
            "official_release_versions": {upstream: "0.2.7"},
        },
        "current_profile": {"version": "0.2.7-baiyu"},
    }
    versions = {upstream: "0.2.5", audit.head: "0.2.7-baiyu", audit.merge_base: "0.2.5"}
    audit.show = lambda ref, path: versions[ref]

    audit.check_versions()

    assert audit.findings[0]["code"] == "version_contract"
    assert audit.findings[0]["level"] == "pass"
    assert audit.findings[0]["details"]["official"] == "0.2.7"


def test_git_paths_are_unquoted_utf8(tmp_path):
    subprocess.run(["git", "init", "--quiet", str(tmp_path)], check=True)
    (tmp_path / "重磅推出活动.md").touch()
    subprocess.run(["git", "add", "."], cwd=tmp_path, check=True)
    audit = module.Audit(tmp_path, "pre-merge", "a" * 40, None, tmp_path / "catalog.json")
    assert audit.git("diff", "--cached", "--name-only") == "重磅推出活动.md"


@pytest.mark.parametrize("parent", ["286", "285"])
def test_profiles_use_contract_from_audited_checkout(tmp_path, monkeypatch, parent):
    source = tmp_path / module.RELEASE_PROFILE_SOURCE
    source.parent.mkdir(parents=True)
    release_package = Path(__file__).resolve().parents[4] / module.RELEASE_PROFILE_SOURCE.parent
    for name in ("__init__.py", "atomic.py", "paths.py", "profiles.py"):
        shutil.copyfile(release_package / name, source.parent / name)

    subprocess.run(["git", "init", "--quiet", str(tmp_path)], check=True)
    contract = tmp_path / ".release" / "contract.json"
    contract.parent.mkdir()
    contract.write_text(json.dumps({
        "schema": 1,
        "profiles": [{"profile": "287", "parent": parent, "version": "0.2.24-baiyu", "new_migrations": []}],
    }), encoding="utf-8")
    monkeypatch.setenv("SUB2API_APP_ROOT", str(tmp_path / "wrong-checkout"))

    audit = module.Audit(tmp_path, "snapshot", "a" * 40, None, tmp_path / "catalog.json")
    audit.catalog = {
        "profile_source": module.RELEASE_PROFILE_SOURCE.as_posix(),
        "historical_profiles": {"286": {"gate_schema": 2, "version": "0.2.23-baiyu"}},
        "current_profile": {"id": "287", "parent": "286", "gate_schema": 2, "version": "0.2.24-baiyu", "new_migrations": []},
    }
    audit.show = lambda ref, path: "0.2.24-baiyu"

    audit.check_profiles()

    codes = [finding["code"] for finding in audit.findings]
    if parent == "286":
        assert codes == ["historical_profile_contract", "current_profile_contract"]
        legacy_migration = "backend/migrations/183_add_usage_log_long_context_billing.sql"
        unregistered = "backend/migrations/314_unregistered.sql"
        audit.catalog["registered_support_paths"] = [legacy_migration, unregistered]
        audit.diff_paths = [legacy_migration, unregistered]
        audit.diff_status = {path: "A" for path in audit.diff_paths}

        audit.check_unregistered_paths()

        assert audit.findings[-1]["code"] == "unregistered_migrations"
        assert audit.findings[-1]["details"]["migrations"] == [unregistered]
    else:
        assert codes == ["profile_source_unreadable"]
        assert "immediate parent" in audit.findings[0]["details"]["error"]


def test_profiles_accept_standalone_source_without_current_pointer(tmp_path):
    (tmp_path / "profiles.py").write_text(
        'PROFILES = {"42": {"name": "42", "gate_schema": 2, "version": "0.2.0-baiyu"}}\n',
        encoding="utf-8",
    )
    audit = module.Audit(tmp_path, "snapshot", "a" * 40, None, tmp_path / "catalog.json")
    audit.catalog = {"profile_source": "profiles.py", "current_profile": {"id": "42", "version": "0.2.0-baiyu"}}
    audit.show = lambda ref, path: "0.2.0-baiyu"

    audit.check_profiles()

    assert [finding["code"] for finding in audit.findings] == ["current_profile_contract"]
