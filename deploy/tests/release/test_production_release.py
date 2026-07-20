from __future__ import annotations

import os
import shutil
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock


DEPLOY_ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(DEPLOY_ROOT))

from release.production import ProductionRelease
from release.production import emit_progress
from release.production import gate_consumption_probe_script
from release.production import quoted_env


class ProductionRecoveryTest(unittest.TestCase):
    def test_progress_output_failure_is_non_fatal(self) -> None:
        with mock.patch("builtins.print", side_effect=BrokenPipeError):
            emit_progress("stage=freeze")

    def test_quoted_env_quotes_shell_metacharacters(self) -> None:
        self.assertEqual(quoted_env({"VALUE": "a b;$(x)"}), "VALUE='a b;$(x)'")

    def release(self) -> ProductionRelease:
        instance = object.__new__(ProductionRelease)
        instance.frozen = True
        instance.units_masked = True
        instance.mask_intent = False
        instance.public_exposed = False
        instance.migration_started = False
        instance.state_dir = "/state"
        instance.release_id = "182-aaaaaaaaaaaa-1-aaaaaaaa"
        instance.release_dir = "/release"
        instance.image_id = "sha256:" + "a" * 64
        instance.active_assets = "/active/assets"
        instance.result = {"status": "running", "history": []}
        instance.stage = mock.Mock()
        instance.run_remote = mock.Mock(side_effect=[
            {"old_application_resumed": "true", "running_image_id": "old"},
            {"backup_units_restored": "true"},
            {"plaintext_state_removed": "true"},
            {"release_claim_reconciled": "true"},
        ])
        return instance

    def test_pre_migration_failure_resumes_old_application(self) -> None:
        release = self.release()
        release.recover()
        first_script = release.run_remote.call_args_list[0].args[1]
        self.assertIn("resume-old.sh", first_script)
        self.assertNotIn("restore.sh", first_script)
        self.assertEqual(release.result["status"], "recovered")

    def test_freeze_marks_state_only_after_remote_success(self) -> None:
        release = self.release()
        release.frozen = False
        release.units_masked = False
        release.run_remote = mock.Mock(side_effect=RuntimeError("freeze failed"))
        with self.assertRaisesRegex(RuntimeError, "freeze failed"):
            release.freeze()
        self.assertFalse(release.frozen)
        self.assertFalse(release.units_masked)

    def test_freeze_drains_scheduler_outbox_before_stopping_application(self) -> None:
        freeze = (DEPLOY_ROOT / "maintenance" / "release" / "freeze.sh").read_text(encoding="utf-8")
        production = (DEPLOY_ROOT / "release" / "production.py").read_text(encoding="utf-8")

        self.assertLess(freeze.index("systemctl stop nginx"), freeze.index("sched:v2:outbox:watermark"))
        self.assertLess(freeze.index("sched:v2:outbox:watermark"), freeze.index("docker compose stop -t 30 sub2api"))
        self.assertEqual(freeze.count("$outbox_watermark -ge $outbox_highwater"), 3)
        self.assertEqual(freeze.count("timeout 3s docker exec"), 4)
        self.assertGreater(freeze.rindex("sched:v2:outbox:watermark"), freeze.index("docker compose stop -t 30 sub2api"))
        self.assertIn("drain_deadline=$((SECONDS + 30))", freeze)
        self.assertIn('"outbox_drained"', production)

    def test_backup_rejects_unready_local_restore_point(self) -> None:
        release = self.release()
        release.profile = {"minimum_backup_free_bytes": 1}
        release.frozen = False
        release.units_masked = False
        release.run_remote = mock.Mock(return_value={
            "backup_units_masked": "true",
            "writes_frozen": "true",
            "state_dir": "/state",
            "pre_switch_image_id": "old",
            "compose_sha256": "digest",
            "artifact": "artifact",
            "transport_artifact": "transport",
            "artifact_size": "1",
            "artifact_sha256": "digest",
            "no_restart_path_proven": "true",
            "local_restore_point_ready": "false",
        })

        with self.assertRaisesRegex(RuntimeError, "local coordinated restore point is not ready"):
            release.backup()

        self.assertFalse(release.frozen)
        self.assertFalse(release.units_masked)

    def test_recovery_detects_committed_remote_freeze(self) -> None:
        release = self.release()
        release.frozen = False
        release.units_masked = False
        release.remote_pre_switch_recovery_needed = mock.Mock(return_value=True)
        release.run_remote.side_effect = [
            {"old_application_resumed": "true", "running_image_id": "old"},
            {"plaintext_state_removed": "true"},
            {"release_claim_reconciled": "true"},
        ]
        release.recover()
        self.assertIn("resume-old.sh", release.run_remote.call_args_list[0].args[1])
        self.assertEqual(release.result["status"], "recovered")

    def test_partial_freeze_probe_requires_recovery(self) -> None:
        release = self.release()
        release.run_remote = mock.Mock(return_value={"recovery_needed": "true"})
        self.assertTrue(release.remote_pre_switch_recovery_needed())
        script = release.run_remote.call_args.args[1]
        self.assertIn('test "$app_status" != running || test "$nginx_status" != active', script)

    def test_remote_freeze_probe_is_fail_closed(self) -> None:
        release = self.release()
        release.run_remote = mock.Mock(side_effect=RuntimeError("ssh interrupted"))
        self.assertIsNone(release.remote_pre_switch_recovery_needed())

    def test_post_migration_failure_runs_coordinated_restore(self) -> None:
        release = self.release()
        release.migration_started = True
        release.run_remote.side_effect = [
            {"coordinated_restore": "verified", "restored_image_id": "old", "application_health": "pass"},
            {"backup_units_restored": "true"},
            {"plaintext_state_removed": "true"},
            {"release_claim_reconciled": "true"},
        ]
        release.recover()
        first_script = release.run_remote.call_args_list[0].args[1]
        self.assertIn("/restore.sh", first_script)
        self.assertNotIn("resume-old.sh", first_script)
        self.assertEqual(release.result["status"], "recovered")

    def test_reconcile_lost_reply_checks_committed_recovery(self) -> None:
        release = self.release()
        release.frozen = False
        release.units_masked = False
        release.remote_pre_switch_recovery_needed = mock.Mock(return_value=False)
        release.run_remote = mock.Mock(side_effect=[{"plaintext_state_removed": "true"}, RuntimeError("reply lost"), {"release_claim_reconciled": "true"}])
        release.recover()
        self.assertIn(".recovered/marker", release.run_remote.call_args_list[2].args[1])
        self.assertEqual(release.result["status"], "recovered")

    def test_public_exposure_failure_never_calls_snapshot_recovery(self) -> None:
        release = self.release()
        release.claimed = True
        release.public_exposed = True
        release.remote_gate_consumed = mock.Mock(return_value=False)
        release.emergency_close = mock.Mock()
        release.recover = mock.Mock()
        release.upload_assets = mock.Mock(side_effect=RuntimeError("canary failed"))
        with self.assertRaisesRegex(RuntimeError, "canary failed"):
            release.execute()
        release.emergency_close.assert_called_once()
        release.recover.assert_not_called()
        self.assertEqual(release.result["status"], "blocked_reconciliation")

    def test_remote_claim_probe_is_fail_closed(self) -> None:
        release = self.release()
        release.release_id = "182-aaaaaaaaaaaa-1-aaaaaaaa"
        release.release_dir = "/opt/sub2api/releases/182-aaaaaaaaaaaa-1-aaaaaaaa"
        release.run_remote = mock.Mock(return_value={"gate_claimed": "true"})
        self.assertTrue(release.remote_gate_claimed())
        script = release.run_remote.call_args.args[1]
        self.assertIn(".active-release/release_id", script)
        self.assertNotIn(".claimed", script)

    def test_remote_claim_probe_failure_does_not_guess(self) -> None:
        release = self.release()
        release.release_id = "182-aaaaaaaaaaaa-1-aaaaaaaa"
        release.release_dir = "/opt/sub2api/releases/182-aaaaaaaaaaaa-1-aaaaaaaa"
        release.run_remote = mock.Mock(side_effect=RuntimeError("ssh interrupted"))
        self.assertIsNone(release.remote_gate_claimed())

    def test_remote_claim_probe_reports_explicit_absence(self) -> None:
        release = self.release()
        release.release_id = "182-aaaaaaaaaaaa-1-aaaaaaaa"
        release.run_remote = mock.Mock(return_value={"gate_claimed": "false"})
        self.assertFalse(release.remote_gate_claimed())
        self.assertIn("gate_claimed=false", release.run_remote.call_args.args[1])

    def test_active_claim_probe_detects_incomplete_claim(self) -> None:
        release = self.release()
        release.run_remote = mock.Mock(return_value={"active_claim": "true"})
        self.assertTrue(release.remote_active_claim_exists())

    def test_active_claim_probe_failure_does_not_guess(self) -> None:
        release = self.release()
        release.run_remote = mock.Mock(side_effect=RuntimeError("ssh interrupted"))
        self.assertIsNone(release.remote_active_claim_exists())

    def test_active_claim_probe_reports_explicit_absence(self) -> None:
        release = self.release()
        release.run_remote = mock.Mock(return_value={"active_claim": "false"})
        self.assertFalse(release.remote_active_claim_exists())
        self.assertIn("active_claim=false", release.run_remote.call_args.args[1])

    def test_consumed_probe_requires_healthy_candidate(self) -> None:
        release = self.release()
        release.image_id = "sha256:" + "a" * 64
        release.release_dir = "/opt/sub2api/releases/182-aaaaaaaaaaaa-1-aaaaaaaa"
        release.run_remote = mock.Mock(return_value={"gate_consumed": "true"})
        self.assertTrue(release.remote_gate_consumed())
        script = release.run_remote.call_args.args[1]
        self.assertIn(".State.Health.Status", script)
        self.assertIn("= healthy", script)

    def test_consumed_probe_failure_is_unknown(self) -> None:
        release = self.release()
        release.run_remote = mock.Mock(side_effect=RuntimeError("reply lost"))
        self.assertIsNone(release.remote_gate_consumed())

    def test_consumed_probe_reports_valid_unconsumed_claim(self) -> None:
        release = self.release()
        release.release_id = "182-aaaaaaaaaaaa-1-aaaaaaaa"
        release.image_id = "sha256:" + "a" * 64
        release.release_dir = "/opt/sub2api/releases/182-aaaaaaaaaaaa-1-aaaaaaaa"
        release.run_remote = mock.Mock(return_value={"gate_consumed": "false"})

        self.assertFalse(release.remote_gate_consumed())
        script = release.run_remote.call_args.args[1]
        self.assertIn("gate_consumed=false", script)
        self.assertIn("sha256sum -c CLAIM_SHA256SUMS", script)
        self.assertIn("gate_consumed=unknown", script)

    def test_consumed_probe_reports_inconsistent_state_as_unknown(self) -> None:
        release = self.release()
        release.run_remote = mock.Mock(return_value={"gate_consumed": "unknown"})
        self.assertIsNone(release.remote_gate_consumed())

    def test_consumption_probe_shell_covers_true_false_and_unknown(self) -> None:
        bash = shutil.which("bash")
        if bash is None and os.name == "nt":
            candidate = Path(os.environ.get("ProgramFiles", r"C:\Program Files")) / "Git" / "bin" / "bash.exe"
            bash = str(candidate) if candidate.is_file() else None
        if bash is None:
            self.skipTest("bash is unavailable")

        release_id = "182-aaaaaaaaaaaa-1-aaaaaaaa"
        image_id = "sha256:" + "a" * 64
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            release_dir = root / "release"
            active = root / "active"
            fake_bin = root / "bin"
            release_dir.mkdir()
            active.mkdir()
            fake_bin.mkdir()
            release_id_file = active / "release_id"
            gate_file = active / "gate.json"
            release_id_file.write_bytes(f"release_id={release_id}\n".encode())
            gate_file.write_bytes(b"{}\n")

            import hashlib

            checksums = []
            for path in (release_id_file, gate_file):
                checksums.append(f"{hashlib.sha256(path.read_bytes()).hexdigest()}  {path.name}")
            checksum_file = active / "CLAIM_SHA256SUMS"
            checksum_file.write_bytes(("\n".join(checksums) + "\n").encode())
            jq = fake_bin / "jq"
            jq.write_bytes(
                (
                    "#!/usr/bin/env bash\ncase \"$2\" in\n"
                    "  .manifest.release_id) printf '%s\\n' \"$FAKE_RELEASE_ID\" ;;\n"
                    "  .evidence.candidate_image_id) printf '%s\\n' \"$FAKE_IMAGE_ID\" ;;\n"
                    "  *) exit 1 ;;\nesac\n"
                ).encode(),
            )
            docker = fake_bin / "docker"
            docker.write_bytes(
                (
                    "#!/usr/bin/env bash\ncase \"$*\" in\n"
                    "  *Health.Status*) printf 'healthy\\n' ;;\n"
                    "  *) printf '%s\\n' \"$FAKE_IMAGE_ID\" ;;\nesac\n"
                ).encode(),
            )
            systemctl = fake_bin / "systemctl"
            systemctl.write_bytes(b"#!/usr/bin/env bash\nprintf 'enabled\\n'\n")
            for executable in (jq, docker, systemctl):
                executable.chmod(0o755)

            environment = os.environ.copy()
            environment.update({
                "FAKE_RELEASE_ID": release_id,
                "FAKE_IMAGE_ID": image_id,
                "PATH": str(fake_bin) + os.pathsep + environment.get("PATH", ""),
            })
            script = gate_consumption_probe_script(release_dir.as_posix(), release_id, image_id, active.as_posix())

            valid = subprocess.run([bash, "-c", script], check=True, capture_output=True, text=True, env=environment)
            self.assertEqual(valid.stdout, "gate_consumed=false\n")

            checksum_file.write_bytes(("0" * 64 + "  gate.json\n").encode())
            corrupt = subprocess.run([bash, "-c", script], check=True, capture_output=True, text=True, env=environment)
            self.assertEqual(corrupt.stdout, "gate_consumed=unknown\n")

            checksum_file.write_bytes(("\n".join(checksums) + "\n").encode())
            environment["FAKE_IMAGE_ID"] = "sha256:" + "b" * 64
            mismatched = subprocess.run([bash, "-c", script], check=True, capture_output=True, text=True, env=environment)
            self.assertEqual(mismatched.stdout, "gate_consumed=unknown\n")

            environment["FAKE_IMAGE_ID"] = image_id
            (release_dir / ".recovered").mkdir()
            contradictory = subprocess.run([bash, "-c", script], check=True, capture_output=True, text=True, env=environment)
            self.assertEqual(contradictory.stdout, "gate_consumed=unknown\n")
            (release_dir / ".recovered").rmdir()

            shutil.rmtree(active)
            consumed = release_dir / ".consumed"
            consumed.mkdir()
            marker = consumed / "marker"
            marker.write_bytes(f"release_id=182-bbbbbbbbbbbb-2-bbbbbbbb\ncandidate_image_id={image_id}\n".encode())
            (consumed / "plaintext-cleaned").write_bytes(b"true\n")
            wrong_release = subprocess.run([bash, "-c", script], check=True, capture_output=True, text=True, env=environment)
            self.assertEqual(wrong_release.stdout, "gate_consumed=unknown\n")

            marker.write_bytes(f"release_id={release_id}\ncandidate_image_id={image_id}\n".encode())
            completed = subprocess.run([bash, "-c", script], check=True, capture_output=True, text=True, env=environment)
            self.assertEqual(completed.stdout, "gate_consumed=true\n")

    def test_unconsumed_claim_before_public_exposure_runs_recovery(self) -> None:
        release = self.release()
        release.claimed = True
        release.public_exposed = False
        release.upload_assets = mock.Mock(side_effect=RuntimeError("preflight failed"))
        release.remote_gate_consumed = mock.Mock(return_value=False)
        release.recover = mock.Mock()

        with self.assertRaisesRegex(RuntimeError, "preflight failed"):
            release.execute()

        release.recover.assert_called_once()

    def test_unknown_consumption_status_closes_public_traffic(self) -> None:
        release = self.release()
        release.claimed = True
        release.public_exposed = True
        release.upload_assets = mock.Mock(side_effect=RuntimeError("reply lost"))
        release.remote_gate_consumed = mock.Mock(return_value=None)
        release.emergency_close = mock.Mock()

        with self.assertRaisesRegex(RuntimeError, "reply lost"):
            release.execute()

        release.emergency_close.assert_called_once()
        self.assertEqual(release.result["status"], "blocked_reconciliation")

    def test_unknown_consumption_status_without_public_exposure_does_not_close(self) -> None:
        release = self.release()
        release.claimed = True
        release.public_exposed = False
        release.upload_assets = mock.Mock(side_effect=RuntimeError("reply lost"))
        release.remote_gate_consumed = mock.Mock(return_value=None)
        release.emergency_close = mock.Mock()

        with self.assertRaisesRegex(RuntimeError, "reply lost"):
            release.execute()

        release.emergency_close.assert_not_called()

    def test_unknown_consumption_status_stays_blocked_when_close_is_unconfirmed(self) -> None:
        release = self.release()
        release.claimed = True
        release.public_exposed = True
        release.upload_assets = mock.Mock(side_effect=RuntimeError("reply lost"))
        release.remote_gate_consumed = mock.Mock(return_value=None)
        release.emergency_close = mock.Mock(side_effect=RuntimeError("close reply lost"))

        with self.assertRaisesRegex(RuntimeError, "reply lost"):
            release.execute()

        self.assertEqual(release.result["status"], "blocked_reconciliation")
        evidence = release.stage.call_args.args[1]
        self.assertEqual(evidence["public_close"], "unknown")

    def test_mask_probe_detects_committed_remote_mask(self) -> None:
        release = self.release()
        release.run_remote = mock.Mock(return_value={"units_masked": "true"})
        self.assertTrue(release.remote_units_masked())

    def test_mask_probe_failure_is_unknown(self) -> None:
        release = self.release()
        release.run_remote = mock.Mock(side_effect=RuntimeError("reply lost"))
        self.assertIsNone(release.remote_units_masked())


