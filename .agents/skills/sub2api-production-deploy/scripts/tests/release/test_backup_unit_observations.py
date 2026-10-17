from __future__ import annotations

import hashlib
import os
import shutil
import subprocess
import tempfile
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[2] / 'maintenance' / '181'
UNITS = ('sub2api-backup.service', 'sub2api-backup.timer')


def function(source, name):
    start = source.index(name + '() {')
    return source[start:source.index('\n}', start) + 2]


STUBS = r'''
observe() {
  builtin printf '%s\n' "$1" >> "$TRACE"
  [[ $FAIL_COMMAND != "$1" ]] || return 17
}
realpath() { command realpath "$@"; observe realpath; }
basename() { command basename "$@"; observe basename; }
dirname() { command dirname "$@"; observe dirname; }
find() { command find "$@"; observe find; }
sha256sum() { command sha256sum "$@"; observe sha256sum; }
awk() { command awk "$@"; observe awk; }
cat() { command cat "$@"; observe cat; }
readlink() { command readlink "$@"; observe readlink; }
systemctl() {
  case "$1" in
    is-active) builtin printf '%s\n' "$ACTIVE_VALUE"; observe systemctl || return 17; return "$ACTIVE_CODE" ;;
    is-enabled) builtin printf '%s\n' "$ENABLED_VALUE"; observe systemctl || return 17; return "$ENABLED_CODE" ;;
    *) observe systemctl ;;
  esac
}
'''


