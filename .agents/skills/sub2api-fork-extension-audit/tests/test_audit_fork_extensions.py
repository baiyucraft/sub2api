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


SEMANTIC_OVERLAP_PATH = "frontend/src/views/user/KeysView.vue"


def make_semantic_overlap_fixture(
    repo: Path,
    upstream_base: str,
    catalog: Path,
    variant: str = "automatic",
) -> tuple[str, str]:
    payload = json.loads(catalog.read_text(encoding="utf-8"))
    payload["registered_support_paths"].append("frontend/**")
    payload["semantic_overlap"] = {
        "ui_path_patterns": ["frontend/src/views/**/*.vue"],
        "minimum_shared_identifiers": 2,
        "evidence_rules": [],
    }
    if variant == "configured":
        payload["semantic_overlap"]["evidence_rules"] = [
            {
                "id": "fixture-platform-filter",
                "paths": [SEMANTIC_OVERLAP_PATH],
                "markers": ["group.platform"],
                "minimum_matches": 1,
            }
        ]

    if variant == "automatic":
        fork_source = """<script setup lang=\"ts\">\nconst formGroupOptions = groups.filter((group) => group.platform === selectedPlatform)\nconst updateGroup = () => { formData.group_id = formGroupOptions[0]?.id ?? null }\n</script>\n"""
        upstream_source = """<script setup lang=\"ts\">\nconst formGroupOptions = groups.filter((group) => group.platform === selectedProvider)\nconst resetGroup = () => { formData.group_id = formGroupOptions.at(0)?.id ?? null }\n</script>\n"""
    elif variant == "configured":
        fork_source = """<script setup lang=\"ts\">\nconst forkPlatformFilter = sourceGroups.filter((group) => group.platform)\n</script>\n"""
        upstream_source = """<script setup lang=\"ts\">\nconst upstreamVendorFilter = providerGroups.some((group) => group.platform)\n</script>\n"""
    else:
        fork_source = """<script setup lang=\"ts\">\nconst platformFilteredGroups = sourceGroups.filter((group) => group.platform)\n</script>\n"""
        upstream_source = """<script setup lang=\"ts\">\nconst providerFilteredKeys = sourceKeys.filter((item) => item.provider)\n</script>\n"""

    write(catalog, json.dumps(payload, indent=2, sort_keys=True) + "\n")
    write(repo / SEMANTIC_OVERLAP_PATH, fork_source)
    commit_all(repo, f"fork semantic overlap fixture {variant}")

    git(repo, "checkout", "-b", f"official-overlap-{variant}", upstream_base)
    write(repo / SEMANTIC_OVERLAP_PATH, upstream_source)
    upstream_target = commit_all(repo, f"official semantic overlap fixture {variant}")
    git(repo, "checkout", "main")
    return upstream_target, fork_source


def merge_semantic_overlap_fixture(repo: Path, upstream_target: str, fork_source: str) -> str:
    proc = run(repo, "git", "merge", "--no-ff", upstream_target, "-m", "merge semantic overlap fixture", check=False)
    if proc.returncode:
        write(repo / SEMANTIC_OVERLAP_PATH, fork_source)
        git(repo, "add", SEMANTIC_OVERLAP_PATH)
        git(repo, "commit", "--no-edit")
    return git(repo, "rev-parse", "HEAD")


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


def test_governance_paths_are_intentionally_excluded_from_catalog(tmp_path: Path) -> None:
    repo, upstream, catalog = make_fixture(tmp_path)
    write(repo / ".wiki/03-模块指南/fork.md", "long-lived fork knowledge\n")
    write(repo / ".spec/changes/example/proposal.md", "process artifact\n")
    write(repo / ".agents/skills/wiki-propose/SKILL.md", "workflow asset\n")
    commit_all(repo, "add governance assets")

    proc, report = audit(repo, "pre-merge", upstream, catalog)

    assert proc.returncode == 0
    assert report["status"] == "pass"
    assert not any(item["code"] == "unregistered_fork_paths" for item in report["findings"])


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