class RouteCanaryRetryTest(unittest.TestCase):
    def release(self, responses: list[dict[str, str]]) -> ProductionRelease:
        instance = object.__new__(ProductionRelease)
        instance.release_id = "194-aaaaaaaaaaaa-1-aaaaaaaa"
        instance.profile = {"public_domain": "example.test"}
        instance.run_remote_with_input = mock.Mock(side_effect=responses)
        instance.stage = mock.Mock()
        return instance

    def response(self, status: str, curl_exit: str, http_code: str) -> dict[str, str]:
        passed = status == "pass"
        return {
            "route_health": "pass",
            "streaming": "pass" if passed else "fail",
            "curl_exit": curl_exit,
            "http_code": http_code,
            "canary_status": status,
        }

    @mock.patch("release.production.time.sleep")
    def test_timeout_retries_then_succeeds(self, sleep: mock.Mock) -> None:
        release = self.release([
            self.response("retryable", "28", "200"),
            self.response("pass", "0", "200"),
        ])

        result = release.run_route_canary("racknerd", "/route-canary.sh", "direct", "192.0.2.1", b"sk-test-key-1234", "post_switch")

        self.assertEqual(result["canary_status"], "pass")
        self.assertEqual(release.run_remote_with_input.call_count, 2)
        self.assertEqual(sleep.call_args.args[0], 5)
        self.assertTrue(result["user_agent"].endswith("-direct"))
        self.assertEqual(len(result["attempt_user_agents"].split(",")), 2)

    @mock.patch("release.production.time.sleep")
    def test_gateway_5xx_retries_then_succeeds(self, sleep: mock.Mock) -> None:
        release = self.release([
            self.response("retryable", "0", "503"),
            self.response("pass", "0", "200"),
        ])

        release.run_route_canary("backup", "/route-canary.sh", "dmit", "192.0.2.2", b"sk-test-key-1234", "pre_switch")

        self.assertEqual(release.run_remote_with_input.call_count, 2)
        sleep.assert_called_once_with(5)

    @mock.patch("release.production.time.sleep")
    def test_hard_failure_does_not_retry(self, sleep: mock.Mock) -> None:
        release = self.release([self.response("failed", "0", "401")])

        with self.assertRaisesRegex(RuntimeError, "failed without retry"):
            release.run_route_canary("racknerd", "/route-canary.sh", "direct", "192.0.2.1", b"sk-test-key-1234", "pre_switch")

        self.assertEqual(release.run_remote_with_input.call_count, 1)
        sleep.assert_not_called()

    @mock.patch("release.production.time.sleep")
    def test_retry_exhaustion_stops_after_three_attempts(self, sleep: mock.Mock) -> None:
        release = self.release([self.response("retryable", "28", "200")] * 3)

        with self.assertRaisesRegex(RuntimeError, "exhausted retries"):
            release.run_route_canary("racknerd", "/route-canary.sh", "direct", "192.0.2.1", b"sk-test-key-1234", "post_switch")

        self.assertEqual(release.run_remote_with_input.call_count, 3)
        self.assertEqual([call.args[0] for call in sleep.call_args_list], [5, 15])

    def test_post_switch_attribution_script_covers_every_attempt(self) -> None:
        release = object.__new__(ProductionRelease)
        release.profile = {
            "public_domain": "example.test",
            "rack_public_ip": "192.0.2.1",
            "dmit_public_ip": "192.0.2.2",
            "canary_api_key_id": 2,
        }
        release.release_id = "194-aaaaaaaaaaaa-1-aaaaaaaa"
        release.release_dir = "/release"
        release.active_assets = "/active/assets"
        release.state_dir = "/state"
        release.result = {"status": "running", "history": []}
        release.stage = mock.Mock()
        direct_agents = ["sub2api-release-direct-attempt-1-direct", "sub2api-release-direct-attempt-2-direct"]
        dmit_agents = ["sub2api-release-dmit-attempt-1-dmit", "sub2api-release-dmit-attempt-2-dmit"]
        release.verify_streaming_routes = mock.Mock(return_value=(
            {
                "route_health": "pass",
                "streaming": "pass",
                "user_agent": direct_agents[-1],
                "attempt_user_agents": ",".join(direct_agents),
            },
            {
                "route_health": "pass",
                "streaming": "pass",
                "user_agent": dmit_agents[-1],
                "attempt_user_agents": ",".join(dmit_agents),
            },
        ))
        captured: dict[str, str] = {}

        def run_remote(_host: str, script: str, allowed: set[str], timeout: int = 300) -> dict[str, str]:
            del timeout
            if "api.ipify.org" in script:
                return {"backup_public_ip": "198.51.100.1"}
            if "SELECT user_agent" in script:
                captured["usage_script"] = script
            return {key: "pass" for key in allowed}

        release.run_remote = mock.Mock(side_effect=run_remote)
        release.verify_and_finalize()

        script = captured["usage_script"]
        for agent in direct_agents + dmit_agents:
            self.assertIn(agent, script)
        bash = shutil.which("bash") or "C:/Program Files/Git/bin/bash.exe"
        completed = subprocess.run([bash, "-n", "-c", script], capture_output=True, text=True, check=False)
        self.assertEqual(completed.returncode, 0, completed.stderr)


