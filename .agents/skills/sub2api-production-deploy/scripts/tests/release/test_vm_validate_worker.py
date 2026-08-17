from __future__ import annotations

import sys
import unittest
from pathlib import Path


DEPLOY_ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(DEPLOY_ROOT))

from release.vm_validate import (  # noqa: E402
    VM_WORKER_FIELDS,
    VMWorkerNotStartedError,
    VMWorkerStateUnknownError,
    _poll_vm_worker_script,
    _remote_vm_worker_script,
    _validate_completed_vm_worker,
    _wait_for_vm_worker,
)


class Result:
    def __init__(self, values: dict[str, str]):
        self.values = values


def worker_result(
    status: str,
    *,
    token: str = "no",
    exit_code: str = "pending",
    raw_log_status: str = "ok",
    gate_ready: str = "no",
) -> Result:
    return Result(
        {
            "worker_status": status,
            "worker_pid": "321",
            "worker_token_match": token,
            "worker_exit_code": exit_code,
            "gate_stage": "candidate_build",
            "gate_failure_category": "absent",
            "gate_output_ready": gate_ready,
            "gate_signature_ready": gate_ready,
            "candidate_archive_ready": gate_ready,
            "raw_log_status": raw_log_status,
            "raw_log_bytes": "123",
        }
    )


class FakeRunner:
    def __init__(self, actions: list[Result | BaseException]):
        self.actions = actions
        self.calls: list[tuple[str, str, set[str], int]] = []

    def run(self, host: str, command: str, fields: set[str], timeout: int = 120) -> Result:
        self.calls.append((host, command, fields, timeout))
        action = self.actions.pop(0)
        if isinstance(action, BaseException):
            raise action
        return action


class VMValidateWorkerTest(unittest.TestCase):
    def wait(self, runner: FakeRunner, **kwargs) -> dict[str, str]:
        return _wait_for_vm_worker(
            runner,
            remote_root="/opt/sub2api-deploy/release-input/validation.test",
            remote_output="/opt/sub2api-deploy/release-gates/239-test/output",
            release_id="239-test",
            timeout_seconds=5,
            poll_seconds=0,
            **kwargs,
        )

    def test_worker_is_detached_and_keeps_raw_output_on_vm(self) -> None:
        script = _remote_vm_worker_script(
            remote_root="/opt/sub2api-deploy/release-input/validation.test",
            remote_manifest="/opt/sub2api-deploy/release-input/validation.test/manifest.json",
            remote_output="/opt/sub2api-deploy/release-gates/239-test/output",
            validator="/usr/local/libexec/sub2api-vm-validate",
            release_id="239-test",
        )
        self.assertIn("command -v setsid", script)
        self.assertIn('nohup setsid "$wrapper" >/dev/null 2>&1 </dev/null &', script)
        self.assertIn('>>"$raw_log" 2>&1', script)
        self.assertIn("/opt/sub2api-deploy/release-logs/239-test/vm-validate.raw.log", script)
        self.assertIn("0:0:700", script)
        self.assertIn("0:0:600:1", script)
        self.assertLess(script.index('state_write exit_code "$exit_code"'), script.index("state_write status exited"))

    def test_poll_only_returns_structured_metadata(self) -> None:
        script = _poll_vm_worker_script(
            remote_root="/opt/sub2api-deploy/release-input/validation.test",
            remote_output="/opt/sub2api-deploy/release-gates/239-test/output",
            release_id="239-test",
        )
        self.assertIn("/proc/$pid/stat", script)
        self.assertIn("/proc/sys/kernel/random/boot_id", script)
        self.assertIn("raw_log_status=", script)
        self.assertIn("raw_log_bytes=", script)
        self.assertNotIn('cat "$raw_log"', script)
        self.assertEqual(VM_WORKER_FIELDS, {
            "worker_status", "worker_pid", "worker_token_match", "worker_exit_code",
            "gate_stage", "gate_failure_category", "gate_output_ready",
            "gate_signature_ready", "candidate_archive_ready", "raw_log_status",
            "raw_log_bytes",
        })

    def test_transient_ssh_failure_recovers_without_relaunch(self) -> None:
        runner = FakeRunner(
            [
                OSError("connection reset"),
                worker_result("running", token="yes"),
                worker_result("exited", exit_code="0", gate_ready="yes"),
            ]
        )
        result = self.wait(runner)
        self.assertEqual(result["worker_status"], "exited")
        self.assertEqual(len(runner.calls), 3)

    def test_exited_state_waits_for_process_identity_to_disappear(self) -> None:
        runner = FakeRunner(
            [
                worker_result("exited", token="yes", exit_code="0", gate_ready="yes"),
                worker_result("exited", token="no", exit_code="0", gate_ready="yes"),
            ]
        )
        result = self.wait(runner)
        self.assertEqual(result["worker_token_match"], "no")

    def test_repeated_absent_handshake_is_safe_not_started_failure(self) -> None:
        runner = FakeRunner([worker_result("absent"), worker_result("absent")])
        with self.assertRaisesRegex(VMWorkerNotStartedError, "startup handshake"):
            self.wait(runner, max_absent_polls=2)

    def test_running_worker_with_lost_identity_fails_closed(self) -> None:
        runner = FakeRunner(
            [worker_result("running", token="no"), worker_result("running", token="no")]
        )
        with self.assertRaisesRegex(VMWorkerStateUnknownError, "lost its process identity"):
            self.wait(runner, max_stale_polls=2)

    def test_invalid_worker_state_contract_fails_closed(self) -> None:
        runner = FakeRunner([worker_result("invalid")])
        with self.assertRaisesRegex(VMWorkerStateUnknownError, "invalid worker status"):
            self.wait(runner)

    def test_nonzero_worker_exit_is_rejected(self) -> None:
        values = worker_result("exited", exit_code="17", gate_ready="yes").values
        with self.assertRaisesRegex(RuntimeError, "remote VM validator failed"):
            _validate_completed_vm_worker(values)

    def test_invalid_raw_log_contract_is_rejected(self) -> None:
        values = worker_result(
            "exited", exit_code="0", raw_log_status="invalid", gate_ready="yes"
        ).values
        with self.assertRaisesRegex(RuntimeError, "raw log contract"):
            _validate_completed_vm_worker(values)

    def test_missing_gate_artifact_is_rejected(self) -> None:
        values = worker_result("exited", exit_code="0", gate_ready="no").values
        with self.assertRaisesRegex(RuntimeError, "complete Gate output"):
            _validate_completed_vm_worker(values)

    def test_unknown_worker_state_preserves_remote_input_contract(self) -> None:
        source = (DEPLOY_ROOT / "release" / "vm_validate.py").read_text(encoding="utf-8")
        launch = source.index("cleanup_remote_root = False")
        wait = source.index("worker = _wait_for_vm_worker", launch)
        cleanup = source.index("if cleanup_remote_root:", wait)
        self.assertLess(launch, wait)
        self.assertLess(wait, cleanup)
        self.assertIn("except VMWorkerNotStartedError", source[wait:cleanup])
        self.assertNotIn("except VMWorkerStateUnknownError", source[wait:cleanup])


if __name__ == "__main__":
    unittest.main()
