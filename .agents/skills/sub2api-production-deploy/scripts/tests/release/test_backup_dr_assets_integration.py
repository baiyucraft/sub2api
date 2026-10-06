from __future__ import annotations

import hashlib
import importlib.util
import shutil
import tempfile
import unittest
from pathlib import Path
from types import SimpleNamespace
from unittest import mock


SCRIPT = Path(__file__).with_name("backup_dr_assets_integration.py")
spec = importlib.util.spec_from_file_location("backup_dr_assets_fixture", SCRIPT)
assets = importlib.util.module_from_spec(spec)
assert spec.loader is not None
spec.loader.exec_module(assets)

CHECKS = {
    "backup_bootstrap_successful_activation", "backup_bootstrap_idempotent_reinstall",
    "backup_bootstrap_lock_symlink_rejected", "backup_bootstrap_version_mismatch_rejected",
    "backup_bootstrap_post_activation_rollback", "backup_production_assets_unchanged",
    "promotion_bundle_contract", "promotion_candidate_evidence_mismatch_rejected",
    "promotion_lock_symlink_rejected", "promotion_conflict_target_rejected",
    "promotion_content_conflict_rejected", "promotion_failure_preserved_old_verified",
    "promotion_successful_activation", "promotion_pointer_and_checksum_verified",
    "promotion_idempotent_replay", "promotion_sources_retained", "cleanup_verified",
}
CLOCK = {
    "drill_id": "dr-195-20261006T010203Z",
    "previous_drill_id": "dr-195-20261006T010202Z",
    "observed_at": "2026-10-06T01:02:03Z",
}
CANDIDATE = {
    "archive_sha256": "c" * 64,
    "candidate_image_id": "sha256:" + "d" * 64,
    "migration_sha256": "1" * 64,
}
BINDING = {"artifact_sha256": "a" * 64, "bundle_sha256": "b" * 64}


class FixtureRunner:
    def __init__(self, *, fail_bound_download: bool = False, fail_cleanup: bool = False) -> None:
        self.scripts: list[tuple[str, str, set[str]]] = []
        self.uploads: list[tuple[str, Path, str, int, bytes]] = []
        self.downloads: list[tuple[str, str, Path]] = []
        self.fail_bound_download = fail_bound_download
        self.fail_cleanup = fail_cleanup
        self.failure = RuntimeError("bound download failed")

    def create_temp_dir(self, host: str, root: str, prefix: str) -> str:
        return f"{root}/{prefix}.TEST1234"

    def run(self, host: str, script: str, fields: set[str], **kwargs: object) -> SimpleNamespace:
        self.scripts.append((host, script, fields))
        if fields == set(CLOCK):
            values = CLOCK.copy()
        elif fields == set(CANDIDATE):
            values = CANDIDATE.copy()
        elif fields == set(BINDING):
            values = BINDING.copy()
        elif fields == {"cleanup"} and self.fail_cleanup:
            raise RuntimeError("cleanup failed")
        else:
            values = dict.fromkeys(fields, "pass")
        return SimpleNamespace(values=values)

    def upload_file(self, host: str, path: Path, remote: str, mode: int) -> None:
        # Mirror the transfer precondition that failed on a clean checkout.
        if not path.is_file() or path.is_symlink():
            raise RuntimeError("local transfer input is not regular")
        self.uploads.append((host, path, remote, mode, path.read_bytes()))

    def download_file(self, host: str, remote: str, path: Path) -> None:
        self.downloads.append((host, remote, path))
        if self.fail_bound_download and remote.endswith("/evidence.json"):
            raise self.failure
        path.write_bytes(f"generated:{Path(remote).name}\n".encode())


