import importlib.util
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
