#!/usr/bin/env bash
# Explicit Linux fixture: private SCRAM PostgreSQL, synthetic data, no host DB.
set -Eeuo pipefail
umask 077
python3 -B - <<'PY'
import hashlib, importlib.util, json, os, pathlib, re, secrets, shutil, subprocess, tempfile, time

root = pathlib.Path(os.environ['TEST_SOURCE_ROOT'])
image = os.environ['TEST_POSTGRES_IMAGE']
redis_image = os.environ['TEST_REDIS_IMAGE']
candidate = os.environ['TEST_CANDIDATE_IMAGE']
old = os.environ['TEST_OLD_IMAGE']
for value in (image, redis_image, candidate, old):
    assert re.fullmatch(r'sha256:[0-9a-f]{64}', value)
helper = root / '.agents/skills/sub2api-production-deploy/scripts/release/vm-probe-config.py'
validator = root / '.agents/skills/sub2api-production-deploy/scripts/release/vm-validate.sh'
spec = importlib.util.spec_from_file_location('fixture_probe_config', helper)
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)
work = pathlib.Path(tempfile.mkdtemp(prefix='sub2api-probe-config-'))
work.chmod(0o700)
suffix = secrets.token_hex(6)
resource_label = 'io.sub2api.probe-config-fixture=' + suffix
network, pg, redis = ('sub2api-pcfg-' + suffix + '-' + kind for kind in ('net', 'pg', 'redis'))
containers, network_created, completed, stage = [], False, False, 'initializing'
passwords = {key: secrets.token_hex(24) for key in ('admin', 'vm', 'prod')}
special_password = "space # = quote' double\" dollar$ backtick` slash\\end"
names = {mode: 'sub2api_v2_' + suffix + '_' + mode for mode in ('fast', 'specialized', 'full', 'wrong')}
counter = 0

def run(argv, data=None, check=True):
    global counter
    counter += 1
    p = subprocess.run(argv, input=data, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=600)
    (work / ('command-%04d.stdout' % counter)).write_bytes(p.stdout)
    (work / ('command-%04d.stderr' % counter)).write_bytes(p.stderr)
    if check and p.returncode:
        raise RuntimeError('command_failed_%04d' % counter)
    return p

def docker(*args, **kwargs):
    if args and args[0] == 'run' and '--name' not in args:
        args = ('run', '--name', 'sub2api-pcfg-' + suffix + '-oneshot-' + str(counter + 1), '--label', resource_label, *args[1:])
    return run(['docker', *map(str, args)], **kwargs)

def remove_owned_container(name):
    found = docker('container', 'ls', '-aq', '--filter', 'name=^/' + name + '$').stdout.decode().splitlines()
    if not found:
        return
    assert len(found) == 1
    identity = found[0]
    metadata = json.loads(docker('container', 'inspect', identity).stdout)[0]
    assert metadata['Name'] == '/' + name and metadata['Config']['Labels'].get(resource_label.split('=', 1)[0]) == suffix
    docker('rm', '-f', '-v', metadata['Id'])

def remove_owned_network():
    found = docker('network', 'ls', '-q', '--filter', 'name=^' + network + '$', '--filter', 'label=' + resource_label).stdout.decode().splitlines()
    if not found:
        return
    assert len(found) == 1
    metadata = json.loads(docker('network', 'inspect', found[0]).stdout)[0]
    assert metadata['Name'] == network and metadata['Labels'].get(resource_label.split('=', 1)[0]) == suffix
    docker('network', 'rm', metadata['Id'])

def remove_all_owned_containers():
    key, value = resource_label.split('=', 1)
    ids = docker('container', 'ls', '-aq', '--filter', 'label=' + key + '=' + value).stdout.decode().splitlines()
    failures = False
    for identity in ids:
        try:
            metadata = json.loads(docker('container', 'inspect', identity).stdout)[0]
            assert metadata['Config']['Labels'].get(key) == value
            assert metadata['Name'].startswith('/sub2api-pcfg-' + suffix + '-')
            docker('rm', '-f', '-v', metadata['Id'])
        except Exception:
            failures = True
    assert not failures
    assert not docker('container', 'ls', '-aq', '--filter', 'label=' + resource_label).stdout.strip()

def sql(db, text, role='postgres'):
    return docker('exec', '-i', pg, 'psql', '-X', '-q', '-A', '-t', '-v', 'ON_ERROR_STOP=1', '-U', role, '-d', db, data=text.encode()).stdout.decode().strip()

def envfile(path, fields):
    path.write_text(''.join(k + '=' + v + '\n' for k, v in fields.items()))
    path.chmod(0o600)

