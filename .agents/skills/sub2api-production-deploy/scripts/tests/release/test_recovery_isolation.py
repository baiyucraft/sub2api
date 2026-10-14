"""Guard logic only; these tests are never accepted as a real recovery drill."""
from copy import deepcopy
import json
import os
from pathlib import Path
import subprocess
import sys
import shutil
from types import SimpleNamespace
import unittest
from unittest import mock


SCRIPT = Path(__file__).resolve().parents[2] / "maintenance/release/recovery-isolation.sh"
NAMESPACES = ("mnt", "pid", "net", "user", "cgroup", "uts", "ipc")
BUNDLE = "a" * 64


class RecoveryIsolationTests(unittest.TestCase):
    def setUp(self):
        self.proof = {
            "schema": 1,
            "backend": "nspawn-offline-private-users",
            "helper_bundle_sha256": BUNDLE,
            "host_namespaces": {name: f"{name}:[100]" for name in NAMESPACES},
            "host_docker_engine_id": "host-engine-0123456789",
        }
        self.guest_namespaces = {name: f"{name}:[200]" for name in NAMESPACES}
        self.uid_map = "0 65536 65536\n"
        self.interfaces = ["lo", "docker0", "br-abcdef012345", "vethabc123"]
        self.default_routes = []
        self.engine = "guest-engine-0123456789"

    def run_guard(self):
        source = SCRIPT.read_text(encoding="utf-8").split("<<'PY'\n", 1)[1].rsplit("\nPY", 1)[0]

        def read_text(path, *args, **kwargs):
            if path.as_posix() == "/run/proof.json":
                return json.dumps(self.proof)
            if path.as_posix() == "/proc/self/uid_map":
                return self.uid_map
            raise AssertionError(f"unexpected fixture read: {path}")

        def readlink(path):
            name = path.rsplit("/", 1)[1]
            return self.guest_namespaces[name]

        with mock.patch.object(Path, "read_text", read_text), \
             mock.patch.object(Path, "is_file", return_value=True), \
             mock.patch.object(Path, "iterdir", return_value=[SimpleNamespace(name=item) for item in self.interfaces]), \
             mock.patch.object(os, "readlink", side_effect=readlink), \
             mock.patch.object(subprocess, "run", return_value=SimpleNamespace(stdout=json.dumps(self.default_routes))), \
             mock.patch.object(sys, "argv", ["isolation", "/run/proof.json", BUNDLE]), \
             mock.patch.dict(os.environ, {"RECOVERY_GUEST_ENGINE_ID": self.engine}):
            exec(compile(source, str(SCRIPT), "exec"), {})

    def test_offline_private_namespaces_are_accepted(self):
        self.run_guard()

    def test_each_shared_host_namespace_is_rejected(self):
        original = deepcopy(self.guest_namespaces)
        for namespace in NAMESPACES:
            with self.subTest(namespace=namespace):
                self.guest_namespaces = deepcopy(original)
                self.guest_namespaces[namespace] = self.proof["host_namespaces"][namespace]
                with self.assertRaises(SystemExit):
                    self.run_guard()

    def test_host_root_mapping_is_rejected(self):
        self.uid_map = "0 0 4294967295\n"
        with self.assertRaises(SystemExit):
            self.run_guard()

    def test_same_docker_engine_is_rejected(self):
        self.engine = self.proof["host_docker_engine_id"]
        with self.assertRaises(SystemExit):
            self.run_guard()

    def test_external_route_is_rejected(self):
        self.default_routes = [{"dst": "default", "gateway": "192.0.2.1"}]
        with self.assertRaises(SystemExit):
            self.run_guard()

    def test_host_facing_network_interface_is_rejected(self):
        for name in ("eth0", "host0", "ve-drill", "tun0"):
            with self.subTest(interface=name):
                self.interfaces = ["lo", "docker0", name]
                with self.assertRaises(SystemExit):
                    self.run_guard()

    def test_proof_bundle_drift_is_rejected(self):
        self.proof["helper_bundle_sha256"] = "b" * 64
        with self.assertRaises(SystemExit):
            self.run_guard()

    def test_partial_or_unknown_proof_is_rejected(self):
        for field in ("host_namespaces", "host_docker_engine_id", "helper_bundle_sha256"):
            with self.subTest(field=field):
                old = self.proof.pop(field)
                with self.assertRaises(SystemExit):
                    self.run_guard()
                self.proof[field] = old
        self.proof["approved"] = True
        with self.assertRaises(SystemExit):
            self.run_guard()

    def test_marker_alone_cannot_replace_runtime_proof(self):
        source = SCRIPT.read_text(encoding="utf-8")
        self.assertIn("0:0:600:1", source)
        self.assertIn("systemd-detect-virt --container", source)
        self.assertIn("docker context show", source)
        self.assertIn('[[ -S /run/docker.sock && ! -L /run/docker.sock ]]', source)
        self.assertIn('RECOVERY_GUEST_ENGINE_ID="$engine_id" python3', source)

    def test_shell_guard_remains_fail_closed_inside_if(self):
        git_bash = Path("C:/Program Files/Git/bin/bash.exe")
        bash = str(git_bash) if git_bash.is_file() else shutil.which("bash")
        if not bash:
            self.skipTest("bash unavailable")
        source = 'source "$1"; python3(){ echo unsafe_python_invoked; }; export RECOVERY_DRILL_MODE=1; if recovery_drill_assert_isolated invalid; then echo unsafe; else echo rejected; fi'
        result = subprocess.run([bash, "-c", source, "guard-test", SCRIPT.as_posix()],
                                capture_output=True, text=True, check=True)
        self.assertEqual(result.stdout.strip(), "rejected")


if __name__ == "__main__":
    unittest.main()
