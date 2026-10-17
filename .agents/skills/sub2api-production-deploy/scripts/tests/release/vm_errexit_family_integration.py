"""Execute actual release guards in an inert, private Linux fixture.

No Docker, service or power operation reaches the host. Existing VM execution
locks are held during the tests; only the unique temporary subtree is writable.
"""
from __future__ import annotations

import ast
import argparse
import base64
import contextlib
import hashlib
import io
import json
import shlex
import re
import sys
import tempfile
from pathlib import Path
from types import SimpleNamespace

import pytest

ROOT = Path(__file__).resolve().parents[2]
sys.path[:0] = [str(ROOT), str(Path(__file__).parent)]
from release import finalization, supervisor, vm_validate
from release.ssh import SSHRunner
from test_vm_preserve_reconciliation import fixture
import vm_preserve_candidate_build_integration as candidate
import vm_preserve_migration_preflight_integration as migration


def shell_function(source: str, name: str) -> str:
    lines = source.splitlines(keepends=True)
    start = next(i for i, line in enumerate(lines) if line.strip() == name + '() {')
    indent = lines[start][:-len(lines[start].lstrip())]
    end = next(i for i in range(start + 1, len(lines)) if lines[i].rstrip() == indent + '}')
    return ''.join(lines[start:end + 1])


def payload(*, full: bool = True, native_backup_boundaries: str | None = None) -> dict:
    captured = []
    runner = SimpleNamespace(run=lambda _host, script, _fields, **_kw: (captured.append(script), SimpleNamespace(values={'vm_tasks_cleanup': 'verified'}))[1])
    manifest = {'release_id': '273-aaaaaaaaaaaa-1-deadbeef'}
    manifest_bytes = b'{"fixture":true}\n'
    finalization._cleanup_vm_tasks(runner, manifest, hashlib.sha256(manifest_bytes).hexdigest(), boot_id='aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee')
    scripts = {'finalization': captured[0]}
    tree = ast.parse((ROOT / 'release/vm_lifecycle.py').read_text())
    complete = next(n for n in ast.walk(tree) if isinstance(n, ast.FunctionDef) and n.name == 'complete')
    scripts['lifecycle'] = next(n.value for n in ast.walk(complete) if isinstance(n, ast.Constant) and isinstance(n.value, str) and n.value.startswith('set -Eeuo pipefail')).replace('BOOT_ID', 'aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee')
    with tempfile.TemporaryDirectory() as local:
        patch = pytest.MonkeyPatch()
        try:
            case = fixture.__wrapped__(Path(local), patch)
            with contextlib.redirect_stdout(io.StringIO()):
                supervisor.reconcile_vm_preserve(case.args)
            scripts['preserve'] = case.calls[0][1]
        finally:
            patch.undo()
    hashes = {key: hashlib.sha256(key.encode()).hexdigest() for key in ('vm_validator_sha256', 'vm_gate_signer_sha256', 'vm_dr_signer_sha256')}
    scripts['assets'] = vm_validate._validator_assets_script(hashes)
    validator = (ROOT / 'release/vm-validate.sh').read_text()
    scripts['old_tag'] = shell_function(validator, 'cleanup_old_image_tag')
    cleaner = (ROOT / 'release/production-space-clean.sh').read_text()
    scripts['cleaner'] = '\n'.join(shell_function(cleaner, name) for name in ('remove_candidate_archives', 'remove_image_candidates'))
    scripts['cleaner_captures'] = re.findall(r'^\s*required_value=\$\(.*\)$', cleaner, re.M)
    scripts['protected_inventory'] = '\n'.join(shell_function(cleaner, name) for name in ('write_container_images','write_protected_images'))
    start = cleaner.index('if [[ $mode == apply ]]; then\n  [[ $plan_sha256 ==')
    end = cleaner.index('\nif [[ $mode == apply ]]; then\n  write_candidate_archive_records', start)
    scripts['cleaner_entry'] = cleaner[start:end]
    scripts['cleaner_after'] = cleaner[cleaner.index('list_image_candidates > "$work_dir/image-candidate-ids-after"'):cleaner.index('build_cache_records_after=')]
    context = (ROOT / 'maintenance/release/context.sh').read_text()
    scripts['drain'] = '\n'.join(shell_function(context, name) for name in ('nginx_draining_worker_count', 'wait_for_application_drain'))
    scripts['log_count'] = shell_function(context, 'critical_startup_log_count')
    scripts['log_gates'] = []
    for name in ('verify.sh', 'finalize.sh'):
        source = (ROOT / 'maintenance/release' / name).read_text()
        lines = source.splitlines()
        for i, line in enumerate(lines):
            if line.lstrip().startswith('critical=$(critical_startup_log_count'):
                scripts['log_gates'].append('\n'.join(lines[i:i + 2]))
    assert len(scripts['log_gates']) == 3
    if full:
        mc, mm = migration.capture_check()
        cc, cm = candidate.capture_check()
        scripts['migration_full'] = migration.locked_fixture_script(mc, mm)
        scripts['candidate_full'] = candidate.locked_fixture_script(cc, cm)
        scripts['backup_unit_test'] = (ROOT / 'tests/release/test_backup_unit_observations.py').read_text(encoding='utf-8')
        scripts['backup_unit_sources'] = {
            name: (ROOT / 'maintenance/181' / name).read_text(encoding='utf-8')
            for name in ('mask-backup-units.sh', 'restore-backup-units.sh')
        }
        if native_backup_boundaries is not None:
            compile(native_backup_boundaries, '<independent-backup-unit-boundaries>', 'exec')
            scripts['backup_unit_boundaries'] = native_backup_boundaries
    paths = [*ROOT.joinpath('release').glob('*.py'), *ROOT.joinpath('release').glob('*.sh'),
             *ROOT.joinpath('maintenance/release').glob('*.sh'), *ROOT.joinpath('maintenance/181').glob('*.sh')]
    paths += [Path(__file__), Path(__file__).with_name('test_backup_unit_observations.py'),
              Path(candidate.__file__), Path(migration.__file__), Path(__file__).with_name('test_vm_preserve_reconciliation.py')]
    source_hashes = {path.relative_to(ROOT).as_posix(): hashlib.sha256(path.read_bytes()).hexdigest() for path in sorted(set(paths))}
    return {'scripts': scripts, 'hashes': hashes, 'source_sha256': source_hashes, 'manifest': manifest_bytes.decode(), 'release_id': manifest['release_id']}


