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
from release.manifest import git_blob_sha256, migration_checksums, release_asset_checksums
from release.profiles import get_profile
from release.state import RunLock, RunState


class ReleaseCoreTest(unittest.TestCase):
    @staticmethod
    def release_unit_checksum(path: Path) -> str:
        return {
            "vm-validate.sh": "validator",
            "sign-gate.sh": "gate-signer",
            "sign-dr-evidence.sh": "dr-signer",
        }[path.name]

    def manifest(self, runner: str, expires_at: int) -> dict:
        profile = get_profile("182")
        return {
            "commit_sha": "a" * 40,
            "profile": "182",
            "runner_sha256": runner,
            "vm_validator_sha256": "validator",
            "vm_gate_signer_sha256": "gate-signer",
            "vm_dr_signer_sha256": "dr-signer",
            "release_asset_sha256": {"asset": "digest"},
            "origin": profile["origin"],
            "vm_identity": profile["vm_identity"],
            "expires_at": expires_at,
        }

    def test_atomic_write_replaces_complete_content(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "state.json"
            atomic_write(path, b"old\n")
            atomic_write(path, b"new\n")
            self.assertEqual(path.read_bytes(), b"new\n")
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
        relative_path = "deploy/release/cli.py"
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            asset = root / relative_path
            asset.parent.mkdir(parents=True)
            asset.write_bytes(b"line one\r\nline two\r\n")
            with (
                mock.patch("release.manifest.workspace_root", return_value=root),
                mock.patch("release.manifest.release_asset_paths", return_value=[asset]),
                mock.patch(
                    "release.manifest.subprocess.check_output",
                    return_value=b"line one\nline two\n",
                ) as check_output,
            ):
                checksums = release_asset_checksums(commit)

        self.assertEqual(checksums[relative_path], hashlib.sha256(b"line one\nline two\n").hexdigest())
        check_output.assert_called_once_with(
            ["git", "show", f"{commit}:{relative_path}"],
            cwd=root,
        )

    def test_git_blob_checksum_rejects_short_commit(self) -> None:
        with self.assertRaisesRegex(ValueError, "complete 40-character"):
            git_blob_sha256("abc123", "deploy/release/cli.py")

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
        expected_release_pattern = "(182|187|191|192|194|195|197|198|199|202|206|207|208|209)"
        expected_profile_check = "$profile == 182 || $profile == 187 || $profile == 191 || $profile == 192 || $profile == 194 || $profile == 195 || $profile == 197 || $profile == 198 || $profile == 199 || $profile == 202 || $profile == 206 || $profile == 207 || $profile == 208 || $profile == 209"
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

    def test_profile_209_migration_defines_permanent_user_usage_aggregation_contract(self) -> None:
        migration = (DEPLOY_ROOT.parent / "backend" / "migrations" / "209_user_usage_aggregation.sql").read_text(encoding="utf-8")
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
        self.assertIn('expected_profile in {"194", "195", "197", "198", "199", "202", "206", "207", "208", "209"}', gate)

    def test_profile_195_requires_semantic_migration_evidence(self) -> None:
        validator = (DEPLOY_ROOT / "release" / "vm-validate.sh").read_text(encoding="utf-8")
        production = (DEPLOY_ROOT / "release" / "production.py").read_text(encoding="utf-8")
        gate = (DEPLOY_ROOT / "release" / "gate.py").read_text(encoding="utf-8")
        switch = (DEPLOY_ROOT / "maintenance" / "release" / "switch.sh").read_text(encoding="utf-8")
        assertion = (DEPLOY_ROOT / "maintenance" / "release" / "migration-195-assert.sh").read_text(encoding="utf-8")

        self.assertIn("migration_195_verified:$migration_195_verified", validator)
        self.assertIn("managed_monitor_key_names_verified:$managed_monitor_key_names_verified", validator)
        self.assertIn('bash "$source_dir/deploy/maintenance/release/migration-195-assert.sh" preflight', validator)
        self.assertIn("MIGRATION_STATUS=absent", validator)
        self.assertIn("MIGRATION_STATUS=verified", validator)
        self.assertIn("migration_195_verified_state", validator)
        self.assertIn("verified_replay=true", validator)
        self.assertIn("verified_low_watermark_rejected=true", validator)
        self.assertIn("probe_migration_195_recorded", validator)
        self.assertIn("migration_195_status=verified", validator)
        self.assertIn('MIGRATION_STATUS="$migration_195_status"', validator)
        self.assertIn('redis-cli SET sched:v2:outbox:watermark "$probe_outbox_highwater"', validator)
        self.assertIn('[[ $consumed_event_id == 0 || $sentinel_event_id -gt $consumed_event_id ]]', validator)
        self.assertIn("ASSERT_DB_USER=\"$database_owner\"", validator)
        self.assertNotIn("ASSERT_DB_USER=\"$database_user\"", validator)
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
        self.assertIn('expected_profile in {"195", "197", "198", "199", "202", "206", "207", "208", "209"}', gate)
        self.assertIn('self.profile["name"] not in {"195", "197", "198", "199", "202", "206", "207", "208", "209"}', production)
        self.assertIn('[[ $profile == 195 || $profile == 197 || $profile == 198 || $profile == 199 || $profile == 202 || $profile == 206 || $profile == 207 || $profile == 208 || $profile == 209 ]]', switch)
        self.assertIn('[[ $profile == 195 || $profile == 197 || $profile == 198 || $profile == 199 || $profile == 202 || $profile == 206 || $profile == 207 || $profile == 208 || $profile == 209 ]]', assertion)
        self.assertIn('expected_profile in {"198", "199", "202", "206", "207", "208", "209"}', gate)
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
        self.assertIn('expected_profile in {"199", "202", "206", "207", "208", "209"}', gate)
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
        self.assertIn('expected_profile in {"202", "206", "207", "208", "209"}', gate)
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
        self.assertIn('expected_profile in {"206", "207", "208", "209"}', gate)
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
        self.assertIn('expected_profile in {"208", "209"}', gate)
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
        self.assertIn('expected_profile == "209"', gate)
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
            manifest = self.manifest("runner", int(time.time()) + 60)
            manifest["profile"] = "194"
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
                mock.patch("release.gate.release_asset_checksums", return_value={"asset": "digest"}),
                mock.patch("release.gate.sha256_file", side_effect=self.release_unit_checksum),
                mock.patch("release.gate.get_profile", return_value={"origin": manifest["origin"], "vm_identity": manifest["vm_identity"]}),
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
            manifest = self.manifest("runner", int(time.time()) + 60)
            manifest["profile"] = "197"
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
                mock.patch("release.gate.release_asset_checksums", return_value={"asset": "digest"}),
                mock.patch("release.gate.sha256_file", side_effect=self.release_unit_checksum),
                mock.patch("release.gate.get_profile", return_value={"origin": manifest["origin"], "vm_identity": manifest["vm_identity"]}),
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
            manifest = self.manifest("runner", int(time.time()) + 60)
            manifest["profile"] = "206"
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
                mock.patch("release.gate.release_asset_checksums", return_value={"asset": "digest"}),
                mock.patch("release.gate.sha256_file", side_effect=self.release_unit_checksum),
                mock.patch("release.gate.get_profile", return_value={"origin": manifest["origin"], "vm_identity": manifest["vm_identity"]}),
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
            manifest = self.manifest("runner", int(time.time()) + 60)
            manifest["profile"] = "208"
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
                mock.patch("release.gate.release_asset_checksums", return_value={"asset": "digest"}),
                mock.patch("release.gate.sha256_file", side_effect=self.release_unit_checksum),
                mock.patch("release.gate.get_profile", return_value={"origin": manifest["origin"], "vm_identity": manifest["vm_identity"]}),
            ):
                with self.assertRaisesRegex(RuntimeError, "profile 208 passkey schema evidence"):
                    verify_gate(root, public_key, "208")

    def test_profile_209_gate_rejects_missing_user_usage_aggregation_schema_evidence(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            private_key = root / "private.pem"
            public_key = root / "public.pem"
            subprocess.run(["openssl", "genpkey", "-algorithm", "ED25519", "-out", str(private_key)], check=True, stdout=subprocess.DEVNULL)
            subprocess.run(["openssl", "pkey", "-in", str(private_key), "-pubout", "-out", str(public_key)], check=True, stdout=subprocess.DEVNULL)
            archive = root / "candidate.tar.gz"
            archive.write_bytes(b"candidate")
            manifest = self.manifest("runner", int(time.time()) + 60)
            manifest["profile"] = "209"
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
                mock.patch("release.gate.release_asset_checksums", return_value={"asset": "digest"}),
                mock.patch("release.gate.sha256_file", side_effect=self.release_unit_checksum),
                mock.patch("release.gate.get_profile", return_value={"origin": manifest["origin"], "vm_identity": manifest["vm_identity"]}),
            ):
                with self.assertRaisesRegex(RuntimeError, "profile 209 user usage aggregation schema evidence"):
                    verify_gate(root, public_key, "209")

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
            with mock.patch("release.gate.runner_checksum", return_value="runner"), mock.patch("release.gate.release_asset_checksums", return_value={"asset": "digest"}), mock.patch("release.gate.sha256_file", side_effect=self.release_unit_checksum):
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
            with mock.patch("release.gate.runner_checksum", return_value="new"), mock.patch("release.gate.release_asset_checksums", return_value={"asset": "digest"}), mock.patch("release.gate.sha256_file", side_effect=self.release_unit_checksum):
                with self.assertRaisesRegex(RuntimeError, "different release runner"):
                    verify_gate(root, public_key, "182")


if __name__ == "__main__":
    unittest.main()
