from __future__ import annotations

import os
import re
import shlex
import shutil
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parents[2]))
from release.doctor import ReleaseDoctor
from release.production import ProductionRelease, gate_consumption_probe_script
from release.production_bootstrap import bootstrap_production


class RequiredShellGuardsTest(unittest.TestCase):
    def setUp(self):
        candidate = Path('C:/Program Files/Git/bin/bash.exe')
        self.bash = str(candidate) if candidate.is_file() else shutil.which('bash')
        if not self.bash:
            self.skipTest('Bash is required for inert failure injection')

    def run_bash(self, script, **kwargs):
        return subprocess.run([self.bash, '-c', script], capture_output=True, text=True, encoding='utf-8', errors='replace', timeout=15, **kwargs)

    def bootstrap_scripts(self):
        runner = mock.Mock()
        runner.run.return_value.values = {}
        codes = []
        original = shlex.quote
        with mock.patch('release.production_bootstrap.shlex.quote', side_effect=lambda x: (codes.append(x), original(x))[1]):
            bootstrap_production('273', runner)
        return runner.run.call_args.args[1], codes

    def assert_required_tests_stop(self, block):
        count = sum(line.strip().startswith('test ') for line in block.splitlines())
        self.assertGreater(count, 0)
        # The Bash builtin is shadowed solely to inject each prerequisite's
        # failure. A following sentinel represents the first operation.
        for failed in range(1, count + 1):
            with self.subTest(block=block, failed=failed):
                script = ('set -Eeuo pipefail\nn=0\ntrust=x; active_slot=x; backup_path=x; managed_upstream=x; site=x; wrapper=x; dropin=x; err_file=x; proxy_config=x\ngrep() { return 0; }\nsha256sum() { return 0; }\nstat() { return 0; }\n'
                          'test() { n=$((n+1)); [[ $n != ' + str(failed) + ' ]]; }\n'
                          + block + '\nprintf "operation_started=true\\n"\n')
                result = self.run_bash(script)
                self.assertNotEqual(result.returncode, 0, result.stdout)
                self.assertNotIn('operation_started=true', result.stdout)

    def test_doctor_required_prerequisites_fail_before_following_probe(self):
        runner = mock.Mock()
        runner.run.return_value.values = {}
        doctor = ReleaseDoctor('273', runner=runner)
        doctor._run_racknerd_readonly = mock.Mock(return_value={})
        doctor.check_racknerd()
        rack = doctor._run_racknerd_readonly.call_args.args[0]
        doctor.check_backup()
        backup = runner.run.call_args.args[1]
        doctor.check_dmit()
        dmit = runner.run.call_args.args[1]
        for start, end in (
            ('test -d /opt/sub2api/releases', 'active_slot='),
            ('test -f "$active_slot"', 'active_container='),
            ('test -f "$backup_path"', "grep -Fq '/run/lock"),
            ('test -r /etc/nginx/nginx.conf', 'if ssh '),
        ):
            self.assert_required_tests_stop(rack[rack.index(start):rack.index(end, rack.index(start))])
        self.assert_required_tests_stop(backup[backup.index('test -d /srv'):backup.index('free_bytes=')])
        self.assert_required_tests_stop(dmit[dmit.index('test -n "$proxy_config"'):dmit.index("grep -Eq '^[[:space:]]*Listen")])

    def test_production_required_log_guards_stop_before_mutation(self):
        release = ProductionRelease.__new__(ProductionRelease)
        release.release_dir = '/tmp/release'
        release._remote_log_sequence = 0
        release.result = {'stage': 'fixture'}
        script = release._wrap_remote_logging('printf "wrapped=true\\n"')
        for start, end in (
            ('test -d /tmp/release\n', 'if test -e /tmp/release/logs'),
            ('  test -d /tmp/release/logs', '\nelse'),
            ('  test -f /tmp/release/logs/production.raw.log', '\nelse'),
        ):
            begin = script.index(start)
            self.assert_required_tests_stop(script[begin:script.index(end, begin)])
        release.active_assets = '/tmp/assets'
        release.run_remote = mock.Mock(return_value={})
        release.restore_backup_units('RELEASE_DIR=/tmp/release')
        script = release.run_remote.call_args.args[1]
        begin = script.index('  test -f "$err_file"')
        self.assert_required_tests_stop(script[begin:script.index('\nelse', begin)])

    def test_bootstrap_required_prerequisites_fail_before_writes(self):
        script, _ = self.bootstrap_scripts()
        for start, end in (
            ('test -f "$trust"', 'for container'),
            ('  test ! -e "$managed_upstream"', '  nginx_text='),
            ('  required_value=$(wc -l', '  site_backup='),
            ('test -f "$active_slot"', 'active_container='),
            ('test -f "$wrapper"', "required_value=$(grep -Fxc"),
        ):
            self.assert_required_tests_stop(script[script.index(start):script.index(end, script.index(start))])

    def test_bootstrap_success_protocol_emits_every_required_field(self):
        script, _ = self.bootstrap_scripts()
        # The f-string expands literal newlines in this protocol printf.
        start = script.index("printf 'production_bootstrap=true")
        result = self.run_bash('active_port=18080\n' + script[start:])
        self.assertEqual(result.returncode, 0, result.stderr)
        runner = mock.Mock()
        runner.run.return_value.values = {}
        bootstrap_production('273', runner)
        required = runner.run.call_args.args[2]
        values = dict(line.split('=', 1) for line in result.stdout.splitlines())
        self.assertEqual(set(values), required)
        self.assertEqual(values['production_bootstrap_recovery'], 'not_needed')

    def test_full_bootstrap_success_uses_only_temporary_files_and_inert_services(self):
        script, _ = self.bootstrap_scripts()
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            script = re.sub(r'/opt/sub2api-release-trust|/opt/sub2api|/etc/nginx|/usr/local/libexec|/etc/systemd/system', lambda m: root.as_posix() + m.group(0), script)
            trust = root / 'opt/sub2api-release-trust/vm-gate-ed25519.pub'
            trust.parent.mkdir(parents=True)
            trust.write_bytes((Path(__file__).resolve().parents[2] / 'release/trust/vm-gate-ed25519.pub').read_bytes())
            sites = root / 'etc/nginx/sites-enabled'
            sites.mkdir(parents=True)
            (sites / 'app.conf').write_text('proxy_pass http://127.0.0.1:18080;\n')
            (root / 'etc/nginx/conf.d').mkdir()
            wrapper = root / 'usr/local/libexec/sub2api-backup-global-lock'
            wrapper.parent.mkdir(parents=True)
            wrapper.write_text('/run/lock/sub2api-backup-global.lock\n')
            dropin = root / 'etc/systemd/system/sub2api-backup.service.d/10-global-lock.conf'
            dropin.parent.mkdir(parents=True)
            dropin.write_text('ExecStart=' + wrapper.as_posix() + '\n')
            image = 'sha256:' + 'a' * 64
            stubs = '''
docker() { case "$*" in *Health.Status*) builtin printf 'healthy\\n';; *) builtin printf '%s\\n' "$FIXTURE_IMAGE";; esac; }
systemctl() {
 case "$*" in
  'is-active sub2api-backup.service') builtin printf 'inactive\\n'; return 3;;
  is-active*) builtin printf 'active\\n';;
  show*) builtin printf '{ path=%s ; }\\n' "$FIXTURE_WRAPPER";;
 esac
}
nginx() { if [[ $* == -T ]]; then builtin printf 'proxy_pass http://127.0.0.1:18080;\\n'; fi; }
curl() { builtin printf '200'; }
install() { if [[ $1 == -d ]]; then shift 3; mkdir -p -- "$@"; else command cp -- "$3" "$4"; fi; }
chmod() { return 0; }
stat() { builtin printf '600\\n'; }
export -f docker systemctl nginx curl install chmod stat
'''
            env = os.environ.copy()
            env.update(FIXTURE_IMAGE=image, FIXTURE_WRAPPER=wrapper.as_posix())
            result = self.run_bash(stubs + script, env=env)
            self.assertEqual(result.returncode, 0, result.stderr)
            values = dict(line.split('=', 1) for line in result.stdout.splitlines())
            self.assertEqual(values['production_bootstrap'], 'true')
            self.assertEqual(values['production_bootstrap_recovery'], 'not_needed')
            slot = root / 'opt/sub2api/active-app'
            self.assertIn('image_id=' + image, slot.read_text())
            self.assertIn('proxy_pass http://sub2api_release_backend;', (sites / 'app.conf').read_text())

    def test_required_command_failure_cannot_be_hidden_by_expected_stdout(self):
        bootstrap, _ = self.bootstrap_scripts()
        runner = mock.Mock()
        runner.run.return_value.values = {}
        doctor = ReleaseDoctor('273', runner=runner)
        doctor._run_racknerd_readonly = mock.Mock(return_value={})
        doctor.check_racknerd()
        scripts = [bootstrap, doctor._run_racknerd_readonly.call_args.args[0]]
        doctor.check_vm()
        scripts.append(runner.run.call_args.args[1])
        doctor.check_dmit()
        scripts.append(runner.run.call_args.args[1])
        doctor.check_backup()
        scripts.append(runner.run.call_args.args[1])
        commands = sorted(set(re.findall(r'^ *required_value=\$\(.*\)$', '\n'.join(scripts), re.M)))
        self.assertGreater(len(commands), 10)
        stubs = '\n'.join(name + '() { builtin printf "healthy\\n"; return 17; }' for name in ('docker', 'systemctl', 'sha256sum', 'awk', 'stat', 'grep', 'curl', 'wc', 'ss'))
        for command in commands:
            with self.subTest(command=command):
                result = self.run_bash('set -Eeuo pipefail\ntrust=x; active_slot=x; container=x; active_container=x; active_port=18080; site=x; managed_upstream=x; dropin=x; asset=x; unit_lock=x\n' + stubs + '\n' + command + '\nprintf "operation_started=true\\n"')
                self.assertEqual(result.returncode, 17, result.stderr)
                self.assertNotIn('operation_started=true', result.stdout)

    def test_each_transaction_and_recovery_command_failure_stops_later_commands(self):
        _, codes = self.bootstrap_scripts()
        self.assertEqual(len(codes), 2)
        initialize, recover = codes
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            # Inert functions replace every native/production operation. The
            # counter file also covers commands executed by substitutions.
            trace = (root / 'trace').as_posix()
            environment = os.environ.copy()
            environment.update(TRACE=trace, FAKE_IMAGE='sha256:' + 'a' * 64)
            stubs = '''
step() {
  local n=0
  if [[ -f $TRACE ]]; then n=$(wc -l < "$TRACE"); fi
  n=$((n+1))
  builtin printf '%s\\n' "$1" >> "$TRACE"
  [[ $n != $FAIL_AT ]]
}
sed() { step sed; }
chmod() { step chmod; }
mv() { step mv; }
nginx() { step nginx; }
systemctl() { step systemctl; }
install() { step install; }
rm() { step rm; }
docker() { step docker || return 1; builtin printf '%s\\n' "$FAKE_IMAGE"; }
printf() { step printf || return 1; builtin printf "$@"; }
export -f step sed chmod mv nginx systemctl install rm docker printf
'''
            for code, total in ((initialize, 10), (recover, 4)):
                for fail_at in range(1, total + 1):
                    with self.subTest(transaction=code is initialize, fail_at=fail_at):
                        (root / 'trace').unlink(missing_ok=True)
                        environment['FAIL_AT'] = str(fail_at)
                        names = ('site', 'upstream', 'slot') if code is initialize else ('backup', 'site', 'upstream', 'slot')
                        args = ' '.join(shlex.quote((root / name).as_posix()) for name in names) + ' 123'
                        result = self.run_bash(stubs + '\nif bash -Eeuo pipefail -c ' + shlex.quote(code) + ' -- ' + args + '; then builtin printf "transaction_succeeded=true\\n"; fi', env=environment)
                        self.assertNotIn('transaction_succeeded=true', result.stdout)
                        self.assertEqual(len((root / 'trace').read_text().splitlines()), fail_at, result.stderr)
                (root / 'trace').unlink(missing_ok=True)
                environment['FAIL_AT'] = '0'
                result = self.run_bash(stubs + '\nbash -Eeuo pipefail -c ' + shlex.quote(code) + ' -- ' + args, env=environment)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual(len((root / 'trace').read_text().splitlines()), total)

    def test_bootstrap_failed_recovery_remains_unknown(self):
        script, _ = self.bootstrap_scripts()
        begin = script.index('  if ! bash -Eeuo pipefail -c ')
        end = script.index('\nfi\ntest -f "$active_slot"', begin)
        conditional = script[begin:end]
        # The real child always fails at the first mutation; independently
        # fail each recovery operation and verify its 125/unknown boundary.
        for operation in ('install', 'rm', 'nginx', 'systemctl'):
            functions = '\n'.join(name + '() { return ' + ('1' if name in ('sed', operation) else '0') + '; }' for name in ('sed', 'install', 'rm', 'nginx', 'systemctl'))
            exports = 'export -f sed install rm nginx systemctl\n'
            result = self.run_bash('set -Eeuo pipefail\nsite=x; managed_upstream=x; active_slot=x; site_backup=x\n' + functions + '\n' + exports + conditional)
            self.assertEqual(result.returncode, 125, result.stderr)
            self.assertEqual(result.stdout, 'production_bootstrap_recovery=unknown\n')

    def test_consumption_probe_strict_child_rejects_source_and_nonfinal_assert_failure(self):
        script = gate_consumption_probe_script('/tmp/release', '273-' + 'a' * 12 + '-1-' + 'a' * 8, 'sha256:' + 'a' * 64)
        line = next(line for line in script.splitlines() if '&& bash -Eeuo pipefail -c' in line)
        # The generated shell quotes a multiline child; recover that exact
        # child using shlex on the complete conditional prefix.
        start = script.index('&& bash -Eeuo pipefail -c ') + len('&& bash -Eeuo pipefail -c ')
        code = shlex.split(script[start:])[0]
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            assets = root / 'assets'
            assets.mkdir()
            ingress = assets / 'nginx-ingress-contract.sh'
            ingress.write_text('assert_nginx_ingress_policy() { return 0; }\n')
            context = assets / 'context.sh'
            for source in (
                'false\nassert_final_compose_closure() { return 0; }\n',
                'assert_final_compose_closure() { false; printf "bad_success=true\\n"; }\n',
            ):
                context.write_text(source)
                result = self.run_bash('if bash -Eeuo pipefail -c ' + shlex.quote(code) + ' -- ' + shlex.quote(root.as_posix()) + ' /tmp/release 18080; then printf "gate_consumed=true\\n"; else printf "gate_consumed=unknown\\n"; fi')
                self.assertEqual(result.stdout, 'gate_consumed=unknown\n')
            context.write_text('assert_final_compose_closure() { return 0; }\n')
            result = self.run_bash('bash -Eeuo pipefail -c ' + shlex.quote(code) + ' -- ' + shlex.quote(root.as_posix()) + ' /tmp/release 18080')
            self.assertEqual(result.returncode, 0, result.stderr)


if __name__ == '__main__':
    unittest.main()