class BackupUnitObservationsTest(unittest.TestCase):
    def setUp(self):
        candidate = Path('C:/Program Files/Git/bin/bash.exe')
        self.bash = str(candidate) if candidate.is_file() else shutil.which('bash')
        if not self.bash:
            self.skipTest('Bash is required for inert unit fixtures')

    def shell(self, script, environment=None):
        env = {key: value for key, value in os.environ.items() if key not in {'BASH_ENV', 'ENV', 'SHELLOPTS'}}
        env.update(environment or {})
        return subprocess.run([self.bash, '-c', script], env=env, capture_output=True, text=True, encoding='utf-8', errors='replace', timeout=20)

    def fixture(self, root):
        state = root / ('273-' + 'a' * 12 + '-1-' + 'b' * 8)
        state.mkdir()
        live = root / 'units'
        live.mkdir()
        for unit in UNITS:
            content = ('[Unit]\nDescription=' + unit + '\n').encode()
            (state / unit).write_bytes(content)
            (live / unit).write_bytes(content)
            (state / (unit + '.enabled')).write_bytes(b'enabled\n')
            (state / (unit + '.active')).write_bytes(b'inactive\n')
        names = [*UNITS, *(x + '.enabled' for x in UNITS), *(x + '.active' for x in UNITS)]
        checksums = ''.join(hashlib.sha256((state / x).read_bytes()).hexdigest() + '  ' + x + '\n' for x in names)
        (state / 'backup-units.sha256').write_bytes(checksums.encode())
        (state / 'masked.committed').write_bytes(b'fixture\n')
        (state / 'restored.committed').write_bytes(b'fixture\n')
        bash_root = root.as_posix()
        if os.name == 'nt':
            result = subprocess.run([self.bash, '-c', 'cygpath -u "$1"', 'fixture', str(root)], check=True, capture_output=True, text=True)
            bash_root = result.stdout.strip()
        environment = {
            'STATE_ROOT': bash_root, 'STATE_DIR': bash_root + '/' + state.name,
            'TRACE': bash_root + '/trace', 'FAIL_COMMAND': 'none',
            'ACTIVE_VALUE': 'inactive', 'ACTIVE_CODE': '3',
            'ENABLED_VALUE': 'enabled', 'ENABLED_CODE': '0',
        }
        return environment, bash_root + '/units'

    def test_conditional_verifiers_and_best_effort_rollback_reject_failed_reads(self):
        mask = (ROOT / 'mask-backup-units.sh').read_text(encoding='utf-8')
        restore = (ROOT / 'restore-backup-units.sh').read_text(encoding='utf-8')
        for name, source in (('validate_state_path', mask), ('verify_snapshot', mask), ('verify_restored', restore), ('restore_original_state', mask)):
            with self.subTest(function=name), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                environment, live = self.fixture(root)
                body = function(source, 'observed_unit_state') + '\n' + function(source, name)
                body = body.replace('/etc/systemd/system', live)
                script = 'set -Eeuo pipefail\n' + STUBS + '\nstate_root=$STATE_ROOT; state_dir=$STATE_DIR; units=(sub2api-backup.service sub2api-backup.timer)\n' + body + '\nif ' + name + '; then printf "observation_accepted=true\\n"; else exit 23; fi\n'
                passed = self.shell(script, environment)
                self.assertEqual(passed.returncode, 0, passed.stderr)
                self.assertIn('observation_accepted=true', passed.stdout)
                kinds = set((root / 'trace').read_text().splitlines())
                self.assertTrue(kinds)
                for kind in kinds:
                    failed = self.shell(script, {**environment, 'FAIL_COMMAND': kind})
                    self.assertEqual(failed.returncode, 23, (name, kind, failed.stdout, failed.stderr))
                    self.assertNotIn('observation_accepted=true', failed.stdout)

    def test_actual_restore_replay_never_acknowledges_a_failed_read(self):
        source = (ROOT / 'restore-backup-units.sh').read_text(encoding='utf-8')
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            environment, live = self.fixture(root)
            script = 'set -Eeuo pipefail\n' + STUBS + source.replace('/etc/systemd/system', live)
            passed = self.shell(script, environment)
            self.assertEqual(passed.returncode, 0, passed.stderr)
            self.assertEqual(passed.stdout, 'backup_units_restored=true\n')
            for kind in set((root / 'trace').read_text().splitlines()):
                failed = self.shell(script, {**environment, 'FAIL_COMMAND': kind})
                self.assertNotEqual(failed.returncode, 0, (kind, failed.stdout))
                self.assertNotIn('backup_units_restored=true', failed.stdout)

    def test_both_actual_systemctl_helpers_require_known_output_and_exit_pair(self):
        for path in ROOT.glob('*backup-units.sh'):
            source = function(path.read_text(encoding='utf-8'), 'observed_unit_state')
            for operation, value, code in (('is-active', 'active', 0), ('is-active', 'inactive', 3), ('is-active', 'failed', 3), ('is-enabled', 'enabled', 0), ('is-enabled', 'disabled', 1), ('is-enabled', 'masked', 1), ('is-enabled', 'static', 0), ('is-enabled', 'generated', 0)):
                for observed in (code, 17):
                    with self.subTest(file=path.name, operation=operation, value=value, code=observed):
                        script = 'set -Eeuo pipefail\nsystemctl() { printf "%s" "$VALUE"; return "$CODE"; }\n' + source + '\nobserved_unit_state "$OPERATION" fixture\n'
                        result = self.shell(script, {'VALUE': value, 'CODE': str(observed), 'OPERATION': operation})
                        self.assertEqual(result.returncode == 0, observed == code)
                        self.assertEqual(result.stdout.strip(), value if observed == code else '')

    def test_native_mask_and_restore_workflow(self):
        # Real symlinks are necessary to verify the mask transaction. Windows
        # hosts without symlink privileges defer this exact fixture to Linux.
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            link = root / 'probe'
            try:
                os.symlink('/dev/null', link)
            except OSError:
                self.skipTest('native symlink capability is unavailable')
            link.unlink()
            environment, live = self.fixture(root)
            state = Path(directory) / Path(environment['STATE_DIR']).name
            for marker in ('masked.committed', 'restored.committed'):
                (state / marker).unlink()
            # Existing complete snapshot first restores the original regular
            # units, then commits the actual /dev/null mask transaction.
            source = (ROOT / 'mask-backup-units.sh').read_text(encoding='utf-8').replace('/etc/systemd/system', live)
            stub = STUBS.replace('builtin printf \'%s\\n\' "$ENABLED_VALUE";', 'if [[ -L "' + live + '/$2" ]]; then ENABLED_VALUE=masked; ENABLED_CODE=1; fi; builtin printf \'%s\\n\' "$ENABLED_VALUE";')
            masked = self.shell(stub + source, environment)
            self.assertEqual(masked.returncode, 0, masked.stderr)
            self.assertEqual(masked.stdout, 'backup_units_masked=true\n')
            self.assertTrue((state / 'masked.committed').is_file())
            source = (ROOT / 'restore-backup-units.sh').read_text(encoding='utf-8').replace('/etc/systemd/system', live)
            restored = self.shell(STUBS + source, environment)
            self.assertEqual(restored.returncode, 0, restored.stderr)
            self.assertEqual(restored.stdout, 'backup_units_restored=true\n')
            self.assertTrue((state / 'restored.committed').is_file())


if __name__ == '__main__':
    unittest.main()
