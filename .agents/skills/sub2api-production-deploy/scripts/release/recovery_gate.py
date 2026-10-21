from __future__ import annotations

import hashlib
import json
import re
import subprocess
from pathlib import Path
from typing import Any, Iterable

from .process import check_output_hidden, run_hidden


REPORT_SCHEMA = 1
MODES = frozenset({"fast", "specialized", "full"})
ESTIMATED_EXTRA_SECONDS = {
    "fast": 0,
    "specialized": 600,
    "full": 2400,
}
FULL_SHA = re.compile(r"^[0-9a-f]{40}$")
SHA256 = re.compile(r"^[0-9a-f]{64}$")

_RECOVERY_PREFIX = ".agents/skills/sub2api-production-deploy/scripts/maintenance/release/"
_SCRIPTS_PREFIX = ".agents/skills/sub2api-production-deploy/scripts/"
# Reviewed profile-only changes, not permission to skip later changes to these files.
_REVIEWED_COMPATIBILITY_TRANCHES = (
    ("16a4a030fe67f4de4a46bcadd02c9003fc954432", "700052e8d67bcb5c92e95ba02530176b2e3a4068"),
    ("71016a197ea2cd3fc44987d8db421afc3983c040", "d40e11c6073e1f541ffb087d295c027f24335076"),
    # Profile 261 wiring; recovery algorithms, formats and trust assets are unchanged.
    ("f670e8051529776fa681b628eb05ce073d73c17c", "a541a5c4bb6ad72e7198d2802fc6dfac22b305ee"),
    # Profile 262 net wiring from the proven production tree; exact blobs and modes only.
    ("bb47353679b65ba37dad6ce9fde06c01e8afed7e", "b710ec5b15b1baf0d01d43bed5aec1eb26836ab4"),
)
_REVIEWED_MIGRATION_GATE_TRANCHES = (
    ("71016a197ea2cd3fc44987d8db421afc3983c040", "7abbf4a504780f6f4ed73b54c9410f6f054f3380"),
)
_MIGRATION_GATE_REVIEW_PATHS = frozenset({
    _SCRIPTS_PREFIX + "release/gate.py",
    _SCRIPTS_PREFIX + "release/migration_planner.py",
    _SCRIPTS_PREFIX + "release/production.py",
    _SCRIPTS_PREFIX + "release/vm-validate.sh",
    _SCRIPTS_PREFIX + "maintenance/release/migration-285-assert.sh",
})
_REVIEWED_GATE_POLICY_TRANCHES = (
    ("700052e8d67bcb5c92e95ba02530176b2e3a4068", "efcd995d52fba41071412d15d198d9fa76afe967"),
    # Re-review the net identity fix from its original blob, not a transitive exemption.
    ("700052e8d67bcb5c92e95ba02530176b2e3a4068", "5d1a13af1e81d3f73a485bcdd6ea659188942018"),
)
# Independently reviewed net changes from production profile 262. The explicit
# path sets prevent another changed file in either tree from acquiring a review.
_PROFILE263_COMPATIBILITY_PATHS = frozenset({
    _SCRIPTS_PREFIX + suffix for suffix in (
        "maintenance/181/mask-backup-units.sh", "maintenance/181/restore-backup-units.sh",
        "maintenance/release/context.sh", "maintenance/release/prepare.sh",
        "maintenance/release/promote-backup.sh", "release/bootstrap_backup_dr_assets.sh",
        "release/bootstrap_vm_signer.sh", "release/gate.py", "release/profiles.py",
        "release/production-recovery-retention-clean.sh", "release/production-space-clean.sh",
        "release/production_cleanup.py", "release/production_recovery_retention.py",
        "release/promote-dr-baseline.sh", "release/sign-dr-evidence.sh", "release/sign-gate.sh",
        "release/vm-only-validate.sh", "release/vm-validate.sh",
    )
} | {
    _SCRIPTS_PREFIX + f"maintenance/release/migration-{number}-assert.sh"
    for number in (195, *range(232, 246), 254, 285)
})
_VM_LIFECYCLE_REVIEW_PATHS = frozenset({
    _SCRIPTS_PREFIX + "release/" + name for name in ("cli.py", "supervisor.py", "vm_lifecycle.py")
})
# Profile 264 changes exactly the same 35 compatibility entry points as 263.
# This frozen path set grants no directory, ancestry, or future-content exemption.
_PROFILE264_COMPATIBILITY_PATHS = frozenset(_PROFILE263_COMPATIBILITY_PATHS)
# Independently reviewed 265 wiring from the verified production 264 tree.
# Only these frozen paths may match the exact before/after blobs and modes.
_PROFILE265_COMPATIBILITY_PATHS = frozenset(_PROFILE264_COMPATIBILITY_PATHS)
_PROFILE265_POSTFLIGHT_PATHS = frozenset({
    _SCRIPTS_PREFIX + "maintenance/release/" + name
    for name in ("activity-reward-cost-postflight.sh", "switch.sh")
})
_REWARD_COST_POSTFLIGHT_REVIEW_PATHS = frozenset({
    _SCRIPTS_PREFIX + path for path in (
        "maintenance/release/activity-reward-cost-postflight.sh",
        "maintenance/release/prepare.sh", "maintenance/release/switch.sh",
        "release/production.py", "release/vm-validate.sh",
    )
})
_VM_PREFLIGHT_RECOVERY_REVIEW_PATHS = frozenset({
    _SCRIPTS_PREFIX + "release/" + name
    for name in ("cli.py", "supervisor.py", "vm_validate.py", "vm-space-clean.sh")
})
_PROFILE265_BUILD_RECOVERY_REVIEW_PATHS = frozenset({
    _SCRIPTS_PREFIX + "release/" + name
    for name in ("cli.py", "supervisor.py", "vm-validate.sh")
})
_PROFILE265_REGISTRY_PROXY_REVIEW_PATHS = frozenset({
    _SCRIPTS_PREFIX + "release/vm-validate.sh",
})
# User-authorized review of the net profile 266-268 release assets. These
# frozen groups bind the proven production tree to the reviewed target blobs;
# additional paths, future content, mode changes and baseline drift still fail.
_PROFILE268_COMPATIBILITY_PATHS = frozenset({
    _SCRIPTS_PREFIX + suffix for suffix in (
        "maintenance/181/mask-backup-units.sh", "maintenance/181/restore-backup-units.sh",
        "maintenance/release/context.sh", "maintenance/release/prepare.sh",
        "maintenance/release/promote-backup.sh", "release/bootstrap_backup_dr_assets.sh",
        "release/bootstrap_vm_signer.sh", "release/gate.py", "release/profiles.py",
        "release/production-recovery-retention-clean.sh", "release/production-space-clean.sh",
        "release/production_cleanup.py", "release/production_recovery_retention.py",
        "release/promote-dr-baseline.sh", "release/sign-dr-evidence.sh", "release/sign-gate.sh",
        "release/vm-only-validate.sh", "release/vm-validate.sh",
    )
} | {
    _SCRIPTS_PREFIX + f"maintenance/release/migration-{number}-assert.sh"
    for number in (195, *range(232, 246), 254, 285)
})
_PROFILE268_POSTFLIGHT_PATHS = frozenset({
    _SCRIPTS_PREFIX + "maintenance/release/" + name
    for name in ("activity-reward-cost-postflight.sh", "switch.sh")
})
# Consumed-release finalization is reviewed independently of profile wiring.
# It neither restores application data nor changes Gate signatures or trust.
_PROFILE268_FINALIZATION_REVIEW_PATHS = frozenset({
    _SCRIPTS_PREFIX + "release/" + name
    for name in ("cli.py", "finalization.py", "supervisor.py", "vm_lifecycle.py")
})
# Exact production-to-target review for profile 269. Keep these disjoint from
# the prior tranche: doctor replaces supervisor in the finalization net diff.
_PROFILE269_COMPATIBILITY_PATHS = frozenset(_PROFILE268_COMPATIBILITY_PATHS)
_PROFILE269_POSTFLIGHT_PATHS = frozenset(_PROFILE268_POSTFLIGHT_PATHS)
_PROFILE269_FINALIZATION_REVIEW_PATHS = frozenset({
    _SCRIPTS_PREFIX + "release/" + name
    for name in ("cli.py", "doctor.py", "finalization.py", "vm_lifecycle.py")
})
# Only initialize the optional pre-Gate input handles before schema dispatch.
# The reviewed implementation does not change restore, cleanup or trust logic.
_FAST_DEPLOY_INITIALIZATION_REVIEW_PATHS = frozenset({_SCRIPTS_PREFIX + "release/cli.py"})
_VM_PLAN_SOURCES_REVIEW_PATHS = frozenset({
    _SCRIPTS_PREFIX + "release/" + name
    for name in ("cli.py", "vm-validate.sh", "vm_failed_preserve.py")
})
# Independently reviewed profile 274 net changes from verified production 273.
# Freeze explicit paths as well as before/after blobs and modes; no future or
# directory-wide exemption. Admission migration rollback remains coordinated.
_PROFILE274_COMPATIBILITY_PATHS = frozenset({
    _SCRIPTS_PREFIX + suffix for suffix in (
        "maintenance/181/mask-backup-units.sh", "maintenance/181/restore-backup-units.sh",
        "maintenance/release/context.sh", "maintenance/release/full-recovery-capture.sh",
        "maintenance/release/migration-195-assert.sh",
        "maintenance/release/migration-232-assert.sh", "maintenance/release/migration-233-assert.sh",
        "maintenance/release/migration-234-assert.sh", "maintenance/release/migration-235-assert.sh",
        "maintenance/release/migration-236-assert.sh", "maintenance/release/migration-237-assert.sh",
        "maintenance/release/migration-238-assert.sh", "maintenance/release/migration-239-assert.sh",
        "maintenance/release/migration-240-assert.sh", "maintenance/release/migration-241-assert.sh",
        "maintenance/release/migration-242-assert.sh", "maintenance/release/migration-243-assert.sh",
        "maintenance/release/migration-244-assert.sh", "maintenance/release/migration-245-assert.sh",
        "maintenance/release/migration-254-assert.sh", "maintenance/release/migration-285-assert.sh",
        "maintenance/release/migration-297-assert.sh", "maintenance/release/migration-298-assert.sh",
        "maintenance/release/migration-299-assert.sh", "maintenance/release/prepare.sh",
        "maintenance/release/promote-backup.sh", "release/baseline_candidate_load.py",
        "release/bootstrap_backup_dr_assets.sh", "release/bootstrap_vm_signer.sh",
        "release/postrelease_baseline.py", "release/production-recovery-retention-clean.sh",
        "release/production-space-clean.sh", "release/production_cleanup.py",
        "release/production_recovery_retention.py", "release/profiles.py",
        "release/promote-dr-baseline.sh", "release/recovery_drill.py",
        "release/sign-dr-evidence.sh", "release/sign-gate.sh",
        "release/stage-dr-baseline-candidate.sh", "release/vm-only-validate.sh",
    )
})
_PROFILE274_MIGRATION_GATE_PATHS = frozenset({
    _SCRIPTS_PREFIX + suffix for suffix in (
        "maintenance/release/migration-301-assert.sh", "release/gate.py",
        "release/manifest.py", "release/migration_planner.py", "release/vm-validate.sh",
    )
})
_PROFILE274_POSTFLIGHT_PATHS = frozenset({
    _SCRIPTS_PREFIX + "maintenance/release/" + name
    for name in ("activity-reward-cost-postflight.sh", "switch.sh")
})
# Independently reviewed process liveness and build-preflight control changes.
# Match the exact production/implementation blobs and modes, never ancestry.
_PROCESS_BUILD_PREFLIGHT_REVIEW_PATHS = frozenset({
    _SCRIPTS_PREFIX + "release/" + name for name in (
        "cli.py", "finalization.py", "observer.py", "plugin_supervisor.py",
        "process.py", "supervisor.py", "vm-build-common.sh", "vm-only-validate.sh",
        "vm-validate.sh", "vm_build.py", "vm_validate.py",
    )
})
# Independent review of the materialized BuildKit dependency proof and its
# callers. Historical process/preflight objects above remain frozen.
_BUILDKIT_DEPENDENCY_REVIEW_PATHS = frozenset({
    _SCRIPTS_PREFIX + "release/" + name for name in (
        "cli.py", "gate.py", "manifest.py", "supervisor.py",
        "vm-build-common.sh", "vm-only-validate.sh", "vm-validate.sh",
        "vm_build.py", "vm_only.py", "vm_validate.py",
    )
})
# Independent review of Windows local state snapshots, safe publication and
# terminal confirmation. Production data restore and trust formats are unchanged.
_WINDOWS_STATE_IO_REVIEW_PATHS = frozenset({
    _SCRIPTS_PREFIX + "release/" + name for name in (
        "atomic.py", "cli.py", "finalization.py", "manifest.py", "plugin_state.py",
        "plugin_supervisor.py", "production_cleanup.py", "state.py", "supervisor.py",
        "vm_failed_preserve.py", "vm_lifecycle.py", "vm_validate.py",
        "worker_errors.py", "terminal_receipt.py",
    )
})
# Exact source-selection review only; dependency materialization, recovery
# logic, proxy scope and all historical reviewed objects remain unchanged.
_OFFICIAL_REGISTRY_SOURCE_REVIEW_PATHS = frozenset({
    _SCRIPTS_PREFIX + "release/" + name for name in (
        "vm-build-common.sh", "vm_build.py",
    )
})
# Independent review of raw root-index resolution; the prior official source
# selection and every historical object/path group remain frozen.
_RAW_INDEX_RESOLUTION_REVIEW_PATHS = frozenset({
    _SCRIPTS_PREFIX + "release/vm-build-common.sh",
})
# Isolated probe DDL ownership, durable failure evidence and the one frozen
# failed-fixture preserve path. No production restore or trust format change.
_VM_FAST_FIXTURE_PRESERVE_REVIEW_PATHS = frozenset({
    _SCRIPTS_PREFIX + "release/" + name for name in (
        "cli.py", "vm-validate.sh", "vm_fast_fixture_preserve.py",
    )
})
# Probe-only connection authority, owned container cleanup and the separate
# frozen restore-probe preserve path. Historical tranches remain immutable.
_VM_PROBE_CONFIG_PRESERVE_REVIEW_PATHS = frozenset({
    _SCRIPTS_PREFIX + "release/" + name for name in (
        "cli.py", "vm-validate.sh", "vm-probe-config.py", "vm_restore_probe_preserve.py",
    )
})
_REVIEWED_SCOPED_TRANCHES = (
    (
        "aeb410bccc524164bdafbd3db865d4f5f588aced",
        "6f5daaaed6e0cbfae2011cfa55b47688f33634eb",
        "reviewed_profile_compatibility_changed",
        _PROFILE265_COMPATIBILITY_PATHS,
    ),
    (
        "aeb410bccc524164bdafbd3db865d4f5f588aced",
        "6f5daaaed6e0cbfae2011cfa55b47688f33634eb",
        "reviewed_reward_cost_postflight_changed",
        _PROFILE265_POSTFLIGHT_PATHS,
    ),
    (
        "aeb410bccc524164bdafbd3db865d4f5f588aced",
        "99d9fcf97125ef6efc6543692697e85e27412396",
        "reviewed_vm_preflight_recovery_changed",
        _PROFILE265_BUILD_RECOVERY_REVIEW_PATHS,
    ),
    (
        "aeb410bccc524164bdafbd3db865d4f5f588aced",
        "67545c6d6e02f5210574ced9ea5c366688d28b87",
        "reviewed_vm_preflight_recovery_changed",
        _PROFILE265_REGISTRY_PROXY_REVIEW_PATHS,
    ),
    (
        "0898fe8c6289f87a1a4d90cf74a50e296689960e",
        "dc464325a572c275c178632aeb847437169b3c58",
        "reviewed_vm_preflight_recovery_changed",
        _VM_PREFLIGHT_RECOVERY_REVIEW_PATHS,
    ),
    (
        "0898fe8c6289f87a1a4d90cf74a50e296689960e",
        "bc57b804fe4467d96b530c8874dd7152d28386a0",
        "reviewed_reward_cost_postflight_changed",
        _REWARD_COST_POSTFLIGHT_REVIEW_PATHS,
    ),
    (
        "0898fe8c6289f87a1a4d90cf74a50e296689960e",
        "7c58225c34f876e1fe3967f1d8cf941d7d3d5f49",
        "reviewed_profile_compatibility_changed",
        _PROFILE264_COMPATIBILITY_PATHS,
    ),
    (
        "218772daab271bb32df2ac6f9969ace0c9ccbce0",
        "8499d5343c5a4510f88a3b13feb58f242d4cdd13",
        "reviewed_profile_compatibility_changed",
        _PROFILE263_COMPATIBILITY_PATHS,
    ),
    (
        "218772daab271bb32df2ac6f9969ace0c9ccbce0",
        "8499d5343c5a4510f88a3b13feb58f242d4cdd13",
        "reviewed_vm_lifecycle_changed",
        _VM_LIFECYCLE_REVIEW_PATHS,
    ),
    (
        "9bf9801a40c4e6732fa81fedbf6ad52f2fa06c62",
        "bd00d35ebc0b4182070510375ee6878101cfbc7a",
        "reviewed_profile_compatibility_changed",
        _PROFILE268_COMPATIBILITY_PATHS,
    ),
    (
        "9bf9801a40c4e6732fa81fedbf6ad52f2fa06c62",
        "bd00d35ebc0b4182070510375ee6878101cfbc7a",
        "reviewed_reward_cost_postflight_changed",
        _PROFILE268_POSTFLIGHT_PATHS,
    ),
    (
        "9bf9801a40c4e6732fa81fedbf6ad52f2fa06c62",
        "bd00d35ebc0b4182070510375ee6878101cfbc7a",
        "reviewed_consumed_finalization_changed",
        _PROFILE268_FINALIZATION_REVIEW_PATHS,
    ),
    (
        "c4d857235cecdc3458c21b5eec9411f1fd115674",
        "f14ca8f65e3526370bf24a2fa0bca3ec490448ab",
        "reviewed_profile_compatibility_changed",
        _PROFILE269_COMPATIBILITY_PATHS,
    ),
    (
        "c4d857235cecdc3458c21b5eec9411f1fd115674",
        "f14ca8f65e3526370bf24a2fa0bca3ec490448ab",
        "reviewed_reward_cost_postflight_changed",
        _PROFILE269_POSTFLIGHT_PATHS,
    ),
    (
        "c4d857235cecdc3458c21b5eec9411f1fd115674",
        "f14ca8f65e3526370bf24a2fa0bca3ec490448ab",
        "reviewed_consumed_finalization_changed",
        _PROFILE269_FINALIZATION_REVIEW_PATHS,
    ),
    (
        "8544cef6f5e87f29929465316467b2765350cd52",
        "a0f7ca7666ddd2e6485536be7a8ef6696344c0ac",
        "reviewed_vm_preflight_recovery_changed",
        _FAST_DEPLOY_INITIALIZATION_REVIEW_PATHS,
    ),
    (
        "7d4ecf44f99e17dc0fa7b83585862c357695ceff",
        "8cbb42d9f358875854a6b0d87a44cdb8b84b6ea4",
        "reviewed_vm_preflight_recovery_changed",
        _VM_PLAN_SOURCES_REVIEW_PATHS,
    ),
    (
        "5deebb2929c2a694145cc2726e895f01c43d28c1",
        "aca670e896ffa0e0e54204f1e6c33c205d5160f5",
        "reviewed_profile_compatibility_changed",
        _PROFILE274_COMPATIBILITY_PATHS,
    ),
    (
        "5deebb2929c2a694145cc2726e895f01c43d28c1",
        "aca670e896ffa0e0e54204f1e6c33c205d5160f5",
        "reviewed_profile274_migration_gate_changed",
        _PROFILE274_MIGRATION_GATE_PATHS,
    ),
    (
        "5deebb2929c2a694145cc2726e895f01c43d28c1",
        "aca670e896ffa0e0e54204f1e6c33c205d5160f5",
        "reviewed_reward_cost_postflight_changed",
        _PROFILE274_POSTFLIGHT_PATHS,
    ),
    (
        "5deebb2929c2a694145cc2726e895f01c43d28c1",
        "1a6271af98027b67e63dfb24915aaf11ab395974",
        "reviewed_process_build_preflight_changed",
        _PROCESS_BUILD_PREFLIGHT_REVIEW_PATHS,
    ),
    (
        "5deebb2929c2a694145cc2726e895f01c43d28c1",
        "89e105dc1618e05018a26a8053f5a8b710db8e98",
        "reviewed_buildkit_dependency_proof_changed",
        _BUILDKIT_DEPENDENCY_REVIEW_PATHS,
    ),
    (
        "5deebb2929c2a694145cc2726e895f01c43d28c1",
        "48d2f9660f51b9037f6875fffcb6e51bb280ce29",
        "reviewed_windows_state_io_changed",
        _WINDOWS_STATE_IO_REVIEW_PATHS,
    ),
    (
        "5deebb2929c2a694145cc2726e895f01c43d28c1",
        "3dcb4c0de414bfc2165b55722e22599287a20b80",
        "reviewed_official_registry_source_changed",
        _OFFICIAL_REGISTRY_SOURCE_REVIEW_PATHS,
    ),
    (
        "5deebb2929c2a694145cc2726e895f01c43d28c1",
        "32aeb69419151beb04610e7ae4452b955c17bf3e",
        "reviewed_raw_index_resolution_changed",
        _RAW_INDEX_RESOLUTION_REVIEW_PATHS,
    ),
    (
        "c88f06e57a829438595263d524e02219aa2f5a2e",
        "85d7274703ad07570403e501ecb9b66c8f0218ea",
        "reviewed_vm_fixture_ownership_preserve_changed",
        _VM_FAST_FIXTURE_PRESERVE_REVIEW_PATHS,
    ),
    (
        "c88f06e57a829438595263d524e02219aa2f5a2e",
        "91a13c6c64558f5d78ae8b75979af0e6a24b60b2",
        "reviewed_vm_probe_config_preserve_changed",
        _VM_PROBE_CONFIG_PRESERVE_REVIEW_PATHS,
    ),
)
_RELEASE_STATE_MACHINE_FILES = frozenset(
    {
        ".agents/skills/sub2api-production-deploy/scripts/release/cli.py",
        ".agents/skills/sub2api-production-deploy/scripts/release/doctor.py",
        ".agents/skills/sub2api-production-deploy/scripts/release/finalization.py",
        ".agents/skills/sub2api-production-deploy/scripts/release/gate.py",
        ".agents/skills/sub2api-production-deploy/scripts/release/manifest.py",
        ".agents/skills/sub2api-production-deploy/scripts/release/production.py",
        ".agents/skills/sub2api-production-deploy/scripts/release/production_snapshot.py",
        ".agents/skills/sub2api-production-deploy/scripts/release/recovery_gate.py",
        ".agents/skills/sub2api-production-deploy/scripts/release/recovery_bundle.py",
        ".agents/skills/sub2api-production-deploy/scripts/release/recovery_drill.py",
        ".agents/skills/sub2api-production-deploy/scripts/release/recovery_evidence.py",
        ".agents/skills/sub2api-production-deploy/scripts/release/state.py",
        ".agents/skills/sub2api-production-deploy/scripts/release/supervisor.py",
        ".agents/skills/sub2api-production-deploy/scripts/release/vm_validate.py",
        ".agents/skills/sub2api-production-deploy/scripts/release/vm_lifecycle.py",
        ".agents/skills/sub2api-production-deploy/scripts/release/vm_preflight_preserve.py",
        ".agents/skills/sub2api-production-deploy/scripts/release/vm_failed_preserve.py",
        ".agents/skills/sub2api-production-deploy/scripts/release/vm_fast_fixture_preserve.py",
        ".agents/skills/sub2api-production-deploy/scripts/release/vm_restore_probe_preserve.py",
    }
)
_SPECIALIZED_PREFIXES = ("backend/migrations/",)
_SPECIALIZED_FILES = {
    "backend/internal/repository/billing_inflight_cache.go": "redis_changed",
}
_RECOVERY_RUNTIME_FILES = frozenset(
    {
        "backup-retention-clean.sh", "backup-release-retention-clean.sh",
        "bootstrap_backup_dr_assets.sh", "bootstrap_vm_signer.sh",
        "production-recovery-retention-clean.sh", "production-space-clean.sh",
        "promote-dr-baseline.sh", "sign-dr-evidence.sh", "sign-gate.sh",
        "vm-only-validate.sh", "vm-validate.sh", "vm-probe-config.py", "vm-space-clean.sh", "vm-build-common.sh", "bootstrap.py", "production_bootstrap.py",
        "production_cleanup.py", "production_recovery_retention.py", "profiles.py",
        "paths.py", "atomic.py", "migration_planner.py",
        # These collectors, signing domains and baseline publishers can alter
        # which recovery evidence is accepted or promoted. A future change to
        # one must obtain a fresh full review, even without a shell edit.
        "backup_keys.py", "baseline.py", "postrelease_baseline.py", "baseline_candidate_load.py",
        "full_recovery_authorization.py", "full_recovery_drill_authorization.py",
        "full_recovery_drill.py", "full_recovery_guest.py", "full_recovery_source.py",
        "sign-full-recovery-review.sh", "stage-dr-baseline-candidate.sh",
        "recovery-compose-drill.sh",
    }
)
# Exact execution and evidence roles. Asset materialization alone is not a
# reason for escalation; these modules can start work, select its transport,
# change its ownership, or change the evidence retained for verification.
_RECOVERY_CONTROL_FILES = frozenset({
    _SCRIPTS_PREFIX + suffix for suffix in (
        "release.py", "release/__init__.py", "release/ssh.py", "release/process.py",
        "release/vm_only.py", "release/vm-only-switch.sh", "release/vm_build.py",
        "release/plugin_api.py", "release/plugin_cli.py", "release/plugin_credentials.py",
        "release/plugin_package.py", "release/plugin_state.py", "release/plugin_supervisor.py",
        "release/observer.py", "release/plugin_observer.py",
        "release/worker_errors.py", "release/terminal_receipt.py",
        "release/backfill-activity-reward-costs.sh", "release/backup-host-space-clean.sh",
        "logging/release_logging/events.py", "logging/release_logging/redact.py",
        "logging/release_logging/retention.py",
    )
})


