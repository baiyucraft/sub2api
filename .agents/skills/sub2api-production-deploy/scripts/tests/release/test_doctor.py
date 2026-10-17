from __future__ import annotations

import sys
import unittest
import os
import shutil
import tempfile
from pathlib import Path
from unittest import mock


DEPLOY_ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(DEPLOY_ROOT))

from release.doctor import ReleaseDoctor, import_trusted_host_keys
from release.production_bootstrap import bootstrap_production
from release.process import run_hidden


class DoctorTest(unittest.TestCase):
    def vm_script(self):
        runner = mock.Mock()
        runner.run.return_value.values = {"vm_ready": "true", "vm_free_bytes": str(20 * 1024 ** 3), "vm_database_bytes": "0", "vm_release_unit_status": "verified"}
        ReleaseDoctor("268", runner=runner).check_vm()
        return runner.run.call_args.args[1]

    def bash(self):
        candidate = Path("C:/Program Files/Git/bin/bash.exe")
        value = str(candidate) if candidate.is_file() else shutil.which("bash")
        if not value:
            self.skipTest("Bash is required for the inert VM doctor preflight fixture")
        return value

    def test_vm_unit_lock_missing_symlink_or_permissions_fail_before_open(self):
        self.check_vm_unit_lock_fixtures(("missing", "bad-mode", "healthy"))

    def test_vm_unit_lock_real_symlink_is_rejected_before_touching_target(self):
        self.check_vm_unit_lock_fixtures(("symlink",))

    def check_vm_unit_lock_fixtures(self, kinds):
        lines = self.vm_script().splitlines()
        start = lines.index('unit_lock=/usr/local/libexec/.sub2api-release-unit.lock')
        end = lines.index('flock -s 8')
        self.assertEqual(lines[start + 1:start + 3], ['test -f "$unit_lock"', 'test ! -L "$unit_lock"'])
        checks = "\n".join(lines[start + 1:end + 1])
        for kind in kinds:
            with self.subTest(kind=kind), tempfile.TemporaryDirectory(prefix="doctor-lock-fixture-") as directory:
                root = Path(directory)
                lock = root / "unit.lock"
                target = root / "untouched-target"
                target.write_bytes(b"sentinel")
                marker = root / "stat-reached"
                if kind in ("bad-mode", "healthy"):
                    lock.touch()
                environment = {**os.environ, "LOCK_PATH": lock.as_posix(), "TARGET_PATH": target.as_posix(),
                               "STAT_MARKER": marker.as_posix(), "MODE": "root:root:644:1" if kind == "bad-mode" else "root:root:600:1"}
                environment.pop("BASH_ENV", None)
                environment.pop("ENV", None)
                prepare = 'ln -s "$TARGET_PATH" "$LOCK_PATH"\ntest -L "$LOCK_PATH"\nprintf "SYMLINK_READY\\n"\n' if kind == "symlink" else ""
                stubs = ('stat() { printf x >> "$STAT_MARKER"; printf "%s\\n" "$MODE"; }\n'
                         'flock() { test "$*" = "-s 8"; }\n')
                script = ('set -Eeuo pipefail\n' + prepare + stubs + 'unit_lock="$LOCK_PATH"\n' + checks
                          + "\nprintf 'LOCK_CHECK_PASSED\\n'\n")
                result = run_hidden([self.bash(), "--noprofile", "--norc", "-s"], input=script, env=environment,
                                    text=True, capture_output=True, timeout=10)
                if kind == "symlink" and "SYMLINK_READY" not in result.stdout:
                    self.skipTest("Host Bash cannot prepare a real symlink; this guard scenario was not verified")
                self.assertEqual(target.read_bytes(), b"sentinel")
                if kind == "healthy":
                    self.assertEqual(result.returncode, 0, result.stderr)
                    self.assertIn("LOCK_CHECK_PASSED", result.stdout)
                else:
                    self.assertNotEqual(result.returncode, 0)
                    self.assertNotIn("LOCK_CHECK_PASSED", result.stdout)
                    if kind in ("missing", "symlink"):
                        self.assertFalse(marker.exists())
                    if kind == "symlink":
                        self.assertIn("SYMLINK_READY", result.stdout, result.stderr)
                    if kind == "missing":
                        self.assertFalse(lock.exists())

    def test_vm_directory_and_asset_assertions_stop_before_later_checks(self):
        script = self.vm_script()
        # Select actual VM directory assertions independently of profile paths.
        lines = script.splitlines()
        start = lines.index('flock -s 8') + 1
        directory_lines = lines[start:start + 6]
        self.assertEqual(len(directory_lines), 6)
        self.assertTrue(all('&&' not in line for line in directory_lines))
        for position, line in enumerate(directory_lines):
            with self.subTest(directory_assertion=position):
                code = 'set -Eeuo pipefail\n' + line
                if line.startswith("test ! -L"):
                    code = 'set -Eeuo pipefail\ntest() { if [[ $1 == "!" && $2 == -L ]]; then return 1; fi; command test "$@"; }\n' + line
                environment = dict(os.environ, MISSING_PATH="/nonexistent-doctor-fixture-path")
                environment.pop("BASH_ENV", None)
                environment.pop("ENV", None)
                # Missing checks are executed against their exact asserted path.
                if line.startswith("test -d"):
                    code = 'set -Eeuo pipefail\n' + 'test -d "$MISSING_PATH"'
                result = run_hidden([self.bash(), "-s"], input=code + "\nprintf 'LATER_CHECK_REACHED\\n'\n",
                                    env=environment, text=True, capture_output=True, timeout=10)
                self.assertNotEqual(result.returncode, 0)
                self.assertNotIn("LATER_CHECK_REACHED", result.stdout)
        self.assertIn('  test -f "$asset"\n  test ! -L "$asset"\n  required_value=$(stat', script)
        self.assertIn('  test -f "$asset"\n  test ! -L "$asset"\ndone', script)

    def test_dmit_check_requires_release_proxy_and_disables_legacy_proxy(self) -> None:
        runner = mock.Mock()
        runner.run.return_value.values = {
            "dmit_ready": "true",
            "proxy_v2_ready": "true",
            "release_proxy_ready": "true",
            "legacy_proxy_disabled": "true",
        }
        ReleaseDoctor("182", runner=runner).check_dmit()
        script = runner.run.call_args.args[1]
        allowed = runner.run.call_args.args[2]
        self.assertIn("sub2api-tinyproxy.service", script)
        self.assertIn("sport = :1080", script)
        self.assertIn("sport = :8888", script)
        self.assertIn("ConnectPort[[:space:]]+1030", script)
        self.assertIn("release_proxy_ready", allowed)
        self.assertIn("legacy_proxy_disabled", allowed)

    def test_complete_repo_host_key_snapshot_does_not_read_user_home(self) -> None:
        private_hosts = mock.Mock()
        private_hosts.lookup.return_value = {"ssh-ed25519": mock.Mock()}
        ssh_config = mock.Mock()
        ssh_config.read_text.return_value = "servers:\n  vm:\n    host: vm.example\n"
        known_hosts = mock.Mock()
        known_hosts.exists.return_value = True
        with (
            mock.patch("release.doctor.SSH_CONFIG", ssh_config),
            mock.patch("release.doctor.KNOWN_HOSTS", known_hosts),
            mock.patch("release.doctor.paramiko.HostKeys", return_value=private_hosts),
            mock.patch("release.doctor.pathlib.Path.home", side_effect=AssertionError("user home must not be read")),
        ):
            import_trusted_host_keys()
        private_hosts.load.assert_called_once()
        private_hosts.save.assert_not_called()

    def test_local_git_reads_use_retrying_helper(self) -> None:
        commit = "a" * 40
        profile = {"origin": "https://example.invalid/repo.git"}
        outputs = ["", profile["origin"] + "\n", commit + "\n", b""]
        with (
            mock.patch("release.doctor.get_profile", return_value=profile),
            mock.patch("release.doctor.SSH_CONFIG", mock.Mock(is_file=mock.Mock(return_value=True))),
            mock.patch("release.doctor.TRUSTED_KEY", mock.Mock(is_file=mock.Mock(return_value=True))),
            mock.patch("release.doctor.import_trusted_host_keys"),
            mock.patch("release.doctor._git_output", side_effect=outputs) as git_output,
        ):
            self.assertEqual(
                ReleaseDoctor("182", commit=commit, runner=mock.Mock()).check_local(),
                {"local_ready": "true", "host_keys_ready": "true"},
            )
        self.assertEqual(git_output.call_count, 4)
        self.assertEqual(git_output.call_args_list[-1].args[0][1:3], ["merge-base", "--is-ancestor"])

    def test_requested_nodes_only_are_checked(self) -> None:
        doctor = ReleaseDoctor("182", runner=mock.Mock())
        doctor.check_vm = mock.Mock(return_value={"vm_ready": "true"})
        doctor.check_backup = mock.Mock(return_value={"backup_ready": "true"})
        result = doctor.run(("vm", "backup"))
        self.assertEqual(result, {"vm_ready": "true", "backup_ready": "true"})

    def test_failure_stops_later_nodes(self) -> None:
        doctor = ReleaseDoctor("182", runner=mock.Mock())
        doctor.check_vm = mock.Mock(side_effect=RuntimeError("vm failed"))
        doctor.check_backup = mock.Mock()
        with self.assertRaisesRegex(RuntimeError, "doctor.vm failed"):
            doctor.run(("vm", "backup"))
        doctor.check_backup.assert_not_called()

    def test_bootstrap_does_not_check_or_create_a_canary_key(self) -> None:
        runner = mock.Mock()
        runner.run.return_value.values = {"production_bootstrap": "true"}
        bootstrap_production("182", runner)
        scripts = "\n".join(call.args[1] for call in runner.run.call_args_list)
        allowed = runner.run.call_args.args[2]
        self.assertNotIn("SELECT key FROM api_keys", scripts)
        self.assertNotIn("canary-api-key", scripts)
        self.assertNotIn("docker system prune", scripts)
        self.assertNotIn("install -o root -g root -m 644", scripts)
        self.assertNotIn("systemctl daemon-reload", scripts)
        health_check = scripts.index("for container in sub2api-postgres sub2api-redis")
        first_app_check = scripts.index("docker inspect -f '{{.State.Health.Status}}' sub2api", scripts.index('if test ! -e "$active_slot"'))
        claim_check = scripts.index("test ! -e /opt/sub2api/releases/.active-release")
        directory_install = scripts.index("install -d -m 700")
        self.assertLess(health_check, directory_install)
        self.assertLess(claim_check, directory_install)
        self.assertGreater(first_app_check, directory_install)
        self.assertIn("active_container=$(sed -n 's/^container=//p'", scripts)
        self.assertIn("legacy_proxy_count=$(grep -Ec", scripts)
        self.assertNotIn("proxy_request_buffering", scripts)
        self.assertNotIn("sub2api_upstream", scripts)
        self.assertNotIn("nginx_ingress_policy", allowed)

    def test_racknerd_doctor_requires_managed_ingress_policy(self) -> None:
        runner = mock.Mock()
        runner.run.return_value.values = {"racknerd_ready": "true"}
        ReleaseDoctor("182", runner=runner).check_racknerd()
        script = runner.run.call_args.args[1]
        allowed = runner.run.call_args.args[2]
        self.assertIn("require_ingress_policy=true", script)
        self.assertIn("sub2api-release-ingress.conf", script)
        self.assertIn("proxy_request_buffering on;", script)
        self.assertIn("proxy_buffering off;", script)
        self.assertIn("sub2api-release-observability.conf", script)
        self.assertIn("sub2api-upstream-access", script)
        self.assertIn("nginx -T", script)
        self.assertNotIn("conflicting server name|duplicate", script)
        self.assertIn("nginx_ingress_policy", allowed)

    def test_racknerd_pre_gate_doctor_allows_ingress_update(self) -> None:
        runner = mock.Mock()
        runner.run.return_value.values = {"racknerd_ready": "true"}
        ReleaseDoctor("182", runner=runner).check_racknerd(require_ingress_policy=False)
        script = runner.run.call_args.args[1]
        self.assertIn("require_ingress_policy=false", script)
        self.assertIn("nginx_ingress_policy=needs_update", script)

    def test_racknerd_snapshot_allowlists_verified_production_commit(self) -> None:
        runner = mock.Mock()
        runner.run.return_value.values = {"racknerd_ready": "true"}
        ReleaseDoctor("182", runner=runner).check_racknerd(require_ingress_policy=False)
        script = runner.run.call_args.args[1]
        allowed = runner.run.call_args.args[2]
        self.assertIn("active_image_ref=$(docker inspect -f '{{.Config.Image}}'", script)
        self.assertIn("production_current_commit_sha=", script)
        self.assertIn("docker image inspect -f '{{.Id}}'", script)
        self.assertIn("resolve_production_commit \"$active_image\" \"$active_image_ref\"", script)
        self.assertIn("{{json .RepoTags}}", script)
        self.assertIn("production_current_commit_sha:$commit", script)
        self.assertIn("production_current_commit_sha", allowed)

    def test_run_forwards_ingress_policy_mode_only_to_racknerd(self) -> None:
        doctor = ReleaseDoctor("182", runner=mock.Mock())
        doctor.check_racknerd = mock.Mock(return_value={"racknerd_ready": "true"})
        doctor.check_backup = mock.Mock(return_value={"backup_ready": "true"})
        doctor.run(("racknerd", "backup"), require_ingress_policy=False)
        doctor.check_racknerd.assert_called_once_with(require_ingress_policy=False)
        doctor.check_backup.assert_called_once_with()

    def test_remote_scripts_do_not_contain_control_characters(self) -> None:
        runner = mock.Mock()
        runner.run.return_value.values = {"racknerd_ready": "true"}
        ReleaseDoctor("182", runner=runner).check_racknerd()
        script = runner.run.call_args.args[1]
        self.assertTrue(all(character in "\n\t" or ord(character) >= 32 for character in script))
        self.assertIn("production_migration_status=verified", script)
        self.assertIn("production_migration_status=absent", script)
        self.assertIn("if ssh -i /root/.ssh/sub2api_backup_upload", script)
        self.assertNotIn("set +e\nssh -i /root/.ssh/sub2api_backup_upload", script)

        bootstrap_runner = mock.Mock()
        bootstrap_runner.run.return_value.values = {"production_bootstrap": "true"}
        bootstrap_production("182", bootstrap_runner)
        bootstrap_scripts = "\n".join(call.args[1] for call in bootstrap_runner.run.call_args_list)
        self.assertTrue(all(character in "\n\t" or ord(character) >= 32 for character in bootstrap_scripts))

    def test_racknerd_readonly_probe_retries_transport_loss(self) -> None:
        runner = mock.Mock()
        success = mock.Mock(values={"racknerd_ready": "true"})
        runner.run.side_effect = [RuntimeError("racknerd stage failed with exit code -1; remote stderr withheld"), success]
        doctor = ReleaseDoctor("182", runner=runner)
        with mock.patch("release.doctor.time.sleep") as sleep:
            result = doctor._run_racknerd_readonly("printf ok", {"racknerd_ready"}, timeout=300)
        self.assertEqual(result, {"racknerd_ready": "true"})
        self.assertEqual(runner.run.call_count, 2)
        sleep.assert_called_once_with(2)

    def test_racknerd_readonly_probe_does_not_retry_remote_failure(self) -> None:
        runner = mock.Mock()
        runner.run.side_effect = RuntimeError("racknerd stage failed with exit code 1; remote stderr withheld")
        doctor = ReleaseDoctor("182", runner=runner)
        with self.assertRaisesRegex(RuntimeError, "exit code 1"):
            doctor._run_racknerd_readonly("false", {"racknerd_ready"}, timeout=300)
        runner.run.assert_called_once()


if __name__ == "__main__":
    unittest.main()
