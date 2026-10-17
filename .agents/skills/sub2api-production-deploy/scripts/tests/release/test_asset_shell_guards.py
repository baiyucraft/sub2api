"""Run the generated asset scripts with inert tools and injected failures.

The test builtin models unsafe filesystem objects. No privileged path is read
or written: the lock and raw-log redirections are routed to /dev/null, and all
filesystem/remote tools are stubs. Successful controls prove each whole script
can reach its terminal marker before individual guards are failed.
"""
from pathlib import Path
from unittest import mock
import os
import shlex
import shutil
import subprocess
import sys

import pytest

sys.path.insert(0, str(Path(__file__).resolve().parents[2]))
from release import recovery_bundle, postrelease_baseline, production_snapshot, plugin_api
from test_recovery_bundle import staging_fixture


@pytest.fixture
def bash():
    candidate = Path('C:/Program Files/Git/bin/bash.exe')
    result = str(candidate) if candidate.is_file() else shutil.which('bash')
    if not result:
        pytest.skip('Bash required for inert shell failure injection')
    return result


def execute(bash, script, *, failed_test='', failed_command=''):
    # Only output destinations change. The complete generated control flow,
    # required assertions and command substitutions run unmodified.
    script = script.replace('/run/lock/sub2api-production-release.lock', '/dev/null')
    script = script.replace('>>"$SUB2API_RELEASE_RAW_LOG"', '>>/dev/null')
    prelude = r'''
test() {
  if [[ "$*" == "$failed_test" ]]; then printf 'injected_test_failure\n' >&2; return 71; fi
  if [[ $1 == '!' && $2 == -e ]]; then return 1; fi
  return 0
}
tool() { printf 'stub-output\n'; if [[ $1 == "$failed_command" ]]; then printf 'injected_command_failure\n' >&2; return 72; fi; }
id() { tool id; }; realpath() { tool realpath; }; stat() { tool stat; }
sha256sum() { tool sha256sum; }; openssl() { tool openssl; }
flock() { tool flock; }; install() { tool install; }; tar() { tool tar; }
find() { tool find; }; mv() { tool mv; }; cmp() { tool cmp; }; rm() { tool rm; }
python3() { tool python3; }; bash() { tool bash; }
sed() { printf '18080\n'; }
grep() { printf '1\n'; }
'''
    env = os.environ.copy()
    env.pop('BASH_ENV', None); env.pop('ENV', None)
    prelude = 'failed_test=' + shlex.quote(failed_test) + '\nfailed_command=' + shlex.quote(failed_command) + '\n' + prelude
    return subprocess.run([bash, '--noprofile', '--norc', '-c', prelude + script],
                          env=env, capture_output=True, text=True, timeout=15)


@pytest.fixture
def staged(tmp_path):
    local, gate, _, manifest, checksum, ssh, release = staging_fixture(tmp_path)
    recovery_bundle.stage_recovery_bundle(ssh, manifest, release, local, tmp_path, gate)
    return ssh.run.call_args.args[1], release, checksum


def test_staging_whole_script_positive_control(bash, staged):
    result = execute(bash, staged[0])
    assert result.returncode == 0, result.stderr
    assert 'recovery_helper_bundle_verified=true' in result.stdout


@pytest.mark.parametrize('object_name,kind', [
    ('release', '-d'), ('temp/bundle.tar', '-f'), ('temp/gate.json', '-f'),
    ('temp/gate.sig', '-f'), ('trust_dir', '-d'), ('trust', '-f'),
    ('parent', '-d'), ('dest', '-d'), ('auth', '-d'),
    ('auth/gate.json', '-f'), ('auth/gate.sig', '-f'),
])
def test_staging_required_objects_stop_before_verified(bash, staged, object_name, kind):
    script, release, checksum = staged
    values = {'release': release, 'temp': '/opt/sub2api/releases/.recovery-helpers-owned',
              'trust_dir': '/opt/sub2api-release-trust',
              'trust': '/opt/sub2api-release-trust/vm-gate-ed25519.pub',
              'parent': release + '/recovery-helpers',
              'dest': release + '/recovery-helpers/' + checksum,
              'auth': release + '/recovery-helpers/' + checksum + '.authorization'}
    root, _, child = object_name.partition('/')
    target = values[root] + ('/' + child if child else '')
    result = execute(bash, script, failed_test=kind + ' ' + target)
    assert 'injected_test_failure' in result.stderr
    assert result.returncode != 0
    assert 'recovery_helper_bundle_verified=true' not in result.stdout


