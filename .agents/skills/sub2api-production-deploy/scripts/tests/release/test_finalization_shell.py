"""Execute the generated consumption proof on local inert Bash fixtures.

File paths, hashes, checksum inventory and shell conditionals run for real.
Only root ownership/modes and external VM/service commands are stubbed; no
remote connection, application lifecycle or production asset is touched.
"""
from __future__ import annotations

import hashlib
import os
import shutil
import sys
import tempfile
import unittest
from pathlib import Path
from types import SimpleNamespace


sys.path.insert(0, str(Path(__file__).resolve().parents[2]))
from release import finalization
from release.process import run_hidden


def bash_path(path: Path) -> str:
    value = path.resolve().as_posix()
    return "/" + value[0].lower() + value[2:] if os.name == "nt" else value


class FinalizationShellTest(unittest.TestCase):
    def setUp(self):
        git_bash = Path("C:/Program Files/Git/bin/bash.exe")
        self.bash = str(git_bash) if git_bash.is_file() else shutil.which("bash")
        if not self.bash:
            self.skipTest("Bash is required for the inert finalization contract fixture")
        self.temp = tempfile.TemporaryDirectory(prefix="finalization-shell-")
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.identifier = "265-aaaaaaaaaaaa-1-deadbeef"
        self.candidate = "sha256:" + "b" * 64
        self.claim = self.root / "opt/sub2api/releases" / self.identifier / ".consumed"
        (self.claim / "assets").mkdir(parents=True)
        (self.root / "run/lock").mkdir(parents=True)
        prefix = ".agents/skills/sub2api-production-deploy/scripts/"
        asset_paths = (
            "maintenance/release/context.sh", "maintenance/release/consume.sh",
            "maintenance/181/mask-backup-units.sh", "maintenance/181/restore-backup-units.sh",
            "release/backfill-activity-reward-costs.sh",
        )
        assets = {}
        for relative in asset_paths:
            content = ("# inert " + relative + "\n").encode()
            (self.claim / "assets" / relative.rsplit("/", 1)[1]).write_bytes(content)
            assets[prefix + relative] = hashlib.sha256(content).hexdigest()
        self.baseline = "sha256:" + "a" * 64
        self.manifest = {"schema": 2, "release_id": self.identifier, "profile": "265", "release_asset_sha256": assets, "production_current_image_id": self.baseline}
        self.recovery_bytes = b"inert-age-encrypted-fixture"
        digest = hashlib.sha256(self.recovery_bytes).hexdigest()
        self.backup_result = {
            "artifact": "fixture.tar.age", "transport_artifact": "transport.tar.age",
            "artifact_size": str(len(self.recovery_bytes)), "artifact_sha256": digest,
            "traffic_preserved": "true", "redis_backup_mode": "rdb", "no_restart_path_proven": "true", "local_restore_point_ready": "true",
        }
        self.backup_evidence = {**self.backup_result, "pre_switch_image_id": self.baseline, "release_sha256": digest}
        self.originals = {}
        for name, content in (("gate.json", b'{"fixture":true}\n'), ("gate.sig", b"fixture-signature"), ("candidate.tar.gz", b"inert-candidate")):
            (self.claim / name).write_bytes(content)
            self.originals["gate/" + name] = hashlib.sha256(content).hexdigest()
        (self.claim / "marker").write_text(f"release_id={self.identifier}\ncandidate_image_id={self.candidate}\nconsumed_at=2026-10-12T00:00:00Z\n", encoding="utf-8", newline="\n")
        (self.claim / "release_id").write_text(f"release_id={self.identifier}\n", encoding="utf-8", newline="\n")
        (self.claim / "plaintext-cleaned").write_text("cleaned_at=fixture\n", encoding="utf-8", newline="\n")
        checksums = {f"assets/{path.rsplit('/', 1)[1]}": digest for path, digest in assets.items()}
        checksums.update({name: digest for name, digest in ((key.split("/", 1)[1], value) for key, value in self.originals.items())})
        (self.claim / "CLAIM_SHA256SUMS").write_text("".join(f"{digest}  {name}\n" for name, digest in sorted(checksums.items())), encoding="utf-8", newline="\n")
        self.slot = self.root / "opt/sub2api/active-app"
        self.slot.write_text(f"container=sub2api\nport=18080\nimage_id={self.candidate}\nrelease_id={self.identifier}\ninstance_id={self.identifier}-active\n", encoding="utf-8", newline="\n")
        self.upstream = self.root / "etc/nginx/conf.d/sub2api-release-upstream.conf"
        self.upstream.parent.mkdir(parents=True)
        self.upstream.write_bytes(b"upstream sub2api_release_backend { server 127.0.0.1:18080; }\n")
        captured = []

        class Capture:
            def run(_self, node, script, allowed, **kwargs):
                assert node in {"racknerd", "backup"}
                captured.append((node, script))
                expected = {**finalization.REMOTE_REQUIRED, "running_image_id": self.candidate}
                return SimpleNamespace(values={key: expected[key] for key in allowed})

        finalization._remote_consumed(Capture(), self.manifest, self.candidate, self.originals, backup_evidence=self.backup_evidence)
        self.scripts = [(node, script.replace("/opt/sub2api", bash_path(self.root / "opt/sub2api")).replace("/run/lock", bash_path(self.root / "run/lock")).replace("/etc/nginx", bash_path(self.root / "etc/nginx"))) for node, script in captured]

    def retained_state(self):
        state = self.root / "opt/sub2api/backups/release-state" / self.identifier
        state.mkdir(parents=True)
        (state / "recovery-point.age").write_bytes(self.recovery_bytes)
        (state / "recovery-point.age.sha256").write_bytes(f"{self.backup_evidence['artifact_sha256']}  recovery-point.age\n".encode())
        (state / "pre-image-id").write_bytes((self.baseline + "\n").encode())
        result = "".join(f"{key}={value}\n" for key, value in self.backup_result.items()).encode()
        (state / "backup-result").write_bytes(result)
        (state / "backup-result.sha256").write_bytes(f"{hashlib.sha256(result).hexdigest()}  backup-result\n".encode())
        return state

    def execute(self, *, image=None, bad_mode=False, lock_busy=False, service_state="inactive", bad_headers_route="", wrong_instance=False, missing_ready=False, failed_command=""):
        # A function stub cannot accidentally invoke docker/systemctl/SSH.  The
        # actual read-only file inventory and sha256sum checks remain real.
        stubs = r'''
id() { test "$*" = '-u' && printf '0\n'; [[ $FIXTURE_FAILED_COMMAND != id ]] || return 17; }
stat() {
  case "$2" in
    '%U:%G:%a') printf 'root:root:700\n' ;;
    '%U:%G:%a:%h')
      if [[ $3 == */assets/* ]]; then printf 'root:root:500:1\n'
      elif [[ $3 == */recovery-point.age || $3 == */recovery-point.age.sha256 || $3 == */pre-image-id ]]; then printf 'root:root:600:1\n'
      elif [[ $3 == */marker && $BAD_MODE == true ]]; then printf 'root:root:644:1\n'
      else printf 'root:root:400:1\n'; fi ;;
    '%s') /usr/bin/stat "$@" ;;
    *) return 90 ;;
  esac
  [[ $FIXTURE_FAILED_COMMAND != stat ]] || return 17
}
flock() { test "$LOCK_BUSY" = false; }
openssl() { test "$1" = pkeyutl && test "$2" = -verify; }
docker() {
  test "$1" = inspect && test "$2" = -f && test "$4" = sub2api || return 90
  case "$3" in '{{.Image}}') printf '%s\n' "$FIXTURE_IMAGE" ;; '{{.State.Health.Status}}') printf 'healthy\n' ;; *) return 90 ;; esac
  [[ $FIXTURE_FAILED_COMMAND != docker ]] || return 17
}
curl() {
  local headers='' url='' route
  while (( $# )); do
    case "$1" in -D) headers=$2; shift 2 ;; *) url=$1; shift ;; esac
  done
  if [[ $FIXTURE_NODE == backup ]]; then route=backup
  elif [[ $url == http://127.0.0.1:* ]]; then route=internal
  else route=direct; fi
  local instance=$FIXTURE_INSTANCE
  if [[ $FIXTURE_BAD_ROUTE == "$route" && $FIXTURE_WRONG_INSTANCE == true ]]; then instance=old-instance; fi
  printf 'HTTP/1.1 200 OK\r\nX-Sub2API-Instance: %s\r\n' "$instance" > "$headers"
  if [[ $FIXTURE_BAD_ROUTE != "$route" || $FIXTURE_MISSING_READY != true ]]; then
    printf 'X-Sub2API-Background-Ready: true\r\n' >> "$headers"
  fi
  printf '\r\n' >> "$headers"
  printf '200'
  [[ $FIXTURE_FAILED_COMMAND != curl ]] || return 17
}
systemctl() {
  case "$*" in
    'is-active nginx'|'is-active sub2api-backup.timer') printf 'active\n' ;;
    'is-enabled sub2api-backup.timer') printf 'enabled\n' ;;
    'is-active sub2api-backup.service')
      printf '%s\n' "$FIXTURE_SERVICE_STATE"
      case "$FIXTURE_SERVICE_STATE" in inactive) return 3 ;; active) return 0 ;; *) return 4 ;; esac ;;
    *) return 90 ;;
  esac
  [[ $FIXTURE_FAILED_COMMAND != systemctl ]] || return 17
}
'''
        # Preserve the actual successful stdout while failing the producer.
        # This exercises the full generated proof rather than a mirrored guard.
        for command in ("realpath", "find", "wc", "grep", "cat", "sha256sum", "sed"):
            stubs += f'\n{command}() {{ command {command} "$@"; local rc=$?; [[ $FIXTURE_FAILED_COMMAND != {command} ]] || return 17; return "$rc"; }}\n'
        env = dict(os.environ, FIXTURE_IMAGE=image or self.candidate, BAD_MODE=str(bad_mode).lower(), LOCK_BUSY=str(lock_busy).lower(), FIXTURE_SERVICE_STATE=service_state,
                   FIXTURE_INSTANCE=self.identifier + "-active", FIXTURE_BAD_ROUTE=bad_headers_route, FIXTURE_WRONG_INSTANCE=str(wrong_instance).lower(), FIXTURE_MISSING_READY=str(missing_ready).lower(), FIXTURE_FAILED_COMMAND=failed_command)
        env.pop("BASH_ENV", None)
        env.pop("ENV", None)
        stdout = ""
        for node, script in self.scripts:
            env["FIXTURE_NODE"] = node
            result = run_hidden([self.bash, "--noprofile", "--norc", "-s"], input=stubs + script, text=True, capture_output=True, env=env, timeout=30)
            stdout += result.stdout
            if result.returncode:
                return result
        return SimpleNamespace(returncode=0, stdout=stdout, stderr="")

    def test_real_claim_inventory_and_existing_service_contract_pass(self):
        result = self.execute()
        self.assertEqual(result.returncode, 0, result.stderr)
        values = dict(line.split("=", 1) for line in result.stdout.splitlines())
        self.assertEqual(values, {**finalization.REMOTE_REQUIRED, "running_image_id": self.candidate})

    def test_successful_stdout_cannot_hide_required_producer_failure(self):
        self.retained_state()
        for command in ("id", "stat", "realpath", "find", "grep", "cat", "sha256sum", "sed", "docker", "curl", "systemctl"):
            with self.subTest(command=command):
                result = self.execute(failed_command=command)
                self.assertNotEqual(result.returncode, 0, result.stdout)
                self.assertNotIn("consumed_marker_verified=true", result.stdout)

    def test_altered_candidate_gate_signature_or_claim_inventory_is_rejected(self):
        for name in ("candidate.tar.gz", "gate.json", "gate.sig", "assets/context.sh", "CLAIM_SHA256SUMS"):
            with self.subTest(name=name):
                target = self.claim / name
                original = target.read_bytes()
                target.write_bytes(original + b"drift\n")
                self.assertNotEqual(self.execute().returncode, 0)
                target.write_bytes(original)

    def test_claim_marker_active_slot_claim_and_runtime_drift_are_rejected(self):
        for target in (self.claim / "marker", self.claim / "release_id", self.slot):
            with self.subTest(target=target.name):
                original = target.read_bytes()
                target.write_bytes(original.replace(self.identifier.encode(), b"another-release"))
                self.assertNotEqual(self.execute().returncode, 0)
                target.write_bytes(original)
        unsafe_directories = (
            self.claim.parent.parent / ".active-release", self.claim.parent / ".recovered",
            self.root / "opt/sub2api/backups/release-state" / self.identifier,
        )
        for active in unsafe_directories:
            with self.subTest(directory=active.name):
                active.mkdir(parents=True)
                self.assertNotEqual(self.execute().returncode, 0)
                active.rmdir()
        self.assertNotEqual(self.execute(image="sha256:" + "c" * 64).returncode, 0)

    def test_wrong_marker_mode_and_busy_production_lock_are_rejected(self):
        self.assertNotEqual(self.execute(bad_mode=True).returncode, 0)
        self.assertNotEqual(self.execute(lock_busy=True).returncode, 0)

    def test_extra_unhashed_asset_file_is_rejected(self):
        (self.claim / "assets/extra.sh").write_bytes(b"# unregistered asset\n")
        self.assertNotEqual(self.execute().returncode, 0)

    def test_active_or_unqueryable_backup_service_is_rejected(self):
        for state in ("active", "unknown", ""):
            with self.subTest(state=state):
                self.assertNotEqual(self.execute(service_state=state).returncode, 0)

    def test_encrypted_five_file_recovery_point_and_optional_failure_are_preserved(self):
        state = self.retained_state()
        before = {path.name: path.read_bytes() for path in state.iterdir()}
        self.assertEqual(self.execute().returncode, 0)
        self.assertEqual(before, {path.name: path.read_bytes() for path in state.iterdir()})
        (state / "backup-failure").write_bytes(f"attempt_id={self.identifier}-1\nstage=upload\nexit_code=1\n".encode())
        self.assertEqual(self.execute().returncode, 0)

    def test_plaintext_extra_checksum_and_pre_image_drift_are_rejected(self):
        state = self.retained_state()
        for name in ("recovery-point.tar", "database.dump", "extra"):
            with self.subTest(extra=name):
                target = state / name
                target.write_bytes(b"forbidden plaintext fixture")
                self.assertNotEqual(self.execute().returncode, 0)
                target.unlink()
        for name in ("recovery-point.age", "recovery-point.age.sha256", "backup-result", "backup-result.sha256", "pre-image-id"):
            with self.subTest(drift=name):
                target = state / name
                original = target.read_bytes()
                target.write_bytes(original + b"drift\n")
                self.assertNotEqual(self.execute().returncode, 0)
                target.write_bytes(original)

    def test_internal_direct_and_dmit_wrong_instance_or_unready_headers_are_rejected(self):
        for route in ("internal", "direct", "backup"):
            with self.subTest(route=route, failure="instance"):
                self.assertNotEqual(self.execute(bad_headers_route=route, wrong_instance=True).returncode, 0)
            with self.subTest(route=route, failure="ready"):
                self.assertNotEqual(self.execute(bad_headers_route=route, missing_ready=True).returncode, 0)

    def test_nginx_upstream_pointing_to_previous_port_is_rejected(self):
        self.upstream.write_bytes(b"upstream sub2api_release_backend { server 127.0.0.1:18081; }\n")
        self.assertNotEqual(self.execute().returncode, 0)


if __name__ == "__main__":
    unittest.main()
