from __future__ import annotations

import sys
import unittest
from pathlib import Path
from unittest import mock


DEPLOY_ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(DEPLOY_ROOT))

from release import bootstrap
from release.doctor import ReleaseDoctor
from release.manifest import create_manifest
from release.profiles import get_profile


class SignerAssetTest(unittest.TestCase):
    def test_bootstrap_installs_the_complete_release_unit(self) -> None:
        script = (DEPLOY_ROOT / "release" / "bootstrap_vm_signer.sh").read_text(encoding="utf-8")
        self.assertIn("GATE_SIGNER_SOURCE", script)
        self.assertIn("DR_SIGNER_SOURCE", script)
        self.assertIn("bash -n", script)
        self.assertIn("openssl pkeyutl -verify", script)
        self.assertIn("flock 9", script)
        self.assertIn("activation_complete != true", script)
        self.assertIn("previous-$asset", script)
        self.assertIn("SUB2API_TEST_FAIL_AFTER_VALIDATOR_ACTIVATION", script)
        self.assertIn("$test_parent == /opt/sub2api-deploy/release-input/$test_name", script)
        self.assertLess(script.index("sub2api-sign-dr-evidence\" \"$selftest_dr_dir"), script.index("mv -T -- \"$validator_target.new\""))
        self.assertLess(script.index('unit_lock="$target_libexec_dir/.sub2api-release-unit.lock"'), script.index('exec 9<>"$unit_lock"'))
        self.assertIn("root:root:600:1", script)

    def test_install_uploads_and_validates_all_three_assets(self) -> None:
        runner = mock.Mock()
        runner.create_temp_dir.return_value = "/opt/sub2api-deploy/release-input/unit"
        runner.run.return_value.values = {
            "signer_status": "ready",
            "public_key_sha256": bootstrap.sha256_file(bootstrap.TRUSTED_KEY),
            "validator_sha256": bootstrap.sha256_file(bootstrap.VALIDATOR),
            "gate_signer_sha256": bootstrap.sha256_file(bootstrap.GATE_SIGNER),
            "dr_signer_sha256": bootstrap.sha256_file(bootstrap.DR_SIGNER),
        }
        bootstrap.install_vm_validator(runner)
        uploaded = [call.args[1] for call in runner.upload_file.call_args_list]
        self.assertEqual(uploaded, [bootstrap.VALIDATOR, bootstrap.GATE_SIGNER, bootstrap.DR_SIGNER, bootstrap.BOOTSTRAP])
        install_call = next(call for call in runner.run.call_args_list if "REQUIRE_EXISTING_SIGNER_KEYS=true" in call.args[1])
        self.assertEqual(install_call.args[2], bootstrap.SIGNER_FIELDS)

    def test_manifest_binds_both_signers(self) -> None:
        profile = get_profile("195")
        with (
            mock.patch("release.manifest.validate_commit", side_effect=lambda value: value),
            mock.patch("release.manifest.subprocess.check_output", side_effect=[profile["origin"], "a" * 40]),
            mock.patch("release.manifest.runner_checksum", return_value="runner"),
            mock.patch("release.manifest.release_asset_checksums", return_value={}),
            mock.patch("release.manifest.migration_checksums", return_value={}),
        ):
            manifest = create_manifest("a" * 40, profile, "195-aaaaaaaaaaaa-1-aaaaaaaa")
        self.assertEqual(manifest["vm_gate_signer_sha256"], bootstrap.sha256_file(bootstrap.GATE_SIGNER))
        self.assertEqual(manifest["vm_dr_signer_sha256"], bootstrap.sha256_file(bootstrap.DR_SIGNER))

    def test_doctor_checks_key_identity_and_complete_unit(self) -> None:
        runner = mock.Mock()
        runner.run.return_value.values = {"vm_ready": "true"}
        ReleaseDoctor("195", runner=runner).check_vm()
        script = runner.run.call_args.args[1]
        self.assertIn("openssl pkey -in /opt/sub2api-release-signer/vm-gate-ed25519.pem -pubout", script)
        self.assertIn("sub2api-sign-gate", script)
        self.assertIn("sub2api-sign-dr-evidence", script)

    def test_dr_signer_contract_rejects_untrusted_shapes(self) -> None:
        script = (DEPLOY_ROOT / "release" / "sign-dr-evidence.sh").read_text(encoding="utf-8")
        self.assertIn("keys | sort", script)
        self.assertIn("temporary_material_destroyed == \"pass\"", script)
        self.assertIn("redis_backup_dbsize - .redis_restored_dbsize", script)
        self.assertIn("redis_backup_dbsize >= .redis_restored_dbsize", script)
        self.assertIn("redis_backup_expiring_keys <= .redis_backup_dbsize", script)
        self.assertIn("$completed >= (now - 86400)", script)
        self.assertIn("flock -s 8", script)
        self.assertIn("[[ ! -e $signature && ! -L $signature ]]", script)
        self.assertIn("[[ $(realpath -e -- \"$evidence\") == \"$evidence\" ]]", script)
        self.assertNotIn("release-gates", script)

    def test_signer_helpers_share_a_non_symlink_lock_contract(self) -> None:
        for name in ("sign-gate.sh", "sign-dr-evidence.sh"):
            script = (DEPLOY_ROOT / "release" / name).read_text(encoding="utf-8")
            self.assertIn("SUB2API_UNIT_LOCK_PATH", script)
            self.assertIn("SUB2API_HELPER_TEST_MODE", script)
            self.assertIn("root:root:600:1", script)
            self.assertIn("exec 8<>\"$unit_lock\"", script)

    def test_validator_holds_the_release_unit_shared_lock(self) -> None:
        validator = (DEPLOY_ROOT / "release" / "vm-validate.sh").read_text(encoding="utf-8")
        self.assertLess(validator.index("flock -s 8"), validator.index("vm_gate_signer_sha256"))
        self.assertLess(validator.index("flock -s 8"), validator.index("/usr/local/libexec/sub2api-sign-gate"))

    def test_backup_promotion_fixes_trust_and_candidate_contracts(self) -> None:
        promoter = (DEPLOY_ROOT / "release" / "promote-dr-baseline.sh").read_text(encoding="utf-8")
        self.assertIn("/opt/sub2api-dr-trust/vm-gate-ed25519.pub", promoter)
        self.assertIn("ea0b628532f8d85d0e57921b5b010c7f00ef8b0f9701da2b0d4ea31105553e08", promoter)
        self.assertIn("root=/srv/sub2api-backups/releases/195", promoter)
        self.assertIn('candidate_link="$root/candidate"', promoter)
        self.assertIn('"$verifier" "$trust_key" "$staging/evidence.json" "$staging/evidence.sig"', promoter)
        self.assertIn("verified-bundles/$target_name", promoter)
        self.assertIn("mv -T -- \"$link_tmp\" \"$verified_link\"", promoter)
        self.assertLess(promoter.index('install -o root -g root -m 400 "$evidence"'), promoter.index('"$verifier" "$trust_key" "$staging/evidence.json"'))
        self.assertLess(promoter.index('prepare_lock "$asset_lock"'), promoter.index('exec 8<>"$asset_lock"'))
        self.assertIn("root:root:600:1", promoter)
        self.assertIn('old_target_path="$root/$old_target"', promoter)
        self.assertIn('stat -c \'%U:%G:%a:%h\' "$target/$file"', promoter)

    def test_backup_asset_bootstrap_runs_negative_tests_before_activation(self) -> None:
        bootstrap_script = (DEPLOY_ROOT / "release" / "bootstrap_backup_dr_assets.sh").read_text(encoding="utf-8")
        activation = bootstrap_script.index("activation_started=true")
        self.assertLess(bootstrap_script.index("tampered.json"), activation)
        self.assertLess(bootstrap_script.index("tampered.sig"), activation)
        self.assertLess(bootstrap_script.index("symlink.json"), activation)
        self.assertLess(bootstrap_script.index("oversize.json"), activation)
        self.assertIn("SUB2API_TEST_FAIL_AFTER_VERIFIER_ACTIVATION", bootstrap_script)
        self.assertIn("promoter_constant verifier_sha256", bootstrap_script)
        self.assertIn("promoter_constant trust_sha256", bootstrap_script)
        self.assertLess(bootstrap_script.index('asset_lock="$target_libexec_dir/.sub2api-dr-assets.lock"'), bootstrap_script.index('exec 9<>"$asset_lock"'))

    def test_verifier_build_contract_is_reproducible_and_static(self) -> None:
        build = (DEPLOY_ROOT / "release" / "drverify" / "build.py").read_text(encoding="utf-8")
        self.assertIn('REQUIRED_GO_VERSION = "go version go1.26.3"', build)
        self.assertIn('"CGO_ENABLED": "0"', build)
        self.assertIn('"GOOS": "linux"', build)
        self.assertIn('"GOARCH": "amd64"', build)
        self.assertIn('"-trimpath"', build)
        self.assertIn("program_type == 3", build)


if __name__ == "__main__":
    unittest.main()
