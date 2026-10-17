from __future__ import annotations

import argparse
import json
import hashlib
import os
import pathlib
import secrets
import select
import shlex
import socketserver
import subprocess
import sys
import threading
import time
import ctypes
from ctypes import wintypes

ROOT = pathlib.Path(__file__).resolve().parents[6]
SOURCES = pathlib.Path(__file__).resolve().parent
HERE = ROOT / ".tmp/upstream-table-perf"
HERE.mkdir(parents=True, exist_ok=True)
sys.path.insert(0, str(ROOT / '.agents/skills/sub2api-production-deploy/scripts'))
from release.ssh import SSHRunner


def measurement_timeouts(seconds, readiness):
    if type(seconds) is not int or not 1 <= seconds <= 7200:
        raise ValueError('--seconds must be an integer from 1 through 7200')
    if type(readiness) is not int or not 1 <= readiness <= 600:
        raise ValueError('--readiness must be an integer from 1 through 600')
    # Include schema/data preparation and the initial scheduler snapshot, then
    # allow in-flight HTTP requests and profile/statistics output to finish.
    go_test_seconds = seconds + readiness + 600 + 120
    return {'initialization_seconds': 600, 'closeout_seconds': 120,
            'go_test_seconds': go_test_seconds, 'ssh_seconds': go_test_seconds + 60}


def version_order(versions, repetition):
    return list(versions) if repetition % 2 == 0 else list(reversed(versions))