@pytest.mark.parametrize('command', ['id', 'realpath', 'stat', 'sha256sum', 'openssl', 'python3', 'cmp', 'rm'])
def test_staging_output_does_not_hide_command_failure(bash, staged, command):
    result = execute(bash, staged[0], failed_command=command)
    if command != 'openssl':  # Production intentionally redirects verifier diagnostics.
        assert 'injected_command_failure' in result.stderr
    assert result.returncode != 0
    assert 'recovery_helper_bundle_verified=true' not in result.stdout


@pytest.mark.parametrize('object_name,kind', [('', ''), ('release', '-d'), ('logs', '-d'),
    ('raw', '-f'), ('identity', '-f')])
def test_recovery_command_required_objects(bash, object_name, kind):
    release = '/opt/sub2api/releases/273-' + 'a'*12 + '-1-deadbeef'
    checksum = 'c'*64
    script = recovery_bundle.recovery_command(release + '/recovery-helpers/' + checksum, checksum, release)
    paths = {'release': release, 'logs': release + '/logs',
             'raw': release + '/logs/production.raw.log',
             'identity': release + '/recovery-checkpoints/identity'}
    failure = kind + ' ' + paths[object_name] if object_name else ''
    result = execute(bash, script, failed_test=failure)
    if object_name:
        assert result.returncode != 0
        assert 'coordinated_restore=verified' not in result.stdout
    else:
        assert result.returncode == 0, result.stderr
        assert 'coordinated_restore=verified' in result.stdout


@pytest.fixture
def signing():
    runner = mock.Mock()
    class Captured(Exception): pass
    runner.run.side_effect = Captured
    with pytest.raises(Captured):
        postrelease_baseline.sign_and_promote(release_id='273-' + 'a'*12 + '-1-deadbeef',
            drill_id='dr-273-20261017T010000Z', evidence_sha256='b'*64, runner=runner)
    return runner.run.call_args.args[1]


@pytest.mark.parametrize('filename', ['evidence.json', 'evidence.sig'])
def test_signing_missing_required_file_cannot_emit_signed_result(bash, signing, filename):
    path = '/opt/sub2api-deploy/dr-evidence/273-' + 'a'*12 + '-1-deadbeef/dr-273-20261017T010000Z/' + filename
    result = execute(bash, signing, failed_test='-f ' + path)
    assert result.returncode != 0
    assert 'injected_test_failure' in result.stderr
    assert 'signature_sha256=' not in result.stdout


@pytest.mark.parametrize('command', ['', 'stat', 'sha256sum', 'openssl'])
def test_signing_positive_and_failed_producers(bash, signing, command):
    result = execute(bash, signing, failed_command=command)
    if command:
        assert result.returncode != 0
        assert 'signature_sha256=' not in result.stdout
    else:
        assert result.returncode == 0, result.stderr
        assert 'signature_sha256=' in result.stdout


def test_promotion_never_starts_after_checksum_producer_error(bash):
    runner = mock.Mock()
    signed = {'evidence_sha256': 'a'*64, 'signature_sha256': 'b'*64}
    runner.run.side_effect = [SimpleResult(signed), SimpleResult({'staged': 'true'}), RuntimeError('captured')]
    runner.create_temp_dir.side_effect = ['/fixture/source', '/fixture/target', '/fixture/promoter']
    with pytest.raises(RuntimeError, match='captured'):
        postrelease_baseline.sign_and_promote(release_id='273-'+'a'*12+'-1-deadbeef',
            drill_id='dr-273-20261017T010000Z', evidence_sha256='a'*64, runner=runner)
    script = runner.run.call_args.args[1]
    # Matching digest output must not hide a nonzero producer before promotion.
    for checksum in ('a'*64, 'b'*64):
        control = ('test() { return 0; }; sha256sum() { printf "%s  file\\n" ' + checksum + '; return 72; };\n')
        result = subprocess.run([bash, '-c', control + script], capture_output=True, text=True, timeout=15)
        assert result.returncode == 72
        assert '/fixture/promoter' not in result.stderr


class SimpleResult:
    def __init__(self, values):
        self.values = values


