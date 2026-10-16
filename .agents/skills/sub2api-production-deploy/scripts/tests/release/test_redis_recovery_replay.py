from __future__ import annotations

import hashlib
import os
from pathlib import Path
import shutil
import sys
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT))
from release.process import run_hidden


class RedisRecoveryReplayTest(unittest.TestCase):
    def setUp(self):
        self.bash = shutil.which('bash')
        if os.name == 'nt':
            candidate = Path(os.environ.get('ProgramFiles', r'C:\Program Files')) / 'Git/bin/bash.exe'
            if candidate.is_file():
                self.bash = str(candidate)
        if not self.bash:
            self.skipTest('bash unavailable')
        source = (ROOT / 'maintenance/release/restore.sh').read_text()
        self.functions = source[source.index('redis_recovery_snapshot() {'):source.index('apply_application_recovery() {')]
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.base = Path(self.temp.name)
        (self.base / 'redis.persistent').write_text('30\n')
        (self.base / 'redis.health-wait-seconds').write_text('71\n')
        (self.base / 'redis.run-id.sha256').write_text(hashlib.sha256(b'a' * 40).hexdigest() + '\n')

    def verify(self, *, after_app=False, snapshot=None, health='healthy', docker_exit=0):
        environment = os.environ.copy()
        environment.pop('BASH_ENV', None)
        environment.pop('ENV', None)
        environment.update(CP_ROOT=str(self.base), APP_DONE='true' if after_app else 'false',
                           SNAPSHOT=snapshot or '80\n50\n80\n' + 'a' * 40,
                           HEALTH=health, DOCKER_EXIT=str(docker_exit))
        script = r'''
set -Eeuo pipefail
if [[ ${OS:-} == Windows_NT ]]; then CP_ROOT=$(cygpath -u "$CP_ROOT"); fi
RECOVERY_CHECKPOINT_ROOT=$CP_ROOT
redis_backup_dbsize=100; redis_backup_expiring=70
recovery_checkpoint_assert_file() { [[ -f $1 && ! -L $1 ]]; }
recovery_checkpoint_completed() { [[ $1 == app_healthy && $APP_DONE == true ]]; }
jq() { cat >/dev/null; printf '\n'; }
docker() {
  if [[ $1 == inspect && ${2:-} == -f ]]; then printf '%s\n' "$HEALTH"; return; fi
  if [[ $1 == inspect ]]; then printf '[]\n'; return; fi
  [[ $1 == exec && ${*: -2:1} == redis-snapshot ]] || return 90
  [[ $* == *' EVAL '* ]] || return 91
  cat >/dev/null
  printf '%s\n' "$SNAPSHOT"
  return "$DOCKER_EXIT"
}
''' + self.functions + '\nverify_redis_recovery\n'
        return run_hidden([self.bash, '--noprofile', '--norc', '-s'], input=script,
                          text=True, capture_output=True, env=environment)

    def test_initial_restore_requires_original_persistent_floor(self):
        self.assertEqual(self.verify().returncode, 0)
        self.assertNotEqual(self.verify(snapshot='79\n50\n79\n' + 'a' * 40).returncode, 0)

    def test_application_cache_deletion_is_accepted_for_the_same_runtime(self):
        result = self.verify(after_app=True, snapshot='60\n50\n60\n' + 'a' * 40)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn('redis_ttl_reconciliation=verified', result.stdout)

    def test_restarted_redis_is_rejected_even_after_application_checkpoint(self):
        for after_app in (False, True):
            with self.subTest(after_app=after_app):
                self.assertNotEqual(self.verify(after_app=after_app, snapshot='80\n50\n80\n' + 'b' * 40).returncode, 0)

    def test_missing_identity_unhealthy_runtime_and_transport_failure_are_rejected(self):
        self.assertNotEqual(self.verify(health='unhealthy', after_app=True).returncode, 0)
        self.assertNotEqual(self.verify(docker_exit=1, after_app=True).returncode, 0)
        (self.base / 'redis.run-id.sha256').unlink()
        self.assertNotEqual(self.verify(after_app=True).returncode, 0)

    def test_malformed_atomic_observations_are_rejected(self):
        for snapshot in ('80\n50\n80', '80\n50\n80\n' + 'a' * 40 + '\nextra', 'x\n50\n80\n' + 'a' * 40):
            with self.subTest(snapshot=snapshot):
                self.assertNotEqual(self.verify(snapshot=snapshot).returncode, 0)
        self.assertNotEqual(self.verify(after_app=True, snapshot='49\n50\n49\n' + 'a' * 40).returncode, 0)


if __name__ == '__main__':
    unittest.main()
