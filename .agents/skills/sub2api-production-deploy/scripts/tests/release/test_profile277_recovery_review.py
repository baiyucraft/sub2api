"""Exact profile 277 review; any later sensitive drift still requires review."""
from __future__ import annotations

import sys
from pathlib import Path
from unittest import mock

import pytest

sys.path.insert(0, str(Path(__file__).resolve().parents[2]))
from release import recovery_gate as gate
from release.paths import WORKSPACE

BASE = "f984872ebb536c84d1234bbe0d7b1de01d1401d5"
TARGET = "51c9f3e029dd9fe1a92013a5670eaa7ee5f42009"
PREFIX = ".agents/skills/sub2api-production-deploy/scripts/"

OBJECTS = {
    "maintenance/181/mask-backup-units.sh": ("100755:06c5c4afd3ea635a4cf14578df53e74064c4fa2a", "100755:bb51536e3ae1564a1422aaee56b47c6741db3ed4"),
    "maintenance/181/restore-backup-units.sh": ("100755:eefa3472a730f2d5bb3fbf765a41999dd01123c6", "100755:20766581bcad1833ec4d84b5cd99388d36a3d3a6"),
    "maintenance/release/activity-reward-cost-postflight.sh": ("100644:a963566a65bc10cd74d540dd2e8b8a1f00d8bf32", "100644:94fe98b676ffa5f7d00271b93df6fe368204c282"),
    "maintenance/release/context.sh": ("100755:31782b5744fbcb9146b2dc44eec3c4a6d60a4607", "100755:64da0affccc5b9ef5b663e4eccef7558372d76f2"),
    "maintenance/release/full-recovery-capture.sh": ("100755:712b442f782fe847561b03576135658ef5d12015", "100755:aa255355789607c9f35ba5707ec0c537501d9d26"),
    "maintenance/release/migration-195-assert.sh": ("100644:1bece26bca0165bdcd6ebbe097814822eb56d799", "100644:f3de56f19d6a9ed2be87a7d2a5280ebd5a47d9f7"),
    "maintenance/release/migration-232-assert.sh": ("100644:a36fef9e005a79d4143b49548c4caa9fe0f3fec3", "100644:ff9b8b35beb8726e3dfeed7b828ab3f43fa0f44e"),
    "maintenance/release/migration-233-assert.sh": ("100644:0392260743016410e98958907d579ad197865ea6", "100644:d539af6694b2af4faf05dda164d7997c3cfa62c8"),
    "maintenance/release/migration-234-assert.sh": ("100644:7f70630a33f568b5377bedfdd8ab1bd6da26a348", "100644:143330d27b95cac9c4d579a0319dfd0d36f226c0"),
    "maintenance/release/migration-235-assert.sh": ("100644:4978359b37aa22bbac73bbbee59106da7e0f6c99", "100644:62b0ec3e918e5df52b0845d39a0de60ced369374"),
    "maintenance/release/migration-236-assert.sh": ("100644:abf4ee7cd50770de5c12444e969a050ee59f4b2b", "100644:16a2c97281c11ebc8fdc5febdb9ce0496b80a817"),
    "maintenance/release/migration-237-assert.sh": ("100644:e3028b7642f16d58ddd6d020bbd749b325fecfa9", "100644:848b8e4f70c289f85ae1f3310a97d0ae087f76f4"),
    "maintenance/release/migration-238-assert.sh": ("100644:3a38a07903b257a8db8704b30f253e423e071c1c", "100644:ef58d2a572a6e36a03beac5eec9e9a98a6937f77"),
    "maintenance/release/migration-239-assert.sh": ("100755:50335bbed5316d9abb228c5e9352d701685e83dd", "100755:c64bc2592d1a2e7112afa3742f9f7451181bcff3"),
    "maintenance/release/migration-240-assert.sh": ("100755:d2176f9d33f7fec0ba70679559edb36f20791cc6", "100755:e5f1a0152c1d8f6f8e327eeb56a25793281d68e2"),
    "maintenance/release/migration-241-assert.sh": ("100755:6c06945afb457b5c168266b7b1d39270f22034b1", "100755:efefb18bf565a3f7eb7dbca01eed052ca2d44371"),
    "maintenance/release/migration-242-assert.sh": ("100644:04bea56fec9b9e69beefdb4444efcd2933c5101d", "100644:18ffdb5256141db0ef7818c619c0f706c79cd4d8"),
    "maintenance/release/migration-243-assert.sh": ("100644:7f0a92eb4da64edaacebb0d51bbd027660fc7695", "100644:9cc128d400b1daceae70403962fad761954d6a99"),
    "maintenance/release/migration-244-assert.sh": ("100644:839bc7cbf7331585b282eebb9216859c5fa398fa", "100644:ecd43cb88e8668d64c3adce1ab3a039f7762f402"),
    "maintenance/release/migration-245-assert.sh": ("100644:358cb9c5d43a199e802e1eae3f0dfd2c5df53cf5", "100644:bd84829254bd06fbf48a202ccf63542d7b8f7ce0"),
    "maintenance/release/migration-254-assert.sh": ("100644:7916324baac7e514499858ea3a3af1c088d033ee", "100644:5be9c33577454721927578aee91775cf84f9f6cc"),
    "maintenance/release/migration-285-assert.sh": ("100644:5ea72fed8e4015150f2aea8adf3f94035d1d99be", "100644:1c6b697849da3e1e3e5367c8c711813cededaf7e"),
    "maintenance/release/migration-297-assert.sh": ("100644:e8a648708fd32a561265cee148e677a2b56b2a8b", "100644:7ac9a47a396dceb9d4a45acf09ad055691717e4e"),
    "maintenance/release/migration-298-assert.sh": ("100755:f00b79e7b9a81aa0d4ffbfe914043ecb8f438c1a", "100755:1f2a0b9c97463538a76e10d9b57f355532537c50"),
    "maintenance/release/migration-299-assert.sh": ("100644:8a3bd04f772660058a923937422956fd54496b9a", "100644:ccb8cbb8833ea1c1aa5a56f99c716f36d6810795"),
    "maintenance/release/migration-301-assert.sh": ("100644:c5791141157efb0fe9fa02a9efed314ff2a1afe8", "100644:3a14bee89da858ce0d28a28dd2b727eb33ed78c4"),
    "maintenance/release/prepare.sh": ("100755:81e16387f070083a7045920767d511e37a531c6c", "100755:280fbca696aecee91d3ceb516052622ba86632d3"),
    "maintenance/release/promote-backup.sh": ("100755:3b926a53b4a81e0fb9da1c1f8286af07cf6276cf", "100755:d2c6c2ea9929a56936a468e23f9e567f0b6ad467"),
    "maintenance/release/switch.sh": ("100755:82b0ef57c4a85e7aa6983827724dd59703d8fee0", "100755:1f0bf4ba04be3d8015574a80775c28ab256c0017"),
    "release/baseline_candidate_load.py": ("100644:55609b6cc0678e9d3592fcfc2bacd4ab9159e684", "100644:b878489c4b959941cba6b0ffbdbaa61534e13b9e"),
    "release/bootstrap_backup_dr_assets.sh": ("100644:d5d6569b04a3891c0a784841418be4757873bd8d", "100644:05b5ae8eac3c8edbc17efdbc3f3267a8f28806b3"),
    "release/bootstrap_vm_signer.sh": ("100755:4be88a90ade508e898d650f498005b1fff6a325a", "100755:6a593a3ce79afa8779fc42a6e9d0ea172e2d6be6"),
    "release/gate.py": ("100644:c05e25b0fe57afaa0ce06dea70c3c9837a9f4abc", "100644:afe8c4b222bdc88aafe52cce7d0fe499aa47728a"),
    "release/manifest.py": ("100644:913d3a6a8006df87e14c7717427eda776f1a23a5", "100644:8b15e55fd1b6b0a850e3951f0194ff78c263246f"),
    "release/postrelease_baseline.py": ("100644:67c688307c9a9181d934ff64445e2b01595d19bc", "100644:48d579e02518bf92996caef2b1e16d27e4bc1f95"),
    "release/production-recovery-retention-clean.sh": ("100644:8daef1e43f1c1f8221755a608cfe38b6691279a9", "100644:b011453afaa60900f2e470dbf194aa298fb71079"),
    "release/production-space-clean.sh": ("100644:0d1ce52ec48429f481332f8a2dbcc340a91f07bb", "100644:05124b1843fc7a41bfc84410b59272240e43b28f"),
    "release/production_cleanup.py": ("100644:97acf6880c4bf00d9ee7178d5979f47bf33ede4a", "100644:b9eebc4460294fe8093f4399def04ab63b132a95"),
    "release/production_recovery_retention.py": ("100644:4873be80500865760c428ddbcfbee9e607fe84a6", "100644:863af89c984d41b0fcec2df1fd27eb199c1231e6"),
    "release/profiles.py": ("100644:72f20e7507d86f47a9af9377791b9b168236a921", "100644:354c70f3809bf0013b2056ac38f1ae3240e15455"),
    "release/promote-dr-baseline.sh": ("100644:b36057c6f3b7a8d99ef70f08c6519aa8d0d758af", "100644:4c39adc6a3900abffd637b6d5d1b7eeb2e24e13c"),
    "release/recovery_drill.py": ("100644:b30e1f45a72d4f46fa1c2bcaccc98066e79a1525", "100644:0320cb76769156b19c1e9b687869d67c606646f2"),
    "release/sign-dr-evidence.sh": ("100644:203cf050dc0372d32cb96ca978142f7030a6efbf", "100644:d081e1984c13853bd7f3ec37172f6d6f6f83fbd1"),
    "release/sign-gate.sh": ("100644:a51cd3c84b130a7ec81064ba2efbc0af1ccf5e13", "100644:ce1197fbcf26c20894ef3e8c43d9f361c9eeaf5c"),
    "release/stage-dr-baseline-candidate.sh": ("100755:c983559f8c74e6499ae3ac4c9496ea24ad2d464e", "100755:7cd39fc433ff880882df480007b66bfee9aad91b"),
    "release/vm-only-validate.sh": ("100644:5de5faa67ab69178c83f166c1a4f89572e0e2a55", "100644:26b032d0efafb1cdf43108614587541994d6a40d"),
    "release/vm-validate.sh": ("100755:924d71be3fef5369647fa9e22b381449488b8ca7", "100755:8df34e72705f50447c8f13797284a8626070fbcc"),
}