REMOTE = r'''
import base64, fcntl, hashlib, json, os, pathlib, shutil, stat, subprocess, sys
P=json.loads(base64.b64decode(PAYLOAD))
root=pathlib.Path(sys.argv[1]).resolve()
assert root.parent == pathlib.Path('/opt/sub2api-deploy/release-input') and root.name.startswith('errexit-family-test.')
boot=pathlib.Path('/proc/sys/kernel/random/boot_id').read_text().strip()
checks=0
phase='initial'
stub = r"""#!/usr/bin/python3
import json, os, pathlib, shutil, sys
name=pathlib.Path(sys.argv[0]).name
args=sys.argv[1:]
key=name+' '+' '.join(args)
config=json.loads(os.environ['FIXTURE_COMMANDS'])
if name == 'stat':
 if 'stat_failure' in config:
  sys.stdout.write(config['stat_failure'][1]);sys.exit(config['stat_failure'][0])
 os.execv('/usr/bin/stat',['stat']+args)
mutating=(name in ('rm','shutdown','systemctl') or key.startswith('docker image rm ') or key.startswith('docker stop '))
if mutating:
 with open(os.environ['FIXTURE_LEDGER'],'a') as f: f.write(key+'\n')
if name == 'rm':
 for value in args:
  if value.startswith('-'): continue
  path=pathlib.Path(value)
  boundary=pathlib.Path(os.environ['FIXTURE_CASE_ROOT'])
  if not path.is_absolute() or not path.is_relative_to(boundary): sys.exit(96)
  if config.get('rm_fail'): sys.exit(70)
  if path.is_dir() and not path.is_symlink(): shutil.rmtree(path)
  elif path.exists() or path.is_symlink(): path.unlink()
 sys.exit(0)
reply=config.get(key)
if reply is None: sys.exit(97)
sys.stdout.write(reply[1]); sys.exit(reply[0])
"""

def write(path, text='', mode=0o600):
 path.parent.mkdir(parents=True,exist_ok=True)
 path.write_text(text);path.chmod(mode)

def case(name):
 global phase
 phase=name
 home=root/name
 home.mkdir(mode=0o700)
 bin=home/'bin';bin.mkdir(mode=0o700)
 for name in ('docker','ss','ps','shutdown','systemctl','rm','stat'):
  write(bin/name,stub,0o700)
 return home

def run(home, script, commands, expected=True, success='fixture_success=true', mutations=None):
 global checks
 env=dict(os.environ,PATH=str(home/'bin')+':'+os.environ['PATH'],FIXTURE_COMMANDS=json.dumps(commands),FIXTURE_LEDGER=str(home/'ledger'),FIXTURE_CASE_ROOT=str(home))
 for old,new in (('/opt/sub2api-deploy/release-gates',str(home/'gates')),('/opt/sub2api-deploy/release-logs',str(home/'logs')),('/usr/local/libexec',str(home/'libexec')),('aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee',boot)):
  script=script.replace(old,new)
 write(home/'actual.sh',script,0o700)
 result=subprocess.run(['bash',str(home/'actual.sh')],env=env,capture_output=True,text=True,timeout=10)
 assert (result.returncode == 0) == expected, (phase,'status',result.returncode)
 assert (success in result.stdout) == expected, (phase,'sentinel')
 if mutations is not None:
  ledger=(home/'ledger').read_text() if (home/'ledger').exists() else ''
  assert len(ledger.splitlines()) == mutations, (phase,'mutations')
 checks+=1
 return result

def lockfiles(home):
 write(home/'libexec/.sub2api-release-unit.lock')
 write(home/'gates/release.lock')

locks=[]
for path in ('/usr/local/libexec/.sub2api-release-unit.lock','/opt/sub2api-deploy/release-gates/release.lock'):
 st=os.lstat(path);assert stat.S_ISREG(st.st_mode) and st.st_uid==st.st_gid==0 and st.st_nlink==1
 assert stat.S_IMODE(st.st_mode) in (0o600,0o644)
 handle=open(path,'r+');fcntl.flock(handle,fcntl.LOCK_EX|fcntl.LOCK_NB);locks.append(handle)
try:
 # Complete generated cleanup script: failures must precede plaintext removal.
 for fault in ('none','containers','networks','v2_database','probe_database','ps_error','network_error','inspect_error','database_error','stat_error'):
  home=case('finalization_'+fault);lockfiles(home)
  state=home/'gates'/P['release_id'];state.mkdir(mode=0o700)
  write(state/'manifest.json',P['manifest'])
  for name in ('probe-data','production-recovery','probe-redis-data'): (state/name).mkdir(mode=0o700)
  c={'docker ps -a --format {{.Names}}':[0,''],'docker network ls --format {{.Name}}':[0,''], 'docker inspect sub2api-postgres':[0,'[{"Config":{"Env":["POSTGRES_USER=fixture"]}}]'], 'docker exec sub2api-postgres psql -X -A -t -U fixture -d postgres -c SELECT datname FROM pg_database':[0,'postgres\n']}
  if fault=='containers': c['docker ps -a --format {{.Names}}'][1]='sub2api-probe-leftover\n'
  if fault=='networks':c['docker network ls --format {{.Name}}'][1]='sub2api-v2-leftover\n'
  if fault in ('v2_database','probe_database'):
   suffix=''.join(ch for ch in P['release_id'] if ch.isalnum())[:24]
   c[next(k for k in c if k.startswith('docker exec'))][1]='sub2api_'+('v2_' if fault=='v2_database' else 'probe_')+suffix+'\n'
  if fault=='stat_error':c['stat_failure']=[70,'root:root:600:1\n']
  if fault.endswith('_error') and fault!='stat_error':
   prefix={'ps_error':'docker ps','network_error':'docker network','inspect_error':'docker inspect','database_error':'docker exec'}[fault]
   c[next(k for k in c if k.startswith(prefix))]=[70,'']
  run(home,P['scripts']['finalization'],c,fault=='none','vm_tasks_cleanup=verified',3 if fault=='none' else 0)
 # Full owned-lease script: no host stop/shutdown commands can execute.
 for fault in ('none','container','ps_error','running_error','status_error','listener','ss_error','stop_error','stat_error'):
  home=case('lifecycle_'+fault);lockfiles(home)
  c={'docker ps -a --format {{.Names}}':[0,''],'docker inspect -f {{.State.Running}} sub2api-dev':[0,'true\n'],'docker stop -t 30 sub2api-dev':[0,''],'docker inspect -f {{.State.Status}} sub2api-dev':[0,'exited\n'],"ss -ltnH ( sport = :8211 )":[0,''],'shutdown -h +1':[0,'']}
  if fault=='container':c['docker ps -a --format {{.Names}}'][1]='sub2api-preview-leftover\n'
  prefix={'ps_error':'docker ps','running_error':'docker inspect -f {{.State.Running}}','status_error':'docker inspect -f {{.State.Status}}','ss_error':'ss ','stop_error':'docker stop'}.get(fault)
  if prefix:c[next(k for k in c if k.startswith(prefix))]=[70,'']
  if fault=='listener':c['ss -ltnH ( sport = :8211 )'][1]='LISTEN 0 128 127.0.0.1:8211\n'
  if fault=='stat_error':c['stat_failure']=[70,'root:root:600:1\n']
  run(home,P['scripts']['lifecycle'],c,fault=='none','vm_shutdown_scheduled=true',2 if fault=='none' else (1 if fault in ('status_error','listener','ss_error','stop_error') else 0))
 # Shared preserve guards: root regular files and FIFO locks cannot pass.
 for fault in ('none','raw_regular','raw_symlink','lock_fifo','lock_symlink','lock_missing','stat_error'):
  home=case('preserve_'+fault);lockfiles(home)
  raw=home/'logs/264-failed';raw.mkdir(parents=True,mode=0o700)
  lock=home/'libexec/.sub2api-release-unit.lock'
  if fault=='raw_regular':raw.rmdir();write(raw,'',0o700)
  if fault=='raw_symlink':raw.rmdir();raw.symlink_to(home/'missing')
  if fault=='lock_fifo':lock.unlink();os.mkfifo(lock,0o600)
  if fault=='lock_symlink':lock.unlink();write(home/'target');lock.symlink_to(home/'target')
  if fault=='lock_missing':lock.unlink()
  c={'ps -eo args=':[0,''],'docker inspect -f {{.State.Health.Status}} sub2api-dev':[0,'healthy\n']}
  if fault=='stat_error':c['stat_failure']=[70,'root:root:700\n']
  run(home,P['scripts']['preserve'],c,fault=='none','vm_preserve_preflight=verified',0)
 # Every installed asset, including non-final loop items, is independently checked.
 assets=('sub2api-vm-validate','sub2api-sign-gate','sub2api-sign-dr-evidence')
 for index in range(3):
  for fault in ('none','mode','owner','symlink','missing','bytes','stat_error'):
   home=case('assets_'+str(index)+'_'+fault)
   for filename,key in zip(assets,P['hashes']):write(home/'libexec'/filename,key,0o700)
   target=home/'libexec'/assets[index]
   if fault=='mode':target.chmod(0o600)
   if fault=='owner':os.chown(target,65534,65534)
   if fault=='symlink':target.rename(home/'target');target.symlink_to(home/'target')
   if fault=='missing':target.unlink()
   if fault=='bytes':target.write_text('drift')
   c={'stat_failure':[70,'root:root:700\n']} if fault=='stat_error' else {}
   run(home,P['scripts']['assets'],c,fault=='none','validator_assets_verified=true',0)
 # Old-tag cleanup is called from both normal completion and best-effort trap.
 for fault in ('none','rm_error','list_error','retained'):
  home=case('oldtag_'+fault)
  c={'docker image rm sub2api:old':[0,''],'docker image ls --format {{.Repository}}:{{.Tag}}':[0,'']}
  if fault=='rm_error':c['docker image rm sub2api:old']=[70,'']
  if fault=='list_error':c['docker image ls --format {{.Repository}}:{{.Tag}}']=[70,'']
  if fault=='retained':c['docker image ls --format {{.Repository}}:{{.Tag}}'][1]='sub2api:old\n'
  script='set -Eeuo pipefail\nold_image_tag_created=true\nold_image_tag=sub2api:old\n'+P['scripts']['old_tag']+'\ncleanup_old_image_tag\n[[ $old_image_tag_created == false ]]\nprintf "fixture_success=true\\n"\n'
  run(home,script,c,fault=='none',mutations=1)
  script='set -Eeuo pipefail\nold_image_tag_created=true\nold_image_tag=sub2api:old\n'+P['scripts']['old_tag']+'\ncleanup_old_image_tag || true\n[[ $old_image_tag_created == '+('false' if fault=='none' else 'true')+' ]]\nprintf "fixture_success=true\\n"\n'
  run(home,script,c,True,mutations=2)
 # Actual apply entry, with real removal functions, not a top-level surrogate.
 for kind in ('image','archive'):
  for fault in ('none','idle','marker','services','rm_error','inspect_error','list_error','retained'):
   home=case('cleaner_'+kind+'_'+fault)
   work=home/'work';work.mkdir()
   release=home/'releases/old/.consumed';release.mkdir(parents=True)
   archive=release/'candidate.tar.gz';write(archive,'archive')
   st=archive.stat();write(work/'archive-candidates',f'{int(st.st_mtime)}\t{st.st_size}\t1\told\n' if kind=='archive' else '')
   image='sha256:'+'d'*64
   c={'docker image inspect -f {{range .RepoTags}}{{println .}}{{end}} '+image:[0,'sub2api:273-'+'a'*40+'\n'],'docker image rm sub2api:273-'+'a'*40:[0,''],'docker image ls -a --no-trunc --format {{.ID}}':[0,'']}
   if fault=='rm_error':c['rm_fail']=True;c['docker image rm sub2api:273-'+'a'*40]=[70,'']
   if fault=='inspect_error':c[next(k for k in c if k.startswith('docker image inspect'))]=[70,'']
   if fault=='list_error':c['docker image ls -a --no-trunc --format {{.ID}}']=[70,'']
   if fault=='retained':c['docker image ls -a --no-trunc --format {{.ID}}'][1]=image+'\n'
   guards='\n'.join('assert_release_'+name+'() { '+('return 70;' if fault==name else ':;')+' }' for name in ('idle','marker'))
   guards+='\nassert_services() { '+('return 70;' if fault=='services' else ':;')+' }\nassert_release_identity() { :; }\nwrite_protected_images() { :; }\n'
   setup=f'work_dir={shlex_quote(str(work))}\nrelease_root={shlex_quote(str(home/"releases"))}\ncandidate_archive_candidates="$work_dir/archive-candidates"\nmode=apply\nplan_sha256=x\nexpected_plan_sha256=x\nbuild_cache_records_before=0\n'
   setup+=guards+'\nlist_image_candidates() { '+('printf "%s\\n" '+image+';' if kind=='image' else ':;')+' }\n'
   script='set -Eeuo pipefail\n'+setup+P['scripts']['cleaner']+'\n'+P['scripts']['cleaner_entry']+'\nprintf "fixture_success=true\\n"\n'
   applicable=kind=='image' or fault in ('none','idle','marker','services','rm_error')
   expected=(fault=='none' or not applicable)
   mutations=0 if fault in ('idle','marker','services') or (kind=='image' and fault=='inspect_error') else 1
   run(home,script,c,expected,mutations=mutations)
 # Explicit after-clean candidate invariant refuses a silently retained ID.
 for count in (0,1):
  home=case('cleaner_after_'+str(count));(home/'work').mkdir()
  script='set -Eeuo pipefail\nmode=apply\nwork_dir='+shlex_quote(str(home/'work'))+'\nlist_image_candidates() { '+('printf "id\\n";' if count else ':;')+' }\n'+P['scripts']['cleaner_after']+'\nprintf "fixture_success=true\\n"\n'
  run(home,script,{},count==0,mutations=0)
 # Required metadata output must never replace the producer's exit status.
 for index,capture in enumerate(P['scripts']['cleaner_captures']):
  home=case('cleaner_producer_'+str(index))
  tools='\n'.join(name+'() { printf "expected-output\\n"; return 70; }' for name in ('ps','awk','tr','docker','systemctl','curl','stat'))
  setup='set -Eeuo pipefail\nactive_container=fixture; pre_switch_image=fixture; active_port=18080; release_state_dir=/tmp; path=/tmp\n'
  run(home,setup+tools+'\n'+capture+'\nprintf "fixture_success=true\\n"\n',{},False,mutations=0)
 home=case('cleaner_protected_pipeline')
 setup='set -Eeuo pipefail\nexpected_current_image=current; pre_switch_image=previous\n'
 run(home,setup+P['scripts']['protected_inventory']+'\nwrite_protected_images | sort\nprintf "fixture_success=true\\n"\n',{'docker ps -aq --no-trunc':[70,'partial-container\n']},False,mutations=0)
 # Existing drain callers receive unknown on failed ps, never a false zero.
 for fault in ('none','worker','ps_error'):
  home=case('drain_'+fault)
  c={'docker inspect app':[0,''],'ps -eo args=':[0,'' if fault=='none' else 'nginx: worker process is shutting down\n']}
  if fault=='ps_error':c['ps -eo args=']=[70,'']
  script='set -Eeuo pipefail\napplication_connection_count() { printf "0\\n"; }\n'+P['scripts']['drain']+'\nresult=$(wait_for_application_drain app 0)\n[[ $result == '+{'none':'0','worker':'timeout','ps_error':'unknown'}[fault]+' ]]\nprintf "fixture_success=true\\n"\n'
  run(home,script,c,True,mutations=0)
 for index,gate in enumerate(P['scripts']['log_gates']):
  for fault in ('none','fatal','docker_error'):
   home=case('logs_'+str(index)+'_'+fault)
   c={'docker logs --since 15m app':[0,''],'docker logs --since 5m sub2api':[0,'']}
   for key in c:c[key]=[70 if fault=='docker_error' else 0,'fatal startup\n' if fault=='fatal' else 'startup ready\n']
   script='set -Eeuo pipefail\ncandidate_container=app\n'+P['scripts']['log_count']+'\n'+gate+'\nprintf "fixture_success=true\\n"\n'
   run(home,script,c,fault=='none',mutations=0)
finally:
 for handle in reversed(locks):fcntl.flock(handle,fcntl.LOCK_UN);handle.close()
for name,field,total in (('candidate_full','vm_preserve_candidate_integration',97),('migration_full','vm_preserve_migration_integration',456)):
 if name not in P['scripts']:continue
 phase=name
 result=subprocess.run(['bash','-c',P['scripts'][name]],capture_output=True,text=True,timeout=120)
 values=dict(line.split('=',1) for line in result.stdout.splitlines() if '=' in line)
 assert result.returncode==0 and values.get(field)=='pass' and values.get('checks')==str(total) and values.get('cleanup')=='pass',(phase,'full')
 checks+=total
if 'backup_unit_test' in P['scripts']:
 phase='backup_unit_native'
 native=root/'backup-unit-native';scripts=native/'scripts';tests=scripts/'tests/release';maintenance=scripts/'maintenance/181'
 tests.mkdir(parents=True);maintenance.mkdir(parents=True);(native/'tmp').mkdir()
 test_path=tests/'test_backup_unit_observations.py';test_path.write_text(P['scripts']['backup_unit_test'])
 for name,text in P['scripts']['backup_unit_sources'].items():
  assert name in ('mask-backup-units.sh','restore-backup-units.sh')
  (maintenance/name).write_text(text)
 environment={**os.environ,'TMPDIR':str(native/'tmp'),'PYTHONDONTWRITEBYTECODE':'1'}
 result=subprocess.run([sys.executable,'-B',str(test_path)],env=environment,capture_output=True,text=True,timeout=120)
 assert result.returncode==0 and 'Ran 4 tests' in result.stderr and 'OK' in result.stderr and 'skipped' not in result.stderr,(phase,result.returncode,result.stderr)
 checks+=4
 if 'backup_unit_boundaries' in P['scripts']:
  phase='backup_unit_native_boundaries'
  boundary_path=tests/'independent_backup_unit_boundaries.py';boundary_path.write_text(P['scripts']['backup_unit_boundaries'])
  result=subprocess.run([sys.executable,'-B',str(boundary_path),str(maintenance)],env=environment,capture_output=True,text=True,timeout=120)
  assert result.returncode==0,(phase,result.returncode,result.stderr)
  receipt=json.loads(result.stdout)
  assert receipt['native_linux'] is True and receipt['no_remote'] is True and receipt['case_count']==6
  assert len(receipt['cases'])==6 and all(item['pass'] is True for item in receipt['cases'])
  assert receipt['source_sha256']=={name:hashlib.sha256(text.encode()).hexdigest() for name,text in P['scripts']['backup_unit_sources'].items()}
  checks+=6
print('errexit_family_integration=pass')
print('checks='+str(checks))
print('failure_phase=none')
'''