def config(path, role, password, db, host='sub2api-postgres'):
    path.parent.mkdir(mode=0o700, exist_ok=True)
    path.write_text('server:\n  host: 0.0.0.0\n  port: 8080\n  mode: release\n'
        + 'database:\n  host: ' + host + '\n  port: 5432\n  user: ' + role
        + '\n  password: ' + json.dumps(password) + '\n  dbname: ' + db + '\n  sslmode: disable\n  max_open_conns: 7\n  max_idle_conns: 3\n'
        + 'redis:\n  host: probe-redis\n  port: 6379\n  username: ""\n  password: ""\n  db: 0\n  enable_tls: false\n  pool_size: 9\n  min_idle_conns: 2\n'
        + 'jwt:\n  secret: ' + secrets.token_hex(32) + '\ntimezone: Asia/Shanghai\n')
    path.chmod(0o600)

def app(data, connection, *args):
    mounts = []
    if args and args[0] == '--migration-apply-plan-json':
        mounts = ['-v', str(work / pathlib.Path(args[1]).name) + ':/input/migration-plan.json:ro']
        args = ('--migration-apply-plan-json', '/input/migration-plan.json')
    return docker('run', '--rm', '--pull', 'never', '--network', network, '--env-file', connection,
        '-e', 'UPSTREAM_SYNC_AUTO_ENABLED=false', '-v', str(data) + ':/app/data',
        *mounts, candidate, '/app/sub2api', *args)

def plan_apply(data, connection, label):
    before = json.loads(app(data, connection, '--migration-plan-json').stdout)
    assert not before['conflicts'] and not before['unknown'] and before['existing_checksums_verified']
    path = work / (label + '-plan.json')
    path.write_text(json.dumps(before))
    path.chmod(0o644)  # Synthetic migration metadata only; parent remains private.
    app(data, connection, '--migration-apply-plan-json', '/fixture/' + path.name)
    after = json.loads(app(data, connection, '--migration-plan-json').stdout)
    assert not after['pending'] and not after['conflicts'] and not after['unknown'] and after['existing_checksums_verified']
    return before

def fingerprint(db):
    raw = docker('exec', pg, 'pg_dump', '--no-owner', '--no-privileges', '-U', 'postgres', db).stdout
    raw = b'\n'.join(line for line in raw.splitlines() if not line.startswith((b'--', b'\\restrict ', b'\\unrestrict ')))
    return hashlib.sha256(raw).hexdigest()

def precheck(connection, expected_role='fixture_vm'):
    result = docker('run', '--rm', '--pull', 'never', '--network', network, '--env-file', connection / 'postgres.env',
        '--entrypoint', 'psql', image, '-X', '-w', '-A', '-t', '-F', '|', '-v', 'ON_ERROR_STOP=1',
        '-c', 'SELECT current_user,current_database()', check=False)
    return result.returncode == 0 and result.stdout.decode().strip() == expected_role + '|sub2api_dev'

def health(data, connection, image_id, label):
    name = 'sub2api-pcfg-' + suffix + '-' + label
    docker('run', '-d', '--pull', 'never', '--name', name, '--network', network,
        '--label', resource_label,
        '--env-file', connection, '-e', 'UPSTREAM_SYNC_AUTO_ENABLED=false', '-e', 'SERVER_HOST=0.0.0.0',
        '-v', str(data) + ':/app/data', '--health-cmd', 'wget -q -T 5 -O /dev/null http://127.0.0.1:8080/health || exit 1',
        '--health-interval', '2s', '--health-timeout', '5s', '--health-start-period', '3s', '--health-retries', '30', image_id)
    containers.append(name)
    deadline = time.monotonic() + 90
    while time.monotonic() < deadline:
        state = docker('inspect', '-f', '{{.State.Status}}|{{.State.Health.Status}}|{{.Image}}', name).stdout.decode().strip()
        if state == 'running|healthy|' + image_id:
            docker('stop', '-t', '20', name)
            remove_owned_container(name)
            containers.remove(name)
            return
        if not state.startswith('running|'):
            break
        time.sleep(2)
    docker('logs', '--tail', '100', name, check=False)
    raise RuntimeError('health_failed')

