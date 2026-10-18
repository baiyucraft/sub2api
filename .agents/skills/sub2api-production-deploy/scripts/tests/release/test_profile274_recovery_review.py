"""Exact independent review of the profile 274 release and migration wiring."""
from __future__ import annotations
import sys
from pathlib import Path
from unittest import mock
import pytest
sys.path.insert(0, str(Path(__file__).resolve().parents[2]))
from release import recovery_gate as gate
from release.paths import WORKSPACE
from release.process import check_output_hidden
BASE = "5deebb2929c2a694145cc2726e895f01c43d28c1"
TARGET = "aca670e896ffa0e0e54204f1e6c33c205d5160f5"
OBJECTS = {'.agents/skills/sub2api-production-deploy/scripts/maintenance/181/mask-backup-units.sh': ('100755:b4ac722187c6e86fe60784cdd5c7c8d37b2fb2fe',
                                                                                           '100755:693e10ad4617448e1027f0dedd1d188add5d426b'),
 '.agents/skills/sub2api-production-deploy/scripts/maintenance/181/restore-backup-units.sh': ('100755:1b33b9739f53f04656259017b0169f7673ad727f',
                                                                                              '100755:0ae8787161835abd4d436f5dd5e09e346b8e6c5a'),
 '.agents/skills/sub2api-production-deploy/scripts/maintenance/release/activity-reward-cost-postflight.sh': ('100644:5bc0cfb7dec7bfebc15e7360ba12e7d273b27abe',
                                                                                                             '100644:e93d5f397c4963e4ca25f08f44ca4f00e820b6bf'),
 '.agents/skills/sub2api-production-deploy/scripts/maintenance/release/context.sh': ('100755:396b16383094c58361f6125fda668d10faa263eb',
                                                                                     '100755:93b88c9ecba1bffb94b11a2f1c4150125976ad58'),
 '.agents/skills/sub2api-production-deploy/scripts/maintenance/release/full-recovery-capture.sh': ('100755:b52a87f7ca6ecece9ca34244f00f26d02c44c398',
                                                                                                   '100755:fd6fc952ad79ad751e9010624a74c513317ec580'),
 '.agents/skills/sub2api-production-deploy/scripts/maintenance/release/migration-195-assert.sh': ('100644:458f016b2a9922b55de9ff288c41e4962de6193d',
                                                                                                  '100644:a8fd4b0a7de9b2d526ac7c41c52d1e53ac2769b3'),
 '.agents/skills/sub2api-production-deploy/scripts/maintenance/release/migration-232-assert.sh': ('100644:61fbbeca38cf694e1b2c43f8b78c1442ad5c1a6b',
                                                                                                  '100644:14d9df1f7da2b6b3cd23fcd66b0c6f7df65d3a29'),
 '.agents/skills/sub2api-production-deploy/scripts/maintenance/release/migration-233-assert.sh': ('100644:c6b819fc42afc9b4bfe8b30b8c06f8813e0dd9e7',
                                                                                                  '100644:17b7706d1b5392e3aee9505c1309123e0bf9ca12'),
 '.agents/skills/sub2api-production-deploy/scripts/maintenance/release/migration-234-assert.sh': ('100644:d08be46f1c527532a76ec9c05a8bdd9ff417c6c6',
                                                                                                  '100644:3d6c615774b98ef96bf05d3442ef4943b1d69425'),
 '.agents/skills/sub2api-production-deploy/scripts/maintenance/release/migration-235-assert.sh': ('100644:fcba5d2026037e85718c627b98f8f5be3c629d1c',
                                                                                                  '100644:485e84861d99c31d2bdcbc879616afe730441086'),
 '.agents/skills/sub2api-production-deploy/scripts/maintenance/release/migration-236-assert.sh': ('100644:38f2fbb9e8daac5686ab207666bc3c556c936ee4',
                                                                                                  '100644:e9e27b6594e5af6bb02d511267ad813c3fe69ece'),
 '.agents/skills/sub2api-production-deploy/scripts/maintenance/release/migration-237-assert.sh': ('100644:58fd4c161edc5780248b81c102857bb119e7aaad',
                                                                                                  '100644:4272ef9abc783c1603de9a50c8a66c5062118c97'),
 '.agents/skills/sub2api-production-deploy/scripts/maintenance/release/migration-238-assert.sh': ('100644:b7aac1634bc90577cc7028cca1ace4b60a9c8251',
                                                                                                  '100644:a06e3dd510f1ec18388e5beccacf2305c52edbe9'),
 '.agents/skills/sub2api-production-deploy/scripts/maintenance/release/migration-239-assert.sh': ('100755:aed305a9c86f2a14b3acd344cfcf5f5cad68b583',
                                                                                                  '100755:68c3430013fb84fb17e9635137539e787a6f0be7'),
 '.agents/skills/sub2api-production-deploy/scripts/maintenance/release/migration-240-assert.sh': ('100755:5478814c92f6e315cb04b48135165a3de565c746',
                                                                                                  '100755:9bf46acb57716f68c87d286ee0d849a5fc03a74c'),
 '.agents/skills/sub2api-production-deploy/scripts/maintenance/release/migration-241-assert.sh': ('100755:b1a27df791ffead255e7415999347bf09f6c9bbc',
                                                                                                  '100755:6360f375e08f8ce9ba51df181d07d87a991a14ab'),
 '.agents/skills/sub2api-production-deploy/scripts/maintenance/release/migration-242-assert.sh': ('100644:52a624cdff07df668e29ee73b8d3ae3e88234f18',
                                                                                                  '100644:09f704e0e699d83dcff0459e1a4d2d8b09fd35c3'),
 '.agents/skills/sub2api-production-deploy/scripts/maintenance/release/migration-243-assert.sh': ('100644:362e827f96d9eb854b66f113c855f67066972875',
                                                                                                  '100644:08e422dffa9287cb4ff722524644f2d5b3d5d786'),
 '.agents/skills/sub2api-production-deploy/scripts/maintenance/release/migration-244-assert.sh': ('100644:a5ade79b94a4a8b975547145620f846ce58650de',
                                                                                                  '100644:39c439e1f8eb6f0b1f274e37aa97b97eaf01add9'),
 '.agents/skills/sub2api-production-deploy/scripts/maintenance/release/migration-245-assert.sh': ('100644:a14eb7fff769131e6e6e720e61d919c89ecb7501',
                                                                                                  '100644:e9f8ea347d7900f70b59fbfa081cff36c57b0186'),
 '.agents/skills/sub2api-production-deploy/scripts/maintenance/release/migration-254-assert.sh': ('100644:94f9a2c2d1a823af93eda4751893424bdb6ae3c4',
                                                                                                  '100644:a8cd2c08225a017aff83061645213ecb44dd2ba7'),
 '.agents/skills/sub2api-production-deploy/scripts/maintenance/release/migration-285-assert.sh': ('100644:5c06e3a8335c5a8a3cbc547d6980e0fc9833146d',
                                                                                                  '100644:c6e1ffa3e094caeaf30d5ed04b66c0798b3f37e6'),
 '.agents/skills/sub2api-production-deploy/scripts/maintenance/release/migration-297-assert.sh': ('100644:95775662d2f43ddc1da159e679275ced4b87dc7d',
                                                                                                  '100644:94d441b8d24b44c8915f9d07e9a7daba4d119995'),
 '.agents/skills/sub2api-production-deploy/scripts/maintenance/release/migration-298-assert.sh': ('100755:7e9f3f9185377d8b2c687856c9a9638b88e3af28',
                                                                                                  '100755:d9c36a901331c9c2e49254da8f9de487472e1a6a'),
 '.agents/skills/sub2api-production-deploy/scripts/maintenance/release/migration-299-assert.sh': ('100644:91b856af8d2d8c698e122cff0b2fe255aedbf244',
                                                                                                  '100644:0f323f48a93d814e48b4d2d1667306c31c590b01'),
 '.agents/skills/sub2api-production-deploy/scripts/maintenance/release/migration-301-assert.sh': (None,
                                                                                                  '100644:8c4a06f387ebdc87b8f5cbcdc767caa780469547'),
 '.agents/skills/sub2api-production-deploy/scripts/maintenance/release/prepare.sh': ('100755:c0df49a7cef36a918053e404825232ba1da53ed2',
                                                                                     '100755:69045bc0a4d11a61a599965654e34da62a6ddaee'),
 '.agents/skills/sub2api-production-deploy/scripts/maintenance/release/promote-backup.sh': ('100755:4fff999f55695423b3d107dd78716d3607b9a046',
                                                                                            '100755:d9ee34c48ef17009e750a2960da08f84dccf67cd'),
 '.agents/skills/sub2api-production-deploy/scripts/maintenance/release/switch.sh': ('100755:761804c2e5172026d804805c597f099a4ba4b035',
                                                                                    '100755:4b2a9f9727d9da6417ce38b576bb39a94ab5409a'),
 '.agents/skills/sub2api-production-deploy/scripts/release/baseline_candidate_load.py': ('100644:a26ea5efa528b8afb1440538e54aba8af2130ab0',
                                                                                         '100644:033716011b790431ede4de89d7422ecb929c7985'),
 '.agents/skills/sub2api-production-deploy/scripts/release/bootstrap_backup_dr_assets.sh': ('100644:9bc932c197edc95118cadc482619cb77db795a04',
                                                                                            '100644:97eb033289f7dd469619501135d82327733e851e'),
 '.agents/skills/sub2api-production-deploy/scripts/release/bootstrap_vm_signer.sh': ('100755:26510ad32334e0282bd5cf5ddaf5c94a6fc40430',
                                                                                     '100755:3e42f4da09ad913911949733c85d7e6d1b45a788'),
 '.agents/skills/sub2api-production-deploy/scripts/release/gate.py': ('100644:ec3e2550ef32a182deccfe44b9fd111fb4aad5a0',
                                                                      '100644:b1ec0ae009479ea83f89492f089006d5107c8103'),
 '.agents/skills/sub2api-production-deploy/scripts/release/manifest.py': ('100644:f50b94e772097fa4b2693fd35cc50eb3e4dac3e1',
                                                                          '100644:ee991ee421a183df3801d225ebef9dfb52ce5fb7'),
 '.agents/skills/sub2api-production-deploy/scripts/release/migration_planner.py': ('100644:bd21abb887be529567c0dbcf773af4390782338e',
                                                                                   '100644:86432c39741636c6e00ecfcd0b8ce853596971bc'),
 '.agents/skills/sub2api-production-deploy/scripts/release/postrelease_baseline.py': ('100644:6f84a462fd58b8a9bb4afb58451db1538ff1965c',
                                                                                      '100644:515a7cd2f1bd3b2113ae185f21013eeebb7555f9'),
 '.agents/skills/sub2api-production-deploy/scripts/release/production-recovery-retention-clean.sh': ('100644:bfa5ae44f5a46dc9b0f69c1f58474cfe9773978a',
                                                                                                     '100644:e35584e06fed24a66716aa2fe69a39f136b70312'),
 '.agents/skills/sub2api-production-deploy/scripts/release/production-space-clean.sh': ('100644:b2a4e003cd3292c744c94b932a72080b80d97b82',
                                                                                        '100644:8b974ffba8387829e8eae14053805e8bdccb33ea'),
 '.agents/skills/sub2api-production-deploy/scripts/release/production_cleanup.py': ('100644:c605c4a911a7b936251c40096a7737d810ad3f8d',
                                                                                    '100644:18034bed071ee362ffca832d4f0937f12e9b4740'),
 '.agents/skills/sub2api-production-deploy/scripts/release/production_recovery_retention.py': ('100644:bb469da767f3442f584e120029abe6e9cda431e2',
                                                                                               '100644:36e723fed9eb82ac6d80e9ad18c59d9363c93673'),
 '.agents/skills/sub2api-production-deploy/scripts/release/profiles.py': ('100644:f44b3c32e4d5669d295a8ebd3bc63d8e38bbf143',
                                                                          '100644:09febaebd213537ba67383fda829db5c1b9d4b47'),
 '.agents/skills/sub2api-production-deploy/scripts/release/promote-dr-baseline.sh': ('100644:b67dbe274922691d231c84e8a062cc91524aa701',
                                                                                     '100644:12fe0ea10a8caeb58b511c8ecfa3064d117b61c2'),
 '.agents/skills/sub2api-production-deploy/scripts/release/recovery_drill.py': ('100644:afa12cfcaa2e9d18407d148cd88ac42f2924b8ae',
                                                                                '100644:9bb4506ac0906d29728d43f4db9b1b8d18b8ee1f'),
 '.agents/skills/sub2api-production-deploy/scripts/release/sign-dr-evidence.sh': ('100644:87e3480995a9f8e2ef7e098b0793f791a4e75b46',
                                                                                  '100644:71bb8f4044c4670d2082f8440da3c89a3d25b9bd'),
 '.agents/skills/sub2api-production-deploy/scripts/release/sign-gate.sh': ('100644:11dd1b938a48e0337fc9a337801d2b743b79ddf0',
                                                                           '100644:78ed918dcc23973237fa03544a13b12f4c7fa010'),
 '.agents/skills/sub2api-production-deploy/scripts/release/stage-dr-baseline-candidate.sh': ('100755:4a65ef285f8a0bce1aff8cf4f972d45932503eaf',
                                                                                             '100755:8a120d5e537cc4e629000b9c388e9180b3ddeb57'),
 '.agents/skills/sub2api-production-deploy/scripts/release/vm-only-validate.sh': ('100644:1a4de877481ecc199ddf539272976182eafecee4',
                                                                                  '100644:5829377c36fd6a007a51553155ab18d6957b158c'),
 '.agents/skills/sub2api-production-deploy/scripts/release/vm-validate.sh': ('100755:11b79feee2ba54575be9642202a59060a01ec5c2',
                                                                             '100755:c882e8848188ff3d933ed155c7f479667c62ea77')}