def add_adopted_tranche(repo: Path, upstream_base: str, catalog: Path, target_version: str = "1.1.0") -> tuple[str, str]:
    git(repo, "branch", "adopted-official", upstream_base)
    git(repo, "checkout", "adopted-official")
    write(repo / "official.txt", "adopted before release\n")
    tip = commit_all(repo, "unreleased official change")
    git(repo, "checkout", "main")
    git(repo, "merge", "--no-ff", tip, "-m", "merge unreleased official tranche")
    merge_commit = git(repo, "rev-parse", "HEAD")
    payload = json.loads(catalog.read_text(encoding="utf-8"))
    payload["adopted_upstream_tranches"] = [
        {
            "id": "fixture-unreleased-tranche",
            "status": "adopted_unreleased",
            "base_commit": upstream_base,
            "tip_commit": tip,
            "merge_commit": merge_commit,
            "official_version_at_adoption": "1.0.0",
            "fork_version_at_adoption": "1.0.0-baiyu",
            "commit_count": 1,
            "non_merge_commit_count": 1,
            "reconciliation": {
                "status": "pending",
                "target_version": target_version,
                "upstream_covered_source_commits": [],
                "fork_retained_source_commits": [],
                "partially_covered_source_commits": [],
                "fork_extension_ids": [],
            },
        }
    ]
    write(catalog, json.dumps(payload, indent=2, sort_keys=True) + "\n")
    commit_all(repo, "register unreleased official tranche")
    return tip, merge_commit


def add_official_release(repo: Path, upstream_base: str, version: str = "1.1.0") -> str:
    git(repo, "branch", "official-release", upstream_base)
    git(repo, "checkout", "official-release")
    write(repo / "backend/cmd/server/VERSION", version + "\n")
    release = commit_all(repo, f"release {version}")
    git(repo, "checkout", "main")
    return release


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


def test_pre_merge_reports_semantic_overlap_candidate_without_auto_decision(tmp_path: Path) -> None:
    repo, upstream_base, catalog = make_fixture(tmp_path)
    upstream_target, _ = make_semantic_overlap_fixture(repo, upstream_base, catalog)

    proc, report = audit(repo, "pre-merge", upstream_target, catalog)

    assert proc.returncode == 0
    assert report["status"] == "warning"
    finding = next(item for item in report["findings"] if item["code"] == "semantic_overlap_candidate")
    assert finding["details"]["path"] == SEMANTIC_OVERLAP_PATH
    assert "formGroupOptions" in finding["details"]["shared_entrypoints"]
    assert finding["details"]["reasons"] == ["shared_entrypoints_and_identifiers"]
    assert "不代表实现等价" in finding["message"]
    assert "equivalent" not in finding["details"]
    assert "delete" not in finding["details"]


def test_post_merge_reports_semantic_overlap_candidate(tmp_path: Path) -> None:
    repo, upstream_base, catalog = make_fixture(tmp_path)
    upstream_target, fork_source = make_semantic_overlap_fixture(repo, upstream_base, catalog)
    merge_commit = merge_semantic_overlap_fixture(repo, upstream_target, fork_source)

    proc, report = audit(repo, "post-merge", upstream_target, catalog, merge_commit)

    assert proc.returncode == 0
    finding = next(item for item in report["findings"] if item["code"] == "semantic_overlap_candidate")
    assert finding["details"]["fork_commit"] == report["parents"][0]
    assert finding["details"]["upstream_commit"] == report["parents"][1]


def test_semantic_overlap_ignores_independent_changes_in_same_ui_file(tmp_path: Path) -> None:
    repo, upstream_base, catalog = make_fixture(tmp_path)
    upstream_target, _ = make_semantic_overlap_fixture(repo, upstream_base, catalog, variant="independent")

    proc, report = audit(repo, "pre-merge", upstream_target, catalog)

    assert proc.returncode == 0
    assert report["status"] == "pass"
    assert not any(item["code"] == "semantic_overlap_candidate" for item in report["findings"])


