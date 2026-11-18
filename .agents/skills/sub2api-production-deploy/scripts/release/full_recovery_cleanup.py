"""Locked, exact-namespace cleanup of a failed offline Full DR Guest.

This command does not issue recovery measurements, a Gate, or a deployment.
The immutable failure records remain in the session directory. A private
diagnostic archive and independently verified cleanup receipt precede release
of the persistent VM owner.
"""
from __future__ import annotations

import base64
from contextlib import contextmanager
import hashlib
import json
from pathlib import Path
import re
import shlex
import sys

from .atomic import atomic_write, canonical_json, read_replaceable_bytes
from .paths import RUN_ROOT, WORKSPACE, TRUSTED_VM_PUBLIC_KEY
from .process import process_identity
from .production_snapshot import decode_snapshot
from .recovery_bundle import expected_files, recovery_bundle_sha256, verify_recovery_bundle
from .ssh import SSHRunner
from .state import RunLock
from . import vm_lifecycle
from .backup_keys import default_root, outside_workspace, prepare_root, private_permissions


REMOTE = r'''
import base64,fcntl,hashlib,json,os,re,shutil,stat,subprocess,sys,time
from pathlib import Path
def need(ok,why):
 if not ok: raise RuntimeError(why)
def canon(value): return json.dumps(value,sort_keys=True,separators=(',',':'),ensure_ascii=True).encode()
def sha(raw): return hashlib.sha256(raw).hexdigest()
def secure(path,kind,modes):
 st=path.lstat()
 need(path.resolve()==path and not stat.S_ISLNK(st.st_mode),'linked_path')
 need((stat.S_ISDIR(st.st_mode) if kind=='dir' else stat.S_ISREG(st.st_mode)) and st.st_uid==st.st_gid==0 and stat.S_IMODE(st.st_mode) in modes,'unsafe_path')
 if kind=='file': need(st.st_nlink==1,'linked_file')
 return st
def read(path,modes=(0o400,0o600),limit=4194304):
 st=secure(path,'file',modes);need(st.st_size<=limit,'oversized_evidence')
 with os.fdopen(os.open(path,os.O_RDONLY|os.O_NOFOLLOW),'rb') as f:
  before=os.fstat(f.fileno());raw=f.read(limit+1);after=os.fstat(f.fileno())
 need((st.st_dev,st.st_ino,st.st_size,st.st_mtime_ns)==(before.st_dev,before.st_ino,before.st_size,before.st_mtime_ns)==(after.st_dev,after.st_ino,after.st_size,after.st_mtime_ns) and len(raw)==st.st_size,'evidence_changed')
 return raw
def run(args):
 p=subprocess.run(args,capture_output=True,timeout=45,check=False)
 need(p.returncode==0,'probe_failed');return p.stdout
def emit(value):
 print(base64.b64encode(canon(value)).decode(),flush=True)
def old_holder_exited(identity):
 old=identity['remote'];need(Path('/proc/sys/kernel/random/boot_id').read_text().strip()==old['boot_id'],'boot_changed')
 try:
  fields=Path('/proc',str(old['pid']),'stat').read_text().rsplit(')',1)[1].split()
 except FileNotFoundError: return
 need(fields[19]!=old['starttime'] or fields[0] in ('Z','X','x'),'old_holder_running')
def unit_state():
 value=dict(line.split('=',1) for line in run(['systemctl','show',unit,'--property=LoadState,ActiveState,SubState,MainPID,ControlGroup']).decode().splitlines())
 need(value['LoadState']=='loaded' and value['ActiveState'] in ('failed','inactive') and value['MainPID']=='0','guest_active')
 group=value['ControlGroup']
 need(not group or (group.startswith('/system.slice/') and '..' not in group.split('/')),'invalid_cgroup')
 if group:
  cgroup=Path('/sys/fs/cgroup')/group.lstrip('/')
  need(cgroup.is_dir() and cgroup.resolve()==cgroup,'guest_cgroup_uncertain')
  for path in cgroup.rglob('cgroup.procs'): need(not path.read_text().strip(),'guest_cgroup_active')
 return value
def no_references():
 namespaces=set();count=0
 def under(value): return value==str(root) or value.startswith(str(root)+'/') or value==str(root)+' (deleted)'
 for proc in Path('/proc').iterdir():
  if not proc.name.isdigit(): continue
  try:
   need(drill.encode() not in (proc/'cmdline').read_bytes(),'guest_process_active')
   namespace=os.readlink(proc/'ns/mnt')
   if namespace not in namespaces:
    for line in (proc/'mountinfo').read_text().splitlines():
     parts=line.split();need(len(parts)>=10,'mount_record_invalid')
     for field in parts[3:5]: need(not under(re.sub(r'\\([0-7]{3})',lambda m:chr(int(m[1],8)),field)),'guest_mounted')
    namespaces.add(namespace)
   for path in [*(proc/'fd').iterdir(),*(proc/name for name in ('root','cwd','exe'))]:
    try: target=os.readlink(path)
    except FileNotFoundError: continue
    need(not under(target),'guest_referenced')
   for line in (proc/'maps').read_text().splitlines():
    fields=line.split(None,5)
    if len(fields)==6:need(not under(fields[5]),'guest_mapped')
   count+=1
  except (FileNotFoundError,ProcessLookupError):
   if proc.exists():
    # Linux kernel threads and exited zombies have no userspace namespace,
    # cwd or exe. Prove that state instead of treating ENOENT as an exit.
    try:
     fields=(proc/'stat').read_text().rsplit(')',1)[1].split()
     need(fields[0] in ('Z','X','x') or int(fields[6]) & 0x00200000,'process_scan_uncertain')
     need(not (proc/'cmdline').read_bytes() and not list((proc/'fd').iterdir()),'process_scan_uncertain')
     for name in ('root','cwd','exe'):
      try:os.readlink(proc/name)
      except FileNotFoundError:continue
      raise RuntimeError('process_scan_uncertain')
    except (FileNotFoundError,ProcessLookupError):need(not proc.exists(),'process_scan_uncertain')
 need(count>0 and namespaces,'empty_process_scan')
def verify_helper():
 helper=Path(data['helper_path']);secure(helper,'dir',(0o700,))
 actual=set()
 for directory,dirs,files in os.walk(helper,followlinks=False):
  secure(Path(directory),'dir',(0o700,))
  for name in files:
   path=Path(directory)/name;relative=path.relative_to(helper).as_posix()
   need(relative in data['helper_files'] and sha(read(path,(0o400,0o500)))==data['helper_files'][relative],'helper_changed');actual.add(relative)
 need(actual==set(data['helper_files']),'helper_incomplete')

def verify_source():
 manifest=read(root/'opt/sub2api-full-recovery-input/guest-manifest.json')
 need(sha(manifest)==data['manifest_sha256']==data['authorization']['guest_manifest_sha256'],'manifest_changed')
 m=json.loads(manifest);need(m['drill_id']==drill and m['release_id']==release,'manifest_identity_changed')
 auth=read(base/'drill-authorization.json')
 need(auth==read(root/'opt/sub2api-full-recovery-input/authorization.json'),'guest_authorization_changed')
 verify_authorization(auth)

def verify_authorization(auth=None):
 if auth is None:auth=read(base/'drill-authorization.json')
 envelope=json.loads(auth);need(envelope['payload']==data['authorization'],'authorization_changed')
 pub=read(Path('/opt/sub2api-release-signer/vm-gate-ed25519.pub'),(0o400,0o444,0o600,0o644))
 need(sha(pub)==data['trust_sha256'],'trust_changed')
 from cryptography.hazmat.primitives import serialization
 from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PublicKey
 key=serialization.load_pem_public_key(pub);need(isinstance(key,Ed25519PublicKey),'wrong_key_type')
 sig=envelope['signature'];payload=envelope['payload']
 need(sig['algorithm']=='Ed25519' and sig['key_sha256']==payload['trusted_public_key_sha256']==data['trust_sha256'],'wrong_signing_domain')
 key.verify(base64.b64decode(sig['value'],validate=True),canon(payload))
 need(payload['target_commit']==data['identity']['target_commit'] and payload['drill_id']==drill and payload['release_id']==release and payload['recovery_helper_bundle_sha256']==data['identity']['bundle_sha256'],'wrong_signed_identity')
 need(payload['created_at']<=data['started_at']<=payload['expires_at'],'authorization_invalid_at_launch')
def diagnostics():
 out=root/'opt/sub2api-full-recovery';rows={}
 paths=[out/'failure.json',*sorted(out.glob('stage-*'))]
 cp=root/'opt/sub2api/releases'/release/'recovery-checkpoints'
 if cp.exists(): paths.extend(sorted(path for path in cp.iterdir() if path.suffix in ('.started','.done')))
 for path in paths:
  need(re.fullmatch(r'failure\.json|stage-[a-z_]+|[a-z_]+\.(started|done)',path.name),'unexpected_diagnostic')
  need(path.name not in rows,'duplicate_diagnostic');rows[path.name]=read(path,limit=65536)
 need(len(rows)<=40 and sum(map(len,rows.values()))<=262144,'diagnostics_too_large')
 failure=json.loads(rows['failure.json'])
 need(failure.get('schema')==1 and failure.get('status')=='blocked' and failure.get('stage')=='guest_restore_phases' and failure.get('reason_code')=='storage_exhausted','unsupported_failure')
 return rows
def inspect():
 old_holder_exited(data['identity']);state=unit_state();no_references()
 if not os.path.lexists(root): return {'rootfs':None,'diagnostics':{},'failure':None}
 info=secure(root,'dir',(0o700,));verify_source();rows=diagnostics()
 for name in ('measurements.json','candidate-load.json','full-recovery-review.json','guest-measurements.json'):
  need(not os.path.lexists(base/name),'success_evidence_present')
 need(not os.path.lexists(root/'opt/sub2api-full-recovery/measurements.json'),'success_evidence_present')
 unit_state();no_references();after=secure(root,'dir',(0o700,))
 need((info.st_dev,info.st_ino)==(after.st_dev,after.st_ino),'rootfs_changed')
 return {'rootfs':{'path':str(root),'device':info.st_dev,'inode':info.st_ino,'uid':info.st_uid,'gid':info.st_gid,'mode':stat.S_IMODE(info.st_mode)},'diagnostics':{name:base64.b64encode(raw).decode() for name,raw in rows.items()},'failure':json.loads(rows['failure.json'])}
def foreign():
 # Import only from the already verified, immutable closure. No candidate or
 # historical evidence is interpreted as Python.
 helper=Path(data['helper_path']);sys.path.insert(0,str(helper))
 import full_recovery_drill as api
 digest=api.foreign_resources()
 dev=[]
 for cid in run(['docker','ps','-aq','--no-trunc']).decode().splitlines():
  item=json.loads(run(['docker','inspect','--type','container',cid]))[0]
  for mount in item['Mounts']:
   if any('data-dev' in str(mount.get(key,'')) for key in ('Source','Destination','Name')):
    source=Path(mount['Source']);info=source.lstat()
    need(source.resolve()==source and stat.S_ISDIR(info.st_mode),'data_dev_identity_uncertain')
    inventory=[]
    for item in sorted(source.iterdir()):
     st=item.lstat();inventory.append([item.name,st.st_dev,st.st_ino,st.st_mode,st.st_uid,st.st_gid])
     need(len(inventory)<=20000,'data_dev_inventory_too_large')
    dev.append([cid,mount,[info.st_dev,info.st_ino,info.st_mode,info.st_uid,info.st_gid],inventory])
 directory=Path('/opt/sub2api-deploy/data-dev');info=directory.lstat()
 need(directory.resolve()==directory and stat.S_ISDIR(info.st_mode),'data_dev_identity_uncertain')
 inventory=[]
 for item in sorted(directory.iterdir()):
  st=item.lstat();inventory.append([item.name,st.st_dev,st.st_ino,st.st_mode,st.st_uid,st.st_gid])
  need(len(inventory)<=20000,'data_dev_inventory_too_large')
 dev.append([str(directory),[info.st_dev,info.st_ino,info.st_mode,info.st_uid,info.st_gid],inventory])
 return {'docker':digest,'data_dev':sha(canon(sorted(dev,key=canon)))}
def preserve_and_delete(expected,expected_diagnostics):
 before=inspect();need(before['rootfs']==expected,'rootfs_plan_changed')
 fbefore=foreign();rows=diagnostics()
 need({name:{'sha256':sha(raw),'bytes':len(raw)} for name,raw in rows.items()}==expected_diagnostics,'diagnostics_changed')
 archive=base/'failure-diagnostics'
 if not archive.exists(): archive.mkdir(mode=0o700)
 secure(archive,'dir',(0o700,))
 preserved={**rows}
 for name in ('guest.raw.log','run-result.json','foreign-before.fingerprints.json'):
  path=base/name
  if path.exists(): preserved['host-'+name]=read(path,limit=33554432)
 helper_log=root/'opt/sub2api-full-recovery/helpers.raw.log'
 if helper_log.exists(): preserved['helpers.raw.log']=read(helper_log,limit=33554432)
 inventory={}
 for name,raw in preserved.items():
  path=archive/name
  if path.exists(): need(read(path,limit=33554432)==raw,'archive_changed')
  else:
   fd=os.open(path,os.O_WRONLY|os.O_CREAT|os.O_EXCL|os.O_NOFOLLOW,0o400)
   with os.fdopen(fd,'wb') as stream: stream.write(raw);stream.flush();os.fsync(stream.fileno())
  need(read(path,limit=33554432)==raw,'archive_readback_failed');inventory[name]={'bytes':len(raw),'sha256':sha(raw)}
 afd=os.open(archive,os.O_RDONLY|os.O_DIRECTORY|os.O_NOFOLLOW)
 try:os.fsync(afd)
 finally:os.close(afd)
 # A durable remote intent distinguishes deletion from evidence publication.
 intent={'schema':1,'rootfs':expected,'archive_sha256':sha(canon(inventory)),'foreign_before_sha256':fbefore['docker']}
 ipath=base/'failed-cleanup-intent.json'
 if ipath.exists(): need(read(ipath)==canon(intent),'cleanup_intent_changed')
 else:
  with os.fdopen(os.open(ipath,os.O_WRONLY|os.O_CREAT|os.O_EXCL|os.O_NOFOLLOW,0o400),'wb') as stream: stream.write(canon(intent));stream.flush();os.fsync(stream.fileno())
 bfd=os.open(base,os.O_RDONLY|os.O_DIRECTORY|os.O_NOFOLLOW)
 try:os.fsync(bfd)
 finally:os.close(bfd)
 again=inspect();need(again['rootfs']==expected and foreign()==fbefore,'cleanup_precondition_changed')
 need(diagnostics()==rows,'diagnostics_changed')
 need(getattr(shutil.rmtree,'avoids_symlink_attacks',False),'unsafe_rmtree_platform')
 shutil.rmtree(root)
 need(not os.path.lexists(root),'rootfs_delete_incomplete')
 fafter=foreign();need(fafter==fbefore,'foreign_resource_changed')
 receipt={'schema':1,'rootfs':expected,'status':'deleted','archive_sha256':sha(canon(inventory)),'foreign_before_sha256':fbefore['docker'],'foreign_after_sha256':fafter['docker'],'completed_at':int(time.time())}
 path=base/'failed-cleanup-receipt.json'
 with os.fdopen(os.open(path,os.O_WRONLY|os.O_CREAT|os.O_EXCL|os.O_NOFOLLOW,0o400),'wb') as stream:stream.write(canon(receipt));stream.flush();os.fsync(stream.fileno())
 bfd=os.open(base,os.O_RDONLY|os.O_DIRECTORY|os.O_NOFOLLOW)
 try:os.fsync(bfd)
 finally:os.close(bfd)
 need(read(path)==canon(receipt),'cleanup_receipt_unproven')
 return receipt
try:
 need(os.geteuid()==0,'root_required');os.umask(0o077)
 data=json.loads(sys.stdin.readline());identity=data['identity'];drill=identity['drill_id'];release=identity['release_id']
 need(re.fullmatch(r'full-dr-[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}',drill) and re.fullmatch(r'[0-9]{3}-[0-9a-f]{12}-[0-9]+-[0-9a-f]{8}',release),'invalid_identity')
 base=Path('/var/lib/sub2api-full-recovery-drills')/drill;root=base/'rootfs';unit='sub2api-'+drill
 for path,mode in ((Path('/'),0o755),(Path('/var'),0o755),(Path('/var/lib'),0o755),(base.parent,0o700),(base,0o700)):secure(path,'dir',(mode,))
 old_holder_exited(identity);held=[]
 for number,path,mode in ((9,Path('/opt/sub2api-deploy/release-gates/release.lock'),fcntl.LOCK_EX),(8,Path('/usr/local/libexec/.sub2api-release-unit.lock'),fcntl.LOCK_SH)):
  before=secure(path,'file',(0o600,0o644) if number==9 else (0o600,));old=identity['remote']['locks'][str(number)]
  need((before.st_dev,before.st_ino)==(old['device'],old['inode']),'shared_lock_identity_changed')
  fd=os.open(path,os.O_RDWR|os.O_NOFOLLOW);fcntl.flock(fd,mode|fcntl.LOCK_NB);after=os.fstat(fd)
  need((after.st_dev,after.st_ino)==(before.st_dev,before.st_ino),'lock_open_changed');held.append(fd)
 verify_helper();verify_authorization()
 emit({'status':'held'})
 for line in sys.stdin:
  request=json.loads(line);command=request['command']
  if command=='inspect': result=inspect()
  elif command=='foreign': result=foreign()
  elif command=='cleanup':result=preserve_and_delete(request['rootfs'],request['diagnostics'])
  elif command=='close':
   for fd in reversed(held):fcntl.flock(fd,fcntl.LOCK_UN);os.close(fd)
   emit({'status':'released'});break
  else:raise RuntimeError('unknown_command')
  emit({'result':result})
except Exception as error:
 why=str(error);why=why if re.fullmatch('[a-z0-9_]{1,100}',why) else type(error).__name__
 emit({'blocked':why});sys.exit(1)
'''