COMPAT = frozenset(['.agents/skills/sub2api-production-deploy/scripts/maintenance/181/mask-backup-units.sh',
 '.agents/skills/sub2api-production-deploy/scripts/maintenance/181/restore-backup-units.sh',
 '.agents/skills/sub2api-production-deploy/scripts/maintenance/release/context.sh',
 '.agents/skills/sub2api-production-deploy/scripts/maintenance/release/full-recovery-capture.sh',
 '.agents/skills/sub2api-production-deploy/scripts/maintenance/release/migration-195-assert.sh',
 '.agents/skills/sub2api-production-deploy/scripts/maintenance/release/migration-232-assert.sh',
 '.agents/skills/sub2api-production-deploy/scripts/maintenance/release/migration-233-assert.sh',
 '.agents/skills/sub2api-production-deploy/scripts/maintenance/release/migration-234-assert.sh',
 '.agents/skills/sub2api-production-deploy/scripts/maintenance/release/migration-235-assert.sh',
 '.agents/skills/sub2api-production-deploy/scripts/maintenance/release/migration-236-assert.sh',
 '.agents/skills/sub2api-production-deploy/scripts/maintenance/release/migration-237-assert.sh',
 '.agents/skills/sub2api-production-deploy/scripts/maintenance/release/migration-238-assert.sh',
 '.agents/skills/sub2api-production-deploy/scripts/maintenance/release/migration-239-assert.sh',
 '.agents/skills/sub2api-production-deploy/scripts/maintenance/release/migration-240-assert.sh',
 '.agents/skills/sub2api-production-deploy/scripts/maintenance/release/migration-241-assert.sh',
 '.agents/skills/sub2api-production-deploy/scripts/maintenance/release/migration-242-assert.sh',
 '.agents/skills/sub2api-production-deploy/scripts/maintenance/release/migration-243-assert.sh',
 '.agents/skills/sub2api-production-deploy/scripts/maintenance/release/migration-244-assert.sh',
 '.agents/skills/sub2api-production-deploy/scripts/maintenance/release/migration-245-assert.sh',
 '.agents/skills/sub2api-production-deploy/scripts/maintenance/release/migration-254-assert.sh',
 '.agents/skills/sub2api-production-deploy/scripts/maintenance/release/migration-285-assert.sh',
 '.agents/skills/sub2api-production-deploy/scripts/maintenance/release/migration-297-assert.sh',
 '.agents/skills/sub2api-production-deploy/scripts/maintenance/release/migration-298-assert.sh',
 '.agents/skills/sub2api-production-deploy/scripts/maintenance/release/migration-299-assert.sh',
 '.agents/skills/sub2api-production-deploy/scripts/maintenance/release/prepare.sh',
 '.agents/skills/sub2api-production-deploy/scripts/maintenance/release/promote-backup.sh',
 '.agents/skills/sub2api-production-deploy/scripts/release/baseline_candidate_load.py',
 '.agents/skills/sub2api-production-deploy/scripts/release/bootstrap_backup_dr_assets.sh',
 '.agents/skills/sub2api-production-deploy/scripts/release/bootstrap_vm_signer.sh',
 '.agents/skills/sub2api-production-deploy/scripts/release/postrelease_baseline.py',
 '.agents/skills/sub2api-production-deploy/scripts/release/production-recovery-retention-clean.sh',
 '.agents/skills/sub2api-production-deploy/scripts/release/production-space-clean.sh',
 '.agents/skills/sub2api-production-deploy/scripts/release/production_cleanup.py',
 '.agents/skills/sub2api-production-deploy/scripts/release/production_recovery_retention.py',
 '.agents/skills/sub2api-production-deploy/scripts/release/profiles.py',
 '.agents/skills/sub2api-production-deploy/scripts/release/promote-dr-baseline.sh',
 '.agents/skills/sub2api-production-deploy/scripts/release/recovery_drill.py',
 '.agents/skills/sub2api-production-deploy/scripts/release/sign-dr-evidence.sh',
 '.agents/skills/sub2api-production-deploy/scripts/release/sign-gate.sh',
 '.agents/skills/sub2api-production-deploy/scripts/release/stage-dr-baseline-candidate.sh',
 '.agents/skills/sub2api-production-deploy/scripts/release/vm-only-validate.sh'])
