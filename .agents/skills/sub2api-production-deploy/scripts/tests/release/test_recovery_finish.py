from __future__ import annotations

import unittest

import test_recovery_checkpoint as checkpoint_module


class RecoveryFinishTest(unittest.TestCase):
    def setUp(self) -> None:
        fixture = checkpoint_module.RecoveryCheckpointTest()
        fixture.setUp()
        self.addCleanup(fixture.doCleanups)
        self.fixture = fixture
        source = (checkpoint_module.ROOT / "maintenance/release/recovery-finish.sh").read_text()
        functions = source[source.index("recovery_finish_run_helper() {"):source.index("# Invoke directly")]
        fixture.script = fixture.script[:fixture.script.index("apply_fixture() {")] + r'''
umask 077
release_id=270-release
release_dir="$CP_BASE/release"; mkdir -p "$release_dir"
checkpoint_root=$RECOVERY_CHECKPOINT_ROOT
state_dir="$CP_BASE/state"; active_claim="$CP_BASE/claim"
pre_image_id=$image
mkdir -p "$state_dir" "$active_claim"
if [[ ! -e $CP_BASE/setup ]]; then
  printf 'encrypted fixture\n' > "$state_dir/recovery-point.age"
  (cd "$state_dir" && sha256sum recovery-point.age > recovery-point.age.sha256)
  printf '%s\n' "$pre_image_id" > "$state_dir/pre-image-id"
  printf 'plaintext fixture\n' > "$state_dir/recovery-point.tar"
  printf 'healthy\n' > "$CP_BASE/health"
  : > "$CP_BASE/setup"
fi
fixture_apply() { :; }; fixture_verify() { :; }
for phase in compose_restored postgres_restored redis_restored app_healthy nginx_restored; do
  recovery_checkpoint_run "$phase" fixture_verify fixture_apply
done
''' + functions + r'''
# This unit fixture executes the production callbacks/state machine. External
# helper effects and OS services are adapted; no production path is written.
recovery_finish_verify_application() { [[ $(cat "$CP_BASE/health") == healthy ]]; }
recovery_finish_backup_fingerprint() { printf 'verified-unit-fixture\n'; }
systemctl() {
  if [[ $1 == is-enabled ]]; then printf 'enabled\n'
  elif [[ $2 == sub2api-backup.service ]]; then printf 'inactive\n'
  else printf 'active\n'; fi
}
if [[ $OS == Windows_NT ]]; then
  stat() {
    local result; result=$(command stat "$@")
    case "$2" in
      '%u:%g:%a') printf '0:0:700\n';;
      '%u:%g:%a:%h') printf '0:0:400:%s\n' "${result##*:}";;
      *) printf '%s\n' "$result";;
    esac
  }
fi
recovery_finish_run_helper() {
  printf '%s\n' "$1" >> "$CP_BASE/helper-calls"
  case "$1" in
    restore-backup-units.sh) printf 'restored\n' > "$state_dir/restored.committed";;
    cleanup-state.sh)
      rm -f -- "$state_dir/recovery-point.tar" "$state_dir/restored.committed"
      if [[ ${STOP_DURING_CLEANUP:-} == yes ]]; then exit 72; fi
      printf 'cleaned\n' > "$active_claim/plaintext-cleaned"; chmod 400 "$active_claim/plaintext-cleaned";;
    reconcile.sh)
      printf 'release_id=270-release\nrecovered_at=2026-10-15T00:00:00Z\n' > "$CP_BASE/claim/marker"
      chmod 400 "$CP_BASE/claim/marker"
      mv -- "$CP_BASE/claim" "$release_dir/.recovered";;
    *) return 1;;
  esac
}
if [[ -d $release_dir/.recovered ]]; then active_claim="$release_dir/.recovered"; fi
recovery_checkpoint_run backup_units_restored recovery_finish_verify_backup_units recovery_finish_apply_backup_units
recovery_checkpoint_run state_cleanup recovery_finish_verify_state_cleanup recovery_finish_apply_state_cleanup
if [[ ${STOP_AFTER_CLEANUP:-} == yes ]]; then exit 79; fi
if [[ ! -d $release_dir/.recovered ]]; then active_claim=/opt/sub2api/releases/.active-release; fi
recovery_checkpoint_run claim_reconciled recovery_finish_verify_claim_reconcile recovery_finish_apply_claim_reconcile
'''

    def calls(self) -> list[str]:
        return (self.fixture.base / "helper-calls").read_text().splitlines()

    def test_cleanup_interruption_resume_only_reconciles_claim(self) -> None:
        first = self.fixture.run_script(STOP_AFTER_CLEANUP="yes")
        self.assertEqual(first.returncode, 79, first.stderr)
        self.assertFalse((self.fixture.base / "state/recovery-point.tar").exists())
        resumed = self.fixture.run_script()
        self.assertEqual(resumed.returncode, 0, resumed.stderr)
        self.assertEqual(self.calls(), ["restore-backup-units.sh", "cleanup-state.sh", "reconcile.sh"])
        final = self.fixture.run_script()
        self.assertEqual(final.returncode, 0, final.stderr)
        self.assertEqual(len(self.calls()), 3)

    def test_incomplete_cleanup_is_refused_without_repeating_helpers(self) -> None:
        first = self.fixture.run_script(STOP_DURING_CLEANUP="yes")
        self.assertEqual(first.returncode, 72, first.stderr)
        calls = self.calls()
        self.assertNotEqual(self.fixture.run_script().returncode, 0)
        self.assertEqual(self.calls(), calls)

    def test_retained_plaintext_and_runtime_drift_refuse_claim_release(self) -> None:
        first = self.fixture.run_script(STOP_AFTER_CLEANUP="yes")
        self.assertEqual(first.returncode, 79, first.stderr)
        (self.fixture.base / "state/unexpected.tar").write_text("plaintext\n")
        self.assertNotEqual(self.fixture.run_script().returncode, 0)
        self.assertNotIn("reconcile.sh", self.calls())
        (self.fixture.base / "state/unexpected.tar").unlink()
        (self.fixture.base / "health").write_text("unhealthy\n")
        self.assertNotEqual(self.fixture.run_script().returncode, 0)
        self.assertNotIn("reconcile.sh", self.calls())

    def test_conditional_callback_helper_failures_cannot_publish_fingerprint_or_move_claim(self) -> None:
        self.fixture.script = self.fixture.script[:self.fixture.script.rindex(
            "recovery_checkpoint_run backup_units_restored")] + r'''
recovery_finish_run_helper() { return 75; }
recovery_finish_backup_fingerprint() {
  printf 'unexpected\n' > "$CP_BASE/fingerprint-called"
  printf 'verified-unit-fixture\n'
}
if recovery_finish_apply_backup_units; then exit 81; fi
[[ ! -e $CP_BASE/fingerprint-called ]]
[[ ! -e $checkpoint_root/backup-units.fingerprint ]]
active_claim=/opt/sub2api/releases/.active-release
export ACTIVE_CLAIM=$active_claim
if recovery_finish_apply_claim_reconcile; then exit 82; fi
[[ $active_claim == /opt/sub2api/releases/.active-release ]]
[[ $ACTIVE_CLAIM == /opt/sub2api/releases/.active-release ]]
[[ ! -d $release_dir/.recovered ]]
# A successful unit helper still cannot report a failed checkpoint write as
# callback success when Bash has disabled errexit for the if condition.
recovery_finish_run_helper() { :; }
recovery_checkpoint_store() { return 76; }
if recovery_finish_apply_backup_units; then exit 83; fi
'''
        result = self.fixture.run_script()
        self.assertEqual(result.returncode, 0, result.stderr)


if __name__ == "__main__":
    unittest.main()
