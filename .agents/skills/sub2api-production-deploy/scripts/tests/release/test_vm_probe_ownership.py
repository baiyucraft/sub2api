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
    v2 = source.split('  database_owner=', 1)[1].split('  docker network create', 1)[0]
    assert v2.count('restore_probe_database "$probe_db" "$database_owner"') == 2
    assert v2.index('assert_probe_database_user') < v2.index('createdb')
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
    'docker run --rm --network="$probe_network" -v "$probe_dir:/app/data" -v "$state_dir/fixture-bootstrap-before.json:/input/migration-plan.json:ro"',
    'probe_plan=$(docker run --rm --network="$probe_network" -v "$probe_dir:/app/data"',
])
def test_fixture_failure_preserves_exit_with_err_trap(tmp_path, command):
    source = VALIDATOR.read_text(encoding='utf-8')
    start = source.index('    if ' + command)
    end = source.index('    [[ $fixture_', start)
    block = source[start:end]
    state = tmp_path / 'state'
    state.mkdir()
    script = 'set -Eeuo pipefail\ntrap "exit 99" ERR\ndocker() { return 23; }\n'
    script += 'state_dir="$1"; probe_dir=/fixture; probe_network=fixture; candidate_image_id=fixture\n'
    script += block + '\nprintf "preserved\\n"\n'
    result = execute(script, state.as_posix())
    assert result.returncode == 0 and result.stdout.strip() == 'preserved'
    exit_file = 'fixture-bootstrap-after.exit' if command.startswith('probe_plan=') else 'fixture-bootstrap.exit'
    assert (state / exit_file).read_text().strip() == '23'