COMPATIBILITY_PATHS = frozenset(path for path in OBJECTS if path != "maintenance/release/prepare.sh")
PREPARE_PATHS = frozenset({"maintenance/release/prepare.sh"})
PATHS = frozenset(OBJECTS)
TRANCHES = (
    (BASE, TARGET, "reviewed_profile_compatibility_changed", frozenset(PREFIX + path for path in COMPATIBILITY_PATHS)),
    (BASE, TARGET, "reviewed_prepare_gate_policy_changed", frozenset(PREFIX + path for path in PREPARE_PATHS)),
)


@pytest.fixture(scope="module")
def reviewed():
    assert len(COMPATIBILITY_PATHS) == 46
    assert len(PREPARE_PATHS) == 1
    assert COMPATIBILITY_PATHS.isdisjoint(PREPARE_PATHS)
    assert COMPATIBILITY_PATHS | PREPARE_PATHS == PATHS
    assert gate._PROFILE277_COMPATIBILITY_REVIEW_PATHS == TRANCHES[0][3]
    assert gate._PREPARE_GATE_POLICY_REVIEW_PATHS == TRANCHES[1][3]
    assert gate._REVIEWED_SCOPED_TRANCHES[-2:] == TRANCHES
    expected = {PREFIX + path: pair for path, pair in OBJECTS.items()}
    old, new = gate._tree_blobs(WORKSPACE, BASE), gate._tree_blobs(WORKSPACE, TARGET)
    assert {path: (old.get(path), new.get(path)) for path in expected} == expected
    assert {path for path in gate._changed_paths(WORKSPACE, BASE, TARGET)
            if gate._is_recovery_sensitive_path(path)} == set(expected)
    return expected