try:
    stage = 'private_environment'
    for image_id in (image, redis_image, candidate, old):
        assert docker('image', 'inspect', '-f', '{{.Id}}', image_id).stdout.decode().strip() == image_id
    docker('network', 'create', '--internal', '--label', resource_label, network)
    network_created = True
    pg_env = work / 'postgres-start.env'
    envfile(pg_env, {'POSTGRES_PASSWORD':passwords['admin'], 'POSTGRES_INITDB_ARGS':'--auth-host=scram-sha-256'})
    docker('run', '-d', '--pull', 'never', '--name', pg, '--network', network, '--network-alias', 'sub2api-postgres',
        '--label', resource_label,
        '--env-file', pg_env, '--tmpfs', '/var/lib/postgresql:rw,nosuid', image)
    containers.append(pg)
    deadline = time.monotonic() + 60
    while time.monotonic() < deadline:
        if docker('exec', pg, 'pg_isready', '-h', '127.0.0.1', '-U', 'postgres', check=False).returncode == 0:
            break
        time.sleep(1)
    else:
        raise RuntimeError('postgres_not_ready')
    docker('run', '-d', '--pull', 'never', '--name', redis, '--network', network, '--network-alias', 'probe-redis',
        '--label', resource_label, '--tmpfs', '/data:rw,nosuid', redis_image)
    containers.append(redis)
    sql('postgres', "CREATE ROLE fixture_vm LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE PASSWORD '" + passwords['vm'] + "';\n"
        + "CREATE ROLE fixture_prod LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE PASSWORD '" + passwords['prod'] + "';\n"
        + "CREATE ROLE fixture_special LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE PASSWORD '" + special_password.replace("'", "''") + "';\n"
        + 'CREATE DATABASE sub2api_dev OWNER fixture_vm;\nCREATE DATABASE fixture_source OWNER fixture_prod;\nCREATE DATABASE fixture_other OWNER fixture_prod;\n'
        + ''.join('CREATE DATABASE ' + db + ' OWNER fixture_vm;\n' for db in names.values()))
    assert sql('sub2api_dev', 'SELECT NOT rolsuper AND NOT rolcreatedb AND NOT rolcreaterole FROM pg_roles WHERE rolname=current_user;', 'fixture_vm') == 't'
    roles_before = sql('postgres', "SELECT md5(string_agg(rolname||rolpassword,',' ORDER BY rolname)) FROM pg_authid;")
    owners_before = sql('postgres', "SELECT md5(string_agg(datname||':'||datdba::text,',' ORDER BY datname)) FROM pg_database;")
    vm = work / 'vm.yaml'
    config(vm, 'fixture_vm', passwords['vm'], 'sub2api_dev')
    vm_before = vm.read_bytes()
    source = work / 'source'
    config(source / 'config.yaml', 'fixture_prod', passwords['prod'], 'fixture_source')
    source_env = work / 'source.env'
    envfile(source_env, {'DATABASE_HOST':'sub2api-postgres','DATABASE_PORT':'5432','DATABASE_USER':'fixture_prod',
        'DATABASE_PASSWORD':passwords['prod'],'DATABASE_DBNAME':'fixture_source','DATABASE_SSLMODE':'disable'})
    sql('fixture_source', 'CREATE TABLE schema_migrations(filename TEXT PRIMARY KEY, checksum TEXT NOT NULL, applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW());', 'fixture_prod')
    stage = 'actual_candidate_source_bootstrap'
    bootstrap = plan_apply(source, source_env, 'source')
    assert len(bootstrap['pending']) > 250
    source_before, other_before, vm_db_before = (fingerprint(db) for db in ('fixture_source','fixture_other','sub2api_dev'))
    dump = docker('exec', pg, 'pg_dump', '-Fc', '-U', 'postgres', 'fixture_source').stdout
    # Extract actual production restore helper; route its fixed service token only.
    code = validator.read_text()
    start = code.index('restore_probe_database() {')
    end = code.index('\n}', start) + 2
    helpers = work / 'restore-helper.sh'
    helpers.write_text('set -Eeuo pipefail\n' + code[start:end] + '\n'
        + 'docker() { local a; local -a v=(); for a in "$@"; do [[ $a != sub2api-postgres ]] || a="$FIXTURE_PG"; v+=("$a"); done; command docker "${v[@]}"; }\n'
        + 'restore_probe_database "$1" "$2"\n')
    # A bad effective password must fail TCP precheck before any restore runs.
    stage = 'wrong_password_pre_restore'
    bad = work / 'wrong'
    config(bad / 'config.yaml', 'fixture_prod', passwords['prod'], 'fixture_source')
    module.prepare(vm, bad / 'config.yaml', ['DATABASE_PASSWORD=incorrect-fixture-password'], names['wrong'], work / 'wrong-connection')
    assert not precheck(work / 'wrong-connection')
    assert sql(names['wrong'], "SELECT to_regclass('public.schema_migrations') IS NULL;") == 't'
    # Docker env-file transports bytes verbatim, including shell metacharacters.
    stage = 'env_file_bytes'
    special = special_password
    special_vm = work / 'special-vm.yaml'
    config(special_vm, 'fixture_special', special, 'sub2api_dev')
    special_probe = work / 'special-probe'
    config(special_probe / 'config.yaml', 'fixture_prod', passwords['prod'], 'fixture_source')
    module.prepare(special_vm, special_probe / 'config.yaml', [], names['wrong'], work / 'special-connection')
    expected = hashlib.sha256(special.encode()).hexdigest()
    actual = docker('run', '--rm', '--pull', 'never', '--network', 'none', '--env-file', work / 'special-connection/connection.env',
        '--entrypoint', 'sh', candidate, '-c', 'test -z "$REDIS_USERNAME" && test -z "$REDIS_PASSWORD" && printf %s "$DATABASE_PASSWORD" | sha256sum').stdout.decode().split()[0]
    assert actual == expected
    # PGPASSWORD bypasses the application's pre-existing unquoted DSN limitation.
    # Authentication confirms special bytes, independently from their hash.
    assert precheck(work / 'special-connection', 'fixture_special')
    override_vm = work / 'override-vm.yaml'
    config(override_vm, 'fixture_prod', passwords['prod'], 'sub2api_dev')
    # Three production modes consume the same connection closure and restore role.
    for mode in ('fast','specialized','full'):
        stage = mode + '_normalize_precheck'
        data = work / mode
        config(data / 'config.yaml', 'fixture_vm' if mode == 'fast' else 'fixture_prod',
            passwords['vm'] if mode == 'fast' else passwords['prod'], 'fixture_source', 'production.invalid')
        environment = [] if mode == 'fast' else ['DATABASE_USER=fixture_vm','DATABASE_PASSWORD='+passwords['vm']] if mode == 'specialized' else ['DATABASE_USER=','DATABASE_PASSWORD=']
        connection = work / (mode + '-connection')
        module.prepare(override_vm if mode == 'specialized' else vm, data / 'config.yaml', environment, names[mode], connection)
        assert precheck(connection)
        assert (connection / 'role').read_text().strip() == 'fixture_vm'
        stage = mode + '_restore'
        restore_env = dict(os.environ, FIXTURE_PG=pg)
        if mode == 'fast':
            payload = docker('exec', pg, 'pg_dump', '-Fc', '-U', 'postgres', 'fixture_source').stdout
        else:
            payload = dump
        p = subprocess.run(['bash', str(helpers), names[mode], 'fixture_vm'], env=restore_env, input=payload, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=180)
        (work / (mode + '-restore.stderr')).write_bytes(p.stderr)
        assert p.returncode == 0
        # Recreate an actual pending migration, ensuring apply runs as VM owner.
        sql(names[mode], "DROP TABLE upstream_cache_guard_admissions; DELETE FROM schema_migrations WHERE filename='301_upstream_cache_guard_admissions.sql';", 'fixture_vm')
        stage = mode + '_candidate_plan_apply_plan'
        before = plan_apply(data, connection / 'connection.env', mode)
        assert [x['filename'] for x in before['pending']] == ['301_upstream_cache_guard_admissions.sql']
        assert sql(names[mode], "SELECT count(*)>0 AND bool_and(tableowner='fixture_vm') FROM pg_tables WHERE schemaname='public';") == 't'
        stage = mode + '_old_new_health'
        health(data, connection / 'connection.env', old, mode + '-old')
        health(data, connection / 'connection.env', candidate, mode + '-new')
        print('vm_probe_config_' + mode + '=pass', flush=True)
    stage = 'invariants'
    assert vm.read_bytes() == vm_before
    assert [fingerprint(db) for db in ('fixture_source','fixture_other','sub2api_dev')] == [source_before,other_before,vm_db_before]
    assert roles_before == sql('postgres', "SELECT md5(string_agg(rolname||rolpassword,',' ORDER BY rolname)) FROM pg_authid;")
    assert owners_before == sql('postgres', "SELECT md5(string_agg(datname||':'||datdba::text,',' ORDER BY datname)) FROM pg_database;")
    completed = True
    print('vm_probe_config_source_unchanged=true\nvm_probe_config_roles_unchanged=true\nvm_probe_config_wrong_password=blocked_before_restore\nvm_probe_config_env_bytes=pass', flush=True)
except Exception as error:
    print('vm_probe_config_failure_stage=' + stage, flush=True)
    print('vm_probe_config_failure_type=' + type(error).__name__, flush=True)
finally:
    cleanup = True
    try:
        remove_all_owned_containers()
    except Exception:
        cleanup = False
    try:
        remove_owned_network()
    except Exception:
        cleanup = False
    print('vm_probe_config_cleanup=' + ('pass' if cleanup else 'failed'), flush=True)
    if completed and cleanup:
        shutil.rmtree(work)
        print('vm_probe_config_integration=pass', flush=True)
    else:
        print('vm_probe_config_evidence=' + str(work), flush=True)
        print('vm_probe_config_integration=failed', flush=True)
        raise SystemExit(1)
PY