def process_metrics(process):
    class Counters(ctypes.Structure):
        _fields_=[('cb',wintypes.DWORD),('PageFaultCount',wintypes.DWORD)]+[(name,ctypes.c_size_t) for name in ['PeakWorkingSetSize','WorkingSetSize','QuotaPeakPagedPoolUsage','QuotaPagedPoolUsage','QuotaPeakNonPagedPoolUsage','QuotaNonPagedPoolUsage','PagefileUsage','PeakPagefileUsage']]
    creation,exited,kernel,user=[wintypes.FILETIME() for _ in range(4)]
    handle=wintypes.HANDLE(int(process._handle))
    ok=ctypes.windll.kernel32.GetProcessTimes(handle,ctypes.byref(creation),ctypes.byref(exited),ctypes.byref(kernel),ctypes.byref(user))
    cpu=sum((x.dwHighDateTime<<32)+x.dwLowDateTime for x in [kernel,user])/10000000 if ok else 0
    counters=Counters();counters.cb=ctypes.sizeof(counters)
    ctypes.windll.psapi.GetProcessMemoryInfo(handle,ctypes.byref(counters),counters.cb)
    return cpu,counters.PeakWorkingSetSize


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--cells', default='')
    parser.add_argument('--versions', default='baseline,optimized')
    parser.add_argument('--result-file', default='results.json')
    parser.add_argument('--results-directory', default='results')
    parser.add_argument('--smoke', action='store_true')
    parser.add_argument('--seconds', type=int, default=85)
    parser.add_argument('--admins', type=int, default=20)
    parser.add_argument('--repetitions', type=int, default=3)
    parser.add_argument('--quality-filter', default='')
    parser.add_argument('--page-number', type=int, default=1)
    parser.add_argument('--readiness', type=int, default=120)
    parser.add_argument('--scenarios', default='normal')
    args = parser.parse_args()
    try:
        timeouts = measurement_timeouts(args.seconds, args.readiness)
    except ValueError as error:
        parser.error(str(error))
    if not 1 <= args.admins <= 100:
        parser.error('--admins must be from 1 through 100')
    if not 1 <= args.repetitions <= 100:
        parser.error('--repetitions must be from 1 through 100')
    if args.page_number < 1:
        parser.error('--page-number must be positive')
    versions = args.versions.split(',')
    if not versions or len(set(versions)) != len(versions) or set(versions) - {'baseline', 'optimized'}:
        parser.error('--versions must contain unique baseline/optimized names')
    scenarios = ['normal'] if args.smoke else args.scenarios.split(',')
    if not scenarios or len(set(scenarios)) != len(scenarios) or set(scenarios) - {'normal', 'slow', 'full'}:
        parser.error('--scenarios must contain unique normal/slow/full names')
    cells = [(100, 100000, 20)] if args.smoke else [(n, 100000, p) for n in [100, 1000, 10000] for p in [20, 100]]
    if args.cells:
        try:
            cells = [tuple(map(int, value.split(':'))) for value in args.cells.split(',')]
            if any(len(cell) != 3 or not 1 <= cell[0] <= 10000 or not 0 <= cell[1] <= 1000000 or not 1 <= cell[2] <= 100 for cell in cells):
                raise ValueError('cell bounds')
            if len(set(cells)) != len(cells):
                raise ValueError('duplicate cells')
        except ValueError:
            parser.error('--cells requires unique accounts:history:page within 1..10000:0..1000000:1..100')
    fixture_digest = hashlib.sha256((SOURCES/'matrix_test.go.txt').read_bytes()).hexdigest()
    fixture_digests = {p.name: hashlib.sha256(p.read_bytes()).hexdigest() for p in SOURCES.glob('*.go.txt')}
    for version in args.versions.split(','):
        manifest = json.loads((HERE/(version+'-sources.json')).read_text())
        if manifest.get('fixture') != fixture_digest:
            raise RuntimeError('rebuild '+version+': compiled fixture does not match current instrumentation')
        if manifest.get('fixture_sha256') != fixture_digests:
            raise RuntimeError('rebuild '+version+': compiled helpers do not match current instrumentation')
        if manifest.get('binary_sha256') != hashlib.sha256((HERE/(version+'-matrix.test')).read_bytes()).hexdigest():
            raise RuntimeError('rebuild '+version+': binary identity does not match its source manifest')
    provenance = {'fixture_sha256': {p.name: hashlib.sha256(p.read_bytes()).hexdigest() for p in SOURCES.glob('*.go.txt')}, 'tool_sha256': {p.name: hashlib.sha256(p.read_bytes()).hexdigest() for p in SOURCES.glob('*.py')}, 'binaries': {v: hashlib.sha256((HERE/(v+'-matrix.test')).read_bytes()).hexdigest() for v in args.versions.split(',')}, 'readiness_seconds': args.readiness, 'measurement_seconds': args.seconds, 'cells': args.cells, 'scenarios': args.scenarios, 'results_directory': args.results_directory, 'admins': args.admins, 'repetitions': args.repetitions, 'quality_filter': args.quality_filter, 'page_number': args.page_number}
    provenance['source_manifest_sha256'] = {v: hashlib.sha256((HERE/(v+'-sources.json')).read_bytes()).hexdigest() for v in args.versions.split(',')}
    provenance['timeout_budgets'] = timeouts
    provenance.update(version_order_rule='reverse supplied version order on odd repetitions', attempted_cell_order=[], completed_cell_order=[])
    provenance_path = HERE/(pathlib.Path(args.result_file).stem+'-provenance.json')
    provenance_path.write_text(json.dumps(provenance,indent=2)+'\n')
    runner = SSHRunner()
    identifier = 'matrix_' + secrets.token_hex(6)
    password = secrets.token_hex(20)
    job_token = secrets.token_hex(24)
    database = identifier
    redis_name = 'codex-' + identifier.replace('_', '-')
    role = identifier
    remote_root = runner.create_temp_dir('local_vm', '/tmp', 'codex-upstream-table-matrix')
    setup = r'''set -eu
role=__ROLE__
database=__DB__
password=__PASSWORD__
redis_name=__REDIS__
network=$(docker inspect sub2api-postgres --format '{{range $key, $value := .NetworkSettings.Networks}}{{println $key}}{{end}}' | head -n 1)
test -n "$network"
docker exec -e MATRIX_ROLE="$role" -e MATRIX_DATABASE="$database" -e MATRIX_PASSWORD="$password" sub2api-postgres sh -c 'export PGHOST=/var/run/postgresql PGUSER="${POSTGRES_USER:-sub2api}" PGPASSWORD="${POSTGRES_PASSWORD:-}"; psql -q -v ON_ERROR_STOP=1 -d postgres -c "CREATE ROLE $MATRIX_ROLE LOGIN PASSWORD '\''$MATRIX_PASSWORD'\'';"; createdb --maintenance-db=postgres -O "$MATRIX_ROLE" "$MATRIX_DATABASE"' >/dev/null
docker run -d --name "$redis_name" --label codex.task=upstream-table-perf --network "$network" --memory 192m redis:8-alpine redis-server --save '' --appendonly no >/dev/null
printf 'pg_ip=%s\n' "$(docker inspect sub2api-postgres --format '{{range .NetworkSettings.Networks}}{{println .IPAddress}}{{end}}' | head -n 1)"
printf 'redis_ip=%s\n' "$(docker inspect "$redis_name" --format '{{range .NetworkSettings.Networks}}{{println .IPAddress}}{{end}}' | head -n 1)"
printf 'setup_owned=true\n'
pg_pid=$(docker inspect sub2api-postgres --format '{{.State.Pid}}')
pg_cgroup=$(awk -F: '$1=="0" {print $3}' "/proc/$pg_pid/cgroup")
if test -n "$pg_cgroup" && test -r "/sys/fs/cgroup${pg_cgroup}/cpu.stat"; then
  printf 'pg_cpu_file=%s\n' "/sys/fs/cgroup${pg_cgroup}/cpu.stat"
else
  printf 'pg_cpu_file=/unavailable\n'
fi
'''
    for key, value in {'__ROLE__':role,'__DB__':database,'__PASSWORD__':password,'__REDIS__':redis_name}.items():
        setup = setup.replace(key, shlex.quote(value))
    owner = {'remote_root': remote_root, 'database': database, 'role': role, 'redis': redis_name}
    (HERE / 'owned.json').write_text(json.dumps(owner, indent=2)+'\n')
    client = None
    servers = []
    results = []
    failed_cells = []
    primary_error = None
    supervisor_uploaded = False
    cleanup_path = HERE/(pathlib.Path(args.result_file).stem+'-cleanup.json')
    cleanup_path.write_text(json.dumps({'status': 'pending', 'owned_resources_cleanup_complete': False},indent=2)+'\n')
    try:
        runner.upload_file('local_vm', SOURCES/'remote_job.py', remote_root+'/remote_job.py')
        supervisor_uploaded = True
        values = runner.run('local_vm', setup, {'pg_ip','redis_ip','setup_owned','pg_cpu_file'}, timeout=60).values
        client = runner.connect('local_vm')
        sftp=client.open_sftp()
        sftp.put(str(SOURCES/'pg_cpu_sample.py'), remote_root+'/pg_cpu_sample.py')
        for version in args.versions.split(','):
            source=HERE/f'{version}-matrix.test'
            destination=remote_root+'/'+version+'-matrix.test'
            sftp.put(str(source),destination);sftp.chmod(destination,0o700)
        for n,h,p,rep in [(n,h,p,rep) for n,h,p in cells for rep in range(args.repetitions)]:
            for scenario in scenarios:
                for version in version_order(versions, rep):
                    cell = f'{version}-a{n}-h{h}-p{p}-m{args.admins}-r{rep}-{scenario}'
                    out = HERE / args.results_directory / cell
                    out.mkdir(parents=True,exist_ok=True)
                    # Reset only this test's dedicated Redis container.
                    runner.run('local_vm', f"docker exec {shlex.quote(redis_name)} redis-cli FLUSHDB >/dev/null\nprintf 'redis_reset=true\\n'\n", {'redis_reset'},timeout=15)
                    remote_out=remote_root+'/'+cell
                    variables=dict(MATRIX_ISOLATED='1',MATRIX_DSN=f"postgres://{role}:{password}@{values['pg_ip']}:5432/{database}?sslmode=disable&TimeZone=UTC",MATRIX_REDIS=f"redis://{values['redis_ip']}:6379/0",MATRIX_ACCOUNTS=str(n),MATRIX_HISTORY=str(h),MATRIX_PAGE=str(p),MATRIX_SECONDS=str(args.seconds),MATRIX_READINESS_SECONDS=str(args.readiness),MATRIX_SCENARIO=scenario,MATRIX_OUT=remote_out,MATRIX_ADMINS=str(args.admins),MATRIX_VERSION=version,MATRIX_QUALITY_FILTER=args.quality_filter,MATRIX_PAGE_NUMBER=str(args.page_number),GOMEMLIMIT='1200MiB',GOGC='50',GOMAXPROCS='2')
                    sftp.mkdir(remote_out, mode=0o700)
                    job = {'environment': variables, 'cpu_file': values['pg_cpu_file'], 'command': [
                        '/usr/bin/time', '-f', 'MATRIX_PROCESS_CPU_USER_SECONDS=%U\nMATRIX_PROCESS_CPU_SYSTEM_SECONDS=%S\nMATRIX_PROCESS_PEAK_RSS_KB=%M',
                        remote_root+'/'+version+'-matrix.test', '-test.run', '^TestUpstreamTableHTTPMatrix$', '-test.v',
                        '-test.timeout', str(timeouts['go_test_seconds'])+'s']}
                    with sftp.open(remote_out+'/job.input.json', 'wx') as stream:
                        stream.write(json.dumps(job))
                    sftp.chmod(remote_out+'/job.input.json', 0o600)
                    command='MATRIX_JOB_TOKEN='+shlex.quote(job_token)+' python3 '+shlex.quote(remote_root+'/remote_job.py')+' execute '+shlex.quote(remote_root)+' '+shlex.quote(cell)
                    print(json.dumps({'starting':cell}),flush=True)
                    provenance['attempted_cell_order'].append(cell)
                    provenance_path.write_text(json.dumps(provenance,indent=2)+'\n')
                    remote_values=runner.run('local_vm',command,{'exit_code'},timeout=timeouts['ssh_seconds']).values
                    exit_code=int(remote_values['exit_code'])
                    provenance['completed_cell_order'].append(cell)
                    provenance_path.write_text(json.dumps(provenance,indent=2)+'\n')
                    for name in sftp.listdir(remote_out):
                        if name in {'job.input.json', 'job.owner.json'}:
                            continue
                        sftp.get(remote_out+'/'+name,str(out/name))
                    cpu_seconds=0.0;peak_rss=0
                    lines=(out/'run.log').read_text(encoding='utf-8').splitlines()
                    data=[json.loads(line.split('=',1)[1]) for line in lines if line.startswith('MATRIX_RESULT=')]
                    for line in lines:
                        if line.startswith('MATRIX_PROCESS_CPU_USER_SECONDS=') or line.startswith('MATRIX_PROCESS_CPU_SYSTEM_SECONDS='):cpu_seconds+=float(line.split('=',1)[1])
                        if line.startswith('MATRIX_PROCESS_PEAK_RSS_KB='):peak_rss=int(line.split('=',1)[1])*1024
                    result={'cell':cell,'version':version,'repetition':rep,'execution_index':len(results),'quality_filter':args.quality_filter,'page_number':args.page_number,'exit':exit_code,'result':data[-1] if data else None,'whole_process_cpu_seconds':cpu_seconds,'peak_rss_bytes':peak_rss,'artifacts_directory':str(out.relative_to(HERE)), 'binary_sha256':provenance['binaries'][version]}
                    cpu_sample=out/'postgres_cpu.json'
                    result['postgres_container_cpu']=json.loads(cpu_sample.read_text()) if cpu_sample.exists() else {'status':'sampler_failed'}
                    results.append(result)
                    (HERE/args.result_file).write_text(json.dumps(results,indent=2)+'\n')
                    print(json.dumps({'finished':cell,'exit':exit_code,'has_result':bool(data)}),flush=True)
                    if exit_code:
                        print('\n'.join(lines[-20:]),flush=True)
                        failed_cells.append(cell)
    except BaseException as error:
        primary_error = {'stage': 'matrix_run', 'error_type': type(error).__name__}
        raise
    finally:
        for server in servers:
            server.shutdown();server.server_close()
        if client:
            client.close()
        # A lost SSH channel does not terminate a remote process. Stop only
        # groups registered by our supervisor before touching their resources.
        stopped = None
        try:
            if supervisor_uploaded:
                stopped = runner.run('local_vm', 'python3 '+shlex.quote(remote_root+'/remote_job.py')+' stop '+shlex.quote(remote_root)+' '+shlex.quote(job_token), {'owned_processes_stopped'}, timeout=120).values
            else:
                # No execute command can precede the successful upload.
                stopped = {'owned_processes_stopped': 'true'}
            if stopped != {'owned_processes_stopped': 'true'}:
                raise RuntimeError('owned process groups not confirmed stopped')
        except BaseException as error:
            failure = {'owned_processes_stopped': False, 'resources_preserved': True,
                       'cleanup_error_type': type(error).__name__, 'primary_error': primary_error}
            cleanup_path.write_text(json.dumps(failure,indent=2)+'\n')
            raise
        finally:
            try:
                if supervisor_uploaded:
                    runner.download_file('local_vm', remote_root+'/process-cleanup.json', HERE/(pathlib.Path(args.result_file).stem+'-process-cleanup.json'))
                else:
                    (HERE/(pathlib.Path(args.result_file).stem+'-process-cleanup.json')).write_text(json.dumps({'owned_processes_stopped': True, 'groups': [], 'no_jobs_launched': True},indent=2)+'\n')
            except Exception as error:
                if stopped == {'owned_processes_stopped': 'true'}:
                    cleanup_path.write_text(json.dumps({'status': 'process_cleanup_evidence_download_failed',
                        'owned_processes_stopped': True, 'owned_resources_cleanup_complete': False, 'resources_preserved': True,
                        'cleanup_error_type': type(error).__name__, 'primary_error': primary_error},indent=2)+'\n')
                    raise
        cleanup_record = {'status': 'processes_stopped', 'owned_resources_cleanup_complete': False,
                          **stopped, 'primary_error': primary_error}
        cleanup_path.write_text(json.dumps(cleanup_record,indent=2)+'\n')
        cleanup = r'''set -eu
database=__DB__
role=__ROLE__
redis_name=__REDIS__
docker rm -f "$redis_name" >/dev/null 2>&1 || true
docker exec -e MATRIX_DATABASE="$database" -e MATRIX_ROLE="$role" sub2api-postgres sh -c 'export PGHOST=/var/run/postgresql PGUSER="${POSTGRES_USER:-sub2api}" PGPASSWORD="${POSTGRES_PASSWORD:-}"; dropdb --if-exists --force --maintenance-db=postgres "$MATRIX_DATABASE"; dropuser --if-exists "$MATRIX_ROLE"' >/dev/null
printf 'owned_database_removed=true\n'
printf 'owned_role_removed=true\n'
test -z "$(docker ps -aq --filter name="^/$redis_name$")"
printf 'owned_redis_removed=true\n'
'''
        for key,value in {'__DB__':database,'__ROLE__':role,'__REDIS__':redis_name}.items():
            cleanup=cleanup.replace(key,shlex.quote(value))
        cleaned=runner.run('local_vm',cleanup,{'owned_database_removed','owned_role_removed','owned_redis_removed'},timeout=60).values
        cleaned.update(stopped)
        if primary_error:
            cleaned['primary_error'] = primary_error
        cleanup_record.update(cleaned, status='database_and_redis_removed')
        cleanup_path.write_text(json.dumps(cleanup_record,indent=2)+'\n')
        observed=runner.run('local_vm', 'docker exec sub2api-postgres sh -c '+shlex.quote('export PGHOST=/var/run/postgresql PGUSER="${POSTGRES_USER:-sub2api}" PGPASSWORD="${POSTGRES_PASSWORD:-}"; psql -qAt -d postgres -c "SELECT \'owned_database_absent=\'||(NOT EXISTS(SELECT 1 FROM pg_database WHERE datname=\''+database+'\'))::text; SELECT \'owned_role_absent=\'||(NOT EXISTS(SELECT 1 FROM pg_roles WHERE rolname=\''+role+'\'))::text;"'), {'owned_database_absent','owned_role_absent'},timeout=20).values
        cleaned.update(observed)
        if observed != {'owned_database_absent':'true','owned_role_absent':'true'}:
            raise RuntimeError('owned synthetic resource absence check failed')
        quoted_remote_root = shlex.quote(remote_root)
        removed = runner.run('local_vm', 'set -eu\ntest -d '+quoted_remote_root+'\ntest ! -L '+quoted_remote_root+'\nrm -r -- '+quoted_remote_root+'\ntest ! -e '+quoted_remote_root+'\ntest ! -L '+quoted_remote_root+"\nprintf 'owned_temp_removed=true\\n'\n", {'owned_temp_removed'},timeout=30).values
        if removed != {'owned_temp_removed': 'true'}:
            raise RuntimeError('owned temporary directory absence check failed')
        cleaned.update(removed)
        cleaned.update(status='complete', owned_resources_cleanup_complete=True)
        (HERE/'cleanup.json').write_text(json.dumps(cleaned,indent=2)+'\n')
        cleanup_path.write_text(json.dumps(cleaned,indent=2)+'\n')
        print(json.dumps({'cleanup':cleaned}),flush=True)
    if failed_cells:
        print(json.dumps({'status':'completed_with_failures','failed_cells':failed_cells}),flush=True)
        raise SystemExit(1)

if __name__=='__main__':
    main()