def _json(directory: Path, name: str) -> dict:
    return json.loads(read_replaceable_bytes(directory / name, max_bytes=4 * 1024 * 1024))


class FailureAdapter:
    def __init__(self, directory: Path, owner_path: Path, session: dict):
        self.directory, self.owner_path, self.session = directory, owner_path, session
        self.identity = session['identity']
        self.runner = SSHRunner(require_environment=True)
        self.settings = vm_lifecycle.load_settings()
        if self.settings is None or self.settings.identity != self.identity['vm_identity']:
            raise RuntimeError('full_dr_vm_configuration_changed')
        self.stdin = self.stdout = self.stderr = self.client = None
        self.last_diagnostics: dict[str, bytes] = {}
        self.expected_diagnostics: dict[str, dict] | None = None

    @contextmanager
    def locks(self):
        with RunLock(RUN_ROOT / '.release.lock', require_existing=True), vm_lifecycle.shared_lock(self.settings, self.owner_path.parent):
            if process_identity(self.identity['pid']) == self.identity['process_token']:
                raise RuntimeError('full_dr_original_holder_alive')
            payload = self._payload()
            self.client = self.runner.connect('local_vm')
            held = False
            try:
                self.stdin, self.stdout, self.stderr = self.client.exec_command('python3 -I -B -c ' + shlex.quote(REMOTE), get_pty=False, timeout=900)
                self.stdout.channel.settimeout(600)
                self.stdin.write(json.dumps(payload) + '\n'); self.stdin.flush()
                ready = self._read()
                if ready != {'status': 'held'}:
                    raise RuntimeError('full_dr_remote_locks_unproven')
                held = True
                yield
            finally:
                failed = sys.exc_info()[0] is not None
                try:
                    if held:
                        self.stdin.write('{"command":"close"}\n'); self.stdin.flush()
                        if self._read() != {'status': 'released'}:
                            raise RuntimeError('full_dr_remote_lock_release_unproven')
                except Exception:
                    if not failed:
                        raise
                finally:
                    self.client.close()

    def _payload(self):
        bundle = _json(self.directory, 'helper/recovery-helper-bundle.json')
        verify_recovery_bundle(self.directory / 'helper', bundle)
        if recovery_bundle_sha256(bundle) != self.identity['bundle_sha256'] or bundle['commit_sha'] != self.identity['target_commit']:
            raise RuntimeError('full_dr_original_bundle_changed')
        request = _json(self.directory, 'offline-request.json')
        request = request.get('payload', request)
        trust = hashlib.sha256(TRUSTED_VM_PUBLIC_KEY.read_bytes()).hexdigest()
        if request['trusted_public_key_sha256'] != trust or request['target_commit'] != self.identity['target_commit']:
            raise RuntimeError('full_dr_pinned_trust_or_target_changed')
        manifest_sha = hashlib.sha256(read_replaceable_bytes(self.directory / 'guest-manifest.json')).hexdigest()
        if request['guest_manifest_sha256'] != manifest_sha or request['recovery_helper_bundle_sha256'] != self.identity['bundle_sha256']:
            raise RuntimeError('full_dr_signed_closure_changed')
        stage = _json(self.directory, 'helper-stage.json')['remote']
        if not re.fullmatch(r'/opt/sub2api-full-recovery-input/review-stage\.[A-Za-z0-9]+', stage):
            raise RuntimeError('full_dr_helper_stage_invalid')
        return {'identity': self.identity, 'manifest_sha256': manifest_sha,
                'helper_path': stage + '/helper', 'helper_files': expected_files(bundle), 'authorization': request,
                'trust_sha256': trust, 'started_at': _json(self.directory, 'drill-launch-receipt.json')['started_at']}

    def _read(self):
        line = self.stdout.readline(1024 * 1024 + 1)
        if not line or len(line) > 1024 * 1024:
            raise RuntimeError('full_dr_cleanup_transport_uncertain')
        value = json.loads(base64.b64decode(line.strip(), validate=True))
        if 'blocked' in value:
            raise RuntimeError('full_dr_cleanup_' + value['blocked'])
        return value

    def _call(self, command, **kwargs):
        self.runner.assert_configuration_current()
        self.stdin.write(json.dumps({'command': command, **kwargs}) + '\n'); self.stdin.flush()
        return self._read()['result']

    def _production(self):
        from .vm_pre_gate_preserve import _production_probe_script
        identifier = self.identity['release_id']
        fields = {'production_pre_gate_preserve', 'production_claim_absent', 'production_state_absent',
                  'production_candidate_absent', 'production_health', 'production_image_id',
                  'production_route_sha256', 'production_active_slot_sha256', 'production_nginx',
                  'production_backup_timer', 'snapshot_b64'}
        value = self.runner.run('racknerd', _production_probe_script(identifier), fields, timeout=120).values
        expected = {'production_pre_gate_preserve': 'verified', 'production_claim_absent': 'true',
                    'production_state_absent': 'true', 'production_candidate_absent': 'true',
                    'production_health': 'healthy', 'production_nginx': 'active', 'production_backup_timer': 'enabled'}
        if any(value.get(key) != item for key, item in expected.items()):
            raise RuntimeError('full_dr_production_side_effects_unproven')
        snapshot = decode_snapshot(value['snapshot_b64'])
        original = _json(self.directory, 'source-snapshot.json')
        if snapshot != original:
            raise RuntimeError('full_dr_production_baseline_changed')
        return hashlib.sha256(canonical_json(snapshot)).hexdigest()

    def observe(self, identity):
        if identity != self.identity or process_identity(identity['pid']) == identity['process_token']:
            raise RuntimeError('full_dr_holder_identity_changed')
        observed = self._call('inspect')
        self.last_diagnostics = {name: base64.b64decode(raw, validate=True) for name, raw in observed['diagnostics'].items()}
        production = self._production()
        return {'schema': 1, 'identity': identity, 'local_holder': 'exited', 'remote_holder': 'exited',
                'unit': 'inactive', 'guest_processes': 0, 'mount_references': 0, 'process_references': 0,
                'rootfs': observed['rootfs'], 'production_before_sha256': production, 'production_now_sha256': production}

    def foreign(self):
        remote = self._call('foreign')
        return {'docker': remote['docker'], 'data_dev': remote['data_dev'], 'backup': self._backup(), 'production': self._production()}

    def _backup(self):
        # Content is never exported. Bind the existing backup root's identity
        # and immediate inventory without reading backup payloads or secrets.
        script = r'''import hashlib,json,os,pwd,stat,sys
from pathlib import Path
user=pwd.getpwnam(json.loads(sys.stdin.readline())['user'])
p=Path('/srv/sub2api-backups');s=p.lstat()
assert p.resolve()==p and stat.S_ISDIR(s.st_mode) and (s.st_uid,s.st_gid)==(user.pw_uid,user.pw_gid) and stat.S_IMODE(s.st_mode)==0o700
items=[]
for x in sorted(p.iterdir()):
 a=x.lstat();items.append((x.name,a.st_dev,a.st_ino,a.st_mode,a.st_size,a.st_mtime_ns));assert len(items)<=20000
raw=json.dumps([s.st_dev,s.st_ino,s.st_mode,items],sort_keys=True,separators=(',',':')).encode()
print('fingerprint='+hashlib.sha256(raw).hexdigest())
'''
        environment = self.runner.deployment_environment
        if environment is None:
            raise RuntimeError('full_dr_deployment_configuration_missing')
        environment.assert_current()
        user = environment.deployment['backup_upload']['user']
        return self.runner.run_with_input('backup', 'python3 -I -B -c ' + shlex.quote(script), {'fingerprint'},
                                          canonical_json({'user': user}) + b'\n', timeout=90).values['fingerprint']

    def cleanup(self, rootfs):
        if not self.expected_diagnostics:
            raise RuntimeError('full_dr_planned_diagnostics_unproven')
        receipt = self._call('cleanup', rootfs=rootfs, diagnostics=self.expected_diagnostics)
        if (set(receipt) != {'schema', 'rootfs', 'status', 'archive_sha256', 'foreign_before_sha256', 'foreign_after_sha256', 'completed_at'}
                or type(receipt['schema']) is not int or receipt['schema'] != 1 or receipt['rootfs'] != rootfs
                or receipt['status'] != 'deleted' or type(receipt['completed_at']) is not int
                or not all(re.fullmatch('[0-9a-f]{64}', receipt[key]) for key in ('archive_sha256', 'foreign_before_sha256', 'foreign_after_sha256'))
                or receipt['foreign_before_sha256'] != receipt['foreign_after_sha256']):
            raise RuntimeError('full_dr_remote_cleanup_receipt_invalid')
        raw = canonical_json(receipt)
        path = self.directory / 'failed-closure-remote-receipt.json'
        if path.exists():
            if read_replaceable_bytes(path) != raw:
                raise RuntimeError('full_dr_remote_cleanup_receipt_changed')
        else:
            atomic_write(path, raw)
        if read_replaceable_bytes(path) != raw:
            raise RuntimeError('full_dr_remote_cleanup_receipt_unproven')
        return {'schema': 1, 'rootfs': rootfs, 'status': 'deleted', 'remote_receipt_sha256': hashlib.sha256(raw).hexdigest()}