@pytest.mark.parametrize('producer', ['systemctl', 'stat', 'find', 'date'])
def test_ingress_required_producers_stop_before_success(bash, producer):
    source = (Path(__file__).resolve().parents[2] / 'maintenance/release/apply-nginx-ingress.sh').read_text(encoding='utf-8')
    fragments = {
        'systemctl': ('nginx_state=$(systemctl is-active nginx)\n[[ $nginx_state == active ]]', 'active'),
        'stat': ('backup_directory_metadata=$(stat -c \'%U:%G:%a\' "$NGINX_SITE_BACKUP_DIR")\n  [[ $backup_directory_metadata == root:root:700 ]]', 'root:root:700'),
        'find': ('find /etc/nginx/sites-enabled -maxdepth 1 -type f -name \'*.sub2api-release-backup\' -print0 > "$txn/stale-inventory.tmp"', ''),
        'date': ('applied_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)\nprintf \'applied_at=%s\\n\' "$applied_at" > "$txn/applied.tmp"', '2026-10-17T00:00:00Z'),
    }
    fragment, output = fragments[producer]
    assert fragment in source
    control = 'set -Eeuo pipefail\nNGINX_SITE_BACKUP_DIR=fixture\ntxn=/dev/null\n'
    fragment = fragment.replace('"$txn/stale-inventory.tmp"', '/dev/null').replace('"$txn/applied.tmp"', '/dev/null')
    for code in (0, 71):
        script = control + producer + '() { printf "%s\\n" ' + shlex.quote(output) + '; return ' + str(code) + '; }\n' + fragment + '\nprintf "accepted=true\\n"'
        result = subprocess.run([bash, '-c', script], capture_output=True, text=True, timeout=15)
        assert (result.returncode == 0) == (code == 0)
        assert ('accepted=true' in result.stdout) == (code == 0)


@pytest.mark.parametrize('path', ['', '/opt/sub2api/active-app', '/etc/nginx/conf.d/sub2api-release-upstream.conf'])
def test_plugin_lookup_required_files(bash, path):
    runner = mock.Mock()
    runner.run.return_value.values = {'active_port': '18080'}
    plugin_api.PluginAPIClient(runner)._base_urls('racknerd')
    script = runner.run.call_args.args[1]
    result = execute(bash, script, failed_test='-f ' + path if path else '')
    if path:
        assert result.returncode != 0
        assert 'active_port=' not in result.stdout
    else:
        assert result.returncode == 0, result.stderr
        assert 'active_port=18080' in result.stdout


@pytest.mark.parametrize('image,passes', [('sha256:'+'a'*64, True), ('sha256:bad', False), ('sha256:*', False)])
def test_snapshot_checks_image_identity_after_runtime_resolution(bash, image, passes):
    script = production_snapshot.snapshot_script()
    # Keep the generated snapshot path, isolating the separately tested shared
    # identity resolver so malformed image output reaches this specific guard.
    script = script.replace(production_snapshot.runtime_commit_script(),
        'resolve_production_commit() { production_current_commit_sha=' + 'b'*40 + '; }\n')
    prelude = 'sed() { printf "container\\n"; }\n'
    prelude += 'docker() { if [[ $1 == exec ]]; then printf "[]\\n"; else printf "%s\\n" ' + shlex.quote(image) + '; fi; }\n'
    prelude += 'jq() { if [[ $1 == -cn ]]; then printf "{}\\n"; else cat >/dev/null; fi; }\n'
    result = execute(bash, prelude + script)
    assert (result.returncode == 0) == passes, result.stderr
    assert ('snapshot_b64=' in result.stdout) == passes


@pytest.mark.parametrize('status,output,passes', [(0, 'ok', True), (0, 'conflicting server name', False), (1, 'bad configuration', False)])
def test_ingress_check_error_triggers_rollback_before_reload(bash, tmp_path, status, output, passes):
    source = (Path(__file__).resolve().parents[2] / 'maintenance/release/apply-nginx-ingress.sh').read_text(encoding='utf-8')
    # Execute the production transaction's exact check and rollback handler.
    rollback = source[source.index('mutation_started=true'):source.index('rewrite="$site')]
    start = source.index('nginx_check=$(nginx -t')
    check = source[start:source.index('assert_nginx_ingress_policy', start)]
    helper = tmp_path / 'rollback-nginx-ingress.sh'
    helper.write_text('#!/usr/bin/env bash\nprintf "rollback_called\\n" >&2\n', encoding='utf-8', newline='\n')
    helper.chmod(0o700)
    script = 'set -Eeuo pipefail\nrelease_dir=fixture; assets_dir=' + shlex.quote(tmp_path.as_posix()) + '\n'
    script += 'nginx() { printf "%s\\n" ' + shlex.quote(output) + '; return ' + str(status) + '; }\n'
    script += 'systemctl() { printf "reload_called\\n"; }\n' + rollback + check
    result = subprocess.run([bash, '-c', script], capture_output=True, text=True, timeout=15)
    assert (result.returncode == 0) == passes, result.stderr
    assert ('reload_called' in result.stdout) == passes
    assert ('rollback_called' in result.stderr) != passes
