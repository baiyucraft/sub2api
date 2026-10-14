"""A real load command is mandatory and only runs after signed input checks."""
import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parents[2]))
from release import baseline_candidate_load as candidate


class CandidateLoadTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name).resolve()
        os.chmod(self.root, 0o700)
        self.release = "270-aaaaaaaaaaaa-1-deadbeef"
        self.drill = "full-dr-12345678-1234-4234-8234-123456789abc"
        self.image = "sha256:" + "a" * 64
        self.paths = {name: self.root / name for name in ("archive", "gate", "signature", "trust")}
        for name, path in self.paths.items():
            path.write_bytes((name + " fixture").encode()); os.chmod(path, 0o600)
        self.document = {"manifest": {"schema": 2, "profile": "270", "release_id": self.release}, "evidence": {"candidate_image_id": self.image, "candidate_archive_sha256": hashlib.sha256(self.paths["archive"].read_bytes()).hexdigest()}}
        self.write_gate()
        self.proof = {"host_namespaces": {}, "rootfs_identity_sha256": "b" * 64, "host_docker_engine_id": "HOST"}
        self.args = {**self.paths, "expected_trust_sha256": hashlib.sha256(self.paths["trust"].read_bytes()).hexdigest(), "proof": self.proof, "drill_id": self.drill, "release_id": self.release, "output": self.root / "candidate-load.json"}

    def write_gate(self):
        self.paths["gate"].write_text(json.dumps(self.document))

    def command(self, argv, **kwargs):
        if argv[0] == "openssl": return subprocess.CompletedProcess(argv, 0, b"", b"")
        if argv == ["docker", "info", "--format", "{{.ID}}"]: raw = b"GUEST\n"
        elif argv == ["docker", "info", "--format", "{{.DockerRootDir}}"]: raw = b"/var/lib/docker\n"
        elif argv[:3] == ["docker", "load", "--input"]: raw = b"sensitive tag suppressed"
        elif argv[:3] == ["docker", "image", "inspect"]: raw = (self.image + "\n").encode()
        else: raise AssertionError(argv)
        return subprocess.CompletedProcess(argv, 0, raw, b"")

    def test_load_and_inspect_are_real_commands_and_receipt_binds_guest(self):
        with mock.patch.object(candidate, "assert_isolated_guest"), mock.patch.object(candidate, "run_hidden", side_effect=self.command) as run:
            result = candidate.collect(**self.args)
        self.assertEqual(json.loads(self.args["output"].read_bytes()), result)
        self.assertEqual(result["guest_docker_engine_id_sha256"], hashlib.sha256(b"GUEST").hexdigest())
        self.assertEqual(result["rootfs_identity_sha256"], self.proof["rootfs_identity_sha256"])
        commands = [call.args[0] for call in run.call_args_list]
        self.assertLess(commands.index(["docker", "load", "--input", str(self.paths["archive"])]), next(i for i, cmd in enumerate(commands) if cmd[:3] == ["docker", "image", "inspect"]))

    def test_isolation_failure_cannot_load(self):
        with mock.patch.object(candidate, "assert_isolated_guest", side_effect=RuntimeError("host namespace")), mock.patch.object(candidate, "run_hidden") as run:
            with self.assertRaises(RuntimeError): candidate.collect(**self.args)
        run.assert_not_called()

    def test_bad_signature_or_archive_refuses_docker(self):
        for bad_signature in (True, False):
            with self.subTest(bad_signature=bad_signature):
                self.write_gate()
                if not bad_signature:
                    self.document["evidence"]["candidate_archive_sha256"] = "c" * 64; self.write_gate()
                with mock.patch.object(candidate, "assert_isolated_guest"), mock.patch.object(candidate, "run_hidden", return_value=subprocess.CompletedProcess([], 1 if bad_signature else 0, b"", b"")) as run:
                    with self.assertRaises(RuntimeError): candidate.collect(**self.args)
                    self.assertTrue(all(call.args[0][0] == "openssl" for call in run.call_args_list))

    def test_host_engine_and_wrong_loaded_image_never_emit_receipt(self):
        for failure in ("host", "image", "load"):
            def command(argv, **kwargs):
                result = self.command(argv, **kwargs)
                if failure == "host" and argv[-1] == "{{.ID}}": result.stdout = b"HOST\n"
                if failure == "image" and argv[:3] == ["docker", "image", "inspect"]: result.stdout = ("sha256:" + "d" * 64).encode()
                if failure == "load" and argv[:2] == ["docker", "load"]: result.returncode = 1
                return result
            with self.subTest(failure=failure), mock.patch.object(candidate, "assert_isolated_guest"), mock.patch.object(candidate, "run_hidden", side_effect=command):
                with self.assertRaises(RuntimeError): candidate.collect(**self.args)
                self.assertFalse(self.args["output"].exists())

    def test_existing_receipt_blocks_before_load(self):
        self.args["output"].write_bytes(b"original failure evidence")
        with mock.patch.object(candidate, "assert_isolated_guest"), mock.patch.object(candidate, "run_hidden") as run:
            with self.assertRaises(RuntimeError): candidate.collect(**self.args)
        run.assert_not_called()
        self.assertEqual(self.args["output"].read_bytes(), b"original failure evidence")

    def test_other_release_gate_cannot_authorize_current_candidate(self):
        self.document["manifest"]["release_id"] = self.release.replace("deadbeef", "deadbeee"); self.write_gate()
        with mock.patch.object(candidate, "assert_isolated_guest"), mock.patch.object(candidate, "run_hidden", side_effect=self.command) as run:
            with self.assertRaisesRegex(RuntimeError, "Gate release identity"): candidate.collect(**self.args)
        self.assertFalse(any(call.args[0][0] == "docker" for call in run.call_args_list))


if __name__ == "__main__": unittest.main()