def test_semantic_overlap_accepts_configured_common_evidence(tmp_path: Path) -> None:
    repo, upstream_base, catalog = make_fixture(tmp_path)
    upstream_target, _ = make_semantic_overlap_fixture(repo, upstream_base, catalog, variant="configured")

    proc, report = audit(repo, "pre-merge", upstream_target, catalog)

    assert proc.returncode == 0
    finding = next(item for item in report["findings"] if item["code"] == "semantic_overlap_candidate")
    assert finding["details"]["shared_entrypoints"] == []
    assert finding["details"]["reasons"] == ["configured_evidence:fixture-platform-filter"]
    assert finding["details"]["configured_evidence"][0]["markers"] == ["group.platform"]


def test_snapshot_skips_semantic_overlap_detection(tmp_path: Path) -> None:
    repo, upstream_base, catalog = make_fixture(tmp_path)
    upstream_target, _ = make_semantic_overlap_fixture(repo, upstream_base, catalog)

    proc, report = audit(repo, "snapshot", upstream_target, catalog)

    assert proc.returncode == 0
    assert not any(item["code"].startswith("semantic_overlap") for item in report["findings"])


def test_adopted_unreleased_tranche_is_preserved_without_version_bump(tmp_path: Path) -> None:
    repo, upstream, catalog = make_fixture(tmp_path)
    add_adopted_tranche(repo, upstream, catalog)

    proc, report = audit(repo, "pre-merge", upstream, catalog)

    assert proc.returncode == 0
    finding = next(item for item in report["findings"] if item["code"] == "adopted_upstream_tranche_preserved")
    assert finding["details"]["official_version"] == "1.0.0"
    assert finding["details"]["commit_count"] == 1
    provisional = next(
        item
        for item in report["findings"]
        if item["code"] == "adopted_upstream_tranche_paths_provisionally_registered"
    )
    assert provisional["details"]["paths"] == ["official.txt"]
    assert not any(item["code"] == "unregistered_fork_paths" for item in report["findings"])


def test_adopted_tranche_commit_count_drift_is_blocker(tmp_path: Path) -> None:
    repo, upstream, catalog = make_fixture(tmp_path)
    add_adopted_tranche(repo, upstream, catalog)
    payload = json.loads(catalog.read_text(encoding="utf-8"))
    payload["adopted_upstream_tranches"][0]["commit_count"] = 99
    write(catalog, json.dumps(payload, indent=2, sort_keys=True) + "\n")
    commit_all(repo, "break tranche count")

    proc, report = audit(repo, "pre-merge", upstream, catalog)

    assert proc.returncode != 0
    assert any(item["code"] == "adopted_upstream_tranche_commit_count_mismatch" for item in report["findings"])


def test_adopted_tranche_does_not_hide_later_fork_changes_on_the_same_path(tmp_path: Path) -> None:
    repo, upstream, catalog = make_fixture(tmp_path)
    add_adopted_tranche(repo, upstream, catalog)
    write(repo / "official.txt", "adopted before release\nnew fork behavior\n")
    commit_all(repo, "extend adopted path in fork")

    proc, report = audit(repo, "pre-merge", upstream, catalog)

    assert proc.returncode != 0
    provisional = next(
        item
        for item in report["findings"]
        if item["code"] == "adopted_upstream_tranche_paths_provisionally_registered"
    )
    assert provisional["details"]["paths"] == []
    assert provisional["details"]["changed_after_adoption_paths"] == ["official.txt"]
    unregistered = next(item for item in report["findings"] if item["code"] == "unregistered_fork_paths")
    assert "official.txt" in unregistered["details"]["paths"]