def classify(reviewed, old, new):
    base, future = "b" * 40, "f" * 40
    old_tree = {path: pair[0] for path, pair in reviewed.items()}
    new_tree = {path: pair[1] for path, pair in reviewed.items()}
    trees = {BASE: old_tree, TARGET: new_tree, base: old, future: new}
    changed = sorted(old.keys() | new.keys())
    with (mock.patch.object(gate, "_tree_blobs", side_effect=lambda _, sha: trees[sha]),
          mock.patch.object(gate, "_changed_paths", return_value=changed),
          mock.patch.object(gate, "_commit_exists", side_effect=lambda _, sha: sha in trees),
          mock.patch.object(gate, "_is_ancestor", return_value=True),
          mock.patch.object(gate, "_REVIEWED_COMPATIBILITY_TRANCHES", ()),
          mock.patch.object(gate, "_REVIEWED_GATE_POLICY_TRANCHES", ()),
          mock.patch.object(gate, "_REVIEWED_MIGRATION_GATE_TRANCHES", ()),
          mock.patch.object(gate, "_REVIEWED_SCOPED_TRANCHES", TRANCHES)):
        return gate.classify(WORKSPACE, base, future)


def test_exact_reviewed_profile277_objects_are_specialized(reviewed):
    old = {path: pair[0] for path, pair in reviewed.items()}
    new = {path: pair[1] for path, pair in reviewed.items()}
    report = gate.classify(WORKSPACE, BASE, TARGET)
    assert report["mode"] == "specialized"
    assert {reason for _, _, reason, _ in TRANCHES} <= set(report["reason_codes"])
    assert "recovery_change_requires_review" not in report["reason_codes"]
    gate.assert_release_allowed(report)
    assert classify(reviewed, old, new)["mode"] == "specialized"