def _is_recovery_sensitive_path(path: str) -> bool:
    return (
        path.startswith(_RECOVERY_PREFIX)
        or (path.startswith(_SCRIPTS_PREFIX + "maintenance/") and path.endswith(".sh"))
        or path.startswith((_SCRIPTS_PREFIX + "release/trust/", _SCRIPTS_PREFIX + "release/drverify/"))
        or path in {_SCRIPTS_PREFIX + "release/" + name for name in _RECOVERY_RUNTIME_FILES}
        or path in _RELEASE_STATE_MACHINE_FILES - {_SCRIPTS_PREFIX + "release/recovery_gate.py"}
        or path in _RECOVERY_CONTROL_FILES
    )


def _tree_blobs(workspace: Path, commit: str) -> dict[str, str]:
    output = check_output_hidden(["git", "ls-tree", "-r", "-t", "-z", commit, "--", _SCRIPTS_PREFIX], cwd=workspace)
    assert isinstance(output, bytes)
    blobs = {}
    for record in output.split(b"\0"):
        if not record:
            continue
        metadata, path = record.split(b"\t", 1)
        mode, _kind, identity = metadata.decode("ascii").split()
        # Retain directories and gitlinks: an existing non-blob object is not
        # an absent path, including when reviewing newly introduced files.
        blobs[path.decode("utf-8")] = mode + ":" + identity
    return blobs