def script(*, full: bool = True, native_backup_boundaries: str | None = None) -> str:
    encoded = base64.b64encode(json.dumps(payload(full=full, native_backup_boundaries=native_backup_boundaries)).encode()).decode()
    # Quote helper is injected into the remote Python namespace; it never sees
    # production values, only paths beneath the newly created fixture root.
    python = ("import shlex\nshlex_quote=shlex.quote\nPAYLOAD=" + repr(encoded)
              + "\ntry:\n exec(" + repr(REMOTE) + ")\nexcept Exception:\n"
              + " print('errexit_family_integration=failed')\n"
              + " print('checks='+str(globals().get('checks',0)))\n"
              + " print('failure_phase='+str(globals().get('phase','initial')))\n")
    compile(python, '<errexit-family-fixture>', 'exec')
    return '''set -Eeuo pipefail
base=/opt/sub2api-deploy/release-input
test -d "$base"
test ! -L "$base"
base_realpath=$(realpath -e "$base")
test "$base_realpath" = "$base"
root=$(mktemp -d "$base/errexit-family-test.XXXXXXXX")
chmod 700 "$root"
cleanup_fixture() {
  test -d "$root" || return 80
  test ! -L "$root" || return 80
  fixture_root_realpath=$(realpath -e "$root") || return 80
  test "$fixture_root_realpath" = "$root" || return 80
  case "$root" in /opt/sub2api-deploy/release-input/errexit-family-test.*) ;; *) return 80 ;; esac
  rm -rf -- "$root" || return 80
  test ! -e "$root" || return 80
  printf 'cleanup=pass\\n'
}
trap cleanup_fixture EXIT
python3 - "$root" <<'FIXTURE_PYTHON'
''' + python + "\nFIXTURE_PYTHON\ncleanup_fixture\ntrap - EXIT\n"


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--guards-only', action='store_true')
    args = parser.parse_args()
    actual = script(full=not args.guards_only)
    # Keep the SSH command below the host's argument-size limit; the generated
    # non-secret fixture program is streamed over stdin rather than argv.
    values = SSHRunner().run_with_input('local_vm', 'bash -s', {'errexit_family_integration','checks','failure_phase','cleanup'}, actual.encode(), timeout=300).values
    print(json.dumps(values,sort_keys=True))
    added = len(payload(full=False)['scripts']['cleaner_captures']) + 1
    assert values == {'errexit_family_integration':'pass', 'checks':str((85 if args.guards_only else 642)+added), 'failure_phase':'none', 'cleanup':'pass'}, 'inert_fixture_unverified'


if __name__ == '__main__':
    main()
