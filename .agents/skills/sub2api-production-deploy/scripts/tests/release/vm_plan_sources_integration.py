"""Run the actual VM migration-plan/hook/evidence shell in an inert fixture.

Only fake Docker and semantic hooks run. The remote entry holds both existing
VM execution locks and removes only its unique release-input subtree.
"""
from __future__ import annotations

import base64
import hashlib
import json
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT))
from release.ssh import SSHRunner


def payload() -> dict:
    source = (ROOT / 'release/vm-validate.sh').read_text(encoding='utf-8')
    predicate_start = source.index('required_jq_predicate() {')
    predicate_end = source.index('\n}\n', predicate_start) + 3
    start = source.index('  mark_v2_stage migration_plan_before\n')
    end = source.index('  if [[ "$recovery_gate_mode" != fast ]]; then\n    mark_v2_stage old_image_health', start)
    evidence_start = source.index('  pending_json=', end)
    evidence_end = source.index('  printf \'candidate_image_id=', evidence_start)
    return {
        'predicate': source[predicate_start:predicate_end],
        'migration': source[start:end],
        'evidence': source[evidence_start:evidence_end],
        'validator_sha256': hashlib.sha256(source.encode()).hexdigest(),
    }


REMOTE = r'''
import base64, copy, fcntl, hashlib, json, os, pathlib, stat, subprocess, sys
P=json.loads(base64.b64decode(PAYLOAD))
root=pathlib.Path(sys.argv[1]).resolve()
assert root.parent == pathlib.Path('/opt/sub2api-deploy/release-input') and root.name.startswith('vm-plan-sources-test.')
checks=0
phase='initial'
catalog='a'*64
policy='b'*64
special={285:'285_upstream_null_rate_lifecycle.sql',297:'297_upstream_cache_guard_account_scope.sql',298:'298_group_scheduling_protection_policies.sql',299:'299_upstream_cache_guard_revisions.sql'}
names=[special.get(i,str(i)+'_fixture.sql') for i in range(285,301)]
items=[{'filename':n,'checksum':hashlib.sha256(n.encode()).hexdigest()} for n in names]
def plan(pending=()):
 pending=list(pending)
 return {'pending':pending,'existing':[{**i,'status':'existing'} for i in items if i not in pending], 'conflicts':[], 'unknown':[], 'existing_checksums_verified':True,'catalog_sha256':catalog,'checksum_policy_sha256':policy,'database_high_watermark':284 if pending else 300}

def write(path,text='',mode=0o600):
 path.parent.mkdir(parents=True,exist_ok=True)
 path.write_text(text);path.chmod(mode)

docker=r''' + '"""' + r'''#!/usr/bin/python3
import json,os,pathlib,sys
args=sys.argv[1:];home=pathlib.Path(os.environ['FIXTURE_HOME']);c=json.loads((home/'config.json').read_text())
with (home/'docker-ledger').open('a') as f:f.write(json.dumps(args)+'\n')
if '--migration-plan-snapshot-json' in args:key='production'
elif '--migration-plan-json' in args:
 count=int((home/'apply-count').read_text()) if (home/'apply-count').exists() else 0
 key='probe' if count==0 else ('bootstrap_after' if c.get('mode','fast')=='fast' and count==1 else 'after')
elif '--migration-apply-plan-json' in args:
 mount=next(v for v in args if v.endswith(':/input/migration-plan.json:ro'))
 actual=json.loads(pathlib.Path(mount.rsplit(':',2)[0]).read_text())
 count=int((home/'apply-count').read_text()) if (home/'apply-count').exists() else 0
 expected=c['probe'] if count==0 else c['bootstrap_after']
 if actual!=expected:sys.exit(93)
 with (home/'applied-plans.jsonl').open('a') as f:f.write(json.dumps(actual)+'\n')
 if c.get('apply_failure')==('bootstrap' if count==0 and c.get('mode','fast')=='fast' else 'regular'):sys.exit(71)
 schema=set(json.loads((home/'schema.json').read_text()))
 schema.update(i['filename'] for i in actual['pending'])
 if c.get('schema_omission'):schema={n for n in schema if not n.startswith(c['schema_omission'])}
 (home/'schema.json').write_text(json.dumps(sorted(schema)))
 (home/'apply-count').write_text(str(count+1))
 (home/'applied').write_text('yes');sys.exit(0)
else:sys.exit(97)
value=c[key]
if c.get('query_failure')==key:
 print(json.dumps(value));sys.exit(70)
print(json.dumps(value) if value is not None else '')
''' + '"""' + r'''
hook=r''' + '"""' + r'''#!/usr/bin/python3
import json,os,pathlib,sys
home=pathlib.Path(os.environ['FIXTURE_HOME']);c=json.loads((home/'config.json').read_text())
filename=pathlib.Path(os.environ['ASSERT_MIGRATION_FILE']).name
row=[filename,sys.argv[1],os.environ['MIGRATION_STATUS']]
# The existing semantic hooks require the schema introduced by predecessors:
# 297 needs 296's fact/runtime tables and 299 needs 297's upstream-key column.
# Bootstrap must complete before ANY verified hook, including migration 285.
if c['probe']['pending'] and not (home/'applied').exists():sys.exit(94)
if sys.argv[1]=='preflight' and os.environ['MIGRATION_STATUS']!='verified':sys.exit(95)
schema=json.loads((home/'schema.json').read_text())
if filename not in schema:sys.exit(96)
number=int(filename.split('_')[0])
dependencies={297:(296,),299:(296,297,298)}.get(number,())
if not all(any(n.startswith(str(dep)+'_') for n in schema) for dep in dependencies):sys.exit(96)
with (home/'hooks-ledger').open('a') as f:f.write(json.dumps(row)+'\n')
assert os.environ['ASSERT_DB_NAME']=='fixture_probe'
assert os.environ['ASSERT_VM_ISOLATED_DB']=='fixture_probe'
if c.get('hook_failure')==sys.argv[1]:sys.exit(72)
''' + '"""' + r'''
jq=r''' + '"""' + r'''#!/usr/bin/python3
import json,os,pathlib,sys
c=json.loads((pathlib.Path(os.environ['FIXTURE_HOME'])/'config.json').read_text())
if c.get('predicate_failure') and any('.pending | any(' in a for a in sys.argv):sys.exit(73)
os.execv('/usr/bin/jq',['jq']+sys.argv[1:])
''' + '"""' + r'''
signer=r''' + '"""' + r'''#!/usr/bin/python3
import os,pathlib,sys
home=pathlib.Path(os.environ['FIXTURE_HOME'])
(home/'signed').write_text('fixture-only-not-a-real-signature')
pathlib.Path(sys.argv[2]).write_text('fixture-only-not-a-real-signature')
''' + '"""' + r'''

def run(name,config,success,expected_pending=(),expected_status=None):
 global phase,checks
 phase=name
 home=root/name;home.mkdir(mode=0o700)
 for directory in ('state','output','bin','probe','source/backend/migrations','source/.agents/skills/sub2api-production-deploy/scripts/maintenance/release'):(home/directory).mkdir(parents=True,mode=0o700)
 write(home/'config.json',json.dumps(config))
 initial_probe=config['probe'] if isinstance(config['probe'],dict) else {}
 write(home/'schema.json',json.dumps([i['filename'] for i in initial_probe.get('existing',[])]))
 write(home/'bin/docker',docker,0o700);write(home/'bin/jq',jq,0o700);write(home/'bin/signer',signer,0o700)
 # Block unexpected external mutations even if future extracted code grows.
 for command in ('shutdown','systemctl','curl','ssh'):
  write(home/'bin'/command,'#!/bin/sh\nexit 98\n',0o700)
 hooks=home/'source/.agents/skills/sub2api-production-deploy/scripts/maintenance/release'
 for number in (195,232,233,239,242,243,244,245,254,285,297,298,299):write(hooks/('migration-'+str(number)+'-assert.sh'),'exec python3 "$FIXTURE_HOME/hook.py" "$@"\n',0o700)
 write(home/'hook.py',hook)
 write(home/'manifest.json',json.dumps({'profile':'273','catalog_sha256':catalog,'checksum_policy_sha256':policy,'production_snapshot_sha256':'c'*64,'migration_catalog':items}))
 write(home/'snapshot.json','{}')
 write(home/'probe/config.yaml','fixture: true\n')
 write(home/'state/candidate.tar.gz','fixture-archive')
 setup="""set -Eeuo pipefail
state_dir="$FIXTURE_HOME/state"
output_dir="$FIXTURE_HOME/output"
source_dir="$FIXTURE_HOME/source"
manifest="$FIXTURE_HOME/manifest.json"
plan_snapshot="$FIXTURE_HOME/snapshot.json"
probe_dir="$FIXTURE_HOME/probe"
candidate_image_id="sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
old_image_id="sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
profile=273
probe_network=fixture_network
probe_db=fixture_probe
probe_redis=fixture_redis
database_owner=fixture_owner
recovery_sha=fixture_recovery_sha
candidate_archive="$state_dir/candidate.tar.gz"
candidate_archive_sha="ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
candidate_size=15
vm_restore_verified=false
restore_points_verified=false
mark_v2_stage() { printf '%s\\n' "$1" > "$state_dir/stage"; }
"""
 setup+='recovery_gate_mode='+config.get('mode','fast')+'\n'
 script=setup+P['predicate']+'\n'+P['migration']+'\n'+P['evidence'].replace('/usr/local/libexec/sub2api-sign-gate','"$FIXTURE_HOME/bin/signer"')+'\nprintf "fixture_success=true\\n"\n'
 write(home/'actual.sh',script,0o700)
 env={**os.environ,'PATH':str(home/'bin')+':'+os.environ['PATH'],'FIXTURE_HOME':str(home)}
 result=subprocess.run(['bash',str(home/'actual.sh')],env=env,capture_output=True,text=True,timeout=20)
 assert (result.returncode==0)==success,(phase,'status',result.returncode)
 assert ('fixture_success=true' in result.stdout)==success,(phase,'success_sentinel')
 assert (home/'signed').exists()==success,(phase,'signed')
 assert (home/'output/gate.sig').exists()==success,(phase,'signature')
 if success:
  gate=json.loads((home/'output/gate.json').read_text())
  evidence=gate['evidence']['migration_evidence']
  assert evidence['pending']==list(expected_pending),(phase,'gate_production_pending')
  assert evidence['database_high_watermark']==config['production']['database_high_watermark'],(phase,'gate_highwater')
  plans=[json.loads(line) for line in (home/'applied-plans.jsonl').read_text().splitlines()]
  assert plans[0]==config['probe'],(phase,'apply_probe_plan')
  if config.get('mode','fast')=='fast':
   assert plans==[config['probe'],config['bootstrap_after']],(phase,'bootstrap_and_regular_plans')
   assert json.loads((home/'state/fixture-bootstrap-before.json').read_text())==config['probe'],(phase,'bootstrap_before')
   assert json.loads((home/'state/fixture-bootstrap-after.json').read_text())==config['bootstrap_after'],(phase,'bootstrap_after')
   assert (home/'state/fixture-bootstrap.exit').read_text().strip()=='0',(phase,'bootstrap_exit')
   assert (home/'state/fixture-bootstrap-after.exit').read_text().strip()=='0',(phase,'bootstrap_after_exit')
  else:assert plans==[config['probe']],(phase,'nonfast_regular_plan')
  rows=[json.loads(line) for line in (home/'hooks-ledger').read_text().splitlines()]
  for number,filename in special.items():
   assert rows.count([filename,'preflight',expected_status])==1,(phase,number,'preflight_status')
   for stage in ('postflight','vm_semantics','verified_replay'):
    assert rows.count([filename,stage,'verified'])==1,(phase,number,stage)
   assert gate['evidence']['migration_evidence']['migration_'+str(number)]['checksum']==next(i['checksum'] for i in items if i['filename']==filename),(phase,number,'gate_hook')
 checks+=1

locks=[]
try:
 for path in ('/usr/local/libexec/.sub2api-release-unit.lock','/opt/sub2api-deploy/release-gates/release.lock'):
  st=os.lstat(path);assert stat.S_ISREG(st.st_mode) and st.st_uid==st.st_gid==0 and st.st_nlink==1
  assert stat.S_IMODE(st.st_mode) in (0o600,0o644)
  handle=open(path,'r+');fcntl.flock(handle,fcntl.LOCK_EX|fcntl.LOCK_NB);locks.append(handle)
 base={'production':plan(),'probe':plan(items),'bootstrap_after':plan(),'after':plan()}
 run('fast_vm_sixteen_pending',base,True,expected_status='verified')
 same={'production':plan(),'probe':plan(),'bootstrap_after':plan(),'after':plan()}
 run('fast_same_existing',same,True,expected_status='verified')
 run('nonfast_same_existing',{**same,'mode':'specialized'},True,expected_status='verified')
 run('nonfast_mismatch',{**base,'mode':'specialized'},False)
 run('fast_production_pending',{**base,'production':plan(items)},False)
 for which in ('production','probe','bootstrap_after','after'):
  for fault in ('unknown','conflicts','unverified','catalog','policy','malformed_pending','empty','query'):
   c=copy.deepcopy(base)
   if fault in ('unknown','conflicts'):c[which][fault]=[{'filename':'unexpected.sql'}]
   elif fault=='unverified':c[which]['existing_checksums_verified']=False
   elif fault=='catalog':c[which]['catalog_sha256']='9'*64
   elif fault=='policy':c[which]['checksum_policy_sha256']='8'*64
   elif fault=='malformed_pending':c[which]['pending']={}
   elif fault=='empty':c[which]=None
   elif fault=='query':c['query_failure']=which
   run(which+'_'+fault,c,False)
 run('bootstrap_apply_failure',{**base,'apply_failure':'bootstrap'},False)
 run('regular_apply_failure',{**base,'apply_failure':'regular'},False)
 run('bootstrap_after_remaining_pending',{**base,'bootstrap_after':plan(items[:1])},False)
 run('after_remaining_pending',{**base,'after':plan(items[:1])},False)
 for stage in ('preflight','postflight','vm_semantics','verified_replay'):
  run('hook_'+stage+'_failure',{**base,'hook_failure':stage},False)
 run('pending_predicate_query_failure',{**base,'predicate_failure':True},False)
 run('bootstrap_missing_296_dependency',{**base,'schema_omission':'296_'},False)
 run('bootstrap_missing_297_dependency',{**base,'schema_omission':'297_'},False)
finally:
 for handle in reversed(locks):fcntl.flock(handle,fcntl.LOCK_UN);handle.close()
print('vm_plan_sources_integration=pass')
print('checks='+str(checks))
print('failure_phase=none')
'''

