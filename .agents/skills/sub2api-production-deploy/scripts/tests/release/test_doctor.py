from __future__ import annotations

import sys
import unittest
from pathlib import Path
from unittest import mock


DEPLOY_ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(DEPLOY_ROOT))

from release.doctor import ReleaseDoctor, import_trusted_host_keys
from release.production_bootstrap import bootstrap_production


class DoctorTest(unittest.TestCase):
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
        self.assertNotIn("SELECT key FROM api_keys", scripts)
        self.assertNotIn("canary-api-key", scripts)
        self.assertNotIn("docker system prune", scripts)
        self.assertNotIn("install -o root -g root -m 644", scripts)
        self.assertNotIn("systemctl daemon-reload", scripts)
        health_check = scripts.index("for container in sub2api-postgres sub2api-redis")
        first_app_check = scripts.index("docker inspect -f", scripts.index("if test ! -e \"$active_slot\""))
        claim_check = scripts.index("test ! -e /opt/sub2api/releases/.active-release")
        directory_install = scripts.index("install -d -m 700")
        self.assertLess(health_check, directory_install)
        self.assertLess(claim_check, directory_install)
        self.assertGreater(first_app_check, directory_install)
        self.assertIn("active_container=$(sed -n 's/^container=//p'", scripts)
        self.assertIn("legacy_proxy_count=$(grep -Ec", scripts)
        self.assertIn('test "$legacy_proxy_count" -ge 1', scripts)
        self.assertNotIn(
            "test \"$(grep -Ec '^[[:space:]]*proxy_pass[[:space:]]+http://127\\.0\\.0\\.1:18080;[[:space:]]*$' <<<\"$nginx_text\")\" = 1",
            scripts,
        )
        self.assertIn('test "$(wc -l <<<"$site")" = 1', scripts)

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
