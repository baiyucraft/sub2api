from __future__ import annotations

import os
from pathlib import Path
import shutil
import sys
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT))
from release.process import run_hidden


class RecoveryCheckpointTest(unittest.TestCase):
    def setUp(self) -> None:
        if os.name != "nt" and os.geteuid() != 0:
            self.skipTest("Linux root metadata required")
        self.bash = shutil.which("bash")
        if os.name == "nt":
            candidate = Path(os.environ.get("ProgramFiles", r"C:\Program Files")) / "Git/bin/bash.exe"
            if candidate.is_file():
                self.bash = str(candidate)
        if not self.bash:
            self.skipTest("bash unavailable")
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.base = Path(self.temporary.name)
        self.checkpoints = self.base / "checkpoints"
        self.checkpoints.mkdir(mode=0o700)
        self.helper = ROOT / "maintenance/release/recovery-checkpoint.sh"
        self.environment = os.environ.copy()
        self.environment.pop("BASH_ENV", None)
        self.environment.pop("ENV", None)
        self.environment.update(CP_BASE=str(self.base), CP_HELPER=str(self.helper))
        self.script = r'''
set -Eeuo pipefail
if [[ $OS == Windows_NT ]]; then
  CP_BASE=$(cygpath -u "$CP_BASE"); CP_HELPER=$(cygpath -u "$CP_HELPER")
  # Git Bash cannot model Linux root ownership or Unix modes. Adapt those
  # metadata fields only; link-count, symlink and canonical paths remain real.
  # Linux runs this same suite with all metadata checks unmodified.
  stat() {
    local result; result=$(command stat "$@")
    case "$2" in
      '%u:%g:%a') printf '0:0:700\n';;
      '%u:%g:%a:%h') printf '0:0:400:%s\n' "${result##*:}";;
      *) printf '%s\n' "$result";;
    esac
  }
  chown() { :; }
fi
source "$CP_HELPER"
point=$(printf a%.0s {1..64}); bundle=$(printf b%.0s {1..64}); image=sha256:$(printf c%.0s {1..64})
recovery_checkpoint_init "$CP_BASE/checkpoints" "270-release" "${POINT_OVERRIDE:-$point}" "$image" "${BUNDLE_OVERRIDE:-$bundle}"
apply_fixture() {
  printf '%s\n' "$CURRENT_PHASE" >> "$CP_BASE/writes"
  if [[ ${STOP_DURING:-} == "$CURRENT_PHASE" ]]; then exit 71; fi
  printf 'valid\n' > "$CP_BASE/live.$CURRENT_PHASE"
}
verify_fixture() { [[ $(cat "$CP_BASE/live.$CURRENT_PHASE" 2>/dev/null) == valid ]]; }
for CURRENT_PHASE in "${RECOVERY_CHECKPOINT_PHASES[@]}"; do
  recovery_checkpoint_run "$CURRENT_PHASE" verify_fixture apply_fixture
  if [[ ${STOP_AFTER:-} == "$CURRENT_PHASE" ]]; then exit 79; fi
done
'''

    def run_script(self, **environment: str):
        env = dict(self.environment, OS=os.environ.get("OS", ""), **environment)
        return run_hidden([self.bash, "--noprofile", "--norc", "-s"], input=self.script,
                          env=env, text=True, capture_output=True, check=False)

    def writes(self) -> list[str]:
        return (self.base / "writes").read_text().splitlines()

    def test_resume_after_each_phase_only_applies_remaining_phases(self) -> None:
        phases = ["compose_restored", "postgres_restored", "redis_restored", "app_healthy",
                  "nginx_restored", "backup_units_restored", "state_cleanup", "claim_reconciled"]
        for phase in phases:
            result = self.run_script(STOP_AFTER=phase)
            self.assertEqual(result.returncode, 79, result.stderr)
        resumed = self.run_script()
        self.assertEqual(resumed.returncode, 0, resumed.stderr)
        self.assertEqual(self.writes(), phases)
        self.assertEqual(resumed.stdout.count("status=reverified"), 8)

    def test_completed_live_drift_fails_without_reapplying_data(self) -> None:
        self.assertEqual(self.run_script(STOP_AFTER="redis_restored").returncode, 79)
        (self.base / "live.postgres_restored").write_text("drift\n")
        original = self.writes()
        self.assertNotEqual(self.run_script().returncode, 0)
        self.assertEqual(self.writes(), original)

    def test_interrupted_destructive_stage_is_never_blindly_replayed(self) -> None:
        self.assertEqual(self.run_script(STOP_DURING="postgres_restored").returncode, 71)
        original = self.writes()
        self.assertNotEqual(self.run_script().returncode, 0)
        self.assertEqual(self.writes(), original)

    def test_verified_interrupted_stage_can_be_adopted_without_reapply(self) -> None:
        self.assertEqual(self.run_script(STOP_DURING="postgres_restored").returncode, 71)
        (self.base / "live.postgres_restored").write_text("valid\n")
        resumed = self.run_script()
        self.assertEqual(resumed.returncode, 0, resumed.stderr)
        self.assertEqual(self.writes().count("postgres_restored"), 1)

    def test_identity_drift_and_phase_gap_are_rejected(self) -> None:
        self.assertEqual(self.run_script(STOP_AFTER="redis_restored").returncode, 79)
        original = self.writes()
        self.assertNotEqual(self.run_script(POINT_OVERRIDE="d" * 64).returncode, 0)
        self.assertNotEqual(self.run_script(BUNDLE_OVERRIDE="e" * 64).returncode, 0)
        missing = self.checkpoints / "postgres_restored.done"
        missing.chmod(0o600)
        missing.unlink()
        self.assertNotEqual(self.run_script().returncode, 0)
        self.assertEqual(self.writes(), original)

    def test_tampered_marker_unknown_file_and_hardlink_are_rejected(self) -> None:
        self.assertEqual(self.run_script(STOP_AFTER="redis_restored").returncode, 79)
        marker = self.checkpoints / "redis_restored.done"
        original = marker.read_bytes()
        marker.chmod(0o600)
        marker.write_text("forged\n")
        marker.chmod(0o400)
        self.assertNotEqual(self.run_script().returncode, 0)
        marker.chmod(0o600)
        marker.write_bytes(original)
        marker.chmod(0o400)
        (self.checkpoints / "unexpected").write_text("unknown\n")
        self.assertNotEqual(self.run_script().returncode, 0)
        (self.checkpoints / "unexpected").unlink()
        identity = self.checkpoints / "identity"
        try:
            os.link(identity, self.base / "identity-link")
        except OSError:
            self.skipTest("hardlinks unavailable")
        self.assertNotEqual(self.run_script().returncode, 0)

    def test_interruption_flag_requires_real_isolation_proof(self) -> None:
        result = self.run_script(RECOVERY_INTERRUPT_AFTER_PHASE="postgres_restored")
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse((self.base / "writes").exists())

    def test_failure_cannot_commit_checkpoint_when_called_in_conditional(self) -> None:
        self.script = self.script[:self.script.index('for CURRENT_PHASE in')]
        self.script += '''
CURRENT_PHASE=compose_restored
verify_failure() { return 1; }
if recovery_checkpoint_run compose_restored verify_failure apply_fixture; then exit 90; fi
[[ ! -e "$CP_BASE/checkpoints/compose_restored.done" ]]
'''
        result = self.run_script()
        self.assertEqual(result.returncode, 0, result.stderr)

    def test_started_write_failure_prevents_any_apply(self) -> None:
        self.script = self.script[:self.script.index('for CURRENT_PHASE in')]
        self.script += '''
CURRENT_PHASE=compose_restored
recovery_checkpoint_store() { return 1; }
if recovery_checkpoint_run compose_restored verify_fixture apply_fixture; then exit 90; fi
[[ ! -e "$CP_BASE/writes" && ! -e "$CP_BASE/checkpoints/compose_restored.done" ]]
'''
        result = self.run_script()
        self.assertEqual(result.returncode, 0, result.stderr)

    def test_done_write_failure_never_reports_completed(self) -> None:
        self.script = self.script[:self.script.index('for CURRENT_PHASE in')]
        self.script += '''
CURRENT_PHASE=compose_restored
eval "$(declare -f recovery_checkpoint_store | sed '1s/recovery_checkpoint_store/recovery_checkpoint_store_original/')"
recovery_checkpoint_store() {
  [[ $1 != compose_restored.done ]] || return 1
  recovery_checkpoint_store_original "$@"
}
if recovery_checkpoint_run compose_restored verify_fixture apply_fixture; then exit 90; fi
[[ -f "$CP_BASE/checkpoints/compose_restored.started" && ! -e "$CP_BASE/checkpoints/compose_restored.done" ]]
'''
        result = self.run_script()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertNotIn("status=completed", result.stdout)


if __name__ == "__main__":
    unittest.main()
