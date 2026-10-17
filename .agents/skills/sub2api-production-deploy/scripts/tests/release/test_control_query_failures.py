"""Execute detached-worker control scripts against private, inert fixtures."""
from pathlib import Path
import ast
import os
import shlex
import shutil
import sys

import pytest

sys.path.insert(0, str(Path(__file__).resolve().parents[2]))
from release import vm_validate
from release.process import run_hidden


@pytest.fixture
def bash():
    bundled = Path('C:/Program Files/Git/bin/bash.exe')
    value = str(bundled) if bundled.is_file() else shutil.which('bash')
    if not value:
        pytest.skip('Bash is required for control-script fault injection')
    return value


def shell_path(path):
    value = path.resolve().as_posix()
    return '/' + value[0].lower() + value[2:] if os.name == 'nt' else value


@pytest.mark.parametrize('failure', ['', 'stat', 'tr'])
def test_poll_rejects_successful_stdout_with_failed_metadata_query(bash, tmp_path, failure):
    root = tmp_path / 'worker-root'
    worker = root / 'worker'
    worker.mkdir(parents=True)
    for name, value in {'pid': '0', 'boot_id': 'fixture', 'start_token': '1',
                        'status': 'exited', 'exit_code': '0'}.items():
        (worker / name).write_text(value + '\n', encoding='utf-8')
    raw_root = tmp_path / 'logs'
    raw_root.mkdir()
    (raw_root / 'vm-validate.raw.log').write_text('fixture\n', encoding='utf-8')
    script = vm_validate._poll_vm_worker_script(
        remote_root=shell_path(root), remote_output=shell_path(tmp_path / 'gate/output'),
        release_id='273-fixture')
    script = script.replace('/opt/sub2api-deploy/release-logs/273-fixture', shell_path(raw_root))
    stubs = '''
stat() {
  case "$2" in
    '%u:%g:%a:%h') printf '0:0:600:1\n' ;;
    '%u:%g:%a') printf '0:0:700\n' ;;
    '%s') printf '8\n' ;;
    *) return 99 ;;
  esac
  [[ $INJECT_QUERY_FAILURE != stat ]] || return 17
}
tr() { command tr "$@"; local rc=$?; [[ $INJECT_QUERY_FAILURE != tr ]] || return 17; return "$rc"; }
'''
    env = os.environ.copy()
    env.pop('BASH_ENV', None); env.pop('ENV', None)
    env['INJECT_QUERY_FAILURE'] = failure
    result = run_hidden([bash, '--noprofile', '--norc', '-s'], input=stubs + script,
                        text=True, capture_output=True, env=env, timeout=15)
    assert result.returncode == 0, result.stderr
    values = dict(line.split('=', 1) for line in result.stdout.splitlines())
    assert values['worker_status'] == ('invalid' if failure else 'exited')
    assert values['raw_log_status'] == ('invalid' if failure == 'stat' else 'ok')


@pytest.mark.parametrize('failure', ['', 'stat'])
def test_failed_metadata_never_launches_detached_validator(bash, tmp_path, failure):
    root = tmp_path / 'launch'
    (root / 'worker').mkdir(parents=True)
    raw_root = tmp_path / 'logs'
    raw_root.mkdir()
    (raw_root / 'vm-validate.raw.log').touch()
    sentinel = tmp_path / 'launched'
    script = vm_validate._remote_vm_worker_script(
        remote_root=shell_path(root), remote_manifest=shell_path(root / 'manifest'),
        remote_output=shell_path(tmp_path / 'gate/output'),
        remote_space_cleaner=shell_path(root / 'cleaner'),
        prebuild_cleanup_applied=False, validator=shell_path(root / 'validator'),
        release_id='273-fixture')
    script = script.replace('/opt/sub2api-deploy/release-logs/273-fixture', shell_path(raw_root))
    stubs = '''
install() { :; }
stat() {
  case "$2" in
    '%u:%g:%a') printf '0:0:700\n' ;;
    '%u:%g:%a:%h')
      case "$3" in */run-validator.sh) printf '0:0:700:1\n' ;; *) printf '0:0:600:1\n' ;; esac ;;
    *) return 99 ;;
  esac
  [[ $INJECT_QUERY_FAILURE != stat ]] || return 17
}
nohup() { printf 'launched\n' > "$LAUNCH_SENTINEL"; }
setsid() { return 98; }
'''
    env = os.environ.copy()
    env.pop('BASH_ENV', None); env.pop('ENV', None)
    env.update(INJECT_QUERY_FAILURE=failure, LAUNCH_SENTINEL=shell_path(sentinel))
    result = run_hidden([bash, '--noprofile', '--norc', '-s'], input=stubs + script + '\nwait\n',
                        text=True, capture_output=True, env=env, timeout=15)
    assert (result.returncode == 0) == (not failure), result.stderr
    assert sentinel.exists() == (not failure)
    assert ('worker_started=true' in result.stdout) == (not failure)


