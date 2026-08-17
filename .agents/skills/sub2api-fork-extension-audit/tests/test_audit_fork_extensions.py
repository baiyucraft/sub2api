from __future__ import annotations

import hashlib
import json
import subprocess
import sys
from pathlib import Path

import pytest


SCRIPT = Path(__file__).resolve().parents[1] / "scripts" / "audit_fork_extensions.py"
REPO_ROOT = Path(__file__).resolve().parents[4]


def run(repo: Path, *args: str, check: bool = True) -> subprocess.CompletedProcess[str]:
    proc = subprocess.run(args, cwd=repo, text=True, capture_output=True)
    if check and proc.returncode:
        raise AssertionError(f"command failed: {args}\nstdout={proc.stdout}\nstderr={proc.stderr}")
    return proc


def git(repo: Path, *args: str) -> str:
    return run(repo, "git", *args).stdout.strip()


def write(path: Path, text: str) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(text, encoding="utf-8")


def commit_all(repo: Path, message: str) -> str:
    git(repo, "add", ".")
    git(repo, "commit", "-m", message)
    return git(repo, "rev-parse", "HEAD")


def make_fixture(tmp_path: Path) -> tuple[Path, str, Path]:
    repo = tmp_path / "repo"
    repo.mkdir()
    git(repo, "init", "-b", "main")
    git(repo, "config", "user.email", "audit@example.test")
    git(repo, "config", "user.name", "Audit Fixture")
    write(repo / ".gitignore", ".tmp/\n")
    write(repo / "backend/cmd/server/VERSION", "1.0.0\n")
    write(repo / "backend/migrations/001_test.sql", "CREATE TABLE audit_fixture(id bigint);\n")
    write(repo / "app.txt", "BASE\n")
    upstream = commit_all(repo, "upstream base")

    write(repo / "backend/cmd/server/VERSION", "1.0.0-baiyu\n")
    write(repo / "app.txt", "BASE\nFORK_MARKER\n")
    write(repo / "tests/test_feature.py", "def test_feature():\n    assert True\n")
    write(
        repo / "profiles.py",
        "PROFILES = {'233': {'name': '233', 'version': '1.0.0-baiyu', "
        "'compatibility_version': '0.9.0-baiyu', 'compatibility_commit': '" + "a" * 40 + "', "
        "'compatibility_image_id': 'sha256:" + "b" * 64 + "', 'migrations': ['001_test.sql']}}\n",
    )
    migration_hash = hashlib.sha256((repo / "backend/migrations/001_test.sql").read_bytes()).hexdigest()
    migration_map_hash = hashlib.sha256(
        json.dumps({"001_test.sql": migration_hash}, sort_keys=True, separators=(",", ":")).encode()
    ).hexdigest()
    catalog = {
        "schema": 1,
        "version_contract": {"fork_suffix": "-baiyu"},
        "profile_source": "profiles.py",
        "historical_profiles": {
            "233": {
                "version": "1.0.0-baiyu",
                "migration_count": 1,
                "migration_map_sha256": migration_map_hash,
                "compatibility_version": "0.9.0-baiyu",
                "compatibility_commit": "a" * 40,
                "compatibility_image_id": "sha256:" + "b" * 64,
            }
        },
        "current_profile": {
            "id": "233",
            "status": "pending",
            "base_profile": "233",
            "version": "1.0.0-baiyu",
            "migration_count": 1,
            "migration_map_sha256": migration_map_hash,
            "appended_migrations": [],
            "compatibility_version": "0.9.0-baiyu",
            "compatibility_commit": "a" * 40,
            "compatibility_image_id": "sha256:" + "b" * 64,
        },
        "migration_contracts": {"001_test.sql": migration_hash},
        "migration_assertions": {},
        "high_risk_paths": ["app.txt"],
        "registered_support_paths": [".audit/**", "app.txt", "backend/**", "profiles.py", "tests/**", ".gitignore"],
        "extensions": [
            {
                "id": "fixture-extension",
                "title": "Fixture",
                "description": "fixture",
                "ownership": "fork",
                "risk_level": "high",
                "paths": ["app.txt"],
                "symbols": ["FORK_MARKER"],
                "api_routes": [],
                "settings_keys": [],
                "migration_files": ["001_test.sql"],
                "required_tests": ["tests/test_feature.py"],
                "invariants": ["marker remains"],
            }
        ],
    }
    catalog_path = repo / ".audit/catalog.json"
    write(catalog_path, json.dumps(catalog, indent=2, sort_keys=True) + "\n")
    commit_all(repo, "fork extension")
    return repo, upstream, catalog_path


def audit(repo: Path, mode: str, upstream: str, catalog: Path, merge_commit: str | None = None) -> tuple[subprocess.CompletedProcess[str], dict]:
    args = [sys.executable, str(SCRIPT), mode, "--upstream-ref", upstream, "--repo-root", str(repo), "--catalog", str(catalog)]
    if merge_commit:
        args.extend(["--merge-commit", merge_commit])
    proc = run(repo, *args, check=False)
    payload = json.loads(proc.stdout)
    report = json.loads((repo / payload["audit_dir"] / "report.json").read_text(encoding="utf-8"))
    return proc, report


@pytest.mark.parametrize("mode", ["snapshot", "pre-merge"])
def test_clean_audit_passes_and_report_is_deterministic(tmp_path: Path, mode: str) -> None:
    repo, upstream, catalog = make_fixture(tmp_path)
    first, report = audit(repo, mode, upstream, catalog)
    first_bytes = (repo / json.loads(first.stdout)["audit_dir"] / "report.json").read_bytes()
    second, report2 = audit(repo, mode, upstream, catalog)
    second_bytes = (repo / json.loads(second.stdout)["audit_dir"] / "report.json").read_bytes()
    assert first.returncode == second.returncode == 0
    assert report["status"] == report2["status"] == "pass"
    assert first_bytes == second_bytes