MIGRATION = frozenset(['.agents/skills/sub2api-production-deploy/scripts/maintenance/release/migration-301-assert.sh',
 '.agents/skills/sub2api-production-deploy/scripts/release/gate.py',
 '.agents/skills/sub2api-production-deploy/scripts/release/manifest.py',
 '.agents/skills/sub2api-production-deploy/scripts/release/migration_planner.py',
 '.agents/skills/sub2api-production-deploy/scripts/release/vm-validate.sh'])
POSTFLIGHT = frozenset(['.agents/skills/sub2api-production-deploy/scripts/maintenance/release/activity-reward-cost-postflight.sh',
 '.agents/skills/sub2api-production-deploy/scripts/maintenance/release/switch.sh'])
PATHS = frozenset(OBJECTS)
TRANCHES = (
    (BASE, TARGET, "reviewed_profile_compatibility_changed", COMPAT),
    (BASE, TARGET, "reviewed_profile274_migration_gate_changed", MIGRATION),
    (BASE, TARGET, "reviewed_reward_cost_postflight_changed", POSTFLIGHT),
)


def test_exact_reviewed_objects_groups_and_prior_registry_are_frozen():
    assert (len(COMPAT), len(MIGRATION), len(POSTFLIGHT)) == (41, 5, 2)
    assert COMPAT.isdisjoint(MIGRATION) and COMPAT.isdisjoint(POSTFLIGHT) and MIGRATION.isdisjoint(POSTFLIGHT)
    assert COMPAT | MIGRATION | POSTFLIGHT == PATHS
    assert gate._PROFILE274_COMPATIBILITY_PATHS == COMPAT
    assert gate._PROFILE274_MIGRATION_GATE_PATHS == MIGRATION
    assert gate._PROFILE274_POSTFLIGHT_PATHS == POSTFLIGHT
    old, new = gate._tree_blobs(WORKSPACE, BASE), gate._tree_blobs(WORKSPACE, TARGET)
    assert {p:(old.get(p),new.get(p)) for p in PATHS} == OBJECTS
    assert {p for p in gate._changed_paths(WORKSPACE, BASE, TARGET) if gate._is_recovery_sensitive_path(p)} == PATHS
    expected = {p: reason for _, _, reason, paths in TRANCHES for p in paths}
    assert gate._reviewed_compatibility_paths(WORKSPACE, BASE, TARGET) == expected
    prior_source = check_output_hidden(["git", "show", TARGET + ":.agents/skills/sub2api-production-deploy/scripts/release/recovery_gate.py"], cwd=WORKSPACE, text=True)
    prior = {"__name__":"profile274_prior","__package__":"release"}
    exec(compile(prior_source, TARGET + ":recovery_gate.py", "exec"), prior)
    for name in ("_REVIEWED_COMPATIBILITY_TRANCHES", "_REVIEWED_GATE_POLICY_TRANCHES", "_REVIEWED_MIGRATION_GATE_TRANCHES"):
        assert getattr(gate,name) == prior[name]
    assert gate._REVIEWED_SCOPED_TRANCHES == (*prior["_REVIEWED_SCOPED_TRANCHES"], *TRANCHES)


