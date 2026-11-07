from __future__ import annotations

import importlib.util
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

import yaml

SCRIPT_ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(SCRIPT_ROOT))
SCRIPT = SCRIPT_ROOT / "check_local_deployment_disclosure.py"
SPEC = importlib.util.spec_from_file_location("check_local_deployment_disclosure", SCRIPT)
assert SPEC and SPEC.loader
checker = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(checker)

from release.environment import load_deployment_environment


class LocalDeploymentDisclosureTest(unittest.TestCase):
    def test_documentation_values_are_not_treated_as_local_endpoints(self) -> None:
        values = checker.deployment_disclosure_values({
            "servers": {"sample": {"host": "app.example.test"}},
            "deployment": {
                "public_domain": "sub2api.example.test",
                "health_routes": {"direct": {"address": "203.0.113.10"}},
                "relay_probe": {"listen_address": "192.0.2.10", "allowed_sources": ["192.0.2.0/24"]},
            },
        })
        self.assertEqual(values, set())

    def test_local_credentials_and_remote_key_paths_are_scanned(self) -> None:
        values = checker.deployment_disclosure_values({
            "servers": {
                "app": {
                    "host": "203.0.113.10",
                    "private_key": "/srv/private/deploy-key",
                    "password": "synthetic-private-password",
                },
            },
            "deployment": {
                "backup_upload": {
                    "user": "restricted-upload",
                    "key_path": "/srv/private/backup-key",
                },
            },
        })
        self.assertEqual(values, {
            b"/srv/private/deploy-key",
            b"synthetic-private-password",
            b"/srv/private/backup-key",
        })

    def test_public_configuration_example_uses_fictional_environment(self) -> None:
        root = SCRIPT_ROOT.parents[3]
        example = root / ".ssh.local.example.yaml"
        document = yaml.safe_load(example.read_text(encoding="utf-8"))
        load_deployment_environment(example)
        self.assertEqual(checker.deployment_disclosure_values(document), set())

    def test_pre_push_scan_reports_paths_without_matching_values(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            subprocess.run(["git", "init", "-q"], cwd=root, check=True)
            subprocess.run(["git", "config", "user.name", "Fixture"], cwd=root, check=True)
            subprocess.run(["git", "config", "user.email", "fixture@example.test"], cwd=root, check=True)
            (root / ".ssh.local").write_text(
                "servers:\n  app:\n    host: 10.23.45.67\n"
                "deployment:\n  schema: 1\n  public_domain: private.example.test\n",
                encoding="utf-8",
            )
            (root / "deploy.conf").write_text("endpoint=10.23.45.67\n", encoding="utf-8")
            subprocess.run(["git", "add", "deploy.conf"], cwd=root, check=True)
            subprocess.run(["git", "commit", "-qm", "fixture"], cwd=root, check=True)
            (root / "deploy.conf").write_text("endpoint=generic.example.test\n", encoding="utf-8")
            subprocess.run(["git", "add", "deploy.conf"], cwd=root, check=True)
            subprocess.run(["git", "commit", "-qm", "remove fixture endpoint"], cwd=root, check=True)
            commit = subprocess.run(["git", "rev-parse", "HEAD"], cwd=root, check=True, capture_output=True, text=True).stdout.strip()
            result = checker.check_push(root, [f"refs/heads/main {commit} refs/heads/main {checker.ZERO_SHA}"])
            self.assertEqual(result, ["deploy.conf"])

    def test_repository_ignores_private_config_and_excludes_it_from_docker_context(self) -> None:
        root = SCRIPT_ROOT.parents[3]
        gitignore = (root / ".gitignore").read_text(encoding="utf-8")
        dockerignore = (root / ".dockerignore").read_text(encoding="utf-8")
        self.assertIn("/.ssh.local", gitignore)
        self.assertIn(".ssh.local.example.yaml", gitignore)
        self.assertIn(".ssh.local*", dockerignore)


if __name__ == "__main__":
    unittest.main()