@pytest.mark.parametrize("path", sorted(PATHS))
@pytest.mark.parametrize("mutation", ["blob", "mode", "delete", "rename", "base"])
def test_any_profile277_object_drift_remains_full(reviewed, path, mutation):
    old = {key: pair[0] for key, pair in reviewed.items()}
    new = {key: pair[1] for key, pair in reviewed.items()}
    key = PREFIX + path
    if mutation == "blob":
        new[key] = new[key].split(":")[0] + ":" + "0" * 40
    elif mutation == "mode":
        new[key] = ("100644" if new[key].startswith("100755") else "100755") + ":" + new[key].split(":")[1]
    elif mutation == "delete":
        old = dict(new)
        del new[key]
    elif mutation == "rename":
        old = dict(new)
        new[key + ".moved"] = new.pop(key)
    else:
        old[key] = "100644:" + "0" * 40
    report = classify(reviewed, old, new)
    assert report["mode"] == "full"
    assert "recovery_change_requires_review" in report["reason_codes"]
    with pytest.raises(RuntimeError, match="recovery changes require review"):
        gate.assert_release_allowed(gate.require_full(report))


@pytest.mark.parametrize("suffix", ["maintenance/release/restore.sh", "release/trust/unregistered.pub"])
def test_additional_sensitive_paths_cannot_borrow_profile277_review(reviewed, suffix):
    old = {path: pair[0] for path, pair in reviewed.items()}
    new = {path: pair[1] for path, pair in reviewed.items()}
    extra = PREFIX + suffix
    old[extra], new[extra] = "100644:" + "1" * 40, "100644:" + "2" * 40
    report = classify(reviewed, old, new)
    assert report["mode"] == "full"
    assert "recovery_change_requires_review" in report["reason_codes"]


def test_descendant_changes_keep_the_exact_profile277_review(reviewed):
    old = {path: pair[0] for path, pair in reviewed.items()}
    new = {path: pair[1] for path, pair in reviewed.items()}
    future_path = PREFIX + "tests/release/test_profile277_recovery_review.py"
    new[future_path] = "100644:" + "3" * 40
    report = classify(reviewed, old, new)
    assert report["mode"] == "specialized"
    gate.assert_release_allowed(report)