def _reviewed_compatibility_paths(workspace: Path, base_commit: str, target_commit: str) -> dict[str, str]:
    base = _tree_blobs(workspace, base_commit)
    target = _tree_blobs(workspace, target_commit)
    reviewed: dict[str, str] = {}
    reviews = [(before, after, "reviewed_profile_compatibility_changed", None) for before, after in _REVIEWED_COMPATIBILITY_TRANCHES]
    reviews += [(before, after, "reviewed_gate_policy_changed", None) for before, after in _REVIEWED_GATE_POLICY_TRANCHES]
    reviews += [(before, after, "reviewed_migration_gate_changed", None) for before, after in _REVIEWED_MIGRATION_GATE_TRANCHES]
    reviews += list(_REVIEWED_SCOPED_TRANCHES)
    for before_commit, after_commit, reason, allowed_paths in reviews:
        if not _commit_exists(workspace, before_commit) or not _commit_exists(workspace, after_commit):
            continue
        before = _tree_blobs(workspace, before_commit)
        after = _tree_blobs(workspace, after_commit)
        for path in before.keys() | after.keys():
            if allowed_paths is not None and path not in allowed_paths:
                continue
            if reason == "reviewed_migration_gate_changed" and path not in _MIGRATION_GATE_REVIEW_PATHS:
                continue
            if reason == "reviewed_gate_policy_changed" and path not in {
                _SCRIPTS_PREFIX + "release/cli.py", _SCRIPTS_PREFIX + "release/production_snapshot.py",
                _SCRIPTS_PREFIX + "release/doctor.py",
                _SCRIPTS_PREFIX + "release/production.py",
                _SCRIPTS_PREFIX + "maintenance/release/preflight.sh",
                _SCRIPTS_PREFIX + "maintenance/release/runtime-identity.sh",
            }:
                continue
            if before.get(path) != after.get(path) and base.get(path) == before.get(path) and target.get(path) == after.get(path):
                reviewed[path] = reason
    return reviewed


