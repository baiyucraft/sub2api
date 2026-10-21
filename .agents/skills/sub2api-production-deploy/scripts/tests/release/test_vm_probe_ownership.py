"""Inert helper tests; real PostgreSQL runs only through the explicit .sh fixture."""
from __future__ import annotations

import os
from pathlib import Path
import re
import shutil
import subprocess

import pytest


HERE = Path(__file__).resolve().parent
VALIDATOR = HERE.parents[1] / 'release' / 'vm-validate.sh'
FIXTURE = HERE / 'vm_probe_ownership_integration.sh'


def bash_path():
    found = shutil.which('bash')
    if not found and os.name == 'nt':
        found = next((value for value in (
            'C:/Program Files/Git/bin/bash.exe',
            'C:/Program Files/Git/usr/bin/bash.exe',
        ) if Path(value).is_file()), None)
    if not found:
        pytest.skip('bash unavailable; explicit Linux fixture required')
    return found


def helper(name):
    source = VALIDATOR.read_text(encoding='utf-8')
    match = re.search(r'^' + re.escape(name) + r'\(\) \{\n.*?^\}\n', source, re.M | re.S)
    assert match, name
    return match.group()


def execute(script, *args):
    return subprocess.run([bash_path(), '--noprofile', '--norc', '-c', script, 'fixture', *args],
                          text=True, capture_output=True)


@pytest.mark.parametrize('role', ['fixture_app', '"fixture_app"', "'fixture_app'", 'fixture_app # role'])
def test_literal_application_user_matches(tmp_path, role):
    config = tmp_path / 'config.yaml'
    config.write_text(f'database:\n  host: fixture\n  user: {role}\n  pool:\n    user: ignored_nested\nredis:\n  user: ignored\n', encoding='utf-8')
    result = execute(helper('assert_probe_database_user') + '\nassert_probe_database_user "$1" fixture_app', config.as_posix())
    assert result.returncode == 0, result.stderr


@pytest.mark.parametrize('config', [
    'database:\n  user: postgres\n',
    'database:\n  host: fixture\n',
    'database:\n  nested:\n    user: fixture_app\n',
    'database:\n  user: fixture_app\n  user: postgres\n',
    'database:\n  user: fixture_app\ndatabase:\n  user: fixture_app\n',
    'database:\n  user: fixture_app\nDatabase:\n  user: postgres\n',
    'database:\n  user: fixture_app\n"database":\n  user: postgres\n',
    'database:\n  user: fixture_app\n  "user": postgres\n',
    'database:\n  user: fixture_app\n  <<: *other\n',
    'database:\n  user: *app\n',
    'database: {user: fixture_app}\n',
    'database:\n\tuser: fixture_app\n',
    'database:\n  user: fixture_app;evil\n',
])
def test_ambiguous_or_mismatched_user_fails_closed(tmp_path, config):
    path = tmp_path / 'config.yaml'
    path.write_text(config, encoding='utf-8')
    result = execute(helper('assert_probe_database_user') + '\nassert_probe_database_user "$1" fixture_app', path.as_posix())
    assert result.returncode != 0
    assert not result.stdout and not result.stderr, 'configuration contents must remain private'


def test_restore_uses_role_without_altering_shared_ownership():
    script = 'docker() { printf "<%s>\\n" "$@"; }\n' + helper('restore_probe_database')
    result = execute(script + '\nrestore_probe_database sub2api_v2_fixture fixture_app')
    assert result.returncode == 0
    assert '<DB_OWNER=fixture_app>' in result.stdout
    assert '<sh>\n<sub2api_v2_fixture>' in result.stdout
    assert '--exit-on-error --no-owner --role "$DB_OWNER"' in result.stdout
    assert 'REASSIGN' not in result.stdout


