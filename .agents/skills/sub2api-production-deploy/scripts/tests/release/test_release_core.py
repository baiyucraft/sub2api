from __future__ import annotations

import hashlib
import json
import os
import subprocess
import sys
import tempfile
import time
import unittest
from pathlib import Path
from unittest import mock


DEPLOY_ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(DEPLOY_ROOT))

from release.atomic import atomic_write, canonical_json
from release.gate import verify_gate
from release.manifest import (
    git_blob_sha256,
    is_release_asset_relative_path,
    manifest_release_asset_layout,
    migration_checksums,
    release_asset_checksums,
    release_asset_relative_paths_from_commit,
    release_unit_relative_paths,
)
from release.paths import LAYOUT_DEPLOY_V1, LAYOUT_SKILL_V1, WORKSPACE
from release.profiles import get_profile
from release.state import RunLock, RunState


class ReleaseCoreTest(unittest.TestCase):
    @staticmethod
    def release_assets(layout: str = LAYOUT_SKILL_V1) -> dict[str, str]:
        units = release_unit_relative_paths(layout)
        return {
            "asset": "digest",
            units["validator"]: "validator",
            units["gate_signer"]: "gate-signer",
            units["dr_signer"]: "dr-signer",
            units["space_cleaner"]: "space-cleaner",
        }

    @staticmethod
    def release_unit_checksum(path: Path) -> str:
        return {
            "vm-validate.sh": "validator",
            "sign-gate.sh": "gate-signer",
            "sign-dr-evidence.sh": "dr-signer",
        }[path.name]

    def manifest(self, runner: str, expires_at: int, profile_name: str = "182") -> dict:
        profile = get_profile(profile_name)
        commit = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=WORKSPACE, text=True).strip()
        return {
            "schema": 1,
            "release_asset_layout": LAYOUT_SKILL_V1,
            "deployment_mode": "blue-green",
            "release_id": f"{profile_name}-{commit[:12]}-1-aaaaaaaa",
            "commit_sha": commit,
            "profile": profile_name,
            "version": profile["version"],
            "migrations": list(profile["migrations"]),
            "migration_sha256": migration_checksums(profile, commit),
            "runner_sha256": runner,
            "vm_validator_sha256": "validator",
            "vm_gate_signer_sha256": "gate-signer",
            "vm_dr_signer_sha256": "dr-signer",
            "release_asset_sha256": self.release_assets(),
            "origin": profile["origin"],
            "vm_identity": profile["vm_identity"],
            "expires_at": expires_at,
        }

    @staticmethod
    def profile_232_evidence(profile: dict) -> dict:
        evidence = {
            "candidate_image_id": "sha256:" + "b" * 64,
            "candidate_archive_sha256": hashlib.sha256(b"candidate").hexdigest(),
            "integration_verified": True,
            "vm_restore_verified": True,
            "prompt_audit_disabled": True,
            "migration_195_verified": True,
            "fixture_rejected": True,
            "restore_completed": True,
            "clean_preflight": True,
            "verified_replay": True,
            "verified_low_watermark_rejected": True,
            "managed_monitor_key_names_verified": True,
            "reasoning_effort_policy_verified": True,
            "vm_old_image_compatibility_verified": True,
            "vm_old_image_id": profile["compatibility_image_id"],
            "alipay_mobile_precreate_migration_verified": True,
            "group_auth_cache_image_generation_verified": True,
            "composite_model_routes_verified": True,
            "session_id_columns_verified": True,
            "live_request_type_verified": True,
            "group_allow_live_verified": True,
            "email_alias_index_verified": True,
            "live_runtime_capability_verified": True,
            "passkey_schema_verified": True,
            "user_usage_aggregation_schema_verified": True,
            "migration_211_status": "verified",
            "migration_212_status": "verified",
            "group_profit_control_schema_verified": True,
            "group_profit_auth_cache_trigger_verified": True,
            "migration_214_status": "verified",
            "migration_215_status": "verified",
            "usage_log_upstream_model_columns_verified": True,
            "usage_log_upstream_model_mismatch_index_verified": True,
            "channel_monitor_v2_schema_verified": True,
            "channel_monitor_v2_defaults_verified": True,
            "group_media_pricing_schema_verified": True,
            "group_media_auth_cache_trigger_verified": True,
            "migration_232_data_plan_verified": True,
            "migration_232_postflight_verified": True,
        }
        for number in range(216, 233):
            evidence[f"migration_{number}_status"] = "verified"
        return evidence

    def test_atomic_write_replaces_complete_content(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "state.json"
            atomic_write(path, b"old\n")
            atomic_write(path, b"new\n")
            self.assertEqual(path.read_bytes(), b"new\n")

    @unittest.skipUnless(os.name == "nt", "Windows ACL regression")
    def test_atomic_write_read_only_mode_remains_readable_by_next_process_on_windows(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "manifest.json"
            atomic_write(path, b'{"schema":1}\n', 0o400)
            result = subprocess.run(
                [sys.executable, "-c", "import pathlib,sys; print(pathlib.Path(sys.argv[1]).read_text())", str(path)],
                check=False,
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
                text=True,
            )
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertIn('"schema":1', result.stdout)
            self.assertFalse(list(path.parent.glob(f".{path.name}.*")))

    def test_stale_lock_file_does_not_block_new_process(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "release.lock"
            path.write_text("pid=999999\n", encoding="utf-8")
            with RunLock(path):
                pass
            self.assertTrue(path.exists())
            self.assertIn(f"pid={os.getpid()}", path.read_text(encoding="utf-8"))

    def test_active_lock_rejects_second_release(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "release.lock"
            with RunLock(path):
                with self.assertRaisesRegex(RuntimeError, "another release process"):
                    with RunLock(path):
                        pass

    def test_terminal_state_cannot_resume_running(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            state = RunState.create(Path(directory) / "state.json", "release")
            state.transition("vm", "failed")
            with self.assertRaisesRegex(RuntimeError, "terminal"):
                state.transition("vm", "running")

    def test_migration_checksum_matches_runner_trimmed_content(self) -> None:
        profile = {"migrations": ["migration.sql"]}
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            migration = root / "backend" / "migrations" / "migration.sql"
            migration.parent.mkdir(parents=True)
            migration.write_text("\nSELECT 1;\n\n", encoding="utf-8")
            with mock.patch("release.manifest.workspace_root", return_value=root):
                checksums = migration_checksums(profile)
        self.assertEqual(checksums["migration.sql"], hashlib.sha256(b"SELECT 1;").hexdigest())

    def test_release_asset_checksum_uses_commit_blob_bytes(self) -> None:
        commit = "a" * 40
        relative_path = ".agents/skills/sub2api-production-deploy/scripts/release/cli.py"
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            asset = root / relative_path
            asset.parent.mkdir(parents=True)
            asset.write_bytes(b"line one\r\nline two\r\n")
            with (
                mock.patch("release.manifest.workspace_root", return_value=root),
                mock.patch("release.manifest.release_asset_relative_paths_from_commit", return_value=[relative_path]),
                mock.patch(
                    "release.manifest.subprocess.check_output",
                    return_value=b"line one\nline two\n",
                ) as check_output,
            ):
                checksums = release_asset_checksums(commit, LAYOUT_SKILL_V1)

        self.assertEqual(checksums[relative_path], hashlib.sha256(b"line one\nline two\n").hexdigest())
        check_output.assert_called_once_with(
            ["git", "show", f"{commit}:{relative_path}"],
            cwd=root,
        )

    def test_git_blob_checksum_rejects_short_commit(self) -> None:
        with self.assertRaisesRegex(ValueError, "complete 40-character"):
            git_blob_sha256("abc123", ".agents/skills/sub2api-production-deploy/scripts/release/cli.py")

    def test_manifest_layout_defaults_only_missing_field_to_deploy_v1(self) -> None:
        self.assertEqual(manifest_release_asset_layout({}), LAYOUT_DEPLOY_V1)
        self.assertEqual(
            manifest_release_asset_layout({"release_asset_layout": LAYOUT_SKILL_V1}),
            LAYOUT_SKILL_V1,
        )
        with self.assertRaisesRegex(RuntimeError, "layout is invalid"):
            manifest_release_asset_layout({"release_asset_layout": LAYOUT_DEPLOY_V1})

    def test_deploy_v1_commit_asset_listing_uses_strict_allowlist(self) -> None:
        commit = "d" * 40
        valid = [
            "deploy/release.py",
            "deploy/release/vm-validate.sh",
            "deploy/release/sign-gate.sh",
            "deploy/release/sign-dr-evidence.sh",
            "deploy/release/vm-space-clean.sh",
            "deploy/maintenance/release/context.sh",
            "deploy/maintenance/181/mask-backup-units.sh",
            "deploy/maintenance/181/restore-backup-units.sh",
        ]
        listing = ("\0".join(valid) + "\0").encode()
        with mock.patch("release.manifest._git_output", return_value=listing) as git_output:
            self.assertEqual(release_asset_relative_paths_from_commit(commit, LAYOUT_DEPLOY_V1), sorted(valid))
        command = git_output.call_args.args[0]
        self.assertEqual(command[:6], ["git", "ls-tree", "-r", "-z", "--name-only", commit])
        self.assertTrue(all(is_release_asset_relative_path(path, LAYOUT_DEPLOY_V1) for path in valid))

    def test_deploy_v1_commit_asset_listing_rejects_nested_unapproved_path(self) -> None:
        listing = b"deploy/release/arbitrary/nested.sh\0"
        with mock.patch("release.manifest._git_output", return_value=listing):
            with self.assertRaisesRegex(RuntimeError, "outside the deploy-v1 allowlist"):
                release_asset_relative_paths_from_commit("e" * 40, LAYOUT_DEPLOY_V1)

    def test_git_blob_checksum_retries_transient_windows_process_init_failure(self) -> None:
        error = subprocess.CalledProcessError(0xC0000142, ["git", "show"])
        with (
            mock.patch("release.manifest.workspace_root", return_value=Path("C:/repo")),
            mock.patch("release.manifest.subprocess.check_output", side_effect=[error, b"blob"]),
            mock.patch("release.manifest.time.sleep") as sleep,
        ):
            self.assertEqual(git_blob_sha256("a" * 40, "path.txt"), hashlib.sha256(b"blob").hexdigest())
        self.assertEqual(sleep.call_args_list, [mock.call(1)])

    def test_migration_checksums_retries_transient_windows_process_init_failure(self) -> None:
        error = subprocess.CalledProcessError(0xC0000142, ["git", "show"])
        profile = {"migrations": ["migration.sql"]}
        with (
            mock.patch("release.manifest.workspace_root", return_value=Path("C:/repo")),
            mock.patch("release.manifest.subprocess.check_output", side_effect=[error, b"SELECT 1;\n"]),
            mock.patch("release.manifest.time.sleep") as sleep,
        ):
            checksums = migration_checksums(profile, "b" * 40)
        self.assertEqual(checksums["migration.sql"], hashlib.sha256(b"SELECT 1;").hexdigest())
        self.assertEqual(sleep.call_args_list, [mock.call(1)])

    def test_git_read_does_not_retry_real_git_errors(self) -> None:
        error = subprocess.CalledProcessError(128, ["git", "show"])
        with (
            mock.patch("release.manifest.workspace_root", return_value=Path("C:/repo")),
            mock.patch("release.manifest.subprocess.check_output", side_effect=error) as check_output,
            mock.patch("release.manifest.time.sleep") as sleep,
        ):
            with self.assertRaises(subprocess.CalledProcessError):
                git_blob_sha256("c" * 40, "missing.txt")
        check_output.assert_called_once()
        sleep.assert_not_called()

    def test_profile_191_extends_profile_187_with_official_migrations(self) -> None:
        profile_187 = get_profile("187")
        profile_191 = get_profile("191")
        self.assertEqual(profile_191["version"], "0.1.157-baiyu")
        self.assertEqual(
            profile_191["migrations"],
            profile_187["migrations"]
            + [
                "188_add_subscription_plan_currency.sql",
                "189_channel_image_input_price.sql",
                "190_usage_log_image_input_tokens.sql",
                "191_audit_logs.sql",
            ],
        )
        self.assertEqual(list(migration_checksums(profile_191)), profile_191["migrations"])

    def test_current_profiles_are_allowed_by_release_entrypoints(self) -> None:
        expected_release_pattern = "(182|187|191|192|194|195|197|198|199|202|206|207|208|209|210|212|213|215|232|233|234|235|236)"
        expected_profile_check = "$profile == 182 || $profile == 187 || $profile == 191 || $profile == 192 || $profile == 194 || $profile == 195 || $profile == 197 || $profile == 198 || $profile == 199 || $profile == 202 || $profile == 206 || $profile == 207 || $profile == 208 || $profile == 209 || $profile == 210 || $profile == 212 || $profile == 213 || $profile == 215 || $profile == 232 || $profile == 233 || $profile == 234 || $profile == 235 || $profile == 236"
        for relative_path in (
            "release/vm-validate.sh",
            "release/sign-gate.sh",
            "release/production-space-clean.sh",
            "maintenance/release/context.sh",
            "maintenance/release/prepare.sh",
            "maintenance/release/promote-backup.sh",
            "maintenance/181/mask-backup-units.sh",
            "maintenance/181/restore-backup-units.sh",
        ):
            content = (DEPLOY_ROOT / relative_path).read_text(encoding="utf-8")
            self.assertIn(expected_release_pattern, content, relative_path)
        for relative_path in (
            "release/vm-validate.sh",
            "maintenance/release/prepare.sh",
        ):
            content = (DEPLOY_ROOT / relative_path).read_text(encoding="utf-8")
            self.assertIn(expected_profile_check, content, relative_path)

        validator = (DEPLOY_ROOT / "release" / "vm-validate.sh").read_text(encoding="utf-8")
        self.assertIn("drverify/[^/]+", validator)

    def test_profile_192_extends_profile_191_with_group_duplicate_migration(self) -> None:
        profile_191 = get_profile("191")
        profile_192 = get_profile("192")
        self.assertEqual(profile_192["version"], "0.1.158-baiyu")
        self.assertEqual(
            profile_192["migrations"],
            profile_191["migrations"] + ["192_group_duplicate_operation_id.sql"],
        )
        self.assertEqual(list(migration_checksums(profile_192)), profile_192["migrations"])

    def test_profile_194_extends_profile_192_with_prompt_audit_migrations(self) -> None:
        profile_192 = get_profile("192")
        profile_194 = get_profile("194")
        self.assertEqual(profile_194["version"], "0.1.160-baiyu")
        self.assertEqual(
            profile_194["migrations"],
            profile_192["migrations"]
            + ["193_prompt_audit.sql", "194_prompt_audit_full_prompt.sql"],
        )
        self.assertEqual(list(migration_checksums(profile_194)), profile_194["migrations"])

    def test_profile_195_extends_profile_194_with_monitor_rate_migration(self) -> None:
        profile_194 = get_profile("194")
        profile_195 = get_profile("195")
        self.assertEqual(profile_195["version"], "0.1.161-baiyu")
        self.assertEqual(
            profile_195["migrations"],
            profile_194["migrations"] + ["195_upstream_scheduling_monitor_rates.sql"],
        )
        self.assertEqual(list(migration_checksums(profile_195)), profile_195["migrations"])

    def test_profile_197_extends_profile_195_with_upstream_migrations(self) -> None:
        profile_195 = get_profile("195")
        profile_197 = get_profile("197")
        self.assertEqual(profile_197["version"], "0.1.162-baiyu")
        self.assertEqual(
            profile_197["migrations"],
            profile_195["migrations"]
            + [
                "196_ops_ingress_reject_aggregates.sql",
                "197_auth_cache_invalidation_outbox.sql",
            ],
        )
        self.assertEqual(list(migration_checksums(profile_197)), profile_197["migrations"])

    def test_profile_198_extends_profile_197_with_managed_monitor_key_name_migration(self) -> None:
        profile_197 = get_profile("197")
        profile_198 = get_profile("198")
        self.assertEqual(profile_198["version"], "0.1.162-baiyu")
        self.assertEqual(
            profile_198["migrations"],
            profile_197["migrations"] + ["198_normalize_managed_monitor_key_names.sql"],
        )
        self.assertEqual(list(migration_checksums(profile_198)), profile_198["migrations"])

    def test_profile_199_extends_profile_198_with_reasoning_effort_policy(self) -> None:
        profile_198 = get_profile("198")
        profile_199 = get_profile("199")
        self.assertEqual(profile_199["version"], "0.1.163-baiyu")
        self.assertEqual(
            profile_199["migrations"],
            profile_198["migrations"] + ["199_group_reasoning_effort_policy.sql"],
        )
        self.assertEqual(list(migration_checksums(profile_199)), profile_199["migrations"])

    def test_profile_202_extends_profile_199_with_upstream_migrations(self) -> None:
        profile_199 = get_profile("199")
        profile_202 = get_profile("202")
        self.assertEqual(profile_202["version"], "0.1.164-baiyu")
        self.assertEqual(
            profile_202["migrations"],
            profile_199["migrations"]
            + [
                "200_alipay_mobile_precreate_deep_link.sql",
                "201_group_auth_cache_image_generation.sql",
                "202_composite_model_routes.sql",
            ],
        )
        self.assertEqual(list(migration_checksums(profile_202)), profile_202["migrations"])

    def test_profile_206_extends_profile_202_with_upstream_migrations(self) -> None:
        profile_202 = get_profile("202")
        profile_206 = get_profile("206")
        self.assertEqual(profile_206["version"], "0.1.165-baiyu")
        self.assertEqual(
            profile_206["migrations"],
            profile_202["migrations"]
            + [
                "203_add_usage_log_session_id.sql",
                "204_allow_live_usage_request_type.sql",
                "205_add_group_allow_live.sql",
                "206_add_users_email_alias_dedup_index_notx.sql",
            ],
        )
        self.assertEqual(list(migration_checksums(profile_206)), profile_206["migrations"])

    def test_profile_207_is_a_version_only_successor_to_profile_206(self) -> None:
        profile_206 = get_profile("206")
        profile_207 = get_profile("207")
        self.assertEqual(profile_206["version"], "0.1.165-baiyu")
        self.assertEqual(profile_207["version"], "0.1.166-baiyu")
        self.assertEqual(profile_207["migrations"], profile_206["migrations"])
        self.assertIsNot(profile_207["migrations"], profile_206["migrations"])
        self.assertEqual(list(migration_checksums(profile_207)), profile_207["migrations"])
        self.assertFalse(any(migration.startswith("207_") for migration in profile_207["migrations"]))

    def test_profile_208_extends_profile_207_with_passkey_migration(self) -> None:
        profile_207 = get_profile("207")
        profile_208 = get_profile("208")
        self.assertEqual(profile_207["version"], "0.1.166-baiyu")
        self.assertEqual(profile_208["version"], "0.1.168-baiyu")
        self.assertEqual(
            profile_208["migrations"],
            profile_207["migrations"] + ["208_passkey_credentials.sql"],
        )
        self.assertIsNot(profile_208["migrations"], profile_207["migrations"])
        self.assertEqual(list(migration_checksums(profile_208)), profile_208["migrations"])
        self.assertEqual(
            [migration for migration in profile_208["migrations"] if migration.startswith("208_")],
            ["208_passkey_credentials.sql"],
        )

    def test_profile_209_extends_profile_208_with_user_usage_aggregation_migration(self) -> None:
        profile_208 = get_profile("208")
        profile_209 = get_profile("209")
        self.assertEqual(profile_208["version"], "0.1.168-baiyu")
        self.assertEqual(profile_209["version"], "0.1.168-baiyu")
        self.assertEqual(
            profile_209["migrations"],
            profile_208["migrations"] + ["209_user_usage_aggregation.sql"],
        )
        self.assertIsNot(profile_209["migrations"], profile_208["migrations"])
        self.assertEqual(list(migration_checksums(profile_209)), profile_209["migrations"])
        self.assertEqual(
            [migration for migration in profile_209["migrations"] if migration.startswith("209_")],
            ["209_user_usage_aggregation.sql"],
        )

    def test_profile_210_is_a_version_only_successor_to_profile_209(self) -> None:
        profile_209 = get_profile("209")
        profile_210 = get_profile("210")
        self.assertEqual(profile_209["version"], "0.1.168-baiyu")
        self.assertEqual(profile_210["version"], "0.1.169-baiyu")
        self.assertEqual(profile_210["migrations"], profile_209["migrations"])
        self.assertIsNot(profile_210["migrations"], profile_209["migrations"])
        self.assertEqual(list(migration_checksums(profile_210)), profile_210["migrations"])
        self.assertFalse(any(migration.startswith("210_") for migration in profile_210["migrations"]))

    def test_profile_212_appends_profit_control_migrations_to_profile_210(self) -> None:
        profile_210 = get_profile("210")
        profile_212 = get_profile("212")
        self.assertEqual(profile_212["version"], "0.1.170-baiyu")
        self.assertEqual(
            profile_212["migrations"],
            profile_210["migrations"]
            + [
                "211_group_profit_control.sql",
                "212_group_profit_control_auth_cache_invalidation.sql",
            ],
        )
        self.assertIsNot(profile_212["migrations"], profile_210["migrations"])
        self.assertEqual(list(migration_checksums(profile_212)), profile_212["migrations"])

    def test_profile_213_is_pure_version_inheritance_from_profile_212(self) -> None:
        profile_212 = get_profile("212")
        profile_213 = get_profile("213")
        self.assertEqual(profile_213["version"], "0.1.171-baiyu")
        self.assertEqual(profile_213["migrations"], profile_212["migrations"])
        self.assertIsNot(profile_213["migrations"], profile_212["migrations"])
        self.assertEqual(list(migration_checksums(profile_213)), profile_213["migrations"])
        self.assertFalse(any(migration.startswith("213_") for migration in profile_213["migrations"]))

    def test_profile_215_appends_usage_model_migrations_to_profile_213(self) -> None:
        profile_213 = get_profile("213")
        profile_215 = get_profile("215")
        self.assertEqual(profile_215["version"], "0.1.172-baiyu")
        self.assertEqual(
            profile_215["migrations"],
            profile_213["migrations"]
            + [
                "214_add_usage_log_upstream_response_model.sql",
                "215_add_usage_log_upstream_model_mismatch_index_notx.sql",
            ],
        )
        self.assertIsNot(profile_215["migrations"], profile_213["migrations"])
        self.assertEqual(list(migration_checksums(profile_215)), profile_215["migrations"])

    def test_profile_232_appends_channel_monitor_and_media_migrations_to_profile_215(self) -> None:
        profile_215 = get_profile("215")
        profile_232 = get_profile("232")
        self.assertEqual(profile_232["version"], "0.1.173-baiyu")
        self.assertEqual(profile_232["compatibility_version"], "0.1.172-baiyu")
        self.assertEqual(profile_232["compatibility_commit"], "74e47e67205084750ccd994c331ead328e4ce35b")
        self.assertEqual(profile_232["compatibility_image_id"], "sha256:cd3dff0ce18762d7faa9d4a4492eb770b616f9b01b66256ce6280c2f4855abd6")
        expected = [f"{number}_{name}" for number, name in (
            (216, "channel_monitor_v2.sql"),
            (217, "channel_monitor_mode.sql"),
            (218, "channel_monitor_v2_ignored_error_categories.sql"),
            (219, "channel_monitor_v2_seed_popular_models.sql"),
            (220, "channel_monitor_v2_health_thresholds.sql"),
            (221, "channel_monitor_v2_fixed_rollups.sql"),
            (222, "channel_monitor_v2_rollup_permissions.sql"),
            (223, "channel_monitor_v2_refresh_5m.sql"),
            (224, "channel_monitor_v2_full_table_permissions.sql"),
            (225, "channel_monitor_v2_default_ignore_and_cache.sql"),
            (226, "channel_monitor_hide_throughput.sql"),
            (227, "channel_monitor_v2_reset_factory_cache_thresholds.sql"),
            (228, "channel_monitor_v2_privacy_defaults.sql"),
            (229, "group_video_model_prices.sql"),
            (230, "group_audio_voice_pricing.sql"),
            (231, "group_search_price_per_1k.sql"),
            (232, "clear_non_grok_video_generation_config.sql"),
        )]
        self.assertEqual(profile_232["migrations"], profile_215["migrations"] + expected)
        self.assertEqual(len(profile_232["migrations"]), 49)
        self.assertEqual(list(migration_checksums(profile_232)), profile_232["migrations"])

    def test_profile_233_appends_consolidated_upstream_management_migration_to_profile_232(self) -> None:
        profile_232 = get_profile("232")
        profile_233 = get_profile("233")
        self.assertEqual(profile_233["version"], "0.1.173-baiyu")
        self.assertEqual(profile_233["compatibility_version"], profile_232["compatibility_version"])
        self.assertEqual(profile_233["compatibility_commit"], profile_232["compatibility_commit"])
        self.assertEqual(profile_233["compatibility_image_id"], profile_232["compatibility_image_id"])
        self.assertEqual(profile_233["migrations"], profile_232["migrations"] + ["233_upstream_management.sql"])
        self.assertEqual(len(profile_233["migrations"]), 50)
        self.assertEqual(list(migration_checksums(profile_233)), profile_233["migrations"])
        gate = (DEPLOY_ROOT / "release" / "gate.py").read_text(encoding="utf-8")
        validator = (DEPLOY_ROOT / "release" / "vm-validate.sh").read_text(encoding="utf-8")
        preflight = (DEPLOY_ROOT / "maintenance" / "release" / "preflight.sh").read_text(encoding="utf-8")
        switch = (DEPLOY_ROOT / "maintenance" / "release" / "switch.sh").read_text(encoding="utf-8")
        self.assertIn('evidence.get("migration_233_status")', gate)
        self.assertIn('evidence.get("migration_233_preflight_verified")', gate)
        self.assertIn('evidence.get("migration_233_postflight_verified")', gate)
        self.assertIn('[[ $version == 0.1.173-baiyu ]]', validator)
        self.assertIn("[[ $(jq -er '.migrations | length' \"$manifest\") == 50 ]]", validator)
        self.assertIn("migration_233_status=$(profile_212_migration_status 233_upstream_management.sql)", validator)
        self.assertIn("233_upstream_management.sql) migration_233_status=verified", preflight)
        self.assertIn('MIGRATION_STATUS="$migration_233_status" "$assets_dir/migration-233-assert.sh" postflight', switch)
        assertion = (DEPLOY_ROOT / "maintenance" / "release" / "migration-233-assert.sh").read_text(encoding="utf-8")
        self.assertIn("upstream_health_observations", assertion)
        self.assertIn("idx_upstream_health_observations_key_observed", assertion)
        self.assertIn("migration_233_trigger_verified=true", assertion)

    def test_profile_234_is_a_version_only_inheritance_of_profile_233(self) -> None:
        profile_233 = get_profile("233")
        profile_234 = get_profile("234")
        self.assertEqual(profile_234["version"], "0.1.175-baiyu")
        self.assertEqual(profile_234["migrations"], profile_233["migrations"])
        self.assertIsNot(profile_234["migrations"], profile_233["migrations"])
        self.assertEqual(profile_234["compatibility_version"], profile_233["compatibility_version"])
        self.assertEqual(profile_234["compatibility_commit"], profile_233["compatibility_commit"])
        self.assertEqual(profile_234["compatibility_image_id"], profile_233["compatibility_image_id"])
        self.assertEqual(len(profile_234["migrations"]), 50)
        self.assertEqual(list(migration_checksums(profile_234)), profile_234["migrations"])

    def test_profile_235_appends_group_model_pricing_to_profile_234(self) -> None:
        profile_234 = get_profile("234")
        profile_235 = get_profile("235")
        self.assertEqual(profile_235["version"], "0.1.176-baiyu")
        self.assertEqual(
            profile_235["migrations"],
            profile_234["migrations"] + ["234_group_model_pricing.sql"],
        )
        self.assertIsNot(profile_235["migrations"], profile_234["migrations"])
        self.assertEqual(profile_235["compatibility_version"], profile_234["compatibility_version"])
        self.assertEqual(profile_235["compatibility_commit"], profile_234["compatibility_commit"])
        self.assertEqual(profile_235["compatibility_image_id"], profile_234["compatibility_image_id"])
        self.assertEqual(len(profile_235["migrations"]), 51)
        self.assertEqual(list(migration_checksums(profile_235)), profile_235["migrations"])

        gate = (DEPLOY_ROOT / "release" / "gate.py").read_text(encoding="utf-8")
        validator = (DEPLOY_ROOT / "release" / "vm-validate.sh").read_text(encoding="utf-8")
        preflight = (DEPLOY_ROOT / "maintenance" / "release" / "preflight.sh").read_text(encoding="utf-8")
        switch = (DEPLOY_ROOT / "maintenance" / "release" / "switch.sh").read_text(encoding="utf-8")
        self.assertIn('expected_profile in {"235", "236"}', gate)
        self.assertIn('evidence.get("migration_234_status")', gate)
        self.assertIn('evidence.get("migration_234_preflight_verified")', gate)
        self.assertIn('evidence.get("migration_234_schema_verified")', gate)
        self.assertIn('[[ $version == 0.1.176-baiyu ]]', validator)
        self.assertIn("[[ $(jq -er '.migrations | length' \"$manifest\") == 51 ]]", validator)
        self.assertIn("234_group_model_pricing.sql) migration_234_status=verified", preflight)
        self.assertIn('migration-234-assert.sh" postflight', switch)
        assertion = (DEPLOY_ROOT / "maintenance" / "release" / "migration-234-assert.sh").read_text(encoding="utf-8")
        self.assertIn("long_context_pricing_enabled", assertion)
        self.assertIn("model_pricing", assertion)
        self.assertIn('> "$state_dir/migration-234-status"', assertion)
        self.assertIn('chmod 600 "$state_dir/migration-234-status"', assertion)
        self.assertIn("migration_234_preflight=pass", assertion)
        self.assertIn("migration_234_postflight=pass", assertion)

        integration = DEPLOY_ROOT / "tests" / "release" / "backup_dr_profile_235_integration.py"
        self.assertTrue(integration.is_file())
        self.assertIn('main("235")', integration.read_text(encoding="utf-8"))

    def test_profile_236_inherits_profile_235_without_migrations(self) -> None:
        profile_235 = get_profile("235")
        profile_236 = get_profile("236")

        self.assertEqual(profile_236["version"], "0.1.177-baiyu")
        self.assertEqual(profile_236["migrations"], profile_235["migrations"])
        self.assertIsNot(profile_236["migrations"], profile_235["migrations"])
        self.assertEqual(profile_236["compatibility_version"], profile_235["compatibility_version"])
        self.assertEqual(profile_236["compatibility_commit"], profile_235["compatibility_commit"])
        self.assertEqual(profile_236["compatibility_image_id"], profile_235["compatibility_image_id"])
        self.assertEqual(len(profile_236["migrations"]), 51)
        self.assertEqual(list(migration_checksums(profile_236)), profile_236["migrations"])

        validator = (DEPLOY_ROOT / "release" / "vm-validate.sh").read_text(encoding="utf-8")
        self.assertIn('[[ $version == 0.1.177-baiyu ]]', validator)
        self.assertIn('$profile == 235 || $profile == 236', validator)

        integration = DEPLOY_ROOT / "tests" / "release" / "backup_dr_profile_236_integration.py"
        self.assertTrue(integration.is_file())
        self.assertIn('main("236")', integration.read_text(encoding="utf-8"))

    def test_profile_212_release_chain_requires_profit_control_evidence(self) -> None:
        validator = (DEPLOY_ROOT / "release" / "vm-validate.sh").read_text(encoding="utf-8")
        switch = (DEPLOY_ROOT / "maintenance" / "release" / "switch.sh").read_text(encoding="utf-8")
        production = (DEPLOY_ROOT / "release" / "production.py").read_text(encoding="utf-8")
        gate = (DEPLOY_ROOT / "release" / "gate.py").read_text(encoding="utf-8")
        for field in ("group_profit_control_schema_verified", "group_profit_auth_cache_trigger_verified"):
            self.assertIn(field, validator)
            self.assertIn(field, switch)
            self.assertIn(f'allowed.add("{field}")', production)
            self.assertIn(field, gate)
        self.assertIn("for trigger_field in status is_exclusive allow_image_generation", validator)
        self.assertIn("profit_control_enabled profit_min_margin profit_safety_buffer", validator)
        self.assertIn('grep -Fq "OLD.$trigger_field IS NOT DISTINCT FROM NEW.$trigger_field"', validator)
        self.assertIn("numeric_precision=10 AND numeric_scale=4", validator)
        self.assertIn("for trigger_field in status is_exclusive allow_image_generation", switch)
        self.assertIn("peak_start peak_end peak_rate_multiplier", switch)
        self.assertIn("profit_control_enabled profit_min_margin profit_safety_buffer deleted_at", switch)
        self.assertIn("/api/v1/auth/me", validator)
        self.assertIn("migration_assertion_profile_212_status", validator)
        self.assertIn("SELECT checksum FROM schema_migrations WHERE filename='$filename'", validator)
        self.assertIn("actual=$(docker exec sub2api-postgres", validator)
        self.assertIn("| tr -d '\\r\\n')", validator)
        self.assertIn("[[ ! $actual =~ ^[0-9a-f]{64}$ || $actual != \"$expected\" ]]", validator)
        self.assertIn('mark_stage "migration_assertion_status_${migration_number}"', validator)
        self.assertIn('mark_stage "migration_assertion_status_manifest_${migration_number}"', validator)
        self.assertIn('mark_stage "migration_assertion_status_query_${migration_number}"', validator)
        self.assertIn('mark_stage "migration_assertion_status_checksum_${migration_number}"', validator)
        self.assertIn("migration_211_status:$migration_211_status", validator)
        self.assertIn("migration_212_status:$migration_212_status", validator)
        self.assertIn('evidence.get("migration_211_status") not in {"absent", "verified"}', gate)

    def test_vm_old_image_compatibility_and_candidate_failures_retain_diagnostics(self) -> None:
        validator = (DEPLOY_ROOT / "release" / "vm-validate.sh").read_text(encoding="utf-8")
        self.assertIn("if [[ $current_stage == old_image_compatibility_* ]]", validator)
        for stage in ("start", "health", "image", "network", "auth"):
            self.assertIn(f"mark_stage old_image_compatibility_{stage}", validator)
        self.assertIn('category="old_image_compatibility_auth_http_$old_image_auth_status"', validator)
        self.assertIn("[[ $old_image_auth_status =~ ^[0-9]{3}$ ]]", validator)
        self.assertIn("curl -q --noproxy '*' --silent --show-error --output /dev/null", validator)
        self.assertIn("--write-out '%{http_code}' --max-time 10", validator)
        self.assertIn('http://127.0.0.1:$old_probe_port/api/v1/auth/me', validator)
        self.assertIn('old_image_auth_command_status=$?', validator)
        self.assertIn('if [[ $old_image_auth_command_status == 0 && $old_image_auth_status =~ ^[0-9]{3}$ ]]', validator)
        self.assertIn('printf \'%s\\n\' "$old_image_auth_status" > "$state_dir/old-image-auth-status"', validator)
        self.assertIn('[[ $old_image_auth_command_status == 0 ]]', validator)
        self.assertIn('[[ $old_image_auth_status == 401 ]]', validator)
        self.assertIn('-p "127.0.0.1::$server_port"', validator)
        self.assertIn('old_probe_port=$(docker port "$old_probe_app"', validator)
        self.assertNotIn('wget -S -O /dev/null --timeout=10', validator)
        self.assertNotIn("old-probe-app.log", validator)
        self.assertIn("candidate_health || $(<\"$state_dir/stage\") == candidate_background_activation", validator)
        self.assertIn('category=$current_stage', validator)
        self.assertIn('candidate-background-headers', validator)
        self.assertIn('candidate-activation-marker', validator)
        self.assertIn('chmod 400 "$state_dir/validator.stderr"', validator)

    def test_profile_209_migration_defines_permanent_user_usage_aggregation_contract(self) -> None:
        migration = (WORKSPACE / "backend" / "migrations" / "209_user_usage_aggregation.sql").read_text(encoding="utf-8")
        self.assertIn("CREATE TABLE IF NOT EXISTS usage_dashboard_user_hourly", migration)
        self.assertIn("PRIMARY KEY (bucket_start, user_id)", migration)
        self.assertIn("CREATE TABLE IF NOT EXISTS usage_dashboard_user_daily", migration)
        self.assertIn("PRIMARY KEY (bucket_date, user_id)", migration)
        self.assertGreaterEqual(migration.count("REFERENCES users(id) ON DELETE CASCADE"), 2)
        self.assertIn("CREATE TABLE IF NOT EXISTS usage_dashboard_user_backfill_state", migration)
        self.assertIn("CHECK (id = 1)", migration)
        self.assertIn("status IN ('available', 'building', 'partial', 'unavailable')", migration)
        self.assertIn("INSERT INTO usage_dashboard_user_backfill_state (id)", migration)
        self.assertIn("idx_usage_dashboard_user_hourly_user_bucket", migration)
        self.assertIn("idx_usage_dashboard_user_daily_user_bucket", migration)

    def test_profile_194_requires_prompt_audit_disabled_evidence(self) -> None:
        validator = (DEPLOY_ROOT / "release" / "vm-validate.sh").read_text(encoding="utf-8")
        context = (DEPLOY_ROOT / "maintenance" / "release" / "context.sh").read_text(encoding="utf-8")
        production = (DEPLOY_ROOT / "release" / "production.py").read_text(encoding="utf-8")
        gate = (DEPLOY_ROOT / "release" / "gate.py").read_text(encoding="utf-8")

        self.assertIn("prompt_audit_state == 't|0|0'", validator)
        self.assertIn("prompt_audit_disabled:$prompt_audit_disabled", validator)
        self.assertIn("assert_prompt_audit_disabled()", context)
        self.assertIn("$profile != 197 && $profile != 198 && $profile != 199 && $profile != 202 && $profile != 206 && $profile != 207 && $profile != 208", context)
        self.assertEqual(production.count('"prompt_audit_disabled", "prompt_audit_jobs", "prompt_audit_events"'), 3)
        self.assertIn('expected_profile in {"194", "195", "197", "198", "199", "202", "206", "207", "208", "209", "210", "212", "213", "215", "232", "233", "234", "235", "236"}', gate)

    def test_profile_195_requires_semantic_migration_evidence(self) -> None:
        validator = (DEPLOY_ROOT / "release" / "vm-validate.sh").read_text(encoding="utf-8")
        production = (DEPLOY_ROOT / "release" / "production.py").read_text(encoding="utf-8")
        gate = (DEPLOY_ROOT / "release" / "gate.py").read_text(encoding="utf-8")
        switch = (DEPLOY_ROOT / "maintenance" / "release" / "switch.sh").read_text(encoding="utf-8")
        assertion = (DEPLOY_ROOT / "maintenance" / "release" / "migration-195-assert.sh").read_text(encoding="utf-8")

        self.assertIn("migration_195_verified:$migration_195_verified", validator)
        self.assertIn("managed_monitor_key_names_verified:$managed_monitor_key_names_verified", validator)
        self.assertIn('bash "$migration_assertion_dir/migration-195-assert.sh" preflight', validator)
        self.assertIn("MIGRATION_STATUS=absent", validator)
        self.assertIn("MIGRATION_STATUS=verified", validator)
        self.assertIn("migration_195_verified_state", validator)
        self.assertIn("verified_replay=true", validator)
        self.assertIn("verified_low_watermark_rejected=true", validator)
        self.assertIn("probe_migration_195_recorded", validator)
        self.assertIn("migration_195_status=verified", validator)
        self.assertIn('MIGRATION_STATUS="$migration_195_status"', validator)
        self.assertIn("mark_stage migration_assertion_profile_195_fixture", validator)
        self.assertIn("INSERT INTO scheduler_outbox (event_type, payload)", validator)
        self.assertIn("payload->'account_ids' = expected_accounts.ids", validator)
        self.assertIn('docker exec -i sub2api-postgres sh -lc "psql -X -q -v ON_ERROR_STOP=1', validator)
        self.assertIn('redis-cli SET sched:v2:outbox:watermark "$probe_outbox_highwater"', validator)
        self.assertIn('[[ $consumed_event_id == 0 || $sentinel_event_id -gt $consumed_event_id ]]', validator)
        self.assertIn("ASSERT_DB_USER=\"$database_owner\"", validator)
        self.assertNotIn("ASSERT_DB_USER=\"$database_user\"", validator)
        self.assertIn("mark_stage runtime_fixture_profile_206_sequences", validator)
        self.assertIn("mark_stage runtime_fixture_profile_206_admin", validator)
        self.assertIn("mark_stage runtime_fixture_profile_206_insert", validator)
        self.assertIn("setval(pg_get_serial_sequence('groups','id'), COALESCE(MAX(id),0)+1, false)", validator)
        self.assertIn("setval(pg_get_serial_sequence('api_keys','id'), COALESCE(MAX(id),0)+1, false)", validator)
        self.assertIn("setval(pg_get_serial_sequence('group_rate_snapshots','id'), COALESCE(MAX(id),0)+1, false)", validator)
        self.assertIn("setval(pg_get_serial_sequence('upstream_events','id'), COALESCE(MAX(id),0)+1, false)", validator)
        self.assertIn("create_probe_database()", validator)
        self.assertIn("if ASSERT_CONTEXT_FILE=", validator)
        self.assertIn('sh -lc "dropdb -U', validator)
        self.assertEqual(validator.count("create_probe_database\n"), 2)
        self.assertEqual(validator.count('docker exec -i sub2api-postgres sh -lc "pg_restore'), 2)
        self.assertEqual(validator.count('< "$state_dir/probe.dump"'), 2)
        self.assertNotIn("dropdb -U \\\"\\${POSTGRES_USER:-postgres}\\\" $probe_db && createdb", validator)
        self.assertIn("fixture_rejected=true", validator)
        self.assertIn("restore_completed=true", validator)
        self.assertIn("clean_preflight=true", validator)
        self.assertIn("migrate-candidate.log", validator)
        self.assertIn('exec 2>"$state_dir/validator.stderr"', validator)
        self.assertIn('rm -f "$state_dir/validator.stderr"', validator)
        self.assertIn("migration_missing_object", validator)
        self.assertIn("migration_constraint", validator)
        self.assertIn("migration_182_semantic", validator)
        self.assertIn("migration_195_semantic", validator)

        prepare = (DEPLOY_ROOT / "maintenance" / "release" / "prepare.sh").read_text(encoding="utf-8")
        self.assertIn("release_asset_layout", prepare)
        self.assertIn("maintenance_asset_prefix", prepare)
        self.assertIn("unit_asset_prefix", prepare)
        self.assertIn("deploy/maintenance/release", prepare)
        self.assertIn("deploy/maintenance/181", prepare)
        self.assertIn(".agents/skills/sub2api-production-deploy/scripts/maintenance/release", prepare)
        self.assertIn(".agents/skills/sub2api-production-deploy/scripts/maintenance/181", prepare)
        self.assertNotIn('source="deploy/maintenance/release/$name"', prepare)
        self.assertIn('source="$unit_asset_prefix/$name"', prepare)
        self.assertIn("release_asset_pattern", validator)
        self.assertIn("migration_assertion_dir", validator)
        self.assertIn(".agents/skills/sub2api-production-deploy/scripts/maintenance/release", validator)
        self.assertNotIn('bash "$source_dir/deploy/maintenance/release/migration-195-assert.sh"', validator)
        self.assertIn('bash "$migration_assertion_dir/migration-195-assert.sh"', validator)

        self.assertIn("failed_migration=$(sed", validator)
        self.assertIn('category="migration_file_$failed_migration"', validator)
        self.assertIn("migration_permission", validator)
        self.assertIn("migration_config", validator)
        self.assertIn("migration_runner_init", validator)
        self.assertIn("migration_advisory_lock", validator)
        self.assertIn("migration_go_timezone", validator)
        self.assertIn("migration_database_timezone", validator)
        self.assertIn('test -f \"/usr/share/zoneinfo/$PROBE_TIMEZONE\"', validator)
        self.assertIn("migration_missing_group_rate_snapshots", validator)
        self.assertIn("migration_missing_timezone_lock", validator)
        self.assertIn("migration_missing_advisory_function", validator)
        self.assertIn("migration_sqlstate=$(sed", validator)
        self.assertIn('category="migration_sqlstate_$migration_sqlstate"', validator)
        self.assertIn("$category == migration_timezone", validator)
        self.assertIn('"migration_195_verified", "fixture_rejected", "restore_completed", "clean_preflight", "verified_replay", "verified_low_watermark_rejected"', gate)
        self.assertIn("any(evidence.get(field) is not True", gate)
        self.assertIn('"migration_195_plan_sha256"', production)
        self.assertIn('migration-195-assert.sh preflight', production)
        self.assertIn('migration-195-assert.sh" postflight', switch)
        self.assertIn("unproven == 0 && $conflict == 0 && $unexpected == 0", assertion)
        self.assertIn('expected_profile in {"195", "197", "198", "199", "202", "206", "207", "208", "209", "210", "212", "213", "215", "232", "233", "234", "235", "236"}', gate)
        self.assertIn('self.profile["name"] not in {"195", "197", "198", "199", "202", "206", "207", "208", "209", "210", "212", "213", "215", "232", "233", "234", "235", "236"}', production)
        self.assertIn('[[ $profile == 195 || $profile == 197 || $profile == 198 || $profile == 199 || $profile == 202 || $profile == 206 || $profile == 207 || $profile == 208 || $profile == 209 || $profile == 210 || $profile == 212 || $profile == 213 || $profile == 215 || $profile == 232 || $profile == 233 || $profile == 234 || $profile == 235 || $profile == 236 ]]', switch)
        self.assertIn('[[ $profile == 195 || $profile == 197 || $profile == 198 || $profile == 199 || $profile == 202 || $profile == 206 || $profile == 207 || $profile == 208 || $profile == 209 || $profile == 210 || $profile == 212 || $profile == 213 || $profile == 215 || $profile == 232 || $profile == 233 || $profile == 234 || $profile == 235 || $profile == 236 ]]', assertion)
        self.assertIn('expected_profile in {"198", "199", "202", "206", "207", "208", "209", "210", "212", "213", "215", "232", "233", "234", "235", "236"}', gate)
        self.assertIn("managed monitor key-name evidence", gate)

    def test_profile_199_requires_reasoning_and_old_image_evidence(self) -> None:
        validator = (DEPLOY_ROOT / "release" / "vm-validate.sh").read_text(encoding="utf-8")
        production = (DEPLOY_ROOT / "release" / "production.py").read_text(encoding="utf-8")
        gate = (DEPLOY_ROOT / "release" / "gate.py").read_text(encoding="utf-8")
        switch = (DEPLOY_ROOT / "maintenance" / "release" / "switch.sh").read_text(encoding="utf-8")

        self.assertIn("reasoning_effort_policy_verified:$reasoning_effort_policy_verified", validator)
        self.assertIn("vm_old_image_compatibility_verified:$vm_old_image_compatibility_verified", validator)
        self.assertIn("vm_old_image_id:$vm_old_image_id", validator)
        self.assertIn("mark_stage old_image_compatibility", validator)
        self.assertIn('"reasoning_effort_policy_verified"', production)
        self.assertIn("reasoning_effort_policy_verified=true", switch)
        self.assertIn('expected_profile in {"199", "202", "206", "207", "208", "209", "210", "212", "213", "215", "232", "233", "234", "235", "236"}', gate)
        self.assertIn("group reasoning-effort policy evidence", gate)
        self.assertIn("VM old-image compatibility evidence", gate)

    def test_profile_202_requires_all_upstream_migration_evidence(self) -> None:
        validator = (DEPLOY_ROOT / "release" / "vm-validate.sh").read_text(encoding="utf-8")
        production = (DEPLOY_ROOT / "release" / "production.py").read_text(encoding="utf-8")
        gate = (DEPLOY_ROOT / "release" / "gate.py").read_text(encoding="utf-8")
        switch = (DEPLOY_ROOT / "maintenance" / "release" / "switch.sh").read_text(encoding="utf-8")

        for evidence in (
            "alipay_mobile_precreate_migration_verified",
            "group_auth_cache_image_generation_verified",
            "composite_model_routes_verified",
        ):
            self.assertIn(f"{evidence}:${evidence}", validator)
            self.assertIn(f'"{evidence}"', production)
            self.assertIn(f"{evidence}=true", switch)
            self.assertIn(f'"{evidence}"', gate)
        self.assertIn('expected_profile in {"202", "206", "207", "208", "209", "210", "212", "213", "215", "232", "233", "234", "235", "236"}', gate)
        self.assertIn("profile 202 migration semantic evidence", gate)
        seed = "INSERT INTO settings (key,value,updated_at) VALUES ('ALIPAY_MOBILE_PRECREATE_DEEP_LINK','true',NOW())"
        self.assertGreater(validator.index(seed), validator.index("restore_completed=true"))
        self.assertLess(validator.index(seed), validator.index('docker run --rm --network "$probe_network"'))
        self.assertIn(
            'group_auth_cache_image_state=$(docker exec -i sub2api-postgres',
            validator,
        )
        for stage in (
            "migration_assertion_profile_202_alipay",
            "migration_assertion_profile_202_group_auth",
            "migration_assertion_profile_202_composite",
            "migration_assertion_195_runtime_current",
            "migration_assertion_195_runtime_replay",
        ):
            self.assertIn(f"mark_stage {stage}", validator)
        self.assertIn("category=$current_stage", validator)

    def test_profile_206_requires_all_upstream_migration_evidence(self) -> None:
        validator = (DEPLOY_ROOT / "release" / "vm-validate.sh").read_text(encoding="utf-8")
        production = (DEPLOY_ROOT / "release" / "production.py").read_text(encoding="utf-8")
        gate = (DEPLOY_ROOT / "release" / "gate.py").read_text(encoding="utf-8")
        switch = (DEPLOY_ROOT / "maintenance" / "release" / "switch.sh").read_text(encoding="utf-8")

        for evidence in (
            "session_id_columns_verified",
            "live_request_type_verified",
            "group_allow_live_verified",
            "email_alias_index_verified",
        ):
            self.assertIn(f"{evidence}:${evidence}", validator)
            self.assertIn(f'"{evidence}"', production)
            self.assertIn(f"{evidence}=true", switch)
            self.assertIn(f'"{evidence}"', gate)
        self.assertIn("live_runtime_capability_verified:$live_runtime_capability_verified", validator)
        self.assertIn('"live_runtime_capability_verified"', gate)
        self.assertNotIn('"live_runtime_capability_verified"', production)
        self.assertNotIn("live_runtime_capability_verified=true", switch)
        self.assertIn('expected_profile in {"206", "207", "208", "209", "210", "212", "213", "215", "232", "233", "234", "235", "236"}', gate)
        self.assertIn("profile 206 migration semantic evidence", gate)
        for stage in (
            "migration_assertion_profile_206_session_id",
            "migration_assertion_profile_206_live_request_type",
            "migration_assertion_profile_206_group_allow_live",
            "migration_assertion_profile_206_email_alias_index",
            "runtime_assertion_profile_206_live_capability",
        ):
            self.assertIn(f"mark_stage {stage}", validator)
        fixture_position = validator.index('fixture_admin_key="admin-vm-gate-profile-206-')
        candidate_start_position = validator.index('mark_stage candidate_health')
        self.assertLess(fixture_position, candidate_start_position)
        self.assertIn(
            '[[ $current_stage == migration_assertion_* || $current_stage == runtime_assertion_* ]]',
            validator,
        )
        self.assertIn('-p "127.0.0.1::$server_port"', validator)
        self.assertIn('docker port "$probe_app" "$server_port/tcp"', validator)
        self.assertNotIn("probe_app_ip=", validator)
        self.assertIn("[[ $live_capability_status == 200 ]]", validator)

    def test_profile_208_requires_passkey_schema_evidence(self) -> None:
        validator = (DEPLOY_ROOT / "release" / "vm-validate.sh").read_text(encoding="utf-8")
        production = (DEPLOY_ROOT / "release" / "production.py").read_text(encoding="utf-8")
        gate = (DEPLOY_ROOT / "release" / "gate.py").read_text(encoding="utf-8")
        switch = (DEPLOY_ROOT / "maintenance" / "release" / "switch.sh").read_text(encoding="utf-8")

        self.assertIn("passkey_schema_verified:$passkey_schema_verified", validator)
        self.assertIn('"passkey_schema_verified"', production)
        self.assertIn("passkey_schema_verified=true", switch)
        self.assertIn("passkey_user_handles", validator)
        self.assertIn("passkey_credentials", validator)
        self.assertIn("passkey_credentials_user_id_idx", validator)
        self.assertIn("passkey_credentials_last_used_at_idx", validator)
        self.assertIn("mark_stage migration_assertion_profile_208_passkey_schema", validator)
        self.assertIn('expected_profile in {"208", "209", "210", "212", "213", "215", "232", "233", "234", "235", "236"}', gate)
        self.assertIn("profile 208 passkey schema evidence", gate)

    def test_profile_209_requires_user_usage_aggregation_schema_evidence(self) -> None:
        validator = (DEPLOY_ROOT / "release" / "vm-validate.sh").read_text(encoding="utf-8")
        production = (DEPLOY_ROOT / "release" / "production.py").read_text(encoding="utf-8")
        gate = (DEPLOY_ROOT / "release" / "gate.py").read_text(encoding="utf-8")
        switch = (DEPLOY_ROOT / "maintenance" / "release" / "switch.sh").read_text(encoding="utf-8")

        self.assertIn("user_usage_aggregation_schema_verified:$user_usage_aggregation_schema_verified", validator)
        self.assertIn('"user_usage_aggregation_schema_verified"', production)
        self.assertIn("user_usage_aggregation_schema_verified=true", switch)
        for schema_object in (
            "usage_dashboard_user_hourly",
            "usage_dashboard_user_daily",
            "usage_dashboard_user_backfill_state",
            "idx_usage_dashboard_user_hourly_user_bucket",
            "idx_usage_dashboard_user_daily_user_bucket",
        ):
            self.assertIn(schema_object, validator)
            self.assertIn(schema_object, switch)
        self.assertIn("mark_stage migration_assertion_profile_209_user_usage_aggregation_schema", validator)
        self.assertIn('expected_profile in {"209", "210", "212", "213", "215", "232", "233", "234", "235", "236"}', gate)
        self.assertIn("profile 209 user usage aggregation schema evidence", gate)

    def test_profile_194_gate_rejects_missing_prompt_audit_disabled_evidence(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            private_key = root / "private.pem"
            public_key = root / "public.pem"
            subprocess.run(["openssl", "genpkey", "-algorithm", "ED25519", "-out", str(private_key)], check=True, stdout=subprocess.DEVNULL)
            subprocess.run(["openssl", "pkey", "-in", str(private_key), "-pubout", "-out", str(public_key)], check=True, stdout=subprocess.DEVNULL)
            archive = root / "candidate.tar.gz"
            archive.write_bytes(b"candidate")
            manifest = self.manifest("runner", int(time.time()) + 60, "194")
            document = {
                "manifest": manifest,
                "evidence": {
                    "candidate_image_id": "sha256:" + "b" * 64,
                    "candidate_archive_sha256": hashlib.sha256(b"candidate").hexdigest(),
                    "integration_verified": True,
                    "vm_restore_verified": True,
                },
            }
            (root / "gate.json").write_bytes(canonical_json(document) + b"\n")
            subprocess.run(["openssl", "pkeyutl", "-sign", "-inkey", str(private_key), "-rawin", "-in", str(root / "gate.json"), "-out", str(root / "gate.sig")], check=True)
            with (
                mock.patch("release.gate.runner_checksum", return_value="runner"),
                mock.patch("release.gate.release_asset_checksums", return_value=self.release_assets()),
                mock.patch("release.gate.sha256_file", side_effect=self.release_unit_checksum),
                mock.patch("release.gate.get_profile", return_value=get_profile(manifest["profile"])),
            ):
                with self.assertRaisesRegex(RuntimeError, "Prompt Audit disabled-state evidence"):
                    verify_gate(root, public_key, "194")

    def test_profile_197_gate_rejects_missing_migration_evidence(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            private_key = root / "private.pem"
            public_key = root / "public.pem"
            subprocess.run(["openssl", "genpkey", "-algorithm", "ED25519", "-out", str(private_key)], check=True, stdout=subprocess.DEVNULL)
            subprocess.run(["openssl", "pkey", "-in", str(private_key), "-pubout", "-out", str(public_key)], check=True, stdout=subprocess.DEVNULL)
            archive = root / "candidate.tar.gz"
            archive.write_bytes(b"candidate")
            manifest = self.manifest("runner", int(time.time()) + 60, "197")
            document = {
                "manifest": manifest,
                "evidence": {
                    "candidate_image_id": "sha256:" + "b" * 64,
                    "candidate_archive_sha256": hashlib.sha256(b"candidate").hexdigest(),
                    "integration_verified": True,
                    "vm_restore_verified": True,
                    "prompt_audit_disabled": True,
                },
            }
            (root / "gate.json").write_bytes(canonical_json(document) + b"\n")
            subprocess.run(["openssl", "pkeyutl", "-sign", "-inkey", str(private_key), "-rawin", "-in", str(root / "gate.json"), "-out", str(root / "gate.sig")], check=True, stdout=subprocess.DEVNULL)
            with (
                mock.patch("release.gate.runner_checksum", return_value="runner"),
                mock.patch("release.gate.release_asset_checksums", return_value=self.release_assets()),
                mock.patch("release.gate.sha256_file", side_effect=self.release_unit_checksum),
                mock.patch("release.gate.get_profile", return_value=get_profile(manifest["profile"])),
            ):
                with self.assertRaisesRegex(RuntimeError, "migration 195 semantic evidence"):
                    verify_gate(root, public_key, "197")

    def test_profile_206_gate_rejects_missing_profile_evidence(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            private_key = root / "private.pem"
            public_key = root / "public.pem"
            subprocess.run(["openssl", "genpkey", "-algorithm", "ED25519", "-out", str(private_key)], check=True, stdout=subprocess.DEVNULL)
            subprocess.run(["openssl", "pkey", "-in", str(private_key), "-pubout", "-out", str(public_key)], check=True, stdout=subprocess.DEVNULL)
            archive = root / "candidate.tar.gz"
            archive.write_bytes(b"candidate")
            manifest = self.manifest("runner", int(time.time()) + 60, "206")
            inherited_evidence = {
                "candidate_image_id": "sha256:" + "b" * 64,
                "candidate_archive_sha256": hashlib.sha256(b"candidate").hexdigest(),
                "integration_verified": True,
                "vm_restore_verified": True,
                "prompt_audit_disabled": True,
                "migration_195_verified": True,
                "fixture_rejected": True,
                "restore_completed": True,
                "clean_preflight": True,
                "verified_replay": True,
                "verified_low_watermark_rejected": True,
                "managed_monitor_key_names_verified": True,
                "reasoning_effort_policy_verified": True,
                "vm_old_image_compatibility_verified": True,
                "vm_old_image_id": "sha256:" + "c" * 64,
                "alipay_mobile_precreate_migration_verified": True,
                "group_auth_cache_image_generation_verified": True,
                "composite_model_routes_verified": True,
                "session_id_columns_verified": True,
                "live_request_type_verified": True,
                "group_allow_live_verified": True,
                "live_runtime_capability_verified": True,
                # email_alias_index_verified is intentionally omitted.
            }
            document = {"manifest": manifest, "evidence": inherited_evidence}
            (root / "gate.json").write_bytes(canonical_json(document) + b"\n")
            subprocess.run(["openssl", "pkeyutl", "-sign", "-inkey", str(private_key), "-rawin", "-in", str(root / "gate.json"), "-out", str(root / "gate.sig")], check=True, stdout=subprocess.DEVNULL)
            with (
                mock.patch("release.gate.runner_checksum", return_value="runner"),
                mock.patch("release.gate.release_asset_checksums", return_value=self.release_assets()),
                mock.patch("release.gate.sha256_file", side_effect=self.release_unit_checksum),
                mock.patch("release.gate.get_profile", return_value=get_profile(manifest["profile"])),
            ):
                with self.assertRaisesRegex(RuntimeError, "profile 206 migration semantic evidence"):
                    verify_gate(root, public_key, "206")

    def test_profile_208_gate_rejects_missing_passkey_schema_evidence(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            private_key = root / "private.pem"
            public_key = root / "public.pem"
            subprocess.run(["openssl", "genpkey", "-algorithm", "ED25519", "-out", str(private_key)], check=True, stdout=subprocess.DEVNULL)
            subprocess.run(["openssl", "pkey", "-in", str(private_key), "-pubout", "-out", str(public_key)], check=True, stdout=subprocess.DEVNULL)
            archive = root / "candidate.tar.gz"
            archive.write_bytes(b"candidate")
            manifest = self.manifest("runner", int(time.time()) + 60, "208")
            inherited_evidence = {
                "candidate_image_id": "sha256:" + "b" * 64,
                "candidate_archive_sha256": hashlib.sha256(b"candidate").hexdigest(),
                "integration_verified": True,
                "vm_restore_verified": True,
                "prompt_audit_disabled": True,
                "migration_195_verified": True,
                "fixture_rejected": True,
                "restore_completed": True,
                "clean_preflight": True,
                "verified_replay": True,
                "verified_low_watermark_rejected": True,
                "managed_monitor_key_names_verified": True,
                "reasoning_effort_policy_verified": True,
                "vm_old_image_compatibility_verified": True,
                "vm_old_image_id": "sha256:" + "c" * 64,
                "alipay_mobile_precreate_migration_verified": True,
                "group_auth_cache_image_generation_verified": True,
                "composite_model_routes_verified": True,
                "session_id_columns_verified": True,
                "live_request_type_verified": True,
                "group_allow_live_verified": True,
                "email_alias_index_verified": True,
                "live_runtime_capability_verified": True,
                # passkey_schema_verified is intentionally omitted.
            }
            document = {"manifest": manifest, "evidence": inherited_evidence}
            (root / "gate.json").write_bytes(canonical_json(document) + b"\n")
            subprocess.run(["openssl", "pkeyutl", "-sign", "-inkey", str(private_key), "-rawin", "-in", str(root / "gate.json"), "-out", str(root / "gate.sig")], check=True, stdout=subprocess.DEVNULL)
            with (
                mock.patch("release.gate.runner_checksum", return_value="runner"),
                mock.patch("release.gate.release_asset_checksums", return_value=self.release_assets()),
                mock.patch("release.gate.sha256_file", side_effect=self.release_unit_checksum),
                mock.patch("release.gate.get_profile", return_value=get_profile(manifest["profile"])),
            ):
                with self.assertRaisesRegex(RuntimeError, "profile 208 passkey schema evidence"):
                    verify_gate(root, public_key, "208")

    def test_profile_209_and_210_gate_reject_missing_user_usage_aggregation_schema_evidence(self) -> None:
        for profile in ("209", "210", "212", "213", "215"):
            with self.subTest(profile=profile), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                private_key = root / "private.pem"
                public_key = root / "public.pem"
                subprocess.run(["openssl", "genpkey", "-algorithm", "ED25519", "-out", str(private_key)], check=True, stdout=subprocess.DEVNULL)
                subprocess.run(["openssl", "pkey", "-in", str(private_key), "-pubout", "-out", str(public_key)], check=True, stdout=subprocess.DEVNULL)
                archive = root / "candidate.tar.gz"
                archive.write_bytes(b"candidate")
                manifest = self.manifest("runner", int(time.time()) + 60, profile)
                inherited_evidence = {
                    "candidate_image_id": "sha256:" + "b" * 64,
                    "candidate_archive_sha256": hashlib.sha256(b"candidate").hexdigest(),
                    "integration_verified": True,
                    "vm_restore_verified": True,
                    "prompt_audit_disabled": True,
                    "migration_195_verified": True,
                    "fixture_rejected": True,
                    "restore_completed": True,
                    "clean_preflight": True,
                    "verified_replay": True,
                    "verified_low_watermark_rejected": True,
                    "managed_monitor_key_names_verified": True,
                    "reasoning_effort_policy_verified": True,
                    "vm_old_image_compatibility_verified": True,
                    "vm_old_image_id": "sha256:" + "c" * 64,
                    "alipay_mobile_precreate_migration_verified": True,
                    "group_auth_cache_image_generation_verified": True,
                    "composite_model_routes_verified": True,
                    "session_id_columns_verified": True,
                    "live_request_type_verified": True,
                    "group_allow_live_verified": True,
                    "email_alias_index_verified": True,
                    "live_runtime_capability_verified": True,
                    "passkey_schema_verified": True,
                    # user_usage_aggregation_schema_verified is intentionally omitted.
                }
                document = {"manifest": manifest, "evidence": inherited_evidence}
                (root / "gate.json").write_bytes(canonical_json(document) + b"\n")
                subprocess.run(["openssl", "pkeyutl", "-sign", "-inkey", str(private_key), "-rawin", "-in", str(root / "gate.json"), "-out", str(root / "gate.sig")], check=True, stdout=subprocess.DEVNULL)
                with (
                    mock.patch("release.gate.runner_checksum", return_value="runner"),
                    mock.patch("release.gate.release_asset_checksums", return_value=self.release_assets()),
                    mock.patch("release.gate.sha256_file", side_effect=self.release_unit_checksum),
                    mock.patch("release.gate.get_profile", return_value=get_profile(manifest["profile"])),
                ):
                    with self.assertRaisesRegex(RuntimeError, "profile 209 user usage aggregation schema evidence"):
                        verify_gate(root, public_key, profile)

    def test_profile_212_gate_rejects_missing_profit_control_evidence(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            private_key = root / "private.pem"
            public_key = root / "public.pem"
            subprocess.run(["openssl", "genpkey", "-algorithm", "ED25519", "-out", str(private_key)], check=True, stdout=subprocess.DEVNULL)
            subprocess.run(["openssl", "pkey", "-in", str(private_key), "-pubout", "-out", str(public_key)], check=True, stdout=subprocess.DEVNULL)
            (root / "candidate.tar.gz").write_bytes(b"candidate")
            manifest = self.manifest("runner", int(time.time()) + 60, "212")
            inherited_evidence = {
                "candidate_image_id": "sha256:" + "b" * 64,
                "candidate_archive_sha256": hashlib.sha256(b"candidate").hexdigest(),
                "integration_verified": True,
                "vm_restore_verified": True,
                "prompt_audit_disabled": True,
                "migration_195_verified": True,
                "fixture_rejected": True,
                "restore_completed": True,
                "clean_preflight": True,
                "verified_replay": True,
                "verified_low_watermark_rejected": True,
                "managed_monitor_key_names_verified": True,
                "reasoning_effort_policy_verified": True,
                "vm_old_image_compatibility_verified": True,
                "vm_old_image_id": "sha256:" + "c" * 64,
                "alipay_mobile_precreate_migration_verified": True,
                "group_auth_cache_image_generation_verified": True,
                "composite_model_routes_verified": True,
                "session_id_columns_verified": True,
                "live_request_type_verified": True,
                "group_allow_live_verified": True,
                "email_alias_index_verified": True,
                "live_runtime_capability_verified": True,
                "passkey_schema_verified": True,
                "user_usage_aggregation_schema_verified": True,
            }
            document = {"manifest": manifest, "evidence": inherited_evidence}
            (root / "gate.json").write_bytes(canonical_json(document) + b"\n")
            subprocess.run(["openssl", "pkeyutl", "-sign", "-inkey", str(private_key), "-rawin", "-in", str(root / "gate.json"), "-out", str(root / "gate.sig")], check=True, stdout=subprocess.DEVNULL)
            with (
                mock.patch("release.gate.runner_checksum", return_value="runner"),
                mock.patch("release.gate.release_asset_checksums", return_value=self.release_assets()),
                mock.patch("release.gate.sha256_file", side_effect=self.release_unit_checksum),
                mock.patch("release.gate.get_profile", return_value=get_profile(manifest["profile"])),
            ):
                with self.assertRaisesRegex(RuntimeError, "profile 212 migration status evidence"):
                    verify_gate(root, public_key, "212")
                inherited_evidence.update(migration_211_status="absent", migration_212_status="absent")
                document = {"manifest": manifest, "evidence": inherited_evidence}
                (root / "gate.json").write_bytes(canonical_json(document) + b"\n")
                subprocess.run(["openssl", "pkeyutl", "-sign", "-inkey", str(private_key), "-rawin", "-in", str(root / "gate.json"), "-out", str(root / "gate.sig")], check=True, stdout=subprocess.DEVNULL)
                with self.assertRaisesRegex(RuntimeError, "profile 212 profit-control migration evidence"):
                    verify_gate(root, public_key, "212")

    def test_profile_215_gate_requires_migration_and_schema_evidence(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            private_key = root / "private.pem"
            public_key = root / "public.pem"
            subprocess.run(["openssl", "genpkey", "-algorithm", "ED25519", "-out", str(private_key)], check=True, stdout=subprocess.DEVNULL)
            subprocess.run(["openssl", "pkey", "-in", str(private_key), "-pubout", "-out", str(public_key)], check=True, stdout=subprocess.DEVNULL)
            (root / "candidate.tar.gz").write_bytes(b"candidate")
            manifest = self.manifest("runner", int(time.time()) + 60, "215")
            evidence = {
                "candidate_image_id": "sha256:" + "b" * 64,
                "candidate_archive_sha256": hashlib.sha256(b"candidate").hexdigest(),
                "integration_verified": True,
                "vm_restore_verified": True,
                "prompt_audit_disabled": True,
                "migration_195_verified": True,
                "fixture_rejected": True,
                "restore_completed": True,
                "clean_preflight": True,
                "verified_replay": True,
                "verified_low_watermark_rejected": True,
                "managed_monitor_key_names_verified": True,
                "reasoning_effort_policy_verified": True,
                "vm_old_image_compatibility_verified": True,
                "vm_old_image_id": "sha256:" + "c" * 64,
                "alipay_mobile_precreate_migration_verified": True,
                "group_auth_cache_image_generation_verified": True,
                "composite_model_routes_verified": True,
                "session_id_columns_verified": True,
                "live_request_type_verified": True,
                "group_allow_live_verified": True,
                "email_alias_index_verified": True,
                "live_runtime_capability_verified": True,
                "passkey_schema_verified": True,
                "user_usage_aggregation_schema_verified": True,
                "migration_211_status": "verified",
                "migration_212_status": "verified",
                "group_profit_control_schema_verified": True,
                "group_profit_auth_cache_trigger_verified": True,
            }

            def sign() -> None:
                (root / "gate.json").write_bytes(canonical_json({"manifest": manifest, "evidence": evidence}) + b"\n")
                subprocess.run(["openssl", "pkeyutl", "-sign", "-inkey", str(private_key), "-rawin", "-in", str(root / "gate.json"), "-out", str(root / "gate.sig")], check=True, stdout=subprocess.DEVNULL)

            with (
                mock.patch("release.gate.runner_checksum", return_value="runner"),
                mock.patch("release.gate.release_asset_checksums", return_value=self.release_assets()),
                mock.patch("release.gate.sha256_file", side_effect=self.release_unit_checksum),
                mock.patch("release.gate.get_profile", return_value=get_profile(manifest["profile"])),
            ):
                sign()
                with self.assertRaisesRegex(RuntimeError, "profile 215 migration status evidence"):
                    verify_gate(root, public_key, "215")
                evidence.update(migration_214_status="absent", migration_215_status="verified")
                sign()
                with self.assertRaisesRegex(RuntimeError, "upstream-model column evidence"):
                    verify_gate(root, public_key, "215")
                evidence["usage_log_upstream_model_columns_verified"] = True
                sign()
                with self.assertRaisesRegex(RuntimeError, "mismatch index evidence"):
                    verify_gate(root, public_key, "215")
                evidence["usage_log_upstream_model_mismatch_index_verified"] = True
                sign()
                self.assertEqual(verify_gate(root, public_key, "215")["manifest"]["profile"], "215")

    def test_profile_232_gate_requires_compatibility_status_and_semantic_evidence(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            private_key = root / "private.pem"
            public_key = root / "public.pem"
            subprocess.run(["openssl", "genpkey", "-algorithm", "ED25519", "-out", str(private_key)], check=True, stdout=subprocess.DEVNULL)
            subprocess.run(["openssl", "pkey", "-in", str(private_key), "-pubout", "-out", str(public_key)], check=True, stdout=subprocess.DEVNULL)
            (root / "candidate.tar.gz").write_bytes(b"candidate")
            profile = get_profile("232")
            manifest = {
                **self.manifest("runner", int(time.time()) + 60, "215"),
                "profile": "232",
                "release_id": "232-aaaaaaaaaaaa-1-aaaaaaaa",
                "version": profile["version"],
                "compatibility_version": profile["compatibility_version"],
                "compatibility_commit": profile["compatibility_commit"],
                "compatibility_image_id": profile["compatibility_image_id"],
            }
            evidence = {
                "candidate_image_id": "sha256:" + "b" * 64,
                "candidate_archive_sha256": hashlib.sha256(b"candidate").hexdigest(),
                "integration_verified": True,
                "vm_restore_verified": True,
                "prompt_audit_disabled": True,
                "migration_195_verified": True,
                "fixture_rejected": True,
                "restore_completed": True,
                "clean_preflight": True,
                "verified_replay": True,
                "verified_low_watermark_rejected": True,
                "managed_monitor_key_names_verified": True,
                "reasoning_effort_policy_verified": True,
                "vm_old_image_compatibility_verified": True,
                "vm_old_image_id": profile["compatibility_image_id"],
                "alipay_mobile_precreate_migration_verified": True,
                "group_auth_cache_image_generation_verified": True,
                "composite_model_routes_verified": True,
                "session_id_columns_verified": True,
                "live_request_type_verified": True,
                "group_allow_live_verified": True,
                "email_alias_index_verified": True,
                "live_runtime_capability_verified": True,
                "passkey_schema_verified": True,
                "user_usage_aggregation_schema_verified": True,
                "migration_211_status": "verified",
                "migration_212_status": "verified",
                "group_profit_control_schema_verified": True,
                "group_profit_auth_cache_trigger_verified": True,
                "migration_214_status": "verified",
                "migration_215_status": "verified",
                "usage_log_upstream_model_columns_verified": True,
                "usage_log_upstream_model_mismatch_index_verified": True,
            }

            def sign() -> None:
                (root / "gate.json").write_bytes(canonical_json({"manifest": manifest, "evidence": evidence}) + b"\n")
                subprocess.run(["openssl", "pkeyutl", "-sign", "-inkey", str(private_key), "-rawin", "-in", str(root / "gate.json"), "-out", str(root / "gate.sig")], check=True, stdout=subprocess.DEVNULL)

            with (
                mock.patch("release.gate.runner_checksum", return_value="runner"),
                mock.patch("release.gate.release_asset_checksums", return_value=self.release_assets()),
                mock.patch("release.gate.sha256_file", side_effect=self.release_unit_checksum),
                mock.patch("release.gate.get_profile", return_value=profile),
                mock.patch("release.gate.validate_manifest_profile_contract"),
            ):
                sign()
                with self.assertRaisesRegex(RuntimeError, "migration 216 status evidence"):
                    verify_gate(root, public_key, "232")
                for number in range(216, 233):
                    evidence[f"migration_{number}_status"] = "verified"
                sign()
                with self.assertRaisesRegex(RuntimeError, "profile 232 migration semantic evidence"):
                    verify_gate(root, public_key, "232")
                for field in (
                    "channel_monitor_v2_schema_verified",
                    "channel_monitor_v2_defaults_verified",
                    "group_media_pricing_schema_verified",
                    "group_media_auth_cache_trigger_verified",
                    "migration_232_data_plan_verified",
                    "migration_232_postflight_verified",
                ):
                    evidence[field] = True
                sign()
                self.assertEqual(verify_gate(root, public_key, "232")["manifest"]["profile"], "232")
                evidence["vm_old_image_id"] = "sha256:" + "d" * 64
                sign()
                with self.assertRaisesRegex(RuntimeError, "compatibility image does not match"):
                    verify_gate(root, public_key, "232")

    def test_profile_233_gate_requires_unique_binding_preflight_and_postflight(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            private_key = root / "private.pem"
            public_key = root / "public.pem"
            subprocess.run(["openssl", "genpkey", "-algorithm", "ED25519", "-out", str(private_key)], check=True, stdout=subprocess.DEVNULL)
            subprocess.run(["openssl", "pkey", "-in", str(private_key), "-pubout", "-out", str(public_key)], check=True, stdout=subprocess.DEVNULL)
            (root / "candidate.tar.gz").write_bytes(b"candidate")
            profile = get_profile("233")
            manifest = {
                # Build the generic manifest from the latest committed profile.
                # Migration 233 is intentionally still uncommitted while this
                # behavior test runs, and the profile contract itself is mocked
                # below so the test can focus on Gate evidence semantics.
                **self.manifest("runner", int(time.time()) + 60, "232"),
                "profile": "233",
                "release_id": "233-aaaaaaaaaaaa-1-aaaaaaaa",
                "version": profile["version"],
                "compatibility_version": profile["compatibility_version"],
                "compatibility_commit": profile["compatibility_commit"],
                "compatibility_image_id": profile["compatibility_image_id"],
            }
            evidence = self.profile_232_evidence(profile)

            def sign() -> None:
                (root / "gate.json").write_bytes(canonical_json({"manifest": manifest, "evidence": evidence}) + b"\n")
                subprocess.run(["openssl", "pkeyutl", "-sign", "-inkey", str(private_key), "-rawin", "-in", str(root / "gate.json"), "-out", str(root / "gate.sig")], check=True, stdout=subprocess.DEVNULL)

            with (
                mock.patch("release.gate.runner_checksum", return_value="runner"),
                mock.patch("release.gate.release_asset_checksums", return_value=self.release_assets()),
                mock.patch("release.gate.sha256_file", side_effect=self.release_unit_checksum),
                mock.patch("release.gate.get_profile", return_value=profile),
                mock.patch("release.gate.validate_manifest_profile_contract"),
            ):
                sign()
                with self.assertRaisesRegex(RuntimeError, "migration 233 status evidence"):
                    verify_gate(root, public_key, "233")

                evidence["migration_233_status"] = "verified"
                sign()
                with self.assertRaisesRegex(RuntimeError, "profile 233 migration semantic evidence"):
                    verify_gate(root, public_key, "233")

                evidence["migration_233_preflight_verified"] = True
                evidence["migration_233_postflight_verified"] = True
                sign()
                self.assertEqual(verify_gate(root, public_key, "233")["manifest"]["profile"], "233")

                evidence["vm_old_image_id"] = "sha256:" + "d" * 64
                sign()
                with self.assertRaisesRegex(RuntimeError, "compatibility image does not match"):
                    verify_gate(root, public_key, "233")

    def test_vm_post_build_space_gate_does_not_double_count_image(self) -> None:
        validator = (DEPLOY_ROOT / "release" / "vm-validate.sh").read_text(encoding="utf-8")
        self.assertIn("required_before=$((database_size * 2 + current_image_size * 2 + 1073741824))", validator)
        self.assertIn("required_free=$((database_size * 2 + candidate_size + 1073741824))", validator)
        self.assertNotIn("required_free=$((database_size + candidate_size", validator)

    def test_gate_rejects_archive_replacement_and_expiry(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            private_key = root / "private.pem"
            public_key = root / "public.pem"
            subprocess.run(["openssl", "genpkey", "-algorithm", "ED25519", "-out", str(private_key)], check=True, stdout=subprocess.DEVNULL)
            subprocess.run(["openssl", "pkey", "-in", str(private_key), "-pubout", "-out", str(public_key)], check=True, stdout=subprocess.DEVNULL)
            archive = root / "candidate.tar.gz"
            archive.write_bytes(b"candidate")
            digest = hashlib.sha256(archive.read_bytes()).hexdigest()
            document = {
                "manifest": self.manifest("runner", int(time.time()) + 60),
                "evidence": {
                    "candidate_image_id": "sha256:" + "b" * 64,
                    "candidate_archive_sha256": digest,
                    "integration_verified": True,
                    "vm_restore_verified": True,
                },
            }
            (root / "gate.json").write_bytes(canonical_json(document) + b"\n")
            subprocess.run(["openssl", "pkeyutl", "-sign", "-inkey", str(private_key), "-rawin", "-in", str(root / "gate.json"), "-out", str(root / "gate.sig")], check=True)
            with mock.patch("release.gate.runner_checksum", return_value="runner"), mock.patch("release.gate.release_asset_checksums", return_value=self.release_assets()), mock.patch("release.gate.sha256_file", side_effect=self.release_unit_checksum):
                verify_gate(root, public_key, "182")
                archive.write_bytes(b"replaced")
                with self.assertRaisesRegex(RuntimeError, "archive checksum"):
                    verify_gate(root, public_key, "182")
                archive.write_bytes(b"candidate")
                document["manifest"]["expires_at"] = int(time.time()) - 1
                (root / "gate.json").write_bytes(canonical_json(document) + b"\n")
                subprocess.run(["openssl", "pkeyutl", "-sign", "-inkey", str(private_key), "-rawin", "-in", str(root / "gate.json"), "-out", str(root / "gate.sig")], check=True)
                with self.assertRaisesRegex(RuntimeError, "expired"):
                    verify_gate(root, public_key, "182")
                self.assertEqual(verify_gate(root, public_key, "182", allow_expired=True)["manifest"]["expires_at"], document["manifest"]["expires_at"])

    def test_gate_binds_profile_version_release_id_and_migration_manifest(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            private_key = root / "private.pem"
            public_key = root / "public.pem"
            subprocess.run(["openssl", "genpkey", "-algorithm", "ED25519", "-out", str(private_key)], check=True, stdout=subprocess.DEVNULL)
            subprocess.run(["openssl", "pkey", "-in", str(private_key), "-pubout", "-out", str(public_key)], check=True, stdout=subprocess.DEVNULL)
            archive = root / "candidate.tar.gz"
            archive.write_bytes(b"candidate")
            manifest = self.manifest("runner", int(time.time()) + 60, "182")
            evidence = {
                "candidate_image_id": "sha256:" + "b" * 64,
                "candidate_archive_sha256": hashlib.sha256(b"candidate").hexdigest(),
                "integration_verified": True,
                "vm_restore_verified": True,
            }

            def sign() -> None:
                (root / "gate.json").write_bytes(canonical_json({"manifest": manifest, "evidence": evidence}) + b"\n")
                subprocess.run(["openssl", "pkeyutl", "-sign", "-inkey", str(private_key), "-rawin", "-in", str(root / "gate.json"), "-out", str(root / "gate.sig")], check=True, stdout=subprocess.DEVNULL)

            with (
                mock.patch("release.gate.runner_checksum", return_value="runner"),
                mock.patch("release.gate.release_asset_checksums", return_value=self.release_assets()),
                mock.patch("release.gate.sha256_file", side_effect=self.release_unit_checksum),
            ):
                sign()
                self.assertEqual(verify_gate(root, public_key, "182")["manifest"]["profile"], "182")
                for field, bad_value, message in (
                    ("version", "0.0.0-bad", "version does not match"),
                    ("release_id", "182-aaaaaaaaaaaa-1-aaaaaaaa", "release ID does not match"),
                    ("migrations", [], "ordered migrations do not match"),
                    ("migration_sha256", {}, "migration checksums do not match"),
                ):
                    original = manifest[field]
                    manifest[field] = bad_value
                    sign()
                    with self.assertRaisesRegex(RuntimeError, message):
                        verify_gate(root, public_key, "182")
                    manifest[field] = original

    def test_gate_rejects_runner_version_mismatch(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            private_key = root / "private.pem"
            public_key = root / "public.pem"
            subprocess.run(["openssl", "genpkey", "-algorithm", "ED25519", "-out", str(private_key)], check=True, stdout=subprocess.DEVNULL)
            subprocess.run(["openssl", "pkey", "-in", str(private_key), "-pubout", "-out", str(public_key)], check=True, stdout=subprocess.DEVNULL)
            archive = root / "candidate.tar.gz"
            archive.write_bytes(b"candidate")
            document = {
                "manifest": self.manifest("old", int(time.time()) + 60),
                "evidence": {"candidate_image_id": "sha256:" + "b" * 64, "candidate_archive_sha256": hashlib.sha256(b"candidate").hexdigest(), "integration_verified": True, "vm_restore_verified": True},
            }
            (root / "gate.json").write_bytes(canonical_json(document) + b"\n")
            subprocess.run(["openssl", "pkeyutl", "-sign", "-inkey", str(private_key), "-rawin", "-in", str(root / "gate.json"), "-out", str(root / "gate.sig")], check=True)
            with mock.patch("release.gate.runner_checksum", return_value="new"), mock.patch("release.gate.release_asset_checksums", return_value=self.release_assets()), mock.patch("release.gate.sha256_file", side_effect=self.release_unit_checksum):
                with self.assertRaisesRegex(RuntimeError, "different release runner"):
                    verify_gate(root, public_key, "182")

    def test_historical_deploy_v1_gate_uses_commit_assets_only_for_audit_or_recovery(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            private_key = root / "private.pem"
            public_key = root / "public.pem"
            subprocess.run(["openssl", "genpkey", "-algorithm", "ED25519", "-out", str(private_key)], check=True, stdout=subprocess.DEVNULL)
            subprocess.run(["openssl", "pkey", "-in", str(private_key), "-pubout", "-out", str(public_key)], check=True, stdout=subprocess.DEVNULL)
            archive = root / "candidate.tar.gz"
            archive.write_bytes(b"candidate")
            manifest = self.manifest("a" * 64, int(time.time()) + 60)
            manifest.pop("release_asset_layout")
            manifest["release_asset_sha256"] = self.release_assets(LAYOUT_DEPLOY_V1)
            document = {
                "manifest": manifest,
                "evidence": {
                    "candidate_image_id": "sha256:" + "b" * 64,
                    "candidate_archive_sha256": hashlib.sha256(b"candidate").hexdigest(),
                    "integration_verified": True,
                    "vm_restore_verified": True,
                },
            }
            (root / "gate.json").write_bytes(canonical_json(document) + b"\n")
            subprocess.run(
                ["openssl", "pkeyutl", "-sign", "-inkey", str(private_key), "-rawin", "-in", str(root / "gate.json"), "-out", str(root / "gate.sig")],
                check=True,
                stdout=subprocess.DEVNULL,
            )
            with mock.patch(
                "release.gate.release_asset_checksums",
                return_value=self.release_assets(LAYOUT_DEPLOY_V1),
            ) as committed_assets, mock.patch("release.gate.runner_checksum") as current_runner, mock.patch("release.gate.sha256_file") as current_asset:
                with self.assertRaisesRegex(RuntimeError, "restricted to audit or recovery"):
                    verify_gate(root, public_key, "182")
                verified = verify_gate(root, public_key, "182", allow_historical_runner=True)
            self.assertEqual(verified["manifest"].get("release_asset_layout"), None)
            committed_assets.assert_called_once_with(manifest["commit_sha"], LAYOUT_DEPLOY_V1)
            current_runner.assert_not_called()
            current_asset.assert_not_called()


if __name__ == "__main__":
    unittest.main()