def test_adopted_tranche_rejects_invalid_target_version_without_exemption(tmp_path: Path) -> None:
    repo, upstream, catalog = make_fixture(tmp_path)
    add_adopted_tranche(repo, upstream, catalog, target_version="not-a-version")

    proc, report = audit(repo, "pre-merge", upstream, catalog)

    assert proc.returncode != 0
    assert any(
        item["code"] == "adopted_upstream_tranche_version_format_invalid"
        for item in report["findings"]
    )
    assert not any(
        item["code"] == "adopted_upstream_tranche_paths_provisionally_registered"
        for item in report["findings"]
    )


def test_adopted_tranche_rejects_complete_reconciliation_before_target_release(tmp_path: Path) -> None:
    repo, upstream, catalog = make_fixture(tmp_path)
    tip, _ = add_adopted_tranche(repo, upstream, catalog)
    payload = json.loads(catalog.read_text(encoding="utf-8"))
    reconciliation = payload["adopted_upstream_tranches"][0]["reconciliation"]
    reconciliation["status"] = "complete"
    reconciliation["fork_retained_source_commits"] = [tip]
    reconciliation["fork_extension_ids"] = ["fixture-extension"]
    write(catalog, json.dumps(payload, indent=2, sort_keys=True) + "\n")
    commit_all(repo, "complete reconciliation too early")

    proc, report = audit(repo, "pre-merge", upstream, catalog)

    assert proc.returncode != 0
    assert any(
        item["code"] == "adopted_upstream_tranche_reconciled_before_target_release"
        for item in report["findings"]
    )


def test_adopted_tranche_reconciliation_rejects_unknown_fork_extension(tmp_path: Path) -> None:
    repo, upstream, catalog = make_fixture(tmp_path)
    tip, _ = add_adopted_tranche(repo, upstream, catalog)
    release = add_official_release(repo, upstream)
    payload = json.loads(catalog.read_text(encoding="utf-8"))
    reconciliation = payload["adopted_upstream_tranches"][0]["reconciliation"]
    reconciliation["status"] = "complete"
    reconciliation["fork_retained_source_commits"] = [tip]
    reconciliation["fork_extension_ids"] = ["missing-extension"]
    write(catalog, json.dumps(payload, indent=2, sort_keys=True) + "\n")
    commit_all(repo, "bind missing extension")

    proc, report = audit(repo, "pre-merge", release, catalog)

    assert proc.returncode != 0
    assert any(
        item["code"] == "adopted_upstream_tranche_fork_extension_unknown"
        for item in report["findings"]
    )


def test_adopted_tranche_reconciliation_requires_bound_path_coverage(tmp_path: Path) -> None:
    repo, upstream, catalog = make_fixture(tmp_path)
    tip, _ = add_adopted_tranche(repo, upstream, catalog)
    release = add_official_release(repo, upstream)
    payload = json.loads(catalog.read_text(encoding="utf-8"))
    reconciliation = payload["adopted_upstream_tranches"][0]["reconciliation"]
    reconciliation["status"] = "complete"
    reconciliation["fork_retained_source_commits"] = [tip]
    reconciliation["fork_extension_ids"] = ["fixture-extension"]
    write(catalog, json.dumps(payload, indent=2, sort_keys=True) + "\n")
    commit_all(repo, "bind extension without path coverage")

    proc, report = audit(repo, "pre-merge", release, catalog)

    assert proc.returncode != 0
    finding = next(
        item
        for item in report["findings"]
        if item["code"] == "adopted_upstream_tranche_fork_paths_uncovered"
    )
    assert finding["details"]["paths"] == ["official.txt"]