@pytest.mark.parametrize('database,role', [
    ('production', 'fixture_app'), ('sub2api_v2_fixture;evil', 'fixture_app'),
    ('sub2api_v2_fixture', 'fixture_app;evil'),
])
def test_restore_rejects_non_probe_database_or_invalid_role(database, role):
    script = 'docker() { echo unexpected_docker; }\n' + helper('restore_probe_database')
    result = execute(script + '\nrestore_probe_database "$1" "$2"', database, role)
    assert result.returncode != 0 and not result.stdout


def test_both_sources_use_current_restore_helper_and_config_guard():
    source = VALIDATOR.read_text(encoding='utf-8')
    v2 = source.split('  database_owner=$probe_role', 1)[1].split('  redis_image=', 1)[0]
    assert v2.count('restore_probe_database "$probe_db" "$database_owner"') == 2
    preparation = source.index('  prepare_probe_connection\n')
    assert preparation < source.index('  docker exec sub2api-postgres sh -lc "createdb')
    assert 'assert_probe_database_user "$probe_dir/config.yaml" "$probe_role"' in helper('prepare_probe_connection')
    assert source.count('restore_probe_database "$probe_db" "$database_owner"') == 4
    assert 'GRANT ALL PRIVILEGES' not in v2
    fixture = FIXTURE.read_text(encoding='utf-8')
    assert 'source "$work/helpers.sh"' in fixture
    assert 'CREATE EXTENSION pg_trgm' in fixture
    assert '285_upstream_null_rate_lifecycle.sql' in fixture
    assert '--network none' in fixture and '--pull never' in fixture


@pytest.mark.parametrize('path', [VALIDATOR, FIXTURE, HERE / 'migration-298-integration.sh'])
def test_shell_syntax(path):
    with path.open('rb') as source:
        result = subprocess.run([bash_path(), '-n'], stdin=source, capture_output=True)
    assert result.returncode == 0, result.stderr.decode(errors='replace')


@pytest.mark.parametrize('command', [
    'docker run --rm --network="$probe_network" --env-file "$probe_connection_env" -v "$probe_dir:/app/data" -v "$state_dir/fixture-bootstrap-before.json:/input/migration-plan.json:ro"',
    'probe_plan=$(docker run --rm --network="$probe_network" --env-file "$probe_connection_env" -v "$probe_dir:/app/data"',
])
def test_fixture_failure_preserves_exit_with_err_trap(tmp_path, command):
    source = VALIDATOR.read_text(encoding='utf-8')
    start = source.index('    if ' + command)
    end = source.index('    [[ $fixture_', start)
    block = source[start:end]
    state = tmp_path / 'state'
    state.mkdir()
    script = 'set -Eeuo pipefail\ntrap "exit 99" ERR\ndocker() { return 23; }\n'
    script += 'state_dir="$1"; probe_dir=/fixture; probe_network=fixture; candidate_image_id=fixture; probe_connection_env=/fixture.env\n'
    script += block + '\nprintf "preserved\\n"\n'
    result = execute(script, state.as_posix())
    assert result.returncode == 0 and result.stdout.strip() == 'preserved'
    exit_file = 'fixture-bootstrap-after.exit' if command.startswith('probe_plan=') else 'fixture-bootstrap.exit'
    assert (state / exit_file).read_text().strip() == '23'


def probe_cleanup_source(generation):
    source = VALIDATOR.read_text(encoding='utf-8')
    name = 'cleanup_v2' if generation == 'v2' else 'cleanup_probe'
    indent = '  ' if generation == 'v2' else ''
    match = re.search(r'^' + indent + name + r'\(\) \{\n.*?^' + indent + r'\}\n', source, re.M | re.S)
    assert match, name
    return match.group()