def _normalize_paths(paths: Iterable[str]) -> list[str]:
    normalized: set[str] = set()
    for path in paths:
        if not path:
            continue
        value = path.replace("\\", "/")
        if value.startswith("./"):
            value = value[2:]
        normalized.add(value)
    return sorted(normalized)


def changed_paths_sha256(paths: Iterable[str]) -> str:
    normalized = _normalize_paths(paths)
    payload = json.dumps(normalized, ensure_ascii=True, separators=(",", ":")).encode("utf-8")
    return hashlib.sha256(payload).hexdigest()


def _is_specialized_path(path: str) -> tuple[bool, str | None]:
    reason = _SPECIALIZED_FILES.get(path)
    if reason:
        return True, reason
    lowered = path.lower()
    if path.startswith(_SPECIALIZED_PREFIXES):
        return True, "migration_changed"
    name = Path(lowered).name
    if "docker-compose" in name or name.startswith("compose.") or name in {"compose.yml", "compose.yaml"}:
        return True, "compose_changed"
    if "redis" in lowered:
        return True, "redis_changed"
    if "postgres" in lowered or "postgresql" in lowered:
        return True, "postgres_changed"
    return False, None


def _changed_paths(workspace: Path, base_commit: str, target_commit: str) -> list[str]:
    output = check_output_hidden(
        ["git", "diff", "--no-renames", "--name-only", "-z", base_commit, target_commit, "--"],
        cwd=workspace,
    )
    assert isinstance(output, bytes)
    return _normalize_paths(item.decode("utf-8") for item in output.split(b"\0") if item)


