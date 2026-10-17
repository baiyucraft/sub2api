from __future__ import annotations

import shutil
import subprocess
import re
from pathlib import Path

import pytest

from vm_errexit_family_integration import ROOT, shell_function


@pytest.fixture
def bash():
    bundled = Path('C:/Program Files/Git/bin/bash.exe')
    value = str(bundled) if bundled.is_file() else shutil.which('bash')
    if not value:
        pytest.skip('Bash is required for actual shell fault injection')
    return value


@pytest.mark.parametrize('failure', ['idle', 'marker', 'services', 'inspect', 'remove', 'list', 'retained', 'none'])
def test_image_removal_formal_entry_propagates_failure(bash, failure):
    source = (ROOT / 'release/production-space-clean.sh').read_text()
    function = shell_function(source, 'remove_image_candidates')
    start = source.index('if [[ $mode == apply ]]; then\n  [[ $plan_sha256 ==')
    end = source.index('\nif [[ $mode == apply ]]; then\n  write_candidate_archive_records', start)
    guards = '\n'.join('assert_release_' + name + '() { ' + ('return 70;' if failure == name else ':;') + ' }' for name in ('idle', 'marker'))
    script = '''set -Eeuo pipefail
work_dir=$(mktemp -d)
trap 'rm -rf -- "$work_dir"' EXIT
mode=apply; plan_sha256=x; expected_plan_sha256=x; build_cache_records_before=0
remove_candidate_archives() { printf '0 0\n'; }
list_image_candidates() { printf 'sha256:fixture\n'; }
write_protected_images() { :; }
assert_release_identity() { :; }
'''+guards+'\nassert_services() { '+('return 70;' if failure=='services' else ':;')+' }\n'+'''
docker() {
  case "$1 $2" in
    'image inspect') [[ $failure != inspect ]] || return 70; printf 'sub2api:273-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n' ;;
    'image rm') [[ $failure != remove ]] || return 70; printf 'mutation=true\n' >&2 ;;
    'image ls') [[ $failure != list ]] || return 70; [[ $failure != retained ]] || printf 'sha256:fixture\n' ;;
    *) return 97 ;;
  esac
}
'''.replace('$failure', failure)+'\n'+function+'\n'+source[start:end]+"\nprintf 'success=true\\n'\n"
    result = subprocess.run([bash, '-c', script], capture_output=True, text=True, timeout=15)
    assert (result.returncode == 0) == (failure == 'none')
    assert ('success=true' in result.stdout) == (failure == 'none')
    if failure in ('idle', 'marker', 'services', 'inspect'):
        assert 'mutation=true' not in result.stderr


def test_process_collection_failure_returns_unknown(bash):
    source = (ROOT / 'maintenance/release/context.sh').read_text()
    functions = '\n'.join(shell_function(source, name) for name in ('nginx_draining_worker_count', 'wait_for_application_drain'))
    script = 'set -Eeuo pipefail\nps() { return 70; }\ndocker() { return 0; }\napplication_connection_count() { printf "0\\n"; }\n'+functions+'\nvalue=$(wait_for_application_drain fixture 0)\n[[ $value == unknown ]]'
    assert subprocess.run([bash, '-c', script], capture_output=True, timeout=15).returncode == 0


@pytest.mark.parametrize('result,expected', [(70, False), (0, True)])
def test_log_collection_status_is_required(bash, result, expected):
    function = shell_function((ROOT / 'maintenance/release/context.sh').read_text(), 'critical_startup_log_count')
    script = f'set -Eeuo pipefail\ndocker() {{ printf "startup ready\\n"; return {result}; }}\n'+function+'\nvalue=$(critical_startup_log_count fixture 5m)\n[[ $value == 0 ]]\nprintf "success=true\\n"'
    outcome = subprocess.run([bash, '-c', script], capture_output=True, text=True, timeout=15)
    assert (outcome.returncode == 0) == expected
    assert ('success=true' in outcome.stdout) == expected


def test_production_cleaner_required_producers_cannot_hide_nonzero(bash):
    source = (ROOT / 'release/production-space-clean.sh').read_text()
    captures = re.findall(r'^\s*required_value=\$\(.*\)$', source, re.M)
    assert len(captures) >= 15
    stubs = '\n'.join(name+'() { printf "expected-output\\n"; return 70; }' for name in ('ps','awk','tr','docker','systemctl','curl','stat'))
    for capture in captures:
        script='set -Eeuo pipefail\nactive_container=fixture; pre_switch_image=fixture; active_port=18080; release_state_dir=/tmp; path=/tmp\n'+stubs+'\n'+capture+'\nprintf "unsafe=true\\n"\n'
        outcome=subprocess.run([bash,'-c',script],capture_output=True,text=True,timeout=15)
        assert outcome.returncode != 0
        assert 'unsafe=true' not in outcome.stdout


def test_protected_image_pipeline_rejects_partial_container_inventory(bash):
    source = (ROOT / 'release/production-space-clean.sh').read_text()
    functions = '\n'.join(shell_function(source,name) for name in ('write_container_images','write_protected_images'))
    script='set -Eeuo pipefail\nexpected_current_image=current; pre_switch_image=previous\ndocker() { printf "partial-image\\n"; return 70; }\n'+functions+'\nwrite_protected_images | sort\nprintf "unsafe=true\\n"\n'
    outcome=subprocess.run([bash,'-c',script],capture_output=True,text=True,timeout=15)
    assert outcome.returncode != 0
    assert 'unsafe=true' not in outcome.stdout