def probe_creation_source(generation):
    """Run the real creation/ownership pairs, with external work stubbed out."""
    source = VALIDATOR.read_text(encoding='utf-8')
    boundary = source.index('\nprobe_database_created=false\n')
    generation_source = source[:boundary] if generation == 'v2' else source[boundary:]
    if generation == 'v2':
        start = source.index('  mark_v2_stage probe_config_identity\n')
        end = source.index('  # GRANT cannot authorize', start)
    else:
        start = source.index('mark_stage isolated_database\n')
        end = source.index('restore_probe_database "$probe_db"', start)
    creation = source[start:end]
    for name in ('probe_redis', 'old_probe_app', 'probe_app'):
        pattern = r'^ *' + name + r'_id=\$\(docker create --name "\$' + name + r'".*?\n'
        pattern += r'^ *docker start "\$' + name + r'_id"[^\n]*\n'
        match = re.search(pattern, generation_source, re.M | re.S)
        assert match, (generation, name)
        creation += match.group()
    return creation


def ownership_harness(tmp_path, generation, fail_at):
    state = tmp_path / 'state'
    state.mkdir()
    log = tmp_path / 'docker.log'
    script = '''set -Eeuo pipefail
state_dir="$1"; operation_log="$2"; fail_at="$3"
probe_dir="$state_dir/probe-data"; recovery_dir="$state_dir/production-recovery"
probe_redis_data="$state_dir/probe-redis-data"; data_dir="$state_dir/source"
probe_network=foreign_network; probe_db=sub2api_probe_foreign
probe_redis=foreign_redis; old_probe_app=foreign_old; probe_app=foreign_app
probe_connection_env=fixture.env; probe_role=fixture_app; database_owner=fixture_app
candidate_image_id=fixture; old_image_id=fixture; compat_image_id=fixture; server_port=8080
recovery_gate_mode=fast; redis_image=fixture; redis_volume_args=(--tmpfs /data:rw,nosuid)
probe_database_created=false; probe_network_created=false; probe_postgres_connected=false
probe_redis_created=false; probe_app_created=false; old_probe_app_created=false
probe_redis_id=; probe_app_id=; old_probe_app_id=; activation_instance=fixture
redis_id=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
old_id=bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
app_id=cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc
mark_stage() { :; }; mark_v2_stage() { :; }; install() { :; }; cp() { :; }
prepare_probe_connection() { [[ $fail_at != auth ]]; }
docker() {
  printf '%s\\n' "$*" >> "$operation_log"
  case "$*" in
    'network create '*) [[ $fail_at != network_create ]] || return 23;;
    'network connect '*) [[ $fail_at != network_connect ]] || return 23;;
    *createdb*) [[ $fail_at != database_create ]] || return 23;;
    'create --name foreign_redis '*) [[ $fail_at != redis_create ]] || return 23; printf '%s\n' "$redis_id";;
    'create --name foreign_old '*) [[ $fail_at != old_app_create ]] || return 23; printf '%s\n' "$old_id";;
    'create --name foreign_app '*) [[ $fail_at != app_create ]] || return 23; printf '%s\n' "$app_id";;
    "start $redis_id") [[ $fail_at != redis_start ]] || return 23;;
    "start $old_id") [[ $fail_at != old_app_start ]] || return 23;;
    "start $app_id") [[ $fail_at != app_start ]] || return 23;;
  esac
}
create_probe_database() { docker exec sub2api-postgres createdb "$probe_db"; }
'''
    script += probe_cleanup_source(generation)
    script += f'\ntrap {"cleanup_v2" if generation == "v2" else "cleanup_probe"} EXIT\n'
    script += probe_creation_source(generation)
    script += '\nprintf "creation_completed\\n"\n'
    result = execute(script, state.as_posix(), log.as_posix(), fail_at)
    return result, log.read_text(encoding='utf-8').splitlines()