def command(args):
    from . import full_recovery_session as api
    directory = Path(args.directory).resolve(strict=True)
    if WORKSPACE not in directory.parents or directory.parent != WORKSPACE / '.tmp':
        raise RuntimeError('full_dr_session_directory_invalid')
    settings = vm_lifecycle.load_settings()
    if settings is None:
        raise RuntimeError('full_dr_managed_vm_required')
    owner = vm_lifecycle._shared_dir(settings) / 'owner.json'
    if args.plan_sha256:
        if not isinstance(args.plan_sha256, str) or re.fullmatch('[0-9a-f]{64}', args.plan_sha256) is None:
            raise RuntimeError('full_dr_plan_digest_invalid')
        raw = read_replaceable_bytes(directory / 'failed-closure-plan.json', max_bytes=16384)
        if hashlib.sha256(raw).hexdigest() != args.plan_sha256:
            raise RuntimeError('full_dr_plan_digest_changed')
        session = json.loads(raw)['session']
    else:
        session = api.inspect_session(directory, owner)
    drill = session['identity']['drill_id']
    if not isinstance(drill, str) or re.fullmatch(r'full-dr-[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}', drill) is None:
        raise RuntimeError('full_dr_archive_identity_invalid')
    archive = default_root().parent / 'failed-full-dr' / drill
    outside_workspace(archive)
    prepare_root(archive)
    for path in archive.iterdir():
        private_permissions(path)
    existing = set(archive.iterdir())
    adapter = FailureAdapter(directory, owner, session)
    if args.plan_sha256:
        result = api.apply_failed_closure(directory, owner, args.plan_sha256, adapter, archive_dir=archive)
    else:
        with adapter.locks():
            observed = adapter.observe(session['identity'])
            foreign = adapter.foreign()
            digest = api.prepare_failure_archive(directory, session, observed, adapter.last_diagnostics,
                                                 archive_dir=archive, foreign=foreign)
            for path in archive.iterdir():
                private_permissions(path, initialize=path not in existing)
            result = {'status': 'planned', 'plan_sha256': digest, 'archive_directory': str(archive)}
    print(canonical_json(result).decode('ascii'))
    return result