def _is_ancestor(workspace: Path, base_commit: str, target_commit: str) -> bool:
    result = run_hidden(
        ["git", "merge-base", "--is-ancestor", base_commit, target_commit],
        cwd=workspace,
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
        check=False,
    )
    return result.returncode == 0


def _commit_exists(workspace: Path, commit: str) -> bool:
    result = run_hidden(
        ["git", "cat-file", "-e", f"{commit}^{{commit}}"],
        cwd=workspace,
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
        check=False,
    )
    return result.returncode == 0


def _unproven_report(target_commit: str, base_commit: str | None = None) -> dict[str, Any]:
    return validate_report(
        {
            "schema": REPORT_SCHEMA,
            "mode": "specialized",
            "base_commit": base_commit,
            "target_commit": target_commit,
            "reason_codes": ["production_commit_unproven"],
            "changed_paths_sha256": changed_paths_sha256([]),
            "estimated_extra_seconds": ESTIMATED_EXTRA_SECONDS["specialized"],
        },
        target_commit=target_commit,
    )


def validate_report(report: dict[str, Any], *, target_commit: str | None = None) -> dict[str, Any]:
    expected_fields = {
        "schema",
        "mode",
        "base_commit",
        "target_commit",
        "reason_codes",
        "changed_paths_sha256",
        "estimated_extra_seconds",
    }
    if isinstance(report, dict) and report.get("schema") == 2:
        expected_fields |= {"full_recovery_evidence", "evidence_sha256"}
    if not isinstance(report, dict) or set(report) != expected_fields:
        raise RuntimeError("recovery Gate report shape is invalid")
    if type(report.get("schema")) is not int or report.get("schema") not in {REPORT_SCHEMA, 2} or report.get("mode") not in MODES:
        raise RuntimeError("recovery Gate report identity is invalid")
    base_commit = report.get("base_commit")
    if base_commit is not None and not FULL_SHA.fullmatch(str(base_commit)):
        raise RuntimeError("recovery Gate base commit is invalid")
    report_target = str(report.get("target_commit", ""))
    if not FULL_SHA.fullmatch(report_target) or (target_commit is not None and report_target != target_commit):
        raise RuntimeError("recovery Gate target commit is invalid")
    reasons = report.get("reason_codes")
    if (
        not isinstance(reasons, list)
        or any(not isinstance(reason, str) or not reason for reason in reasons)
        or reasons != sorted(set(reasons))
    ):
        raise RuntimeError("recovery Gate reason codes are invalid")
    if not SHA256.fullmatch(str(report.get("changed_paths_sha256", ""))):
        raise RuntimeError("recovery Gate changed-path checksum is invalid")
    if report.get("estimated_extra_seconds") != ESTIMATED_EXTRA_SECONDS[report["mode"]]:
        raise RuntimeError("recovery Gate time estimate is invalid")
    if report["schema"] == 2:
        if report["mode"] != "full" or "recovery_change_requires_review" not in reasons:
            raise RuntimeError("reviewed full recovery report classification is invalid")
        if not isinstance(report["full_recovery_evidence"], dict) or not SHA256.fullmatch(str(report["evidence_sha256"])):
            raise RuntimeError("reviewed full recovery report evidence is invalid")
    return dict(report)