@pytest.mark.parametrize('module,kind,failure', [
    ('full_recovery_drill.py', 'isolation', ''),
    ('full_recovery_drill.py', 'isolation', 'source'),
    ('recovery_drill.py', 'isolation', 'source'),
    ('full_recovery_drill.py', 'compose', ''),
    ('full_recovery_drill.py', 'compose', 'source'),
    ('full_recovery_drill.py', 'compose', 'load'),
])
def test_dr_source_and_load_failures_stop_before_final_proof(bash, tmp_path, module, kind, failure):
    path = Path(__file__).resolve().parents[2] / 'release' / module
    tree = ast.parse(path.read_text(encoding='utf-8'))
    marker = 'recovery-isolation.sh' if kind == 'isolation' else 'recovery-compose.sh'
    candidates = [n.value for n in ast.walk(tree) if isinstance(n, ast.Constant)
                  and isinstance(n.value, str) and n.value.startswith('set -Eeuo pipefail; source ')
                  and marker in n.value]
    assert len(candidates) == 1
    helper = tmp_path / marker
    helper.write_text('''
recovery_drill_assert_isolated() { printf 'final_proof=true\n'; }
release_compose_args=()
load_release_compose_files() { [[ $INJECT_QUERY_FAILURE != load ]] || return 17; }
_recovery_compose_render_sha256() { printf 'final_proof=true\n'; }
[[ $INJECT_QUERY_FAILURE != source ]] || return 17
''', encoding='utf-8', newline='\n')
    env = os.environ.copy()
    env.pop('BASH_ENV', None); env.pop('ENV', None)
    env['INJECT_QUERY_FAILURE'] = failure
    result = run_hidden([bash, '--noprofile', '--norc', '-c', candidates[0], 'fixture', shell_path(tmp_path), 'checksum'],
                        text=True, capture_output=True, env=env, timeout=15)
    assert (result.returncode == 0) == (not failure), result.stderr
    assert ('final_proof=true' in result.stdout) == (not failure)


@pytest.mark.parametrize('failed_link', ['', 'gate.json', 'gate.sig', 'candidate.tar.gz', 'SHA256SUMS'])
def test_vm_only_failed_early_link_never_reports_download_ready(bash, failed_link):
    path = Path(__file__).resolve().parents[2] / 'release/vm_only.py'
    tree = ast.parse(path.read_text(encoding='utf-8'))
    node = next(n for n in ast.walk(tree) if isinstance(n, ast.JoinedStr)
                and any(isinstance(v, ast.Constant) and isinstance(v.value, str)
                        and 'download_ready=true' in v.value for v in n.values))
    shell = eval(compile(ast.Expression(node), str(path), 'eval'),
                 {'shlex': shlex, 'download_dir': '/inert/download', 'remote_output': '/inert/gate'})
    prelude = '''
install() { :; }
ln() { [[ ${1##*/} != "$INJECT_FAILED_LINK" ]] || return 17; }
'''
    env = os.environ.copy()
    env.pop('BASH_ENV', None); env.pop('ENV', None)
    env['INJECT_FAILED_LINK'] = failed_link
    result = run_hidden([bash, '--noprofile', '--norc', '-s'], input=prelude + shell,
                        text=True, capture_output=True, env=env, timeout=15)
    assert (result.returncode == 0) == (not failed_link), result.stderr
    assert ('download_ready=true' in result.stdout) == (not failed_link)


@pytest.mark.parametrize('state,code,expected', [('inactive', 3, True), ('failed', 3, True),
                                              ('active', 0, False), ('inactive', 17, False),
                                              ('', 17, False)])
def test_ingress_apply_rejects_unknown_backup_service_before_transaction(bash, state, code, expected):
    path = Path(__file__).resolve().parents[2] / 'maintenance/release/apply-nginx-ingress.sh'
    source = path.read_text(encoding='utf-8')
    start = source.index('backup_service_code=0')
    end = source.index('[[ -f $state_dir/backup-result', start)
    shell = 'set -Eeuo pipefail\nsystemctl() { printf "%s\\n" "$INJECT_SERVICE_STATE"; return "$INJECT_SERVICE_CODE"; }\n'
    shell += source[start:end] + '\nprintf "transaction_allowed=true\\n"\n'
    env = os.environ.copy()
    env.pop('BASH_ENV', None); env.pop('ENV', None)
    env.update(INJECT_SERVICE_STATE=state, INJECT_SERVICE_CODE=str(code))
    result = run_hidden([bash, '--noprofile', '--norc', '-s'], input=shell,
                        text=True, capture_output=True, env=env, timeout=15)
    assert (result.returncode == 0) == expected
    assert ('transaction_allowed=true' in result.stdout) == expected