@pytest.mark.parametrize('generation', ['v1', 'v2'])
@pytest.mark.parametrize('fail_at,owned', [
    ('network_create', set()),
    ('network_connect', {'network'}),
    ('auth', {'network', 'connection'}),
    ('database_create', {'network', 'connection'}),
    ('redis_create', {'network', 'connection', 'database'}),
    ('redis_start', {'network', 'connection', 'database', 'redis'}),
    ('old_app_create', {'network', 'connection', 'database', 'redis'}),
    ('old_app_start', {'network', 'connection', 'database', 'redis', 'old'}),
    ('app_create', {'network', 'connection', 'database', 'redis', 'old'}),
    ('app_start', {'network', 'connection', 'database', 'redis', 'old', 'app'}),
])
def test_failed_creation_or_auth_never_removes_foreign_same_name(tmp_path, generation, fail_at, owned):
    result, operations = ownership_harness(tmp_path, generation, fail_at)
    assert result.returncode != 0 and 'creation_completed' not in result.stdout
    cleanup = '\n'.join(operations)
    assert ('network rm foreign_network' in operations) == ('network' in owned)
    assert ('network disconnect foreign_network sub2api-postgres' in operations) == ('connection' in owned)
    assert ('dropdb --if-exists' in cleanup) == ('database' in owned)
    for name, identifier in (('redis', 'a' * 64), ('old', 'b' * 64), ('app', 'c' * 64)):
        assert f'rm -f -v foreign_{name}' not in operations
        assert (f'rm -f -v {identifier}' in operations) == (name in owned)


@pytest.mark.parametrize('generation', ['v1', 'v2'])
def test_successful_creation_cleanup_removes_only_owned_containers_and_volumes(tmp_path, generation):
    result, operations = ownership_harness(tmp_path, generation, 'none')
    assert result.returncode == 0 and result.stdout.strip() == 'creation_completed', result.stderr
    for name, identifier in (('redis', 'a' * 64), ('old', 'b' * 64), ('app', 'c' * 64)):
        assert f'rm -f -v foreign_{name}' not in operations
        assert operations.count(f'rm -f -v {identifier}') == 1


@pytest.mark.parametrize('fail_at,drop_count', [('create', 1), ('restore', 2)])
def test_migration195_recreation_tracks_new_database_ownership(tmp_path, fail_at, drop_count):
    source = VALIDATOR.read_text(encoding='utf-8')
    start = source.index('  docker exec sub2api-postgres sh -lc "dropdb -U')
    end = source.index('  {\n', start)
    block = source[start:end]
    state = tmp_path / 'state'
    state.mkdir()
    (state / 'probe.dump').write_bytes(b'fixture')
    log = tmp_path / 'docker.log'
    script = '''set -Eeuo pipefail
state_dir="$1"; operation_log="$2"; fail_at="$3"
probe_dir="$state_dir/probe-data"; probe_db=sub2api_probe_fixture; database_owner=fixture_app
probe_database_created=true; probe_network_created=false; probe_postgres_connected=false
probe_redis_created=false; probe_app_created=false; old_probe_app_created=false
docker() { printf '%s\\n' "$*" >> "$operation_log"; }
create_probe_database() { [[ $fail_at != create ]]; }
restore_probe_database() { return 23; }
'''
    script += probe_cleanup_source('v1') + '\ntrap cleanup_probe EXIT\n' + block
    result = execute(script, state.as_posix(), log.as_posix(), fail_at)
    assert result.returncode != 0
    assert log.read_text(encoding='utf-8').count('dropdb') == drop_count


def test_one_shot_clients_and_unbound_redis_do_not_create_anonymous_volumes():
    source = VALIDATOR.read_text(encoding='utf-8')
    assert '--tmpfs /var/lib/postgresql:rw,nosuid' in helper('prepare_probe_connection')
    assert 'redis_volume_args=(--tmpfs /data:rw,nosuid)' in source
    assert 'redis_volume_args=(-v "$probe_redis_data:/data")' in source
    legacy = source.split('mark_stage isolated_redis\n', 1)[1].split('mark_stage migrate_candidate\n', 1)[0]
    assert '--tmpfs /data:rw,nosuid' in legacy
