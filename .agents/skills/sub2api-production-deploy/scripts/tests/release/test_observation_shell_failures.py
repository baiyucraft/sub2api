from __future__ import annotations

import hashlib
import ast
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
from types import SimpleNamespace

sys.path.insert(0, str(Path(__file__).resolve().parents[2]))
from release.doctor import ReleaseDoctor
from release.manifest import canonical_json
from release.production import ProductionRelease, gate_consumption_probe_script
from release.production_bootstrap import bootstrap_production


class ObservationShellFailuresTest(unittest.TestCase):
    def setUp(self):
        candidate = Path('C:/Program Files/Git/bin/bash.exe')
        self.bash = str(candidate) if candidate.is_file() else shutil.which('bash')
        if not self.bash:
            self.skipTest('Bash is required for inert observation fixtures')

    def shell(self, script, environment=None):
        env = {key: value for key, value in os.environ.items() if key not in {'BASH_ENV', 'ENV', 'SHELLOPTS'}}
        env.update(environment or {})
        return subprocess.run([self.bash, '-c', script], env=env, capture_output=True, text=True, encoding='utf-8', errors='replace', timeout=15)

    def test_actual_generated_required_comparisons_reject_failed_producers(self):
        source = Path(sys.modules[ProductionRelease.__module__].__file__).read_text()
        context = {
            'self': SimpleNamespace(release_dir='/tmp/release', state_dir='/tmp/state', profile={'public_domain': 'fixture.invalid', 'dmit_public_ip': '127.0.0.1'}),
            'shlex': shlex, 'trust_sha': 'a' * 64, 'bundle_sha256': 'b' * 64,
            'stage_dir': '/tmp/stage', 'remote_bundle': '/tmp/stage/bundle', 'attempt_id': 'fixture',
            'profile_name': '273',
        }
        guards = []
        for node in ast.walk(ast.parse(source)):
            if not isinstance(node, ast.Call) or not isinstance(node.func, ast.Attribute) or node.func.attr != 'run_remote' or len(node.args) < 2:
                continue
            expression = ast.get_source_segment(source, node.args[1]) or ''
            if 'required_observed' not in expression:
                continue
            script = eval(compile(ast.Expression(node.args[1]), '<generated-script>', 'eval'), context)
            syntax = self.shell('bash -n -c ' + shlex.quote(script))
            self.assertEqual(syntax.returncode, 0, syntax.stderr)
            guards.extend(re.findall(r'\{ required_observed=\$\(.*?\) \|\| exit 125; \} && test "\$required_observed" = (?:\'[^\']*\'|[^\s;&]+)', script))
        self.assertEqual(len(guards), 24)
        for guard in guards:
            expected = shlex.split(guard[guard.index(' && test '):])[-1]
            for code in (0, 17):
                with self.subTest(guard=guard, code=code):
                    definitions = '\n'.join(name + '() { builtin printf "%s" "$EXPECTED"; return "$PRODUCER_CODE"; }' for name in ('stat', 'sha256sum', 'awk', 'find', 'grep', 'sed', 'curl', 'docker', 'systemctl'))
                    result = self.shell('set -Eeuo pipefail\n' + definitions + '\nactive_container=sub2api; state=/tmp/state; slot=/tmp/slot; file=/tmp/file; failure_file=/tmp/file; init_failure_file=/tmp/file; switch_file=/tmp/file; m195_file=/tmp/file\n' + guard + '\nprintf "comparison_accepted=true\\n"', {'EXPECTED': expected, 'PRODUCER_CODE': str(code)})
                    self.assertEqual(result.returncode, 0 if code == 0 else 125, (guard, expected, result.stderr))
                    self.assertEqual('comparison_accepted=true' in result.stdout, code == 0)

    def test_actual_inactive_checks_require_known_exit_status(self):
        runner = mock.Mock()
        runner.run.return_value.values = {}
        bootstrap_production('273', runner)
        scripts = [runner.run.call_args.args[1]]
        doctor = ReleaseDoctor('273', runner=runner)
        doctor._run_racknerd_readonly = mock.Mock(return_value={})
        doctor.check_racknerd()
        scripts.append(doctor._run_racknerd_readonly.call_args.args[0])
        for script in scripts:
            start = script.index('backup_service_code=0')
            block = script[start:script.index('esac', start) + 4]
            for state, code, accepted in (('inactive', 3, True), ('failed', 3, True), ('active', 0, False), ('inactive', 17, False), ('', 4, False)):
                result = self.shell('set -Eeuo pipefail\nsystemctl() { builtin printf "%s" "$STATE"; return "$CODE"; }\n' + block, {'STATE': state, 'CODE': str(code)})
                self.assertEqual(result.returncode == 0, accepted, (state, code))
        doctor.check_dmit()
        script = runner.run.call_args.args[1]
        blocks = re.findall(r'tinyproxy_code=0\n.*?\ntest "\$tinyproxy_state:\$tinyproxy_code" = (?:inactive:3|disabled:1)', script, re.S)
        self.assertEqual(len(blocks), 2)
        for block, state, code in ((blocks[0], 'inactive', 3), (blocks[1], 'disabled', 1)):
            for observed_code in (code, 0, 17):
                result = self.shell('set -Eeuo pipefail\nsystemctl() { builtin printf "%s" "$STATE"; return "$CODE"; }\n' + block, {'STATE': state, 'CODE': str(observed_code)})
                self.assertEqual(result.returncode == 0, observed_code == code)

    def test_actual_consumption_probe_rejects_correct_output_with_failed_command(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            release = root / 'release'
            consumed = release / '.consumed'
            assets = consumed / 'assets'
            assets.mkdir(parents=True)
            rid = '273-' + 'a' * 12 + '-1-' + 'a' * 8
            image = 'sha256:' + 'b' * 64
            (consumed / 'marker').write_text(f'release_id={rid}\ncandidate_image_id={image}\n')
            (assets / 'context.sh').write_text('assert_final_compose_closure() { return 0; }\n')
            (assets / 'nginx-ingress-contract.sh').write_text('assert_nginx_ingress_policy() { return 0; }\n')
            slot = root / 'slot'
            slot.write_text(f'container=sub2api\nport=18080\nimage_id={image}\nrelease_id={rid}\ninstance_id={rid}-active\n')
            upstream = root / 'upstream'
            upstream.write_text('server 127.0.0.1:18080;\n')
            script = gate_consumption_probe_script(release.as_posix(), rid, image, (root / 'active').as_posix(), slot.as_posix())
            stubs = '''
sed() { command sed "$@"; [[ $FAIL != slot ]]; }
docker() {
 case "$*" in *Health.Status*) builtin printf 'healthy\\n'; [[ $FAIL != health ]];; *) builtin printf '%s\\n' "$IMAGE"; [[ $FAIL != image ]];; esac
}
systemctl() { case "$1" in is-active) builtin printf 'active\\n'; [[ $FAIL != nginx ]];; *) builtin printf 'enabled\\n'; [[ $FAIL != timer ]];; esac; }
curl() { while [[ $# -gt 0 ]]; do if [[ $1 == -D ]]; then shift; builtin printf 'x-sub2api-instance: %s-active\\nx-sub2api-background-ready: true\\n' "$RID" > "$1"; fi; shift; done; }
export -f sed docker systemctl curl
'''
            env = {'IMAGE': image, 'RID': rid, 'NGINX_MANAGED_UPSTREAM': upstream.as_posix()}
            for case in ('pass', 'slot', 'image', 'health', 'nginx', 'timer'):
                with self.subTest(case=case):
                    result = self.shell(stubs + script, {**env, 'FAIL': case})
                    self.assertEqual(result.returncode, 0, result.stderr)
                    self.assertEqual(result.stdout, 'gate_consumed=' + ('true' if case == 'pass' else 'unknown') + '\n')
            # The unconsumed branch must also reject jq errors despite exact
            # expected stdout, rather than returning a trusted false result.
            shutil.rmtree(consumed)
            active = root / 'active'
            active.mkdir()
            (active / 'release_id').write_text(f'release_id={rid}\n')
            (active / 'gate.json').write_text('{}\n')
            (active / 'CLAIM_SHA256SUMS').write_bytes(('\n'.join(hashlib.sha256((active / n).read_bytes()).hexdigest() + '  ' + n for n in ('release_id', 'gate.json')) + '\n').encode())
            jq = '''
jq() { case "$2" in .manifest.release_id) builtin printf '%s\\n' "$RID"; [[ $FAIL != jq_release ]];; *) builtin printf '%s\\n' "$IMAGE"; [[ $FAIL != jq_image ]];; esac; }
export -f jq
'''
            for case in ('pass', 'jq_release', 'jq_image'):
                result = self.shell(stubs + jq + script, {**env, 'FAIL': case})
                self.assertEqual(result.stdout, 'gate_consumed=' + ('false' if case == 'pass' else 'unknown') + '\n', result.stderr)

    def test_actual_doctor_ingress_conditional_propagates_every_producer_error(self):
        runner = mock.Mock()
        doctor = ReleaseDoctor('273', runner=runner)
        doctor._run_racknerd_readonly = mock.Mock(return_value={})
        doctor.check_racknerd()
        source = doctor._run_racknerd_readonly.call_args.args[0]
        function = source[source.index('assert_ingress_policy() {'):source.index('nginx_ingress_policy=needs_update')]
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            function = function.replace('/etc/nginx', root.as_posix() + '/nginx').replace('/etc/logrotate.d', root.as_posix() + '/logrotate')
            site = root / 'nginx/sites-enabled/app.conf'
            site.parent.mkdir(parents=True)
            site.write_text('include /etc/nginx/snippets/sub2api-release-ingress.conf;\nproxy_pass http://sub2api_release_backend;\n')
            for name in ('nginx/snippets/sub2api-release-ingress.conf', 'nginx/conf.d/sub2api-release-observability.conf', 'logrotate/sub2api-upstream-access'):
                p = root / name
                p.parent.mkdir(parents=True, exist_ok=True)
                p.write_text('fixture\n')
            (root / 'nginx/sub2api-release-backups').mkdir()
            # Count calls, then inject after emitting the correct stdout at
            # every find/stat/grep/awk/nginx call from the real function.
            stubs = '''
step() { local n=0; if [[ -f $TRACE ]]; then n=$(command wc -l < "$TRACE"); fi; n=$((n+1)); builtin printf '%s\\n' "$1" >> "$TRACE"; [[ $n != $FAIL_AT ]]; }
find() { command find "$@"; step find; }
stat() { case "$*" in *sub2api-release-backups*) builtin printf 'root:root:700\\n';; *logrotate*) builtin printf 'root:root:644:1\\n';; *) builtin printf 'root:root:600:1\\n';; esac; step stat; }
awk() { step awk; }
grep() {
 if [[ "$*" == *'conflicting server name'* ]]; then step grep || return 17; return 1; fi
 if [[ $1 == -Fxc ]]; then builtin printf '1\\n'; fi
 step grep || return 17
}
nginx() { builtin printf '# configuration file %s/nginx/sites-enabled/app.conf:\\n# configuration file %s/nginx/snippets/sub2api-release-ingress.conf:\\n# configuration file %s/nginx/conf.d/sub2api-release-observability.conf:\\n' "$ROOT" "$ROOT" "$ROOT"; step nginx; }
'''
            env = {'ROOT': root.as_posix(), 'TRACE': (root / 'trace').as_posix(), 'FAIL_AT': '0'}
            code = stubs + function + '\nif assert_ingress_policy; then printf "ingress_policy=ready\\n"; else printf "ingress_policy=needs_update\\n"; fi'
            result = self.shell(code, env)
            self.assertEqual(result.stdout, 'ingress_policy=ready\n', result.stderr)
            count = len((root / 'trace').read_text().splitlines())
            for index in range(1, count + 1):
                with self.subTest(index=index):
                    (root / 'trace').unlink()
                    result = self.shell(code, {**env, 'FAIL_AT': str(index)})
                    self.assertEqual(result.stdout, 'ingress_policy=needs_update\n', result.stderr)

    def observer_release(self, root, schema=2):
        release = ProductionRelease.__new__(ProductionRelease)
        release.release_id = '273-' + 'a' * 12 + '-1-' + 'a' * 8
        release.release_dir = (root / 'release').as_posix()
        release.state_dir = (root / 'state').as_posix()
        Path(release.release_dir).mkdir(exist_ok=True)
        Path(release.state_dir).mkdir(exist_ok=True)
        pending = [{'filename': 'fixture.sql', 'checksum': 'c' * 64}]
        release.manifest = {'schema': schema, 'catalog_sha256': 'd' * 64, 'migrations': []}
        release.evidence = {'migration_evidence': {'pending': pending}, 'production_snapshot_sha256': 'e' * 64}
        release.profile = {'name': '195'}
        source = Path(__file__).resolve().parents[2] / 'release/production.py'
        for attr in set(re.findall(r'self\.(migration_[0-9]+_status)', source.read_text())):
            setattr(release, attr, 'absent')
        return release, pending

    def test_actual_recovery_observers_propagate_failed_proofs_as_none(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            release, pending = self.observer_release(root)
            state = Path(release.state_dir)
            marker = state / 'migration-committed'
            marker.write_text('plan_sha256=' + hashlib.sha256(canonical_json(pending)).hexdigest() + '\ncatalog_sha256=' + 'd' * 64 + '\nproduction_snapshot_sha256=' + 'e' * 64 + '\nmigration=fixture.sql checksum=' + 'c' * 64 + '\n')
            stubs = '''
stat() { builtin printf 'root:root:600:1\\n'; [[ $FAIL != stat ]]; }
docker() { if [[ $1 == ps ]]; then [[ $FAIL != ps ]]; else builtin printf '%s\\n' "$CHECKSUM"; [[ $FAIL != query ]]; fi; }
grep() { command grep "$@"; [[ $FAIL != grep ]] || return 17; }
'''
            def execute(host, script, allowed):
                script = script.replace('/opt/sub2api/active-app', (root / 'slot').as_posix()).replace('/etc/nginx/conf.d/sub2api-release-upstream.conf', (root / 'upstream').as_posix())
                result = self.shell(stubs + script, {'FAIL': case, 'CHECKSUM': 'c' * 64})
                if result.returncode:
                    raise RuntimeError('inert observation command failed')
                return dict(line.split('=', 1) for line in result.stdout.splitlines())
            release.run_remote = execute
            for case in ('pass', 'stat', 'query', 'ps', 'grep'):
                with self.subTest(observer='migration-v2', case=case):
                    self.assertIs(release.remote_migration_committed(), True if case == 'pass' else None)
            marker.unlink()
            for case in ('pass', 'ps'):
                self.assertIs(release.remote_migration_committed(), False if case == 'pass' else None)

    def test_systemctl_observer_accepts_known_nonzero_states_but_rejects_errors(self):
        from release.production import observation_shell_helpers
        for operation, value, code, valid in (
            ('is-active', 'active', 0, True), ('is-active', 'inactive', 3, True),
            ('is-active', 'active', 17, False), ('is-active', 'inactive', 17, False),
            ('is-enabled', 'masked', 1, True), ('is-enabled', 'disabled', 1, True),
            ('is-enabled', 'masked', 17, False), ('is-enabled', 'enabled', 17, False),
        ):
            result = self.shell('systemctl() { printf "%s\\n" ' + shlex.quote(value) + '; return ' + str(code) + '; }\n' + observation_shell_helpers() + '\nobserved_systemctl ' + operation + ' fixture')
            self.assertEqual(result.returncode == 0, valid)
            self.assertEqual(result.stdout, value + '\n' if valid else '')

    def test_actual_legacy_migration_observer_rejects_failed_inventory_and_metadata(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            release, _ = self.observer_release(root, schema=1)
            state = Path(release.state_dir)
            (state / 'migration-195-plan.sha256').write_bytes(b'plan\n')
            (state / 'migration-committed').write_bytes(b'plan_sha256=plan\nmigration_manifest_sha256=fixture\n')
            stubs = '''
stat() { builtin printf 'root:root:600:1\\n'; [[ $FAIL != stat ]]; }
cat() { command cat "$@"; [[ $FAIL != plan ]]; }
sed() { builtin printf 'fixture\\n'; [[ $FAIL != marker_read ]]; }
jq() { if [[ $1 == -cS ]]; then builtin printf '{}\\n'; [[ $FAIL != manifest ]]; else [[ $FAIL != inventory ]]; fi; }
sha256sum() { builtin printf 'fixture  -\\n'; }
docker() { [[ $FAIL != ps ]]; }
grep() { command grep "$@"; [[ $FAIL != grep ]] || return 17; }
'''
            def execute(host, script, allowed):
                result = self.shell(stubs + script, {'FAIL': case})
                if result.returncode:
                    raise RuntimeError('inert observation command failed')
                return dict(line.split('=', 1) for line in result.stdout.splitlines())
            release.run_remote = execute
            for case in ('pass', 'stat', 'plan', 'marker_read', 'manifest', 'inventory', 'ps', 'grep'):
                with self.subTest(case=case):
                    self.assertIs(release.remote_migration_committed(), True if case == 'pass' else None)

    def test_actual_pre_switch_and_mask_observers_reject_every_failed_read(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            release, _ = self.observer_release(root)
            state = Path(release.state_dir)
            (root / 'slot').write_bytes(b'container=sub2api\nport=18080\n')
            (root / 'upstream').write_bytes(b'server 127.0.0.1:18080;\n')
            (state / 'pre-image-id').write_bytes(b'image\n')
            (state / 'SHA256SUMS').write_bytes(b'fixture\n')
            (state / 'masked.committed').write_bytes(b'fixture\n')
            (state / 'route-switch-intent').write_bytes(b'fixture\n')
            (state / 'route-switched').write_bytes(b'route_port=18080\n')
            stubs = '''
step() { local n=0; if [[ -f $TRACE ]]; then n=$(command wc -l < "$TRACE"); fi; n=$((n+1)); builtin printf '%s\\n' "$1" >> "$TRACE"; [[ $n != $FAIL_AT ]]; }
sed() { command sed "$@"; step sed; }
grep() { command grep "$@"; step grep; }
cat() { command cat "$@"; step cat; }
docker() { case "$*" in *State.Status*) builtin printf 'running\\n';; *) builtin printf 'image\\n';; esac; step docker; }
systemctl() {
 if [[ $1 == is-active ]]; then builtin printf 'active\\n'; step systemctl || return 17;
 else builtin printf '%s\\n' "$UNIT_STATE"; step systemctl || return 17; [[ $UNIT_STATE == enabled ]] || return 1; fi
}
'''
            def execute(host, script, allowed):
                script = script.replace('/opt/sub2api/active-app', (root / 'slot').as_posix()).replace('/etc/nginx/conf.d/sub2api-release-upstream.conf', (root / 'upstream').as_posix())
                result = self.shell(stubs + script, {'TRACE': (root / 'trace').as_posix(), 'FAIL_AT': str(fail_at), 'UNIT_STATE': unit_state})
                if result.returncode:
                    raise RuntimeError('inert observation command failed')
                return dict(line.split('=', 1) for line in result.stdout.splitlines())
            release.run_remote = execute
            for observer, unit_state, expected in ((release.remote_pre_switch_recovery_needed, 'enabled', False), (release.remote_units_masked, 'masked', True)):
                fail_at = 0
                (root / 'trace').unlink(missing_ok=True)
                self.assertIs(observer(), expected)
                total = len((root / 'trace').read_text().splitlines())
                for fail_at in range(1, total + 1):
                    with self.subTest(observer=observer.__name__, fail_at=fail_at):
                        (root / 'trace').unlink()
                        self.assertIsNone(observer())


if __name__ == '__main__':
    unittest.main()
