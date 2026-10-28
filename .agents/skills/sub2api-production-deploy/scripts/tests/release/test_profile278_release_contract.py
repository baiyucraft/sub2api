"""采样策略 profile 278 的发布合同。"""
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


def test_profile_278_is_additive_and_has_no_migration():
    assert CURRENT_RELEASE_PROFILE == "280"
    assert list(PROFILES)[-2:] == ["279", "280"]
    profile = get_profile("278")
    assert (profile["version"], profile["parent"], profile["new_migrations"], profile["gate_schema"]) == (
        "0.2.20-baiyu", "277", [], 2
    )
    assert (get_profile("277")["version"], get_profile("277")["parent"], get_profile("277")["new_migrations"]) == (
        "0.2.19-baiyu", "276", ["304_upstream_confidence_fingerprint.sql"]
    )
    assert get_profile("278")["name"] == "278"
    assert get_release_profile("280")["name"] == "280"
    assert RELEASE_ID.fullmatch("278-aaaaaaaaaaaa-1-aaaaaaaa")
    assert RELEASE_ID.fullmatch("279-aaaaaaaaaaaa-1-aaaaaaaa")


def test_profile_278_registry_and_vm_admission_are_consistent():
    registry = json.loads((WORKSPACE / ".agents/skills/sub2api-fork-extension-audit/references/extensions.yaml").read_text(encoding="utf-8"))
    current = registry["current_profile"]
    profile = get_profile("280")
    assert current["id"] == "280"
    for key in ("version", "parent", "new_migrations", "gate_schema", "release_policy"):
        assert current[key] == profile[key]
    assert current["new_migrations"] == []
    assert "304_upstream_confidence_fingerprint.sql" in registry["migration_contracts"]

    context = (ROOT / "maintenance/release/context.sh").read_text(encoding="utf-8")
    validator = (ROOT / "release/vm-validate.sh").read_text(encoding="utf-8")
    signer = (ROOT / "release/sign-gate.sh").read_text(encoding="utf-8")
    prepare = (ROOT / "maintenance/release/prepare.sh").read_text(encoding="utf-8")
    release_path_guard = next(line for line in signer.splitlines() if line.startswith("[[ $gate =~"))
    assert "|278|279|280)" in release_path_guard
    assert re.search(r"\|277\|278\|279\|280\)", context)
    schema_two_profile_guard = next(line for line in validator.splitlines() if line.lstrip().startswith('[[ "$profile" == 280'))
    assert schema_two_profile_guard.strip() == '[[ "$profile" == 280 ]]'
    assert 'elif [[ "$profile" == 278 ]]; then' in validator
    assert 'elif [[ "$profile" == 280 ]]; then' in validator
    assert '"$version" == 0.2.21-baiyu' in validator
    assert '"$profile" == 277' in validator
    gate_v2_guard = next(line for line in signer.splitlines() if line.startswith("if [[ $gate_profile == 242"))
    assert "$gate_profile == 279" in gate_v2_guard
    assert '[[ $profile == 280' in prepare