def classify(workspace: Path, base_commit: str | None, target_commit: str) -> dict[str, Any]:
    if not FULL_SHA.fullmatch(target_commit):
        raise ValueError("target commit must be a complete 40-character lowercase SHA")
    if base_commit is None or not FULL_SHA.fullmatch(base_commit):
        return _unproven_report(target_commit)
    if not _commit_exists(workspace, target_commit):
        raise ValueError("target commit is not available in the local repository")
    if not _commit_exists(workspace, base_commit):
        return _unproven_report(target_commit, base_commit)

    paths = _changed_paths(workspace, base_commit, target_commit)
    sensitive_paths = {path for path in paths if _is_recovery_sensitive_path(path)}
    reviewed = _reviewed_compatibility_paths(workspace, base_commit, target_commit) if sensitive_paths else {}
    reasons: set[str] = set()
    mode = "fast"
    if not _is_ancestor(workspace, base_commit, target_commit):
        mode = "specialized"
        reasons.add("base_commit_not_ancestor")
    for path in paths:
        if path in sensitive_paths:
            reasons.add("release_state_machine_changed")
            if path in reviewed:
                if mode != "full":
                    mode = "specialized"
                reasons.add(reviewed[path])
            else:
                mode = "full"
                reasons.add("recovery_change_requires_review")
            continue
        if path.startswith(_RECOVERY_PREFIX) or path in _RELEASE_STATE_MACHINE_FILES:
            if mode != "full":
                mode = "specialized"
            reasons.add("release_state_machine_changed")
            if path.startswith(_RECOVERY_PREFIX):
                reasons.add("recovery_logic_changed")
            continue
        specialized, reason = _is_specialized_path(path)
        if specialized and mode != "full":
            mode = "specialized"
        if reason:
            reasons.add(reason)
    if not reasons:
        reasons.add("ordinary_change")
    return validate_report(
        {
            "schema": REPORT_SCHEMA,
            "mode": mode,
            "base_commit": base_commit,
            "target_commit": target_commit,
            "reason_codes": sorted(reasons),
            "changed_paths_sha256": changed_paths_sha256(paths),
            "estimated_extra_seconds": ESTIMATED_EXTRA_SECONDS[mode],
        },
        target_commit=target_commit,
    )