def test_target_release_requires_adopted_tranche_reconciliation(tmp_path: Path) -> None:
    repo, upstream, catalog = make_fixture(tmp_path)
    add_adopted_tranche(repo, upstream, catalog, target_version="1.1.0")
    release = add_official_release(repo, upstream)

    proc, report = audit(repo, "pre-merge", release, catalog)

    assert proc.returncode != 0
    finding = next(item for item in report["findings"] if item["code"] == "adopted_upstream_tranche_reconciliation_required")
    assert finding["level"] == "catalog_update_required"
    assert finding["details"]["preliminary_coverage"]["not_detected_commits"]
    unregistered = next(item for item in report["findings"] if item["code"] == "unregistered_fork_paths")
    assert "official.txt" in unregistered["details"]["paths"]
    assert not any(
        item["code"] == "adopted_upstream_tranche_paths_provisionally_registered"
        for item in report["findings"]
    )


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


def test_real_catalog_records_adopted_and_layered_extension_ownership() -> None:
    catalog_path = REPO_ROOT / ".agents/skills/sub2api-fork-extension-audit/references/extensions.yaml"
    catalog = json.loads(catalog_path.read_text(encoding="utf-8"))
    extensions = {item["id"]: item for item in catalog["extensions"]}

    assert len(extensions) == len(catalog["extensions"])
    assert extensions["prompt-security-audit"]["ownership"] == "upstream-adopted"
    assert {"193_prompt_audit.sql", "194_prompt_audit_full_prompt.sql"} == set(
        extensions["prompt-security-audit"]["migration_files"]
    )
    assert "upstream" in extensions["prompt-security-audit"]["description"].lower()
    assert extensions["codex-gpt6-astra-catalog"]["ownership"] == "upstream-adopted"
    assert "回归测试" in extensions["codex-gpt6-astra-catalog"]["description"]

    layered = {
        "registration-email-abuse-guard",
        "user-balance-auto-notify",
        "channel-monitor-v2",
        "group-profit-control-display",
        "quality-and-usage-aggregation",
        "upstream-cost-attribution",
        "shared-concurrency-loadfactor",
    }
    for extension_id in layered:
        extension = extensions[extension_id]
        assert extension["ownership"] == "fork"
        text = " ".join([extension["description"], *extension["invariants"]]).lower()
        assert "官方" in text or "upstream" in text, extension_id
        assert "fork" in text, extension_id

    assert "upstream-observation-and-precise-rate" not in extensions
    assert extensions["upstream-observation-preference"]["migration_files"] == [
        "240_upstream_observation_preference.sql"
    ]
    assert extensions["upstream-precise-effective-rate"]["migration_files"] == [
        "241_precise_upstream_effective_rate.sql"
    ]
    assert set(extensions["fork-scheduling-compatibility-foundation"]["paths"]) == {
        "backend/internal/forkscheduling/contracts.go",
        "backend/internal/forkscheduling/contracts_test.go",
        "backend/internal/forkscheduling/legacy/*.go",
        "backend/internal/service/fork_scheduling_bridge.go",
        "backend/internal/service/fork_scheduling_bridge_test.go",
    }


def test_real_catalog_registers_unreleased_adopted_upstream_tranche() -> None:
    catalog_path = REPO_ROOT / ".agents/skills/sub2api-fork-extension-audit/references/extensions.yaml"
    catalog = json.loads(catalog_path.read_text(encoding="utf-8"))
    tranche = next(item for item in catalog["adopted_upstream_tranches"] if item["id"] == "upstream-main-2026-09-18-unreleased-0.2.5")

    assert tranche["base_commit"] == "881f3202694c6bc932446931a30c27d9675178b9"
    assert tranche["tip_commit"] == "efe9aab1e4ec89a42ba45e8dac20e882c5409a6a"
    assert tranche["merge_commit"] == "f501d6ee9461b86077a376002f0048d94e16cd61"
    assert tranche["official_version_at_adoption"] == "0.2.5"
    assert tranche["fork_version_at_adoption"] == "0.2.5-baiyu"
    assert tranche["commit_count"] == 52
    assert tranche["non_merge_commit_count"] == 27
    assert tranche["reconciliation"]["target_version"] == "0.2.6"
