from __future__ import annotations

import sys
import unittest
from pathlib import Path
from unittest import mock


DEPLOY_ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(DEPLOY_ROOT))

from release.doctor import ReleaseDoctor
from release.production_bootstrap import bootstrap_production


class DoctorTest(unittest.TestCase):
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

    def test_bootstrap_never_embeds_a_canary_secret(self) -> None:
        runner = mock.Mock()
        runner.run.return_value.values = {"production_bootstrap": "true"}
        bootstrap_production("182", runner)
        scripts = "\n".join(call.args[1] for call in runner.run.call_args_list)
        self.assertIn("SELECT key FROM api_keys", scripts)
        self.assertNotRegex(scripts, r"sk-[A-Za-z0-9]{16}")
        self.assertNotIn("docker system prune", scripts)
        self.assertNotIn("install -o root -g root -m 644", scripts)
        self.assertNotIn("systemctl daemon-reload", scripts)
        health_check = scripts.index("for container in sub2api sub2api-postgres sub2api-redis")
        claim_check = scripts.index("test ! -e /opt/sub2api/releases/.active-release")
        directory_install = scripts.index("install -d -m 700")
        self.assertLess(health_check, directory_install)
        self.assertLess(claim_check, directory_install)

    def test_remote_scripts_do_not_contain_control_characters(self) -> None:
        runner = mock.Mock()
        runner.run.return_value.values = {"racknerd_ready": "true"}
        ReleaseDoctor("182", runner=runner).check_racknerd()
        script = runner.run.call_args.args[1]
        self.assertTrue(all(character in "\n\t" or ord(character) >= 32 for character in script))
        self.assertIn("production_migration_status=verified", script)
        self.assertIn("production_migration_status=absent", script)

        bootstrap_runner = mock.Mock()
        bootstrap_runner.run.return_value.values = {"production_bootstrap": "true"}
        bootstrap_production("182", bootstrap_runner)
        bootstrap_scripts = "\n".join(call.args[1] for call in bootstrap_runner.run.call_args_list)
        self.assertTrue(all(character in "\n\t" or ord(character) >= 32 for character in bootstrap_scripts))


if __name__ == "__main__":
    unittest.main()
