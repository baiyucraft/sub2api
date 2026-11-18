"""Profile 286 wiring keeps the real Specialized restore and binds exact Git objects."""
from __future__ import annotations

import json
import re
import shutil
import subprocess
import sys
from pathlib import Path
from unittest import mock

import pytest

sys.path.insert(0, str(Path(__file__).resolve().parents[2]))
from release import recovery_gate as gate
from release.paths import WORKSPACE

BASE = "19ec27a4d2895101622424dfa1356870004c6de9"
TARGET = "6fab0a01de746a4554dead67811c0247ebed644f"
PREFIX = ".agents/skills/sub2api-production-deploy/scripts/"
OBJECTS = {
    "maintenance/release/activity-reward-cost-postflight.sh": ("100644:c409a029e2e4184400fdb79d4cdb6cd1e1504451", "100644:bfbd4b855688d42eb4f7d92da8f3f8052001d41a"),
    "maintenance/release/context.sh": ("100755:c2b9e23c59c1c8ac2b24b1fd0758f2cc6081ebf8", "100755:39c3e919e4a8d323f64fd8f886a32ebc13b67de0"),
    "maintenance/release/full-recovery-capture.sh": ("100755:dccc23160f0a47cc0b06a67663246f016f8deaa3", "100755:1631d4c155ccb7efe1dd3e4e8fa26d39873aeaa5"),
    "maintenance/release/migration-195-assert.sh": ("100644:0d70fa4cace570a808cb83642db169d51856a903", "100644:5384d3ccdd9b1b46b5234290bc5b9cf79bdfa67e"),
    "maintenance/release/migration-232-assert.sh": ("100644:2a95bbff2cea8e8c923fb29be3fc2776a5555a72", "100644:4e01a955ff338f82fd1dfd049ef32c6c053431be"),
    "maintenance/release/migration-233-assert.sh": ("100644:2f4ed8a388d676ff3a121e8634b59508f784e1ca", "100644:d6beb74ac3d491fe190c4141f8cc1549d1f22e54"),
    "maintenance/release/migration-234-assert.sh": ("100644:2b0087c64d89a39f77e92e5ad8b1b500fa621d10", "100644:35e7b19358238845d5c742930f730041a297640e"),
    "maintenance/release/migration-235-assert.sh": ("100644:e71e3c738b9f1f73d62c83a4ad1a71a687c17cf3", "100644:6b627c84554efddafeb9799fab5e8df5f1e08e1f"),
    "maintenance/release/migration-236-assert.sh": ("100644:37adcfd60d751f1d2b13c1fafd447dde82775dde", "100644:27c037dd7b86b542167769927c7c440780848239"),
    "maintenance/release/migration-237-assert.sh": ("100644:cebfe7676b7b675f7d20d98214262e4e05d4dd45", "100644:a523bbd5939c263c8ceedc9b1370cc117de3b1c7"),
    "maintenance/release/migration-238-assert.sh": ("100644:d1303133c4c0a65010e3a9749068dfe590c89859", "100644:4e9cd15438417efb46b6e3d9d8d250c708685369"),
    "maintenance/release/migration-239-assert.sh": ("100755:fccee6db41880529f762938654db711f79eaeeef", "100755:5ecf14d7d98b2e03763f523db6f0c2364d91ee54"),
    "maintenance/release/migration-240-assert.sh": ("100755:feac67c2f7a8ff37e08bb39684562350ae12e4cc", "100755:f06c8ffe35dad50c656e9b17a6ae4ab12b016695"),
    "maintenance/release/migration-241-assert.sh": ("100755:6791bd014e1acb8f340aa52bfa33baab57091d10", "100755:00d1e22c9230cd781647efd27e35ce93d272b871"),
    "maintenance/release/migration-242-assert.sh": ("100644:d63a831138e848d012d5491dd38051ffda542f55", "100644:97e23880af3f57d1976fe354d487060d2be6468d"),
    "maintenance/release/migration-243-assert.sh": ("100644:d7029d97c2f12d7357ce43cf0cef85673230465e", "100644:0fcbbcd368216e2bd094aa82bede5b719e69d11b"),
    "maintenance/release/migration-244-assert.sh": ("100644:a1c73da393a37d36607460170daf332181d9dfec", "100644:a0cdb2b48c7e3000ecff8a874471281d2ef73ecd"),
    "maintenance/release/migration-245-assert.sh": ("100644:40d1748319e151a40000e8cc668c76b0cde5f567", "100644:d79c5ad60d1501216a57ec0d919aaa9d4383fc84"),
    "maintenance/release/migration-254-assert.sh": ("100644:750edee5b3a124bb4179fda36f888b23c20d711d", "100644:aaae1e64be5b70e70441c32e7656a76df724da02"),
    "maintenance/release/migration-285-assert.sh": ("100644:76797d99d92543c30c7f23b0ec32eb615bd04538", "100644:b1de2a344b26b61e0177379c39dc71b0a1d1b4ea"),
    "maintenance/release/migration-297-assert.sh": ("100644:8c956832f48e1597b106af20dbb2b650bc20cc3b", "100644:b41b43abf288c4bac056384d9c88a73ecc6435f8"),
    "maintenance/release/migration-298-assert.sh": ("100755:6c47eb6ce6a8ce917d482d2b67c973f41a3f9396", "100755:2042ca1e4e86133d9636e7d736caf8611790c13a"),
    "maintenance/release/migration-299-assert.sh": ("100644:a89ea33a56d547c917ad30bf8d14e099a5115acb", "100644:33dfa429b1450ead096a91ca65d7ccfee5453ac1"),
    "maintenance/release/migration-301-assert.sh": ("100644:44748d382eca9011908d7d9e611936428a0578b8", "100644:cc380c3c9d2a46519737915994ec80e3426f4d4d"),
    "maintenance/release/prepare.sh": ("100755:c1f79564730a24f262fd9296d107c8c230dba1d6", "100755:5f747928b1e085d2c7d6ba2167a3aa1bd8b0fd0e"),
    "maintenance/release/promote-backup.sh": ("100755:d355f7045ca9be3e421624cdd0538ce7d7d9b935", "100755:35953b42bb6ad63f30a1152d52db2fedf3f2ef9e"),
    "maintenance/release/switch.sh": ("100755:52b824ec56437df45bf699890a0889c87834ed6c", "100755:48cab18ced9ddf88f7968ac15b6852dc7f3c5c67"),
    "release/baseline_candidate_load.py": ("100644:d46a81c9e23e4066e35898a46d8aa144a956076d", "100644:3e0efc35028948f91476c371eab613c0c5b4f063"),
    "release/bootstrap_backup_dr_assets.sh": ("100644:6bdd8c259bd8ab8930f06edfa19416d0710a1d8e", "100644:5b57043c45d793d512864d4f258384d13968915e"),
    "release/bootstrap_vm_signer.sh": ("100755:05fedfceb1e20d67898aa73d408d3b4d6b8ad1da", "100755:e16b8c958943a958b89db6031adfe3d66288f68a"),
    "release/gate.py": ("100644:fd54179b75dc2f41fa2425e8f65e0a7f5951fd43", "100644:d58858b26c4612456e49e00afe21a4989a38d457"),
    "release/manifest.py": ("100644:dfca403098da9f456d9428bea0e39219b7396e46", "100644:2385d8f73a879a8cc2f5b287f074cc1fb0e474eb"),
    "release/postrelease_baseline.py": ("100644:33f09bd249a8511e931a364f45656c957b163243", "100644:bbf9e8491229bd0d8e15d8350fc77211bee16901"),
    "release/production-recovery-retention-clean.sh": ("100644:c170e31f27068ab0c86d4b324a0d59ac98befea8", "100644:41d6c34d5a006604972b3177e6443ce6b6a667da"),
    "release/production-space-clean.sh": ("100644:18a0608e72b85077b08df97a344cf7354802a6e5", "100644:dc621bdc78731c72f96bda354a1597d545851223"),
    "release/production_cleanup.py": ("100644:96a192f22ec8c71d5aaa974ca835953c95c1502c", "100644:7893df16c3445be7f94b03c00612a8b26311fdb9"),
    "release/production_recovery_retention.py": ("100644:0c1a5170f861e49d71d992f809a6a9e66695391e", "100644:d4c5bb64cc712078c07d8fdf7cd7e321a0c62724"),
    "release/profiles.py": ("100644:dc9596aa03a992d2ea183ca7dd54e0949131acd4", "100644:56d1492f2754c9d258242ea2af95f85a34c0f9f4"),
    "release/promote-dr-baseline.sh": ("100644:af05296b51d502822f9ecb308d376a5d257c6c58", "100644:6e146441a87b7264cf113632143ef1455311ace3"),
    "release/recovery_drill.py": ("100644:39c74bf89ee20e67fdb18de3fb599943b346d034", "100644:1219406bde1edde861c229b66047bb6bde23b5ac"),
    "release/sign-dr-evidence.sh": ("100644:688332c86c42198d84e9fd5d9c8a83a81d0b9416", "100644:8bc153e1f6ccbfd29b27342670aa8a215dd2d0d3"),
    "release/sign-gate.sh": ("100644:a9a8885ba22a289d420a0c38a1a0cdc7a3dee376", "100644:0e40cd42321df198a343ec0b4ce6f62f5a9778d6"),
    "release/stage-dr-baseline-candidate.sh": ("100755:64f0aff1c6cedbf79eddc38ee71f513d19330852", "100755:8df80bef4915bdbfe12b89667c67321d714046e5"),
    "release/vm-only-validate.sh": ("100644:f85c01ab540e30e5feaf5f094e67a38531aa0b99", "100644:1072ee2f3f9cc7f37d1f63349000e1266c63fa4b"),
    "release/vm-validate.sh": ("100755:7ab621e20a036d34d381c342b34451cebc48c909", "100755:64ed05313465b4be91060521510c91fa4571e5ee"),
}
PATHS = frozenset(PREFIX + path for path in OBJECTS)
TRANCHE = (BASE, TARGET, "reviewed_profile_compatibility_changed", PATHS)
PRE_GATE = {PREFIX + "release/" + name for name in ("cli.py", "vm_pre_gate_preserve.py")}
NEW_CLEANUP = tuple(PREFIX + "release/" + name for name in ("full_recovery_session.py", "full_recovery_cleanup.py"))
MIGRATION = "backend/migrations/313_channel_pricing_ultrafast_multiplier.sql"
MIGRATION_NAME = MIGRATION.rsplit("/", 1)[1]


