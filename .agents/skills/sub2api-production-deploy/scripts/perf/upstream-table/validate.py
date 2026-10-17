"""Run opt-in equivalence checks using disposable local-VM PostgreSQL/Redis.

Build first from backend with Go 1.27.2:
  GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go test -tags upstreamtablepg -c \
    -o ../.tmp/upstream-table-pg-linux.test ./internal/repository
No application or existing development database is modified.
"""
from __future__ import annotations

import argparse
import hashlib
import json
import pathlib
import re
import secrets
import shlex
import sys

ROOT = pathlib.Path(__file__).resolve().parents[6]
sys.path.insert(0, str(ROOT / '.agents/skills/sub2api-production-deploy/scripts'))
from release.ssh import SSHRunner


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--binary', default='.tmp/upstream-table-pg-linux.test')
    parser.add_argument('--output', default='.tmp/upstream-table-pg-validation')
    parser.add_argument('--explain', action='store_true', help='record exact synthetic SQL plans; requires frozen baseline from prepare.py')
    parser.add_argument('--pre-dedup-sql', help='optional frozen intermediate confidence SQL for same-fact regression diagnosis')
    args = parser.parse_args()
    output = ROOT / args.output
    output.mkdir(parents=True, exist_ok=True)
    binary = ROOT / args.binary
    migration = ROOT / 'backend/migrations/300_upstream_health_history_index.sql'
    runner = SSHRunner()
    identifier = 'upstream_table_test_' + secrets.token_hex(6)
    password = secrets.token_hex(24)
    redis_name = 'codex-' + identifier.replace('_', '-')
    remote = runner.create_temp_dir('local_vm', '/tmp', 'codex-upstream-table-validation')
    result = {'binary_sha256': hashlib.sha256(binary.read_bytes()).hexdigest(),
              'migration_sha256': hashlib.sha256(migration.read_bytes()).hexdigest(),
              'owned_database': identifier, 'owned_role': identifier, 'owned_redis': redis_name,
              'remote_temp': remote, 'scope': 'disposable synthetic resources on local_vm only', 'tests': {}, 'cleanup': {}}
    result['test_source_sha256'] = {
        p.name: hashlib.sha256(p.read_bytes()).hexdigest()
        for p in (ROOT / 'backend/internal/repository').glob('*upstream*pg_test.go')
    }
    for name in ['upstream_health_postgres_equivalence_test.go', 'usage_log_repo_quality_single_pass_pg_test.go', 'upstream_table_proxy_redis_test.go']:
        source = ROOT / 'backend/internal/repository' / name
        result['test_source_sha256'][name] = hashlib.sha256(source.read_bytes()).hexdigest()
    (output / 'result.json').write_text(json.dumps(result, indent=2) + '\n')
    client = None
    try:
        setup = r'''set -eu
network=$(docker inspect sub2api-postgres --format '{{range $key, $value := .NetworkSettings.Networks}}{{println $key}}{{end}}' | head -n 1)
test -n "$network"
docker exec -e TEST_ID=__ID__ -e TEST_PASSWORD=__PASSWORD__ sub2api-postgres sh -c 'export PGHOST=/var/run/postgresql PGUSER="${POSTGRES_USER:-sub2api}" PGPASSWORD="${POSTGRES_PASSWORD:-}"; psql -q -v ON_ERROR_STOP=1 -d postgres -c "CREATE ROLE $TEST_ID LOGIN PASSWORD '\''$TEST_PASSWORD'\'';"; createdb --maintenance-db=postgres -O "$TEST_ID" "$TEST_ID"' >/dev/null
docker run -d --name __REDIS__ --label codex.task=upstream-table-validation --network "$network" --memory 128m redis:8-alpine redis-server --save '' --appendonly no >/dev/null
printf 'pg_ip=%s\n' "$(docker inspect sub2api-postgres --format '{{range .NetworkSettings.Networks}}{{println .IPAddress}}{{end}}' | head -n 1)"
printf 'redis_ip=%s\n' "$(docker inspect __REDIS__ --format '{{range .NetworkSettings.Networks}}{{println .IPAddress}}{{end}}' | head -n 1)"
printf 'go_available=%s\n' "$(command -v go >/dev/null && echo true || echo false)"
printf 'gcc_available=%s\n' "$(command -v gcc >/dev/null && echo true || echo false)"
'''
        for key, value in {'__ID__': identifier, '__PASSWORD__': password, '__REDIS__': redis_name}.items():
            setup = setup.replace(key, shlex.quote(value))
        values = runner.run('local_vm', setup, {'pg_ip', 'redis_ip', 'go_available', 'gcc_available'}, timeout=60).values
        result['race_prerequisites'] = {key: values[key] for key in ['go_available', 'gcc_available']}
        client = runner.connect('local_vm')
        sftp = client.open_sftp()
        sftp.put(str(binary), remote + '/repository.test')
        sftp.chmod(remote + '/repository.test', 0o700)
        sftp.put(str(migration), remote + '/300.sql')
        dsn = f"postgres://{identifier}:{password}@{values['pg_ip']}:5432/{identifier}?sslmode=disable&TimeZone=UTC"
        environment = {'SUB2API_UPSTREAM_TABLE_TEST_DSN': dsn, 'SUB2API_HEALTH_TEST_DATABASE_URL': dsn,
                       'SUB2API_UPSTREAM_TABLE_TEST_ISOLATED': '1', 'SUB2API_MIGRATION300_PATH': remote + '/300.sql',
                       'SUB2API_UPSTREAM_TABLE_TEST_REDIS': f"redis://{values['redis_ip']}:6379/0", 'GOMAXPROCS': '2'}
        if args.explain:
            environment['SUB2API_UPSTREAM_TABLE_EXPLAIN_DIR'] = remote + '/explain'
            for version, tree in [('baseline', ROOT / '.tmp/upstream-table-perf/baseline/backend'), ('optimized', ROOT / 'backend')]:
                source = tree / 'internal/repository/upstream_health_observation_repo.go'
                match = re.search(r'listPersistedUpstreamHealthHistories.*?QueryContext\(ctx, `(.+?)`', source.read_text(encoding='utf-8'), re.S)
                if not match:
                    raise RuntimeError('history query extraction failed: ' + version)
                environment['SUB2API_HEALTH_HISTORY_' + version + '_SQL'] = match.group(1)
            if args.pre_dedup_sql:
                frozen = ROOT / args.pre_dedup_sql
                environment['SUB2API_HEALTH_CONFIDENCE_BEFORE_DEDUP_SQL'] = frozen.read_text(encoding='utf-8')
                result['pre_dedup_sql_sha256'] = hashlib.sha256(frozen.read_bytes()).hexdigest()
        for name in ['TestUpstreamHealthPostgresExactEquivalence', 'TestAccountQualitySinglePassPostgresMatchesFrozenSQL',
                     'TestUpstreamTableMigration300Compatibility', 'TestUpstreamTableProxyPageBatchRedis']:
            log = remote + '/' + name + '.log'
            assignments = ' '.join(k + '=' + shlex.quote(v) for k, v in environment.items())
            command = assignments + ' ' + shlex.quote(remote + '/repository.test') + ' -test.v -test.run ' + shlex.quote('^' + name + '$') + ' -test.timeout 120s >' + shlex.quote(log) + ' 2>&1; code=$?; printf "exit_code=%s\n" "$code"'
            status = runner.run('local_vm', command, {'exit_code'}, timeout=140).values
            sftp.get(log, str(output / (name + '.log')))
            content = (output / (name + '.log')).read_text(encoding='utf-8')
            ran = '=== RUN   ' + name in content and '--- SKIP:' not in content
            result['tests'][name] = {'exit_code': int(status['exit_code']), 'ran': ran,
                                      'status': 'pass' if status['exit_code'] == '0' and ran else 'fail'}
            print(json.dumps({'test': name, **result['tests'][name]}), flush=True)
            (output / 'result.json').write_text(json.dumps(result, indent=2) + '\n')
        if args.explain:
            plans = output / 'explain'
            plans.mkdir(exist_ok=True)
            for name in sftp.listdir(remote + '/explain'):
                sftp.get(remote + '/explain/' + name, str(plans / name))
            result['explain_artifacts'] = sorted(p.name for p in plans.glob('*.json'))
    finally:
        if client:
            client.close()
        cleanup = r'''set -eu
docker rm -f __REDIS__ >/dev/null 2>&1 || true
docker exec -e TEST_ID=__ID__ sub2api-postgres sh -c 'export PGHOST=/var/run/postgresql PGUSER="${POSTGRES_USER:-sub2api}" PGPASSWORD="${POSTGRES_PASSWORD:-}"; dropdb --if-exists --force --maintenance-db=postgres "$TEST_ID"; dropuser --if-exists "$TEST_ID"' >__REMOTE__/cleanup.log 2>&1
test -z "$(docker ps -aq --filter name="^/__REDIS_RAW__$")"
printf 'redis_absent=true\n'
docker exec sub2api-postgres sh -c __CHECK__
test -d __REMOTE__ && test ! -L __REMOTE__ && rm -r -- __REMOTE__
test ! -e __REMOTE__
printf 'temp_absent=true\n'
'''
        check = 'export PGHOST=/var/run/postgresql PGUSER="${POSTGRES_USER:-sub2api}" PGPASSWORD="${POSTGRES_PASSWORD:-}"; psql -qAt -d postgres -c "SELECT \'database_absent=\'||(NOT EXISTS(SELECT 1 FROM pg_database WHERE datname=\'' + identifier + '\'))::text; SELECT \'role_absent=\'||(NOT EXISTS(SELECT 1 FROM pg_roles WHERE rolname=\'' + identifier + '\'))::text;"'
        for key, value in {'__REDIS__': shlex.quote(redis_name), '__REDIS_RAW__': redis_name,
                           '__ID__': shlex.quote(identifier), '__CHECK__': shlex.quote(check), '__REMOTE__': shlex.quote(remote)}.items():
            cleanup = cleanup.replace(key, value)
        result['cleanup'] = runner.run('local_vm', cleanup, {'redis_absent', 'database_absent', 'role_absent', 'temp_absent'}, timeout=60).values
        (output / 'result.json').write_text(json.dumps(result, indent=2) + '\n')
        print(json.dumps({'cleanup': result['cleanup']}), flush=True)
    if len(result['tests']) != 4 or any(t['status'] != 'pass' for t in result['tests'].values()) or any(v != 'true' for v in result['cleanup'].values()):
        raise SystemExit(1)


if __name__ == '__main__':
    main()