class BackupDRFixtureTest(unittest.TestCase):
    def setUp(self) -> None:
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.verifier = self.root / "versioned-verifier-fixture"
        self.verifier.write_bytes(b"versioned verifier")
        self.addCleanup(mock.patch.stopall)
        mock.patch.object(assets, "ROOT", self.root).start()
        self.resolver = mock.patch.object(assets, "verifier_binary", return_value=self.verifier).start()
        mock.patch.object(assets.time, "time", return_value=1791234000).start()
        mock.patch.object(assets.secrets, "token_hex", return_value="deadbeef").start()
        self.local_temps: list[tempfile.TemporaryDirectory[str]] = []
        self.remote_temps: list[tuple[str, str]] = []
        self.addCleanup(lambda: [temp.cleanup() for temp in self.local_temps])

    def generate(self) -> FixtureRunner:
        runner = FixtureRunner()
        assets.run(runner, self.remote_temps, self.local_temps)
        return runner

    def test_clean_checkout_generates_every_transfer_input_and_binds_final_evidence(self) -> None:
        self.assertFalse((self.root / ".tmp").exists())
        runner = self.generate()
        self.resolver.assert_called_once_with()
        self.assertFalse((self.root / ".tmp" / "releases").exists())
        uploaded = {Path(remote).name: content for _, _, remote, _, content in runner.uploads}
        for name in ("gate.json", "gate.sig", "candidate.tar.gz", "SHA256SUMS",
                     "evidence.json", "evidence.sig", "mismatch.json", "mismatch.sig"):
            self.assertEqual(uploaded[name], f"generated:{name}\n".encode())
        scripts = "\n".join(script for _, script, _ in runner.scripts)
        for forbidden in ("195-0314be7299e0-1784375727-31508cb8", ".tmp/releases",
                          "production_candidate", "production_root=/srv/sub2api-backups/releases"):
            self.assertNotIn(forbidden, scripts)
        candidate = next(script for _, script, fields in runner.scripts if fields == set(CANDIDATE))
        self.assertIn('profile:"195",schema:1', candidate)
        self.assertIn('migration_sha=$(printf \'1%.0s\' {1..64})', candidate)
        self.assertIn('.manifest.migration_sha256["195_upstream_scheduling_monitor_rates.sql"]', candidate)
        self.assertNotIn("jq -cS '.manifest.migration_sha256'", scripts)
        assembly_index = next(i for i, (_, _, fields) in enumerate(runner.scripts) if fields == set(BINDING))
        bound_index = next(i for i, (_, _, fields) in enumerate(runner.scripts) if fields == {"bound_evidence_ready"})
        self.assertLess(assembly_index, bound_index)
        assembly = runner.scripts[assembly_index][1]
        bound = runner.scripts[bound_index][1]
        self.assertIn('candidate_dir="$remote/releases/195/candidates/$release_id"', assembly)
        self.assertIn("sha256sum artifact.tar.age candidate.tar.gz gate.json gate.sig manifest SHA256SUMS", assembly)
        for checksum in (*CANDIDATE.values(), BINDING["bundle_sha256"], BINDING["artifact_sha256"]):
            self.assertIn(checksum, bound)
        self.assertIn(f"observed_at={CLOCK['observed_at']}", bound)
        self.assertIn("write_bound $(printf '0%.0s' {1..64}) mismatch", bound)
        promotion = runner.scripts[-1][1]
        release_id = "195-000000000000-1791234000-deadbeef"
        self.assertIn(f"release_id={release_id}", promotion)
        old_name = release_id + "--" + CLOCK["previous_drill_id"]
        self.assertRegex(old_name, r"^195-[0-9a-f]{12}-[0-9]+-[0-9a-f]{8}--dr-195-[0-9]{8}T[0-9]{6}Z$")
        self.assertIn(f'old_target_name="$release_id--{CLOCK["previous_drill_id"]}"', promotion)

    def test_bootstrap_faults_promotion_failures_and_fixed_asset_guards_remain(self) -> None:
        runner = self.generate()
        host, script, fields = runner.scripts[-1]
        self.assertEqual(host, "backup")
        self.assertEqual(fields, CHECKS)
        for field in CHECKS:
            self.assertIn(f"printf '{field}=pass\\n'", script)
        self.assertIn('SUB2API_TEST_FAIL_AFTER_VERIFIER_ACTIVATION="$fail_after"', script)
        self.assertIn('run_bootstrap "$remote/promoter-mutated" "$mutated_sha" true', script)
        self.assertIn('ln -s "$remote/lock-sentinel" "$remote/libexec/.sub2api-dr-assets.lock"', script)
        self.assertIn('ln -s "$remote/promotion-lock-sentinel" "$promotion_root/.promotion.lock"', script)
        self.assertIn('if run_promoter "$mismatch_input"', script)
        self.assertIn('if run_promoter "$valid_input"', script)
        self.assertIn('promotion_output_second=$(run_promoter "$valid_input")', script)
        self.assertIn("root:root:400:1", script)
        self.assertIn("LC_ALL=C sort", script)
        for path, variable in (
            ("/usr/local/libexec/sub2api-verify-dr-evidence", "verifier"),
            ("/usr/local/libexec/sub2api-promote-dr-baseline", "promoter"),
            ("/opt/sub2api-dr-trust/vm-gate-ed25519.pub", "trust"),
        ):
            self.assertIn(f"before_{variable}=$(asset_state {path})", script)
            self.assertIn(f'[[ $(asset_state {path}) == "$before_{variable}" ]]', script)
        self.assertEqual(len(self.remote_temps), 4)
        self.assertTrue(any("/release-gates/195-" in path for _, path in self.remote_temps))
        self.assertTrue(any("/dr-evidence/195-" in path for _, path in self.remote_temps))

    def test_all_generated_shell_has_valid_bash_syntax(self) -> None:
        bash = shutil.which("bash")
        git_bash = Path("C:/Program Files/Git/bin/bash.exe")
        if git_bash.is_file():
            bash = str(git_bash)
        if bash is None:
            self.skipTest("Bash is required to check generated remote scripts")
        runner = self.generate()
        assets.cleanup_remote_temp(runner, "local_vm", self.remote_temps[0][1])
        for host, script, fields in runner.scripts:
            with self.subTest(host=host, fields=fields):
                result = assets.run_hidden([bash, "--noprofile", "--norc", "-n"], input=script,
                                        capture_output=True, text=True, timeout=10)
                self.assertEqual(result.returncode, 0, result.stderr)

    def test_missing_check_result_is_rejected(self) -> None:
        runner = FixtureRunner()
        original = runner.run

        def incomplete(host: str, script: str, fields: set[str], **kwargs: object) -> SimpleNamespace:
            result = original(host, script, fields, **kwargs)
            if fields == CHECKS:
                result.values.pop("cleanup_verified")
            return result

        runner.run = incomplete
        with self.assertRaisesRegex(RuntimeError, "did not pass"):
            assets.run(runner, self.remote_temps, self.local_temps)

    def test_main_cleans_registered_scopes_and_preserves_primary_failure(self) -> None:
        for cleanup_failure in (False, True):
            with self.subTest(cleanup_failure=cleanup_failure):
                runner = FixtureRunner(fail_bound_download=True, fail_cleanup=cleanup_failure)
                with mock.patch.object(assets, "SSHRunner", return_value=runner):
                    with self.assertRaises(RuntimeError) as caught:
                        assets.main()
                self.assertIs(caught.exception, runner.failure)
                cleaned = [script for _, script, fields in runner.scripts if fields == {"cleanup"}]
                self.assertEqual(len(cleaned), 4)
                expected = ("/srv/sub2api-backups/dr-promotion-test.TEST1234",
                            "/opt/sub2api-deploy/dr-evidence/195-000000000000-1791234000-deadbeef",
                            "/opt/sub2api-deploy/release-gates/195-000000000000-1791234000-deadbeef",
                            "/opt/sub2api-deploy/release-input/promotion-evidence.TEST1234")
                for script, path in zip(cleaned, expected):
                    self.assertIn(path, script)
                    self.assertIn("realpath -e", script)
                    self.assertIn("! -L", script)
                self.assertEqual(list((self.root / ".tmp").iterdir()), [])
                if cleanup_failure:
                    self.assertIn("could not be cleaned", " ".join(caught.exception.__notes__))