def test_real_git_objects_are_exact_and_exclude_reconciliation():
    expected = {PREFIX + path: pair for path, pair in OBJECTS.items()}
    old, new = gate._tree_blobs(WORKSPACE, BASE), gate._tree_blobs(WORKSPACE, TARGET)
    assert len(OBJECTS) == 45
    assert TRANCHE in gate._REVIEWED_SCOPED_TRANCHES
    assert {path: (old.get(path), new.get(path)) for path in PATHS} == expected
    assert {path for path in gate._changed_paths(WORKSPACE, BASE, TARGET)
            if gate._is_recovery_sensitive_path(path)} == PATHS | PRE_GATE
    assert gate._reviewed_compatibility_paths(WORKSPACE, BASE, TARGET) == dict.fromkeys(PATHS, TRANCHE[2])
    report = gate.classify(WORKSPACE, BASE, TARGET)
    assert report["mode"] == "full"
    assert "recovery_change_requires_review" in report["reason_codes"]


def test_exact_wiring_and_migration_stay_specialized_and_drift_remains_full():
    frozen_old = {PREFIX + path: pair[0] for path, pair in OBJECTS.items()}
    frozen_new = {PREFIX + path: pair[1] for path, pair in OBJECTS.items()}
    base, target = "a" * 40, "b" * 40
    trees = {BASE: frozen_old, TARGET: frozen_new}
    old, new = frozen_old.copy(), frozen_new.copy()

    def classify():
        trees.update({base: old, target: new})
        changed = [path for path in old.keys() | new.keys() if old.get(path) != new.get(path)]
        with mock.patch.object(gate, "_changed_paths", return_value=[*changed, MIGRATION]):
            return gate.classify(WORKSPACE, base, target)

    with (
        mock.patch.object(gate, "_tree_blobs", side_effect=lambda _, sha: trees[sha]),
        mock.patch.object(gate, "_commit_exists", side_effect=lambda _, sha: sha in trees),
        mock.patch.object(gate, "_is_ancestor", return_value=True),
    ):
        report = classify()
        assert report["mode"] == "specialized"
        assert {"migration_changed", TRANCHE[2]} <= set(report["reason_codes"])
        gate.assert_release_allowed(report)
        for path in PATHS:
            oid = frozen_new[path]
            new[path] = oid.split(":")[0] + ":" + "0" * 40
            assert classify()["mode"] == "full", ("blob drift", path)
            new[path] = ("100755" if oid.startswith("100644") else "100644") + oid[6:]
            assert classify()["mode"] == "full", ("mode drift", path)
            del new[path]
            assert classify()["mode"] == "full", ("deletion", path)
            new[path + ".moved"] = oid
            assert classify()["mode"] == "full", ("relocation", path)
            del new[path + ".moved"]
            new[path] = oid
            old[path] = frozen_old[path].split(":")[0] + ":" + "0" * 40
            assert classify()["mode"] == "full", ("base drift", path)
            old[path] = frozen_old[path]
        for path in (*NEW_CLEANUP, *PRE_GATE, PREFIX + "release/trust/unreviewed.pub"):
            new[path] = "100644:" + "1" * 40
            report = classify()
            assert report["mode"] == "full", path
            with pytest.raises(RuntimeError, match="independent full DR"):
                gate.assert_release_allowed(report)
            del new[path]