EXPECTED_CHECKS = 48


def script() -> str:
    encoded = base64.b64encode(json.dumps(payload()).encode()).decode()
    program = ('PAYLOAD=' + repr(encoded) + '\ntry:\n exec(' + repr(REMOTE)
               + ')\nexcept Exception:\n print("vm_plan_sources_integration=failed")\n'
               + ' print("checks="+str(globals().get("checks",0)))\n'
               + ' print("failure_phase="+str(globals().get("phase","initial")))\n')
    compile(program, '<vm-plan-sources-fixture>', 'exec')
    return '''set -Eeuo pipefail
base=/opt/sub2api-deploy/release-input
test -d "$base"
test ! -L "$base"
test "$(realpath -e "$base")" = "$base"
root=$(mktemp -d "$base/vm-plan-sources-test.XXXXXXXX")
chmod 700 "$root"
cleanup_fixture() {
  test -d "$root" || return 80
  test ! -L "$root" || return 80
  test "$(realpath -e "$root")" = "$root" || return 80
  test "$(stat -c '%u:%g:%a' "$root")" = 0:0:700 || return 80
  case "$root" in /opt/sub2api-deploy/release-input/vm-plan-sources-test.*) ;; *) return 80 ;; esac
  rm -rf -- "$root" || return 80
  test ! -e "$root" || return 80
  printf 'cleanup=pass\\n'
}
trap cleanup_fixture EXIT
python3 - "$root" <<'FIXTURE_PYTHON'
''' + program + "\nFIXTURE_PYTHON\ncleanup_fixture\ntrap - EXIT\n"


def main() -> None:
    values = SSHRunner().run_with_input(
        'local_vm', 'bash -s',
        {'vm_plan_sources_integration', 'checks', 'failure_phase', 'cleanup'},
        script().encode(), timeout=180,
    ).values
    print(json.dumps(values, sort_keys=True))
    assert values == {'vm_plan_sources_integration':'pass', 'checks':str(EXPECTED_CHECKS),
                      'failure_phase':'none', 'cleanup':'pass'}, 'inert_fixture_unverified'


if __name__ == '__main__':
    main()