class VerifierFixtureTest(unittest.TestCase):
    def setUp(self) -> None:
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.expected_bytes = b"versioned verifier"
        self.expected = hashlib.sha256(self.expected_bytes).hexdigest()
        checksums = self.root / "release" / "drverify"
        checksums.mkdir(parents=True)
        (checksums / "linux-amd64.sha256").write_text(self.expected + "  verifier\n", encoding="ascii")
        self.binary = self.root / ".tmp" / "sub2api-verify-dr-evidence"
        self.addCleanup(mock.patch.stopall)
        mock.patch.object(assets, "ROOT", self.root).start()
        mock.patch.object(assets, "DEPLOY_ROOT", self.root).start()

    def write_binary(self, value: bytes) -> None:
        self.binary.parent.mkdir(exist_ok=True)
        self.binary.write_bytes(value)

    def test_matching_versioned_binary_is_reused(self) -> None:
        self.write_binary(self.expected_bytes)
        with mock.patch.object(assets, "run_hidden") as build:
            self.assertEqual(assets.verifier_binary(), self.binary)
        build.assert_not_called()

    def test_missing_or_stale_binary_is_built_and_checked(self) -> None:
        for stale in (False, True):
            with self.subTest(stale=stale):
                if stale:
                    self.write_binary(b"old verifier")
                with mock.patch.object(assets, "run_hidden", side_effect=lambda *a, **k: self.write_binary(self.expected_bytes)) as build:
                    self.assertEqual(assets.verifier_binary(), self.binary)
                build.assert_called_once_with(
                    [assets.sys.executable, str(self.root / "release" / "drverify" / "build.py"), "--output", str(self.binary)],
                    cwd=self.root, check=True,
                )

    def test_rebuild_with_wrong_checksum_is_rejected(self) -> None:
        with mock.patch.object(assets, "run_hidden", side_effect=lambda *a, **k: self.write_binary(b"untrusted verifier")):
            with self.assertRaisesRegex(RuntimeError, "repository checksum"):
                assets.verifier_binary()