def require_specialized(report: dict[str, Any], reason: str) -> dict[str, Any]:
    value = validate_report(report, target_commit=str(report.get("target_commit", "")))
    if not reason:
        raise ValueError("recovery Gate escalation reason is required")
    if value["mode"] == "fast":
        value["mode"] = "specialized"
        value["estimated_extra_seconds"] = ESTIMATED_EXTRA_SECONDS["specialized"]
    value["reason_codes"] = sorted(set(value["reason_codes"]) | {reason})
    return validate_report(value, target_commit=value["target_commit"])


def assert_release_allowed(report: dict[str, Any] | None, *, allow_historical_evidence: bool = False) -> None:
    reasons = set((report or {}).get("reason_codes", []))
    if "production_commit_unproven" in reasons:
        raise RuntimeError("production commit is unproven; recovery classification requires an immutable release identity")
    if "recovery_change_requires_review" in reasons:
        if (report or {}).get("schema") == 2:
            validate_report(report)
            from .paths import WORKSPACE
            from .recovery_evidence import verify_reviewed_report
            verify_reviewed_report(report, workspace=WORKSPACE, allow_expired=allow_historical_evidence)
            return
        raise RuntimeError(
            "recovery changes require review and an independent full DR drill before production; "
            "--recovery-gate-mode full only verifies the VM restore portion"
        )


def require_full(report: dict[str, Any], reason: str = "manual_full_drill") -> dict[str, Any]:
    value = validate_report(report, target_commit=str(report.get("target_commit", "")))
    if not reason:
        raise ValueError("full recovery Gate reason is required")
    value["mode"] = "full"
    value["estimated_extra_seconds"] = ESTIMATED_EXTRA_SECONDS["full"]
    value["reason_codes"] = sorted(set(value["reason_codes"]) | {reason})
    return validate_report(value, target_commit=value["target_commit"])


def unproven_report(target_commit: str) -> dict[str, Any]:
    return classify(Path.cwd(), None, target_commit)