def test_actual_review_requires_specialized():
    report = gate.classify(WORKSPACE, BASE, TARGET)
    assert report["mode"] == "specialized"
    assert {r for _,_,r,_ in TRANCHES} <= set(report["reason_codes"])
    assert "recovery_change_requires_review" not in report["reason_codes"]
    gate.assert_release_allowed(report)


def report_for(old,new,paths):
    base,future = "b"*40,"f"*40
    trees = {BASE:{p:a for p,(a,_) in OBJECTS.items() if a}, TARGET:{p:b for p,(_,b) in OBJECTS.items()},base:old,future:new}
    with (mock.patch.object(gate,"_tree_blobs",side_effect=lambda _root,sha:trees[sha]),
          mock.patch.object(gate,"_changed_paths",return_value=paths),
          mock.patch.object(gate,"_commit_exists",side_effect=lambda _root,sha:sha in trees),
          mock.patch.object(gate,"_is_ancestor",return_value=True)):
        return gate.classify(WORKSPACE,base,future)


@pytest.mark.parametrize("path",sorted(PATHS))
@pytest.mark.parametrize("mutation",["blob","mode","delete","rename","base"])
def test_each_reviewed_path_rejects_future_or_baseline_drift(path,mutation):
    old={p:a for p,(a,_) in OBJECTS.items() if a}
    new={p:b for p,(_,b) in OBJECTS.items()}
    if mutation=="blob": new[path]=new[path].split(":")[0]+":"+"0"*40
    elif mutation=="mode": new[path]=("100644" if new[path].startswith("100755") else "100755")+":"+new[path].split(":")[1]
    elif mutation=="delete": old=new.copy();del new[path]
    elif mutation=="rename": old=new.copy();new[path+".moved"]=new.pop(path)
    else: old[path]="100644:"+"0"*40
    report=report_for(old,new,sorted(set(old)|set(new)))
    assert report["mode"]=="full" and "recovery_change_requires_review" in report["reason_codes"]
    with pytest.raises(RuntimeError,match="recovery changes require review"):
        gate.assert_release_allowed(gate.require_full(report))


@pytest.mark.parametrize("suffix",["maintenance/release/restore.sh","release/ssh.py","release/trust/extra.pub"])
def test_additional_sensitive_changes_cannot_borrow_review(suffix):
    old={p:a for p,(a,_) in OBJECTS.items() if a}
    new={p:b for p,(_,b) in OBJECTS.items()}
    extra=".agents/skills/sub2api-production-deploy/scripts/"+suffix
    old[extra],new[extra]="100644:"+"1"*40,"100644:"+"2"*40
    report=report_for(old,new,sorted(set(old)|set(new)))
    assert report["mode"]=="full" and "recovery_change_requires_review" in report["reason_codes"]


def test_metadata_descendant_only_preserves_reviewed_objects():
    old={p:a for p,(a,_) in OBJECTS.items() if a}
    new={p:b for p,(_,b) in OBJECTS.items()}
    report=report_for(old,new,sorted(set(old)|set(new)))
    assert report["mode"]=="specialized"
    gate.assert_release_allowed(report)
