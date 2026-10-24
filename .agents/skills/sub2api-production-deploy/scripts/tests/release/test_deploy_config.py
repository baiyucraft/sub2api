"""Small local configuration contract; never connects to a server."""
from __future__ import annotations

import json
import os
from pathlib import Path
import runpy
import subprocess
import sys
import tempfile
import unittest
from unittest import mock


SCRIPTS = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(SCRIPTS))
from release.deploy_config import load_config, prepare_arguments


DOCUMENT = {"schema": 1, "commit": "a" * 40, "profile": "275", "mode": "downtime"}


class DeployConfigTest(unittest.TestCase):
    def setUp(self) -> None:
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.workspace = Path(temporary.name)
        self.config = self.workspace / ".release" / "deploy.local.json"
        self.config.parent.mkdir()

    def write_config(self, value=None) -> None:
        self.config.write_text(json.dumps(DOCUMENT if value is None else value), encoding="utf-8")

    def test_default_config_fills_only_options_supported_by_each_command(self):
        self.write_config()
        for command in ("deploy", "deploy-start", "deploy-follow", "vm-validate", "doctor", "vm-only-validate", "recovery-review-scope"):
            with self.subTest(command=command):
                result = prepare_arguments([command], self.workspace)
                expected = [command, "--profile", "275", "--commit", "a" * 40]
                if command in {"deploy", "deploy-start", "deploy-follow", "vm-validate"}:
                    expected += ["--mode", "downtime"]
                self.assertEqual(result, expected)

    def test_explicit_config_works_before_or_after_command_and_with_equals(self):
        self.write_config()
        elsewhere = self.workspace / "other"
        for arguments in (["--config", str(self.config), "doctor"],
                          ["doctor", "--config=" + str(self.config)]):
            self.assertEqual(prepare_arguments(arguments, elsewhere),
                             ["doctor", "--profile", "275", "--commit", "a" * 40])

    def test_explicit_options_override_valid_config(self):
        self.write_config()
        arguments = ["deploy", "--commit=" + "b" * 40, "--profile", "273", "--mode", "blue-green"]
        self.assertEqual(prepare_arguments(arguments, self.workspace), arguments)

    def test_missing_default_preserves_original_explicit_arguments(self):
        arguments = ["deploy", "--commit", "b" * 40, "--mode", "downtime"]
        self.assertEqual(prepare_arguments(arguments, self.workspace), arguments)

    def test_observation_verification_and_internal_worker_ignore_default(self):
        self.config.write_text("invalid JSON", encoding="utf-8")
        for command in ("status", "follow", "wait", "verify-result", "verify-recovery-result", "_deploy-worker"):
            with self.subTest(command=command), mock.patch("release.deploy_config.load_config") as read:
                arguments = [command, "existing-release"]
                self.assertEqual(prepare_arguments(arguments, self.workspace), arguments)
                read.assert_not_called()

    def test_explicit_config_is_rejected_for_observation_commands(self):
        with self.assertRaisesRegex(ValueError, "not supported"):
            prepare_arguments(["status", "existing-release", "--config", str(self.config)], self.workspace)

    def test_missing_explicit_config_is_an_error(self):
        with self.assertRaises(FileNotFoundError):
            prepare_arguments(["doctor", "--config", str(self.config)], self.workspace)

    def test_bad_json_duplicate_fields_unknown_fields_and_missing_fields(self):
        for raw in ("{", "[]", '{"schema":1,"schema":1}',
                    json.dumps({**DOCUMENT, "command": "ignored"}),
                    json.dumps({key: value for key, value in DOCUMENT.items() if key != "commit"})):
            with self.subTest(raw=raw), self.assertRaises(ValueError):
                self.config.write_text(raw, encoding="utf-8")
                load_config(self.config)

    def test_schema_types_and_version_are_strict(self):
        for value in (True, "1", 1.0, 2, None):
            with self.subTest(value=value), self.assertRaisesRegex(ValueError, "schema"):
                self.write_config({**DOCUMENT, "schema": value})
                load_config(self.config)

    def test_bad_sha_profile_and_mode_are_rejected_even_if_cli_overrides_them(self):
        cases = (("commit", "a" * 39), ("commit", "A" * 40), ("commit", "main"),
                 ("profile", "276"), ("profile", 275), ("mode", "automatic"))
        for field, value in cases:
            with self.subTest(field=field, value=value), self.assertRaises(ValueError):
                self.write_config({**DOCUMENT, field: value})
                prepare_arguments(["deploy", "--commit", "b" * 40, "--profile", "275", "--mode", "downtime"], self.workspace)

    def test_duplicate_cli_options_and_aliases_are_rejected(self):
        for options in (["--commit", "a" * 40, "--commit=" + "b" * 40],
                        ["--profile", "275", "--profile=275"],
                        ["--mode=downtime", "--mode", "blue-green"],
                        ["--node", "all", "--node=local"], ["--comm", "a" * 40]):
            with self.subTest(options=options), self.assertRaises(ValueError):
                prepare_arguments(["doctor", *options], self.workspace)

    def test_bad_explicit_values_and_missing_values_fail_without_config(self):
        for options in (["--commit", "main"], ["--profile=276"], ["--mode=unknown"],
                        ["--commit"], ["--profile", "--node", "all"], ["--mode="]):
            with self.subTest(options=options), self.assertRaises(ValueError):
                prepare_arguments(["deploy", *options], self.workspace)

    def test_missing_and_duplicate_config_arguments_are_rejected(self):
        for arguments in (["doctor", "--config"], ["doctor", "--config="],
                          ["--config", "a.json", "doctor", "--config=b.json"]):
            with self.subTest(arguments=arguments), self.assertRaises(ValueError):
                prepare_arguments(arguments, self.workspace)

    def test_help_does_not_read_config(self):
        with mock.patch("release.deploy_config.load_config") as read:
            self.assertEqual(prepare_arguments(["doctor", "--help", "--config=missing.json"], self.workspace),
                             ["doctor", "--help"])
            read.assert_not_called()

    def test_original_cli_receives_resolved_values_without_unsupported_options(self):
        from release import cli, observer, supervisor
        self.write_config()
        cases = (("doctor", cli, "doctor"), ("deploy", cli, "deploy"),
                 ("vm-validate", cli, "vm_validate"), ("deploy-start", supervisor, "start"),
                 ("deploy-follow", observer, "deploy_follow"))
        for command, module, name in cases:
            with self.subTest(command=command), mock.patch.object(module, name) as handler:
                handler.return_value = None
                with mock.patch.object(sys, "argv", [str(SCRIPTS / "release.py"), *prepare_arguments([command], self.workspace)]):
                    cli.main()
                namespace = handler.call_args.args[0]
                self.assertEqual((namespace.profile, namespace.commit), ("275", "a" * 40))
                if command != "doctor":
                    self.assertEqual(namespace.deployment_mode, "downtime")

    def test_real_isolated_launcher_rejects_invalid_config_before_ssh(self):
        self.write_config({**DOCUMENT, "password": "do-not-print-this-value"})
        result = subprocess.run([sys.executable, "-I", "-S", "-B", str(SCRIPTS / "release.py"),
                                 "doctor", "--config", str(self.config)], cwd=self.workspace,
                                capture_output=True, text=True, timeout=30)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("deploy config requires exactly", result.stderr)
        self.assertNotIn("do-not-print-this-value", result.stderr + result.stdout)
        self.assertNotIn(".ssh.local", result.stderr)

    def test_real_isolated_launcher_help_and_dependency_imports(self):
        # -I must ignore a caller PYTHONPATH containing a conflicting package.
        poison = self.workspace / "poison" / "release"
        poison.mkdir(parents=True)
        (poison / "__init__.py").write_text('raise RuntimeError("caller_path_loaded")', encoding="utf-8")
        environment = dict(os.environ, PYTHONPATH=str(poison.parent))
        entrypoint = str(SCRIPTS / "release.py")
        help_result = subprocess.run([sys.executable, "-I", "-S", "-B", entrypoint, "--help"],
                                     cwd=self.workspace, env=environment, capture_output=True, text=True, timeout=30)
        self.assertEqual(help_result.returncode, 0, help_result.stderr)
        self.assertIn("deploy-follow", help_result.stdout)
        code = ("import runpy; from pathlib import Path; "
                f"entry=runpy.run_path({entrypoint!r}); "
                f"entry['_maintenance_imports'](Path({str(SCRIPTS)!r})); "
                "import paramiko, yaml, socks, release.cli; print('isolated_imports_ok')")
        result = subprocess.run([sys.executable, "-I", "-S", "-B", "-c", code],
                                cwd=self.workspace, env=environment, capture_output=True, text=True, timeout=30)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout.strip(), "isolated_imports_ok")


if __name__ == "__main__":
    unittest.main()