@pytest.mark.parametrize("number", (297, 298, 299, 301))
def test_inherited_assertion_jq_profile_branch(number):
    jq = shutil.which("jq")
    if jq is None:
        pytest.skip("jq is required for shell manifest predicate verification")
    script = WORKSPACE / PREFIX / f"maintenance/release/migration-{number}-assert.sh"
    source = script.read_text(encoding="utf-8")
    match = re.search(
        r'''jq -e --arg filename "\$filename" --arg checksum "\$(?:catalog_)?checksum" '\n(.*?)\n\s*' ''',
        source,
        re.DOTALL,
    )
    assert match
    expression = match[1]
    filename = f"{number}_fixture.sql"
    checksum = "f" * 64
    catalog = [{"filename": filename, "checksum": checksum}]
    old = {
        "schema": 2,
        "profile": "285",
        "parent_profile": "284",
        "new_migrations": ["311_group_force_openai_ultrafast.sql", "312_group_usage_rollup_nonblocking_build.sql"],
        "migration_catalog": catalog,
        "deployment_mode": "downtime",
    }
    new = dict(old, profile="286", parent_profile="285", new_migrations=[MIGRATION_NAME])

    def verify(document, code):
        result = subprocess.run(
            [jq, "-e", "--arg", "filename", filename, "--arg", "checksum", checksum, expression],
            input=json.dumps(document), capture_output=True, text=True, timeout=10,
        )
        assert result.returncode == code, result.stderr

    for manifest in (old, new):
        verify(manifest, 0)
        verify({"manifest": manifest}, 0)
    for bad in (
        dict(new, profile="287"),
        dict(new, parent_profile="284"),
        dict(new, new_migrations=[]),
        dict(new, schema=1),
        dict(new, migration_catalog=[]),
        dict(new, migration_catalog=catalog * 2),
        dict(new, migration_catalog=[dict(catalog[0], checksum="0" * 64)]),
    ):
        verify(bad, 1)