class ReleaseClaimScriptTest(unittest.TestCase):
    def script(self, name: str) -> str:
        return (DEPLOY_ROOT / "maintenance" / "release" / name).read_text(encoding="utf-8")

    def run_route_canary(self, *, stream_exit: int = 0, stream_code: str = "200", stream_body: str = "data: ok") -> dict[str, str]:
        bash = shutil.which("bash")
        if bash is None:
            git_bash = Path("C:/Program Files/Git/bin/bash.exe")
            bash = str(git_bash) if git_bash.exists() else None
        if bash is None:
            self.skipTest("bash is unavailable")
        with tempfile.TemporaryDirectory() as directory:
            fake_curl = Path(directory) / "curl"
            fake_curl.write_text(
                """#!/usr/bin/env bash
set -uo pipefail
output=
args=(\"$@\")
for ((index=0; index < ${#args[@]}; index++)); do
  if [[ ${args[$index]} == -o ]]; then
    output=${args[$((index + 1))]}
  fi
done
url=${args[$((${#args[@]} - 1))]}
if [[ $url == */health ]]; then
  printf '%s' \"${FAKE_HEALTH_CODE:-200}\"
  exit \"${FAKE_HEALTH_EXIT:-0}\"
fi
if [[ -n $output && $output != /dev/null ]]; then
  printf '%s' \"${FAKE_STREAM_BODY:-}\" > \"$output\"
fi
printf '%s' \"${FAKE_STREAM_CODE:-200}\"
exit \"${FAKE_STREAM_EXIT:-0}\"
""",
                encoding="utf-8",
                newline="\n",
            )
            fake_curl.chmod(0o755)
            env = os.environ.copy()
            env.update(
                {
                    "PUBLIC_DOMAIN": "example.test",
                    "ROUTE_IP": "192.0.2.1",
                    "ROUTE_NAME": "direct",
                    "MARKER": "194-aaaaaaaaaaaa-1-aaaaaaaa-test",
                    "FAKE_STREAM_EXIT": str(stream_exit),
                    "FAKE_STREAM_CODE": stream_code,
                    "FAKE_STREAM_BODY": stream_body,
                }
            )
            command = [bash, "maintenance/release/route-canary.sh"]
            if os.name == "nt":
                env["FAKE_CURL_DIR"] = directory
                command = [
                    bash,
                    "-lc",
                    'export PATH="$(cygpath -u "$FAKE_CURL_DIR"):$PATH"; exec bash maintenance/release/route-canary.sh',
                ]
            else:
                env["PATH"] = directory + os.pathsep + env["PATH"]
            completed = subprocess.run(
                command,
                cwd=DEPLOY_ROOT,
                env=env,
                input="sk-test-key-1234\n",
                text=True,
                capture_output=True,
                timeout=15,
                check=False,
            )
        self.assertEqual(completed.returncode, 0, completed.stderr)
        self.assertEqual(completed.stderr, "")
        return dict(line.split("=", 1) for line in completed.stdout.splitlines())

    def test_prepare_rejects_linked_candidate_and_copies_assets(self) -> None:
        script = self.script("prepare.sh")
        self.assertIn("! -L $release_dir/candidate.tar.gz", script)
        self.assertIn("stat -c '%h' \"$release_dir/candidate.tar.gz\"", script)
        self.assertIn("install -m 500 \"$path\"", script)
        self.assertNotIn("$release_dir/.claimed", script)

    def test_context_reads_release_id_in_prepared_format(self) -> None:
        context = self.script("context.sh")
        self.assertIn('grep -Fxq "release_id=$release_id" "$active_claim/release_id"', context)

    def test_cleanup_supports_failure_before_recovery_point(self) -> None:
        cleanup = self.script("cleanup-state.sh")
        self.assertIn("if [[ -d $state_dir && ! -L $state_dir ]]", cleanup)
        self.assertIn("[[ ! -e $state_dir && ! -L $state_dir ]]", cleanup)
        self.assertIn('pre-migrations.tsv', cleanup)
        self.assertIn('SELECT filename,checksum FROM schema_migrations ORDER BY filename', cleanup)
        self.assertIn("systemctl is-enabled sub2api-backup.timer", cleanup)

    def test_preflight_accepts_absent_or_matching_migration_only(self) -> None:
        preflight = self.script("preflight.sh")
        self.assertIn("migration_status=absent", preflight)
        self.assertIn("migration_status=verified", preflight)
        self.assertIn('[[ $migration_state == "$migration|$migration_checksum" ]]', preflight)

    def test_migration_195_assertion_is_summary_only_and_fail_closed(self) -> None:
        assertion = self.script("migration-195-assert.sh")
        switch = self.script("switch.sh")
        self.assertIn("information_schema.columns", assertion)
        self.assertIn("source_rate_column_exists", assertion)
        self.assertIn("CEIL((k.rate_multiplier*c.recharge_rate)*100)/100", assertion)
        self.assertIn("migration-195-source-rate-column-existed", assertion)
        self.assertIn("migration-195-timezone.name", assertion)
        self.assertIn("ASSERT_CONFIG_FILE", (DEPLOY_ROOT / "release" / "vm-validate.sh").read_text(encoding="utf-8"))
        self.assertNotIn("s.timezone=COALESCE(NULLIF(current_setting('TIMEZONE'", assertion)
        self.assertIn("matching_outbox.count=1", assertion)
        self.assertIn("matching_outbox.count>=1", assertion)
        self.assertIn("migration-195-account-ids-mismatch.count", assertion)
        self.assertIn("migration-195-trigger-missing.count", assertion)
        self.assertIn("migration_195_data_plan_sha256", assertion)
        self.assertIn("migration_195_account_mismatch", assertion)
        self.assertIn("migration_195_snapshot_missing", assertion)
        self.assertIn("migration_195_outbox_missing", assertion)
        self.assertIn("sched:v2:outbox:watermark", assertion)
        self.assertIn("migration-195-outbox-already-consumed", assertion)
        self.assertIn("migration_195_constraint_missing", assertion)
        self.assertIn("migration_195_trigger_missing", assertion)
        self.assertIn("[[ $recompute_mismatch == 0", assertion)
        self.assertGreater(switch.index('migration-195-assert.sh" postflight_db'), switch.index("docker compose run"))
        self.assertGreater(switch.index('migration-195-assert.sh" postflight_runtime'), switch.index("docker compose up"))

    def test_coordinated_restore_reads_redis_password_from_startup_arguments(self) -> None:
        restore = self.script("restore.sh")
        self.assertIn('index("--requirepass")', restore)
        self.assertIn('startswith("--requirepass=")', restore)
        self.assertIn("IFS= read -r REDISCLI_AUTH", restore)
        self.assertNotIn('export REDISCLI_AUTH="${REDIS_PASSWORD:-}"', restore)
        self.assertIn("redis_backup_expiring", restore)
        self.assertIn("redis_restored_expiring", restore)
        self.assertIn("redis_backup_dbsize - redis_dbsize", restore)

    def test_migration_195_preflight_precedes_switch_and_commit_is_reconciled(self) -> None:
        production = (DEPLOY_ROOT / "release" / "production.py").read_text(encoding="utf-8")
        switch = self.script("switch.sh")
        execute = production[production.index("def execute(self)"):]
        self.assertLess(execute.index("self.freeze()"), execute.index("self.migration_preflight()"))
        self.assertLess(execute.index("self.migration_preflight()"), execute.index("self.backup()"))
        self.assertLess(execute.index("self.backup()"), execute.index("self.bind_migration_plan()"))
        self.assertLess(execute.index("self.bind_migration_plan()"), execute.index("self.switch()"))
        self.assertLess(switch.index('migration-195-assert.sh" postflight_db'), switch.index("docker compose up"))
        self.assertIn("migration-committed", switch)
        self.assertIn("remote_migration_committed", production)
        self.assertIn("migration 195 committed state is unknown", production)

    def test_freeze_creates_release_state_root(self) -> None:
        freeze = self.script("freeze-backup.sh")
        self.assertIn("install -d -m 700 /opt/sub2api/backups/release-state", freeze)
        self.assertIn("docker compose stop -t 30 sub2api >/dev/null 2>&1", self.script("freeze.sh"))
        self.assertNotIn('"$assets_dir/backup.sh"', freeze)

    def test_backup_reads_redis_requirepass_without_cli_secret(self) -> None:
        backup = self.script("backup.sh")
        self.assertIn('index("--requirepass")', backup)
        self.assertIn('printf \'%s\\n\' "$redis_password" | docker exec -i', backup)
        self.assertNotIn("redis-cli -a", backup)
        self.assertIn("docker compose stop -t 30 redis >/dev/null 2>&1", backup)
        self.assertIn("docker compose start redis >/dev/null 2>&1", backup)

    def test_backup_keeps_temporary_local_restore_tar_until_release_finishes(self) -> None:
        backup = self.script("backup.sh")
        restore = self.script("restore.sh")
        cleanup = self.script("cleanup-state.sh")

        self.assertIn('install -m 600 "$plain" "$state_dir/recovery-point.tar"', backup)
        self.assertLess(backup.index("umask 077"), backup.index('tar -C "$work" -cf "$plain"'))
        self.assertIn("local_restore_point_ready=true", backup)
        self.assertIn("find . -type f ! -name SHA256SUMS", backup)
        self.assertIn('sha256sum -c recovery-point.tar.sha256', restore)
        self.assertIn('tar -C "$recovery" -xf "$state_dir/recovery-point.tar"', restore)
        self.assertIn('image: $(<"$state_dir/pre-image-id")', restore)
        self.assertIn("COMPOSE_FILE=docker-compose.yml:docker-compose.release-active.yml", restore)
        self.assertIn("SUB2API_RELEASE_IMAGE=%s", restore)
        self.assertNotIn("age-identity", restore)
        self.assertIn("if ! docker info", restore)
        self.assertIn("elif docker inspect sub2api", restore)
        self.assertIn("if ! container_names=$(docker ps -a", restore)
        self.assertIn('case "$nginx_status" in', restore)
        self.assertIn("inactive|failed", restore)
        self.assertIn("(( failed == 0 )) || exit 125", restore)
        self.assertNotIn("! -name recovery-point.tar", cleanup)

    def test_racknerd_verifier_does_not_hairpin_through_dmit(self) -> None:
        verify = self.script("verify.sh")
        finalize = self.script("finalize.sh")
        self.assertNotIn("DMIT_IP", verify)
        self.assertNotIn("DMIT_IP", finalize)
        self.assertNotIn("dmit_health", verify)

    def test_route_canary_reads_secret_from_stdin(self) -> None:
        script = self.script("route-canary.sh")
        self.assertIn("IFS= read -r api_key", script)
        self.assertNotIn("CANARY_KEY_FILE", script)
        self.assertIn("ROUTE_IP", script)

    def test_route_canary_classifies_only_timeout_and_gateway_errors_as_retryable(self) -> None:
        script = self.script("route-canary.sh")
        self.assertIn("$curl_exit == 28", script)
        for code in ("502", "503", "504"):
            self.assertIn(f"$http_code == {code}", script)
        self.assertIn("canary_status=failed", script)
        self.assertIn("canary_status=pass", script)
        self.assertIn("curl_exit=%s", script)

    def test_route_canary_runtime_classifies_timeout_as_retryable(self) -> None:
        values = self.run_route_canary(stream_exit=28, stream_code="200", stream_body="")
        self.assertEqual(values["canary_status"], "retryable")
        self.assertEqual(values["curl_exit"], "28")
        self.assertEqual(values["http_code"], "200")

    def test_route_canary_runtime_rejects_missing_sse_without_retry(self) -> None:
        values = self.run_route_canary(stream_exit=0, stream_code="200", stream_body="plain response")
        self.assertEqual(values["canary_status"], "failed")
        self.assertEqual(values["streaming"], "fail")

    def test_route_canary_runtime_accepts_streaming_response(self) -> None:
        values = self.run_route_canary(stream_exit=0, stream_code="200", stream_body="data: ok")
        self.assertEqual(values["canary_status"], "pass")
        self.assertEqual(values["route_health"], "pass")
        self.assertEqual(values["streaming"], "pass")

    def test_cleanup_handles_backup_failure_before_recovery_point(self) -> None:
        cleanup = self.script("cleanup-state.sh")
        self.assertIn("sha256sum -c SHA256SUMS", cleanup)
        self.assertIn('rm -rf -- "$state_dir"', cleanup)
        self.assertIn("restored.committed", cleanup)

    def test_consume_atomically_commits_active_claim(self) -> None:
        script = self.script("consume.sh")
        self.assertIn('mv -T -- "$active_claim" "$release_dir/.consumed"', script)
        self.assertNotIn('rm -rf "$active_claim"', script)
        self.assertNotIn(".claimed", script)

    def test_reconcile_atomically_commits_active_claim(self) -> None:
        script = self.script("reconcile.sh")
        self.assertIn('mv -T -- "$active_claim" "$release_dir/.recovered"', script)
        self.assertNotIn('rm -rf "$active_claim"', script)
        self.assertNotIn(".claimed", script)


if __name__ == "__main__":
    unittest.main()