def test_short_sha_is_blocker(tmp_path: Path) -> None:
    repo, upstream, catalog = make_fixture(tmp_path)
    proc, report = audit(repo, "pre-merge", upstream[:8], catalog)
    assert proc.returncode != 0
    assert report["status"] == "blocker"
    assert any(item["code"] == "short_or_invalid_upstream_sha" for item in report["findings"])


def test_dirty_worktree_is_blocker(tmp_path: Path) -> None:
    repo, upstream, catalog = make_fixture(tmp_path)
    write(repo / "app.txt", "dirty\n")
    proc, report = audit(repo, "pre-merge", upstream, catalog)
    assert proc.returncode != 0
    assert any(item["code"] == "dirty_worktree" for item in report["findings"])


def test_version_and_marker_failures_are_blockers(tmp_path: Path) -> None:
    repo, upstream, catalog = make_fixture(tmp_path)
    write(repo / "backend/cmd/server/VERSION", "9.9.9-baiyu\n")
    write(repo / "app.txt", "BASE\n")
    commit_all(repo, "break contracts")
    proc, report = audit(repo, "pre-merge", upstream, catalog)
    codes = {item["code"] for item in report["findings"]}
    assert proc.returncode != 0
    assert {"fork_version_mismatch", "extension_marker_missing"} <= codes


def test_migration_checksum_drift_is_blocker(tmp_path: Path) -> None:
    repo, upstream, catalog = make_fixture(tmp_path)
    write(repo / "backend/migrations/001_test.sql", "SELECT 1;\n")
    commit_all(repo, "rewrite released migration")
    proc, report = audit(repo, "pre-merge", upstream, catalog)
    codes = {item["code"] for item in report["findings"]}
    assert proc.returncode != 0
    assert "migration_checksum_drift" in codes
    assert "historical_profile_drift" in codes
    assert "current_profile_drift" in codes


def test_current_profile_drift_is_blocker(tmp_path: Path) -> None:
    repo, upstream, catalog = make_fixture(tmp_path)
    payload = json.loads(catalog.read_text(encoding="utf-8"))
    payload["current_profile"]["version"] = "9.9.9-baiyu"
    write(catalog, json.dumps(payload, indent=2, sort_keys=True) + "\n")
    commit_all(repo, "drift current profile catalog")
    proc, report = audit(repo, "pre-merge", upstream, catalog)
    assert proc.returncode != 0
    finding = next(item for item in report["findings"] if item["code"] == "current_profile_drift")
    assert finding["details"]["mismatches"]["version"]["actual"] == "1.0.0-baiyu"


def test_unregistered_path_requires_catalog_update(tmp_path: Path) -> None:
    repo, upstream, catalog = make_fixture(tmp_path)
    write(repo / "unexpected/feature.txt", "new fork feature\n")
    commit_all(repo, "add uncatalogued extension")
    proc, report = audit(repo, "pre-merge", upstream, catalog)
    assert proc.returncode != 0
    assert report["status"] == "catalog_update_required"
    assert any(item["code"] == "unregistered_fork_paths" for item in report["findings"])


def make_merge(repo: Path, upstream_base: str) -> tuple[str, str]:
    git(repo, "branch", "official", upstream_base)
    git(repo, "checkout", "official")
    write(repo / "app.txt", "BASE\nUPSTREAM\n")
    upstream_target = commit_all(repo, "official change")
    git(repo, "checkout", "main")
    proc = run(repo, "git", "merge", "--no-ff", upstream_target, "-m", "merge official", check=False)
    assert proc.returncode != 0
    git(repo, "checkout", "--ours", "app.txt")
    git(repo, "add", "app.txt")
    git(repo, "commit", "--no-edit")
    return upstream_target, git(repo, "rev-parse", "HEAD")


def test_post_merge_parent_and_whole_file_resolution(tmp_path: Path) -> None:
    repo, upstream_base, catalog = make_fixture(tmp_path)
    upstream_target, merge_commit = make_merge(repo, upstream_base)
    proc, report = audit(repo, "post-merge", upstream_target, catalog, merge_commit)
    assert proc.returncode == 0
    assert report["status"] == "warning"
    assert any(item["code"] == "whole_file_resolution_suspected" for item in report["findings"])

    wrong, wrong_report = audit(repo, "post-merge", upstream_base, catalog, merge_commit)
    assert wrong.returncode != 0
    assert any(item["code"] == "wrong_merge_parent" for item in wrong_report["findings"])


def test_real_catalog_registers_upstream_model_capability_sync() -> None:
    catalog_path = REPO_ROOT / ".agents/skills/sub2api-fork-extension-audit/references/extensions.yaml"
    catalog = json.loads(catalog_path.read_text(encoding="utf-8"))
    extension = next(item for item in catalog["extensions"] if item["id"] == "upstream-model-capability-sync")

    for relative in [*extension["paths"], *extension["required_tests"]]:
        assert (REPO_ROOT / relative).is_file(), relative

    source = "\n".join((REPO_ROOT / relative).read_text(encoding="utf-8") for relative in extension["paths"])
    for symbol in extension["symbols"]:
        assert symbol in source, symbol

    invariants = "\n".join(extension["invariants"])
    for marker in ("sync_managed", "model_limits", "30 分钟", "24 小时", "scheduler", "凭据"):
        assert marker in invariants, marker
