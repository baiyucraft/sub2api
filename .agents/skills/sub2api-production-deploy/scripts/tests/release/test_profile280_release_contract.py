"""统一跨平台可信度推断 profile 280 的发布合同。"""
from __future__ import annotations

import json
import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT))
from release.paths import WORKSPACE
from release.profiles import CURRENT_RELEASE_PROFILE, PROFILES, get_profile, get_release_profile
from release.production_cleanup import RELEASE_ID


def test_profile_280_is_additive_and_inherits_279_without_migration():
    assert CURRENT_RELEASE_PROFILE == "281"
    assert list(PROFILES)[-1] == "281"
    assert (get_profile("279")["version"], get_profile("279")["parent"], get_profile("279")["new_migrations"]) == (
        "0.2.21-baiyu", "278", []
    )
    profile = get_profile("280")
    assert (profile["version"], profile["parent"], profile["new_migrations"], profile["gate_schema"]) == (
        "0.2.22-baiyu", "279", [], 2
    )
    assert get_release_profile("281")["name"] == "281"
    assert RELEASE_ID.fullmatch("279-aaaaaaaaaaaa-1-aaaaaaaa")
    assert RELEASE_ID.fullmatch("280-aaaaaaaaaaaa-1-aaaaaaaa")
    assert not RELEASE_ID.fullmatch("282-aaaaaaaaaaaa-1-aaaaaaaa")


def test_profile_280_registry_and_current_only_admission_are_consistent():
    registry = json.loads((WORKSPACE / ".agents/skills/sub2api-fork-extension-audit/references/extensions.yaml").read_text(encoding="utf-8"))
    current = registry["current_profile"]
    profile = get_profile("281")
    assert current["id"] == "281"
    for key in ("version", "parent", "new_migrations", "gate_schema", "release_policy"):
        assert current[key] == profile[key]
    assert (WORKSPACE / "backend/cmd/server/VERSION").read_text(encoding="utf-8").strip() == "0.2.19-baiyu"

    context = (ROOT / "maintenance/release/context.sh").read_text(encoding="utf-8")
    validator = (ROOT / "release/vm-validate.sh").read_text(encoding="utf-8")
    signer = (ROOT / "release/sign-gate.sh").read_text(encoding="utf-8")
    prepare = (ROOT / "maintenance/release/prepare.sh").read_text(encoding="utf-8")
    release_path_guard = next(line for line in signer.splitlines() if line.startswith("[[ $gate =~"))
    assert "|279|280|281)" in release_path_guard
    assert re.search(r"\|279\|280\|281\)", context)
    schema_two_profile_guard = next(line for line in validator.splitlines() if line.lstrip().startswith('[[ "$profile" == 281'))
    assert schema_two_profile_guard.strip() == '[[ "$profile" == 281 ]]'
    assert 'elif [[ "$profile" == 279 ]]; then' in validator
    assert 'elif [[ "$profile" == 281 ]]; then' in validator
    assert '"$version" == 0.2.22-baiyu' in validator
    assert '[[ $profile == 281' in prepare
    gate_v2_guard = next(line for line in signer.splitlines() if line.startswith("if [[ $gate_profile == 242"))
    assert "$gate_profile == 280" in gate_v2_guard


def test_profile_280_migration_compatibility_is_inherited():
    validator = (ROOT / "release/vm-validate.sh").read_text(encoding="utf-8")
    signer_bootstrap = (ROOT / "release/bootstrap_vm_signer.sh").read_text(encoding="utf-8")
    backup_bootstrap = (ROOT / "release/bootstrap_backup_dr_assets.sh").read_text(encoding="utf-8")
    assert '[[ "$profile" == 281 ]]' in validator
    assert "280" in signer_bootstrap
    assert "selftest_profile == 280" in signer_bootstrap
    assert "279 280 281; do" in backup_bootstrap
