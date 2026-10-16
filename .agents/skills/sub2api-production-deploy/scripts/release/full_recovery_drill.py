"""Real encrypted baseline recovery runner; synthetic recovery_drill is unchanged."""
from __future__ import annotations
import argparse, base64, hashlib, importlib, json, os, re, shutil, stat, subprocess, sys, threading, time, uuid
from collections import Counter
from pathlib import Path, PurePosixPath
from typing import Any

BACKEND="nspawn-offline-private-users"
DRILL=re.compile(r"full-dr-[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}")
RELEASE=re.compile(r"[0-9]{3}-[0-9a-f]{12}-[0-9]+-[0-9a-f]{8}")
SHA=re.compile(r"[0-9a-f]{64}"); IMAGE=re.compile(r"sha256:[0-9a-f]{64}")
PHASES=("compose_restored","postgres_restored","redis_restored","app_healthy","nginx_restored","backup_units_restored","state_cleanup","claim_reconciled")
NAMESPACES=("mnt","pid","net","user","cgroup","uts","ipc")
ROOT=Path("/var/lib/sub2api-full-recovery-drills")
FIELDS=frozenset({"schema","drill_id","backend","fixture_kind","release_id","artifact_sha256","pre_image_id","recovery_helper_bundle_sha256","approved_identity_sha256","approved_recipient_sha256","artifact_path","identity_path","authorization_path","guest_manifest_path","guest_manifest_sha256","redis_health_budget_seconds"})
RECEIPT=frozenset({"schema","type","drill_id","created_at","completed_at","source","measurements","recovery_helper_bundle_sha256"})
CURRENT_GUEST_STAGE="guest_entry"
class FullDrillBlocked(RuntimeError): pass
def need(ok,msg):
    if not ok: raise FullDrillBlocked(msg)
def canon(v): return json.dumps(v,sort_keys=True,separators=(",",":"),ensure_ascii=True).encode()

def read_json(path,private=False):
    sf(path,private)
    need(path.stat().st_size<=4*1024*1024,"JSON size limit")
    def unique(pairs):
        result={}
        for key,value in pairs:
            need(key not in result,"duplicate JSON field")
            result[key]=value
        return result
    def no_fraction(value): raise FullDrillBlocked("noninteger JSON number")
    value=json.loads(path.read_bytes(),object_pairs_hook=unique,parse_constant=no_fraction,parse_float=no_fraction)
    need(isinstance(value,dict),"JSON object required")
    return value
def sha(p):
    h=hashlib.sha256()
    with open(p,"rb") as f:
        for b in iter(lambda:f.read(1024*1024),b""): h.update(b)
    return h.hexdigest()
def mod(n):
    if __package__: return importlib.import_module("."+n,__package__)
    if n=="recovery_evidence":
        # Formal execution aliases are flattened; the full relative-import
        # package remains in the already verified immutable sources closure.
        base=Path(__file__).resolve().parent
        sources=base/"sources/.agents/skills/sub2api-production-deploy/scripts"
        package_root=sources if sources.is_dir() else base.parent
        need((package_root/"release/recovery_evidence.py").is_file(),"verified evidence package missing")
        need(package_root.resolve()==package_root,"unsafe evidence package path")
        sys.path.insert(0,str(package_root))
        return importlib.import_module("release."+n)
    return importlib.import_module(n)
def sf(p,private=False):
    i=p.lstat(); need(stat.S_ISREG(i.st_mode) and i.st_nlink==1 and p.is_absolute() and p.resolve()==p,"unsafe file")
    if private: need(i.st_uid==0 and stat.S_IMODE(i.st_mode) in {0o400,0o600},"private file contract")
def sd(p):
    i=p.lstat(); need(stat.S_ISDIR(i.st_mode) and p.is_absolute() and p.resolve()==p and i.st_uid==0 and stat.S_IMODE(i.st_mode)==0o700,"unsafe directory")
def cfg(d):
    need(isinstance(d,dict) and set(d) in (FIELDS,FIELDS|{"postrelease_candidate"}),"full drill fields")
    need(type(d["schema"]) is int and d["schema"]==1 and d["backend"]==BACKEND and d["fixture_kind"]=="encrypted-production-baseline","synthetic fixture forbidden")
    need(isinstance(d["drill_id"],str) and DRILL.fullmatch(d["drill_id"]) and isinstance(d["release_id"],str) and RELEASE.fullmatch(d["release_id"]),"identity")
    for k in ("artifact_sha256","recovery_helper_bundle_sha256","approved_identity_sha256","approved_recipient_sha256","guest_manifest_sha256"): need(isinstance(d[k],str) and SHA.fullmatch(d[k]),"fingerprint")
    need(isinstance(d["pre_image_id"],str) and IMAGE.fullmatch(d["pre_image_id"]),"image")
    for k in ("artifact_path","identity_path","authorization_path","guest_manifest_path"): need(isinstance(d[k],str) and d[k].startswith("/") and ".." not in PurePosixPath(d[k]).parts and str(PurePosixPath(d[k]))==d[k] and "\\" not in d[k] and "\n" not in d[k] and "\x00" not in d[k],"Guest path")
    need(d["identity_path"].startswith("/opt/sub2api-full-recovery-input/"),"ephemeral Guest identity input")
    need(type(d["redis_health_budget_seconds"]) is int and 61<=d["redis_health_budget_seconds"]<=180,"Redis budget")
    if "postrelease_candidate" in d:
        candidate=d["postrelease_candidate"]
        need(candidate=={"archive":"/opt/sub2api-full-recovery-input/baseline/candidate.tar.gz","gate":"/opt/sub2api-full-recovery-input/baseline/gate.json","signature":"/opt/sub2api-full-recovery-input/baseline/gate.sig"},"postrelease candidate input paths")
    return d
def probe():
    tools={x:bool(shutil.which(x)) for x in ("systemd-nspawn","docker","ip","python3")}; b=["missing_"+x for x,v in tools.items() if not v]
    if not sys.platform.startswith("linux"): b.append("linux_host_required")
    elif os.geteuid()!=0: b.append("root_host_required")
    if sys.platform.startswith("linux") and not Path("/sys/fs/cgroup/cgroup.controllers").is_file(): b.append("unified_cgroup_required")
    return {"schema":1,"backend":BACKEND,"read_only":True,"tools":tools,"blockers":b,"status":"ready_for_plan" if not b else "blocked"}
def helper(d): return Path("/opt/sub2api/releases")/d["release_id"]/"recovery-helpers"/d["recovery_helper_bundle_sha256"]
def nspawn(root,d): return ["systemd-nspawn","--directory="+str(root),"--machine="+d["drill_id"],"--boot","--private-network","--private-users=pick","--settings=no","--register=no","--link-journal=no","--property=Delegate=yes"]
def nomount(root):
    for line in Path("/proc/self/mountinfo").read_text().splitlines():
        t=line.split(" ")[4].replace("\\040"," ").replace("\\134","\\"); need(t!=str(root) and not t.startswith(str(root)+"/"),"Guest mount remains")
def validate_root(root,d):
    need(root==ROOT/d["drill_id"]/"rootfs" and root.resolve()==root,"owned root")
    for p in (ROOT,root.parent,root): sd(p)
    nomount(root)
    for x in ("usr/lib/systemd/systemd","usr/bin/dockerd","usr/bin/docker","usr/sbin/nginx","usr/bin/python3","usr/sbin/ip","usr/bin/age","usr/bin/age-keygen"): need((root/x).is_file(),"Guest runtime")
    for k in ("artifact_path","identity_path","authorization_path","guest_manifest_path"): sf(root/d[k].lstrip("/"),True)
    for value in d.get("postrelease_candidate",{}).values(): sf(root/value.lstrip("/"),True)
    b=root/str(helper(d)).lstrip("/"); mod("recovery_drill").verify_helper_bundle(b,d["recovery_helper_bundle_sha256"]); sf(b/"full_recovery_drill.py"); need(sha(b/"full_recovery_drill.py")==sha(Path(__file__).resolve()),"collector drift")
    state=root/"opt/sub2api/backups/release-state"/d["release_id"]; need(not state.exists() and not state.is_symlink(),"preexisting recovery state")
    need(sha(root/d["guest_manifest_path"].lstrip('/'))==d["guest_manifest_sha256"],"Guest manifest drift")


def disable_optional_boot_services(root: Path) -> None:
    """Avoid boot retries before archived Guest configuration is restored."""
    for name in ("nginx.service", "systemd-resolved.service"):
        for parent in (
            root / "etc/systemd/system/multi-user.target.wants",
            root / "lib/systemd/system/multi-user.target.wants",
        ):
            link = parent / name
            if link.is_symlink():
                # Resolve links inside the archived rootfs.  An absolute unit
                # link such as /lib/systemd/system/nginx.service must be
                # mapped below ``root``; Path.resolve() would otherwise look
                # at the host's /lib and incorrectly classify a valid Guest
                # unit as dangling.
                raw_target = os.readlink(link)
                target = (root / raw_target.lstrip("/") if raw_target.startswith("/") else link.parent / raw_target).resolve()
                try:
                    target.relative_to(root)
                except ValueError:
                    need(False, "optional service target escaped Guest rootfs")
                need(target.is_file() and target.name == name, "unexpected optional service enablement")
                link.unlink()
def plan(root,d):
    cfg(d); p=probe()
    if p["blockers"]: return {**p,"drill_id":d["drill_id"],"required_resources":["offline authorization","approved age identity","encrypted baseline","formal helper bundle","dedicated nspawn rootfs"]}
    validate_root(root,d); return {"schema":1,"status":"planned","drill_id":d["drill_id"],"backend":BACKEND,"network":"offline_private_namespace","host_binds":[]}
def run(argv,env=None,timeout=1800): return subprocess.run(argv,capture_output=True,env=env,timeout=timeout,check=False)
def vals(argv,env,raw):
    r=run(argv,env)
    with open(raw,"ab") as stream: stream.write(r.stdout+r.stderr)
    out={}; events=[]
    for line in r.stdout.decode("utf8","replace").splitlines():
        m=re.fullmatch(r"recovery_checkpoint=([a-z_]+) status=(completed|reverified)",line)
        if m: need(m[1] in PHASES,"checkpoint event"); events.append((m[1],m[2]))
        k,s,v=line.partition("=")
        if s and re.fullmatch(r"[a-z_]{1,80}",k) and re.fullmatch(r"[A-Za-z0-9:._-]{1,160}",v): out[k]=v
    return r.returncode,out,events
def checkpoint(root,source,bundle,events):
    ident=(f"schema=1\nrelease_id={source['release_id']}\nrecovery_point_sha256={source['recovery_point_sha256']}\npre_image_id={source['pre_image_id']}\nrecovery_helper_bundle_sha256={bundle}\n").encode(); sf(root/"identity",True); need((root/"identity").read_bytes()==ident,"checkpoint identity")
    done=Counter(p for p,s in events if s=="completed"); rev=Counter(p for p,s in events if s=="reverified"); result={}
    for p in PHASES:
        x={"completed":done[p]==1,"live_reverified":rev[p]>=1,"apply_count":done[p],"resume_apply_count":max(done[p]-1,0),"binding_sha256":hashlib.sha256(ident).hexdigest()}
        for st in ("started","done"):
            q=root/(p+"."+st); sf(q,True); raw=q.read_bytes(); need(raw==ident+(f"phase={p}\n").encode(),"checkpoint bytes"); x[st+"_base64"]=base64.b64encode(raw).decode(); x[st+"_sha256"]=hashlib.sha256(raw).hexdigest()
        need(x["completed"] and x["live_reverified"],"checkpoint not measured"); result[p]=x
    return result
def guest_environment(d): return {"PATH":"/usr/sbin:/usr/bin:/sbin:/bin","LANG":"C.UTF-8","PYTHONDONTWRITEBYTECODE":"1","RELEASE_DIR":"/opt/sub2api/releases/"+d["release_id"],"RECOVERY_ASSETS_DIR":str(helper(d)),"RECOVERY_HELPER_BUNDLE_SHA256":d["recovery_helper_bundle_sha256"],"RECOVERY_DRILL_MODE":"1","RECOVERY_FULL_DRILL_ID":d["drill_id"],"RECOVERY_DRILL_AUTHORIZATION":d["authorization_path"]}


def observed(argv, env=None, timeout=30):
    result=run(argv,env,timeout); need(result.returncode==0,"bounded runtime observation failed")
    return result.stdout

def private_write(path,data,mode=0o400):
    # File creation is exclusive; retries never overwrite failed evidence.
    parent=path.parent; info=parent.lstat()
    need(stat.S_ISDIR(info.st_mode) and parent.resolve()==parent and info.st_uid==0 and not (stat.S_IMODE(info.st_mode)&0o022),"unsafe private output parent")
    fd=os.open(path,os.O_WRONLY|os.O_CREAT|os.O_EXCL|getattr(os,"O_NOFOLLOW",0),mode)
    with os.fdopen(fd,"wb") as stream:
        stream.write(data); stream.flush(); os.fsync(stream.fileno())


def _failure_reason_code(error: BaseException) -> str:
    """Return a bounded, non-secret category for root-only drill diagnostics."""
    text = str(error).lower()
    if re.fullmatch(r"redis_delay_[a-z_]+",text): return text
    categories = (
        ("redis_boundary_probe_failed", ("redis boundary",)),
        ("redis_source_observation_failed", ("redis source",)),
        ("redis_lazy_expiry_unavailable", ("redis natural lazy expiry unavailable", "redis lua counts missing")),
        ("redis_lazy_observation_failed", ("redis lazy observation failed",)),
        ("application_restore_failed", ("actual app interruption missing", "formal restore resume failed", "completed restore live re-verification failed")),
        ("recovery_cleanup_failed", ("actual cleanup interruption missing", "formal finish resume failed", "final finish re-verification failed", "formal cleanup incomplete")),
        ("redis_delay", ("redis_delay_",)),
        ("source_approval", ("source approval", "approved source", "recipient differs", "identity differs", "artifact differs")),
        ("guest_isolation", ("namespace", "route", "interface", "uid mapping", "isolated guest")),
        ("source_decryption", ("age decryption", "decrypt", "identity recipient")),
        ("source_archive", ("archive", "tar member", "manifest", "checksum inventory")),
        ("input_filesystem", ("private file", "output directory", "regular private", "no such file", "permission")),
    )
    for code, needles in categories:
        if any(needle in text for needle in needles):
            return code
    return "unclassified"


def _write_failure_marker(out: Path, stage_name: str, error: BaseException) -> None:
    """Persist only a stable category; never copy exception text to evidence."""
    marker = {
        "schema": 1,
        "status": "blocked",
        "stage": stage_name,
        "error_type": type(error).__name__,
        "reason_code": _failure_reason_code(error),
    }
    try:
        private_write(out / "failure.json", canon(marker), 0o400)
    except Exception:
        # Preserve the original failure if the diagnostic marker itself cannot
        # be created; the controller still reports a blocked drill.
        pass


def collect_candidate(d,proof,approval,out):
    """Optional postrelease work executes inside the already measured Guest.

    The current signed application Gate independently authorizes the candidate;
    its trust checksum comes from the verified offline authorization. No
    candidate image is run and the old-image recovery observations stay intact.
    """
    candidate=d.get("postrelease_candidate")
    if candidate is None: return "not_required"
    trust=out/"candidate-trust.pub"
    public=Path("/opt/sub2api-release-trust/vm-gate-ed25519.pub")
    sf(public)
    need(sha(public)==approval["trusted_public_key_sha256"],"postrelease trust differs")
    private_write(trust,public.read_bytes())
    try:
        mod("baseline_candidate_load").collect(**{k:Path(v) for k,v in candidate.items()},trust=trust,
            expected_trust_sha256=approval["trusted_public_key_sha256"],proof=proof,
            drill_id=d["drill_id"],release_id=d["release_id"],output=out/"candidate-load.json")
    finally:
        trust.unlink()
    return "measured"


def preserve_candidate(rootfs,out,receipt,d):
    """Validate and copy the real sidecar before deleting the owned Guest."""
    if "postrelease_candidate" not in d:
        need(not (out/"candidate-load.json").exists(),"unexpected candidate receipt")
        return "not_required"
    load=read_json(out/"candidate-load.json",True)
    fields={"schema","type","drill_id","release_id","candidate_archive_sha256","candidate_image_id","observed_image_id","guest_docker_engine_id_sha256","rootfs_identity_sha256","load_exit_code","created_at"}
    need(set(load)==fields and type(load["schema"]) is int and load["schema"]==1 and load["type"]=="sub2api-baseline-candidate-image-load","candidate load receipt fields")
    need(all(load[k]==d[k] for k in ("drill_id","release_id")) and type(load["load_exit_code"]) is int and load["load_exit_code"]==0,"candidate load identity")
    need(type(load["created_at"]) is int and receipt["created_at"]<=load["created_at"]<=receipt["completed_at"],"candidate load timestamp")
    need(all(load[k]==receipt["measurements"]["isolation"][k] for k in ("guest_docker_engine_id_sha256","rootfs_identity_sha256")),"candidate loaded outside measured Guest")
    candidate=d["postrelease_candidate"]
    gate=read_json(rootfs/candidate["gate"].lstrip("/"),True)
    need(load["candidate_image_id"]==load["observed_image_id"]==gate["evidence"]["candidate_image_id"] and load["candidate_archive_sha256"]==gate["evidence"]["candidate_archive_sha256"]==sha(rootfs/candidate["archive"].lstrip("/")),"candidate archive readback differs")
    private_write(rootfs.parent/"candidate-load.json",canon(load))
    return "measured"

def isolation(d,proof):
    ns={n:{"host":proof["host_namespaces"][n],"guest":os.readlink("/proc/self/ns/"+n),"guest_pid1":os.readlink("/proc/1/ns/"+n)} for n in NAMESPACES}
    mapping=[tuple(int(x) for x in line.split()) for line in Path("/proc/self/uid_map").read_text().splitlines()]
    need(len(mapping)==1 and mapping[0][0]==0 and mapping[0][1]>=65536 and mapping[0][2]>=65536,"private root mapping")
    no_routes=all(json.loads(observed(["ip","-j",fam,"route","show","default"]))==[] for fam in ("-4","-6"))
    no_host=all(re.fullmatch(r"lo|docker0|br-[0-9a-f]{12}|veth[0-9a-f]+",p.name) is not None for p in Path("/sys/class/net").iterdir())
    need(no_routes and no_host and all(x["host"]!=x["guest"]==x["guest_pid1"] for x in ns.values()),"actual Guest isolation")
    engine=observed(["docker","info","--format","{{.ID}}"]).strip()
    root=observed(["docker","info","--format","{{.DockerRootDir}}"]).decode().strip()
    need(root=="/var/lib/docker" and Path("/run/docker.sock").is_socket() and not Path("/run/docker.sock").is_symlink(),"Guest Docker endpoint")
    return {"backend":BACKEND,"drill_id":d["drill_id"],"release_id":d["release_id"],"recovery_helper_bundle_sha256":d["recovery_helper_bundle_sha256"],"namespaces":ns,"host_identity_sha256":proof["host_identity_sha256"],"guest_identity_sha256":hashlib.sha256(Path("/etc/machine-id").read_bytes()).hexdigest(),"rootfs_identity_sha256":proof["rootfs_identity_sha256"],"host_docker_engine_id_sha256":hashlib.sha256(proof["host_docker_engine_id"].encode()).hexdigest(),"guest_docker_engine_id_sha256":hashlib.sha256(engine).hexdigest(),"docker_root_dir":root,"docker_socket":"/run/docker.sock","default_routes_absent":no_routes,"host_facing_interfaces_absent":no_host,"uid0_host":mapping[0][1],"uid0_range":mapping[0][2],"unified_cgroup2":Path("/sys/fs/cgroup/cgroup.controllers").is_file()}

def postgres_observation():
    prefix=["docker","exec","sub2api-postgres","psql","-XAt","-v","ON_ERROR_STOP=1","-U","sub2api","-d","sub2api","-c"]
    migrations=observed(prefix+["SELECT filename||'|'||checksum FROM schema_migrations ORDER BY filename"])
    sql=" UNION ALL ".join("SELECT '"+t+"='||count(*) FROM "+t for t in ("accounts","users","api_keys","upstream_configs","upstream_keys"))
    return migrations,canonical_rowcounts(observed(prefix+[sql]))

def canonical_rowcounts(raw):
    expected=('accounts','users','api_keys','upstream_configs','upstream_keys'); values={}
    for line in raw.decode('ascii').splitlines():
        match=re.fullmatch(r'([a-z_]+)=([0-9]+)',line)
        need(match and match[1] in expected and match[1] not in values,'actual PostgreSQL count fields')
        values[match[1]]=int(match[2])
    need(set(values)==set(expected),'actual PostgreSQL counts incomplete')
    return ''.join(f'{name}={values[name]}\n' for name in expected).encode()

REDIS_SCAN_LUA="""local function snapshot() local n=redis.call('DBSIZE'); local info=redis.call('INFO','keyspace'); local exp=0; for db,k,e in string.gmatch(info,'db(%d+):keys=(%d+),expires=(%d+)') do if tonumber(db)~=0 and tonumber(k)>0 then return redis.error_reply('unsupported nonzero Redis DB') end; if tonumber(db)==0 then exp=tonumber(e) end end; return {n,exp} end; local before=snapshot(); if before.err then return before end; local cursor='0'; local rounds=0; repeat local result=redis.call('SCAN',cursor,'COUNT',1000); cursor=result[1]; rounds=rounds+1; if rounds>100000 then return redis.error_reply('bounded scan exhausted') end until cursor=='0'; local after=snapshot(); if after.err then return after end; return {before[1],before[2],after[1],after[2]}"""


def redis_lazy_observation(env,raw):
    # A single Lua call excludes an active-expiry cycle between before/after.
    # SCAN invokes Redis's own lazy expiration; it never adds or changes keys.
    config=json.loads(observed(['docker','inspect','sub2api-redis'],env))[0]['Config']
    args=(config.get('Entrypoint') or [])+(config.get('Cmd') or []); password=''
    for i,value in enumerate(args):
        if value=='--requirepass': need(i+1<len(args),'Redis password argument'); password=args[i+1]
        elif value.startswith('--requirepass='): password=value.split('=',1)[1]
    need('\n' not in password and '\x00' not in password,'Redis auth input')
    lua=REDIS_SCAN_LUA
    shell='IFS= read -r REDISCLI_AUTH; export REDISCLI_AUTH; exec redis-cli --no-auth-warning --raw EVAL "$1" 0'
    result=subprocess.run(['docker','exec','-i','sub2api-redis','sh','-c',shell,'redis-lazy',lua],input=(password+'\n').encode(),capture_output=True,env=env,timeout=180,check=False)
    with raw.open('ab') as stream: stream.write(result.stdout+result.stderr)
    need(result.returncode==0,'actual Redis lazy observation failed')
    lines=result.stdout.decode('ascii').splitlines(); need(len(lines)==4 and all(re.fullmatch(r'[0-9]+',v) for v in lines),'actual Redis Lua counts missing')
    before,before_exp,after,after_exp=map(int,lines)
    need(before>=after>=after_exp>=0 and before>=before_exp>=after_exp and before-after==before_exp-after_exp,'actual Redis source count reconciliation failed')
    return before,before_exp,after,after_exp


def redis_boundary_probe(env,raw,*,image_id,isolated):
    """Owned, non-production canary; never inserts into restored Redis.

    Only this network-none/tmpfs Redis disables active expiry. Its immutable
    image and actual engine must match the independently measured source.
    """
    need(isinstance(image_id,str) and IMAGE.fullmatch(image_id),'Redis boundary image')
    engine=observed(['docker','info','--format','{{.ID}}'],env).strip()
    need(hashlib.sha256(engine).hexdigest()==isolated['guest_docker_engine_id_sha256'],'Redis boundary engine differs')
    token=uuid.uuid4().hex; name='sub2api-ttl-boundary-'+token
    label='sub2api.full-dr-ttl-boundary'; cid=None; created=int(time.time())
    def command(argv,timeout=30):
        result=run(argv,env,timeout)
        with raw.open('ab') as stream: stream.write(result.stdout+result.stderr)
        need(result.returncode==0,'Redis boundary command failed')
        return result.stdout.decode('ascii').strip()
    def cli(*args): return command(['docker','exec',cid,'redis-cli','--raw',*map(str,args)])
    def owned():
        item=json.loads(command(['docker','container','inspect',name]))[0]
        need(item['Config'].get('Labels',{}).get(label)==token and item['Name']=='/'+name,'Redis boundary ownership differs')
        return item
    result=None
    try:
        command(['docker','run','-d','--pull','never','--name',name,'--label',label+'='+token,'--network','none','--tmpfs','/data:rw,noexec,nosuid,size=32m','--read-only','--memory','128m','--cpus','0.5','--pids-limit','64',image_id,'redis-server','--save','','--appendonly','no','--enable-debug-command','yes'])
        item=owned(); cid=item['Id']; host=item['HostConfig']
        need(re.fullmatch(r'[0-9a-f]{64}',cid) and item['Image']==image_id,'Redis boundary actual image differs')
        # Docker versions may omit HostConfig.Tmpfs mounts from Mounts.
        # Require the exact host config plus the actual in-container mount.
        need(host['NetworkMode']=='none' and not host.get('PortBindings') and not host.get('Binds') and host['ReadonlyRootfs'] is True and host.get('Tmpfs')=={'/data':'rw,noexec,nosuid,size=32m'} and all(m['Type']=='tmpfs' and m['Destination']=='/data' for m in item['Mounts']),'Redis boundary isolation differs')
        mounts=command(['docker','exec',cid,'cat','/proc/mounts']).splitlines()
        data_mount=[line.split() for line in mounts if len(line.split())>=4 and line.split()[1]=='/data']
        need(len(data_mount)==1 and data_mount[0][2]=='tmpfs' and {'rw','nosuid','noexec'}.issubset(set(data_mount[0][3].split(','))),'Redis boundary actual tmpfs differs')
        deadline=time.monotonic()+15
        while True:
            try:
                if cli('PING')=='PONG': break
            except FullDrillBlocked: pass
            need(time.monotonic()<deadline,'Redis boundary startup timeout'); time.sleep(.1)
        need(cli('CONFIG','GET','enable-debug-command').splitlines()==['enable-debug-command','yes'],'Redis boundary debug config differs')
        need(cli('DEBUG','SET-ACTIVE-EXPIRE',0)=='OK','Redis boundary active expiry disable failed')
        sentinel='persistent-'+token
        need(cli('SET','boundary:persistent',sentinel)=='OK','Redis boundary persistent set failed')
        before_digest=hashlib.sha256(cli('GET','boundary:persistent').encode()).hexdigest()
        ttl=2000; clock=time.monotonic()
        need(cli('SET','boundary:ttl','ttl-'+token,'PX',ttl)=='OK','Redis boundary TTL set failed')
        initial_pttl=int(cli('PTTL','boundary:ttl')); need(0<initial_pttl<=ttl,'Redis boundary initial TTL unavailable')
        time.sleep(ttl/1000+.2)
        elapsed=int((time.monotonic()-clock)*1000)
        counts=cli('EVAL',REDIS_SCAN_LUA,0).splitlines()
        need(counts==['2','1','1','0'],'Redis boundary lazy deletion differs')
        final_pttl=int(cli('PTTL','boundary:ttl'))
        after_digest=hashlib.sha256(cli('GET','boundary:persistent').encode()).hexdigest()
        need(final_pttl==-2 and before_digest==after_digest==hashlib.sha256(sentinel.encode()).hexdigest(),'Redis boundary sentinel differs')
        result={'schema':1,'type':'redis-owned-ttl-boundary','production_source':False,
            **{k:isolated[k] for k in ('drill_id','release_id','guest_docker_engine_id_sha256','rootfs_identity_sha256')},
            'image_id':item['Image'],'container_id_sha256':hashlib.sha256(cid.encode()).hexdigest(),
            'network_mode':'none','tmpfs_data':True,'read_only_rootfs':True,'published_ports_absent':True,'volumes_absent':True,
            'debug_config':'yes','active_expiry_disable_reply':'OK','ttl_ms':ttl,'initial_pttl_ms':initial_pttl,'wait_elapsed_ms':elapsed,'final_pttl_ms':final_pttl,
            'before_dbsize':2,'before_expiring':1,'after_dbsize':1,'after_expiring':0,
            'persistent_before_sha256':before_digest,'persistent_after_sha256':after_digest,'created_at':created}
    finally:
        # Inspect the random owned name even when `docker run` timed out after
        # creation. Only a confirmed No-such-container is absence evidence.
        check=run(['docker','container','inspect',name],env,30)
        if check.returncode==0:
            owned(); command(['docker','rm','--force',name])
            check=run(['docker','container','inspect',name],env,30)
        need(check.returncode!=0 and ('No such container: '+name).encode() in check.stderr,'Redis boundary cleanup unverified')
    need(result is not None,'Redis boundary measurements unavailable')
    result.update(owned_resources_cleaned=True,completed_at=int(time.time()))
    return result

def fault(code,before,after):
    return {"exit_code":code,"resume_verified":code==79 and before==after,"postgres_oid_before":int(before[0]),"postgres_oid_after":int(after[0]),"redis_run_before_sha256":hashlib.sha256(before[1].encode()).hexdigest(),"redis_run_after_sha256":hashlib.sha256(after[1].encode()).hexdigest()}

class RedisDelay:
    """Observe a fresh Redis phase before spending its restart budget.

    The 1800s phase wait shares restore.sh's bounded invocation lifetime;
    execute() additionally bounds the entire Guest lifetime. No PG fingerprint
    work consumes the 150s Redis restart observation budget.
    """
    def __init__(self, checkpoint, *, deadline=None, container="sub2api-redis"):
        self.checkpoint=Path(checkpoint)
        need(re.fullmatch(r"[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}",container),"redis_delay_invalid_container")
        self.container=container
        self.stop=threading.Event(); self.failure=None; self.elapsed=0.0
        self.phase="redis_delay_wait_phase"
        self.deadline=time.monotonic()+1800 if deadline is None else deadline
        need(not self.checkpoint.exists() and not self.checkpoint.is_symlink(),"redis_delay_stale_phase")
        self.initial=self.state()["StartedAt"]
        self.thread=threading.Thread(target=self.watch,daemon=True)

    def state(self):
        return json.loads(observed(["docker","inspect","-f","{{json .State}}",self.container],timeout=5))

    def start(self): self.thread.start()

    def watch(self):
        paused=False
        try:
            # A single fresh phase marker is written only after live PG replay.
            while not self.checkpoint.exists():
                need(not self.checkpoint.is_symlink(),"redis_delay_unsafe_phase")
                need(time.monotonic()<self.deadline,"redis_delay_phase_timeout")
                need(not self.stop.wait(.1),"redis_delay_cancelled")
            sf(self.checkpoint,True)
            need(self.checkpoint.read_bytes().endswith(b"phase=redis_restored\n"),"redis_delay_phase_identity")
            self.phase="redis_delay_wait_restart"
            deadline=min(self.deadline,time.monotonic()+150)
            while time.monotonic()<deadline:
                need(not self.stop.is_set(),"redis_delay_cancelled")
                state=self.state()
                if state["Running"] and state["StartedAt"]!=self.initial:
                    need(state.get("Health",{}).get("Status")=="starting","redis_delay_missed_starting")
                    self.phase="redis_delay_pause"
                    # A timed-out CLI may already have paused the daemon's
                    # container. Always attempt the owned unpause on that path.
                    paused=True
                    observed(["docker","pause",self.container],timeout=5)
                    start=time.monotonic()
                    interrupted=self.stop.wait(min(65,max(0,self.deadline-start)))
                    self.phase="redis_delay_unpause"
                    observed(["docker","unpause",self.container],timeout=5); paused=False
                    self.elapsed=time.monotonic()-start
                    need(not interrupted and self.elapsed>60 and time.monotonic()<=self.deadline,"redis_delay_interrupted")
                    self.phase="redis_delay_complete"
                    return
                self.stop.wait(.1)
            raise FullDrillBlocked("redis_delay_restart_timeout")
        except Exception as error:
            # Only stable bounded codes escape. Do not discard the stage.
            value=str(error)
            self.failure=value if re.fullmatch(r"redis_delay_[a-z_]+",value) else "redis_delay_observation_failed"
        finally:
            if paused:
                try: observed(["docker","unpause",self.container],timeout=5)
                except Exception: self.failure="redis_delay_unpause_failed"

    def finish(self, *, cancel=False):
        if cancel: self.stop.set()
        # Restore normally returns after the pause; an early exit cancels the
        # waiter rather than spending the rest of the PG/phase budget.
        remaining=max(0,self.deadline-time.monotonic())
        self.thread.join(timeout=min(180,remaining)+10)
        if self.thread.is_alive():
            self.stop.set(); self.thread.join(timeout=15)
        need(not self.thread.is_alive(),"redis_delay_thread_timeout")
        need(self.failure is None,self.failure or "redis_delay_failed")
        need(self.elapsed>60,"redis_delay_not_measured")


def remove_helper_caches(root):
    for cache in root.rglob("__pycache__"):
        need(cache.is_dir() and not cache.is_symlink(), "unsafe helper cache")
        shutil.rmtree(cache)


def guest_run():
    os.umask(0o077)
    config_path=Path("/etc/sub2api-full-recovery.json"); proof_path=Path("/etc/sub2api-full-recovery-host.json")
    sf(config_path,True); sf(proof_path,True)
    d=cfg(read_json(config_path,True)); proof=read_json(proof_path,True)
    h=helper(d); out=Path("/opt/sub2api-full-recovery"); sd(out); env=guest_environment(d)
    raw=out/"helpers.raw.log"; private_write(raw,b"",0o600)
    # Keep failure diagnosis structured and root-only without persisting exception text.
    def stage(name):
        global CURRENT_GUEST_STAGE
        CURRENT_GUEST_STAGE=name
        need(re.fullmatch(r"[a-z_]+",name) is not None,"invalid Guest stage")
        private_write(out/("stage-"+name),canon({"schema":1,"stage":name}))

    stage("guest_preflight")
    # The formal isolation guard's proof format remains unchanged.
    guard={k:proof[k] for k in ("schema","backend","helper_bundle_sha256","host_namespaces","host_docker_engine_id")}
    private_write(Path("/run/sub2api-recovery-drill-host.json"),canon(guard),0o600)
    private_write(Path("/run/sub2api-recovery-drill-isolated"),(d["recovery_helper_bundle_sha256"]+"\n").encode(),0o600)
    stage("guest_isolation_guard")
    code,_,_=vals(["bash","-c",'source "$1/recovery-isolation.sh"; recovery_drill_assert_isolated "$2"',"guard",str(h),d["recovery_helper_bundle_sha256"]],env,raw)
    need(code==0,"formal isolation guard rejected Guest")
    stage("guest_isolation_observation")
    isolated=isolation(d,proof)
    stage("guest_authorization")
    # Python may create a cache directory while importing the signed helper.
    # Remove only caches inside this owned bundle before its exact inventory is
    # verified; no helper source or signed asset is changed.
    remove_helper_caches(h)
    approval=mod("full_recovery_drill_authorization").verify_authorization(Path(d["authorization_path"]),trust=Path("/opt/sub2api-release-trust/vm-gate-ed25519.pub"),bundle=h,drill_id=d["drill_id"],release_id=d["release_id"],expected_bundle=d["recovery_helper_bundle_sha256"])
    for name in ("artifact_sha256","pre_image_id","approved_identity_sha256","approved_recipient_sha256","guest_manifest_sha256"):
        need(approval[name]==d[name],"approved source differs from config")
    need(approval["collector_sha256"]==sha(Path(__file__).resolve()),"approved collector differs")
    guest_api=mod('full_recovery_guest')
    manifest=guest_api.approved_manifest(Path(d['guest_manifest_path']),approval)
    stage("guest_prepare_system")
    guest_api.prepare_system(manifest,approval,h,env,raw)
    source_api=mod("full_recovery_source"); artifact=Path(d["artifact_path"]); staged_identity=Path(d["identity_path"])
    for file in (artifact,staged_identity): sf(file,True)
    private_root=Path("/run/sub2api-full-recovery/private")
    need(not private_root.exists(),"private identity copy already exists")
    private_root.mkdir(parents=True,mode=0o700); os.chmod(private_root.parent,0o700)
    identity=private_root/"identity.age"; private_write(identity,staged_identity.read_bytes(),0o600); staged_identity.unlink()
    started=int(time.time())
    stage("guest_decrypt_source")
    try:
        state=Path("/opt/sub2api/backups/release-state")/d["release_id"]; sd(state); point=state/"recovery-point.tar"
        expected={k:approval[k] for k in ("artifact_sha256","artifact_bytes","approved_identity_sha256","approved_recipient_sha256","recipient_type","release_id","pre_image_id")}
        source=source_api.decrypt_source(artifact,identity,point,expected=expected,host_namespaces=proof["host_namespaces"])
    except Exception as error:
        _write_failure_marker(out, "guest_decrypt_source", error)
        raise
    private_write(state/"recovery-point.tar.sha256",(sha(point)+"  recovery-point.tar\n").encode(),0o600)
    with artifact.open("rb") as inp,(state/"recovery-point.age").open("xb") as dst:
        shutil.copyfileobj(inp,dst)
    os.chmod(state/"recovery-point.age",0o600)
    private_write(state/"recovery-point.age.sha256",(sha(artifact)+"  recovery-point.age\n").encode(),0o600)
    inventory=out/"source-inventory"
    source_api.extract_archive(point,inventory,release_id=d["release_id"],pre_image_id=d["pre_image_id"])
    guest_api.prepare_source(manifest,approval,inventory,source,h,env,raw)
    stage("guest_restore_phases")
    render_before=(inventory/"config/app/.recovery-compose-render.sha256").read_text().strip()
    need(SHA.fullmatch(render_before),"archive Compose render missing")
    migrations_before=b"".join(line+b"\n" for line in (inventory/"metadata/postgres.txt").read_bytes().splitlines() if re.fullmatch(rb"[^|]+\.sql\|[0-9a-f]{64}",line))
    need(migrations_before,"archive migration evidence missing")
    counts_before=canonical_rowcounts((inventory/"metadata/core-counts.txt").read_bytes())
    need(counts_before,"archive count evidence missing")
    cp=Path(env["RELEASE_DIR"])/"recovery-checkpoints"
    need(not cp.exists() and not cp.is_symlink(),"preexisting checkpoints")
    events=[]
    def invoke(name,interrupt=None):
        invocation=dict(env)
        if interrupt: invocation["RECOVERY_INTERRUPT_AFTER_PHASE"]=interrupt
        code,values,observations=vals(["bash",str(h/name)],invocation,raw); events.extend(observations)
        return code,values
    # PostgreSQL counts must be observed before the application can write.
    code,_=invoke("restore.sh","postgres_restored"); need(code==79,"actual pre-app interruption missing")
    migrations_after,counts_after=postgres_observation()
    need(migrations_before==migrations_after and counts_before==counts_after,"actual PostgreSQL restore differs")
    stage("guest_redis_delayed_start")
    delay=RedisDelay(cp/"redis_restored.started",deadline=time.monotonic()+1800); delay.start()
    try:
        code,redis=invoke("restore.sh","redis_restored")
    except BaseException:
        # Preserve the primary restore error even if cancellation also fails.
        try: delay.finish(cancel=True)
        except Exception: pass
        raise
    if code!=79:
        try: delay.finish(cancel=True)
        except Exception: pass
        need(False,"actual Redis interruption missing")
    try:
        delay.finish()
    except Exception as error:
        _write_failure_marker(out,delay.phase,error)
        raise
    stage("guest_redis_lazy_expiry")
    need(code==79,'actual Redis interruption missing')
    lazy_before,lazy_exp_before,lazy_after,lazy_exp_after=redis_lazy_observation(env,raw)
    source_image=observed(['docker','inspect','-f','{{.Image}}','sub2api-redis'],env).decode().strip()
    need(source_image==next(item['image_id'] for item in manifest['images'] if item['role']=='redis'),'Redis source manifest image differs')
    source_observation={'schema':1,'type':'redis-restored-source-atomic-scan','image_id':source_image,
        **{key:isolated[key] for key in ('drill_id','release_id','guest_docker_engine_id_sha256','rootfs_identity_sha256')},
        'before_dbsize':lazy_before,'before_expiring':lazy_exp_before,'after_dbsize':lazy_after,'after_expiring':lazy_exp_after,'observed_at':int(time.time())}
    stage('guest_redis_boundary_probe')
    boundary_probe=redis_boundary_probe(env,raw,image_id=source_image,isolated=isolated)
    stage("guest_application_interrupt")
    code,_=invoke('restore.sh','app_healthy')
    need(code==79,"actual app interruption missing")
    runtime_before=mod("recovery_drill").data_runtime_identity()
    stage("guest_application_resume")
    resumed,restored=invoke("restore.sh")
    runtime_after=mod("recovery_drill").data_runtime_identity()
    need(resumed==0 and restored.get("restored_image_id")==d["pre_image_id"],"formal restore resume failed")
    app_fault=fault(code,runtime_before,runtime_after)
    stage("guest_application_reverify")
    verified,_=invoke("restore.sh")
    need(verified==0 and mod("recovery_drill").data_runtime_identity()==runtime_after,"completed restore live re-verification failed")
    ingress=state/"nginx-ingress-transaction"
    stale_observed=(state/"nginx-recovery-restored").is_file() and (ingress/"applied").is_file()
    need(stale_observed,"stale ingress fault not observed")
    stage("guest_cleanup_interrupt")
    code,_=invoke("recovery-finish.sh","backup_units_restored"); need(code==79,"actual cleanup interruption missing")
    cleanup_before=mod("recovery_drill").data_runtime_identity()
    stage("guest_cleanup_resume")
    resumed,finished=invoke("recovery-finish.sh")
    cleanup_after=mod("recovery_drill").data_runtime_identity()
    need(resumed==0 and finished.get("plaintext_state_removed")=="true","formal finish resume failed")
    cleanup_fault=fault(code,cleanup_before,cleanup_after)
    stage("guest_cleanup_reverify")
    replay,_=invoke("recovery-finish.sh"); need(replay==0,"final finish re-verification failed")
    checkpoints=checkpoint(cp,source,d["recovery_helper_bundle_sha256"],events)
    shell='source "$1/recovery-compose.sh"; load_release_compose_files /opt/sub2api; _recovery_compose_render_sha256 /opt/sub2api "${release_compose_args[@]}"'
    render_after=observed(["bash","-c",shell,"compose",str(h)],env).decode().strip()
    image=observed(["docker","inspect","-f","{{.Image}}","sub2api"]).decode().strip()
    counts={k:int(redis.get("redis_"+k,"-1")) for k in ("backup_dbsize","backup_expiring","restored_dbsize","restored_expiring","restored_keyspace_keys")}
    counts.update(restored_dbsize=lazy_after,restored_keyspace_keys=lazy_after,restored_expiring=lazy_exp_after)
    wait=int(redis.get("redis_restore_health_wait_seconds","-1"))*1000
    # Source counts stay independent of the deterministic canary boundary.
    measured={"isolation":isolated,"compose":{"closure_restored":render_before==render_after,"render_before_sha256":render_before,"render_after_sha256":render_after,"old_image_id":d["pre_image_id"],"restored_image_id":image},"postgres":{"restore_verified":migrations_before==migrations_after and counts_before==counts_after,"dump_sha256":source["archive_inventory"]["postgres_dump_sha256"],"migrations_before_sha256":hashlib.sha256(migrations_before).hexdigest(),"migrations_after_sha256":hashlib.sha256(migrations_after).hexdigest(),"rowcounts_before_sha256":hashlib.sha256(counts_before).hexdigest(),"rowcounts_after_sha256":hashlib.sha256(counts_after).hexdigest()},"redis":{"restore_verified":redis.get("redis_ttl_reconciliation")=="verified","rdb_sha256":source["archive_inventory"]["redis_rdb_sha256"],**counts,"persistent_before":counts["backup_dbsize"]-counts["backup_expiring"],"persistent_after":counts["restored_dbsize"]-counts["restored_expiring"],"health_wait_ms":wait,"health_budget_ms":d["redis_health_budget_seconds"]*1000,"ttl_reconciled":redis.get("redis_ttl_reconciliation")=="verified","lazy_expiry_observed":lazy_before>lazy_after,"source_observation":source_observation,"boundary_probe":boundary_probe},"faults":{"app_healthy_interrupt":app_fault,"cleanup_before_interrupt":cleanup_fault,"stale_ingress":{"recovery_restored_and_applied_observed":stale_observed,"live_health_reverified":replay==0,"applied_removed":not (ingress/"applied").exists()}},"checkpoints":checkpoints}
    need(not point.exists() and not Path("/opt/sub2api/releases/.active-release").exists(),"formal cleanup incomplete")
    shutil.rmtree(inventory); identity.unlink()
    measured["cleanup"]={"plaintext_destroyed":not point.exists() and not inventory.exists() and not identity.exists() and not staged_identity.exists(),"active_claim_cleared":not Path("/opt/sub2api/releases/.active-release").exists(),"stale_ingress_removed":not (ingress/"applied").exists()}
    candidate_status=collect_candidate(d,proof,approval,out)
    document={"schema":1,"type":"sub2api-full-recovery-measurements","drill_id":d["drill_id"],"created_at":started,"completed_at":int(time.time()),"source":source,"measurements":measured,"recovery_helper_bundle_sha256":d["recovery_helper_bundle_sha256"]}
    stage("guest_measurement_write")
    private_write(out/"guest-measurements.json",canon(document))
    return {"schema":1,"status":"measured","drill_id":d["drill_id"],"signed_review":"pending","candidate_load_status":candidate_status}

def execute(rootfs: Path, document: dict[str, Any], timeout: int = 7200) -> dict[str, Any]:
    document=cfg(document)
    prepared=plan(rootfs,document)
    need(prepared.get("status") == "planned", "full drill resources unavailable; Guest not launched")
    disable_optional_boot_services(rootfs)
    need(type(timeout) is int and 300<=timeout<=7200,"execution timeout")
    foreign_before=foreign_resources(rootfs.parent/"foreign-before.fingerprints.json")
    proof={"schema":1,"backend":BACKEND,"helper_bundle_sha256":document["recovery_helper_bundle_sha256"],"host_namespaces":{n:os.readlink("/proc/1/ns/"+n) for n in NAMESPACES},"host_docker_engine_id":observed(["docker","info","--format","{{.ID}}"]).decode().strip(),"host_identity_sha256":hashlib.sha256(Path("/etc/machine-id").read_bytes()).hexdigest(),"rootfs_identity_sha256":hashlib.sha256(canon({"device":rootfs.stat().st_dev,"inode":rootfs.stat().st_ino,"guest_machine_id":(rootfs/"etc/machine-id").read_text().strip(),"collector":sha(Path(__file__).resolve())})).hexdigest()}
    root_identity=(rootfs.stat().st_dev,rootfs.stat().st_ino)
    for filename,value in (("sub2api-full-recovery.json",document),("sub2api-full-recovery-host.json",proof)):
        private_write(rootfs/"etc"/filename,canon(value))
    service=rootfs/"etc/systemd/system/sub2api-full-recovery.service"
    service_data="[Unit]\nAfter=docker.service nginx.service\nRequires=docker.service\n[Service]\nType=oneshot\nUMask=0077\nEnvironment=PYTHONDONTWRITEBYTECODE=1\nExecStart=/usr/bin/python3 -B "+str(helper(document)/"full_recovery_drill.py")+" guest-run\nExecStopPost=/usr/bin/systemctl --no-block poweroff\nTimeoutStartSec=infinity\n[Install]\nWantedBy=multi-user.target\n"
    private_write(service,service_data.encode(),0o600)
    wants=service.parent/"multi-user.target.wants"
    need(not wants.is_symlink(),"unsafe Guest systemd wants")
    wants.mkdir(mode=0o755,exist_ok=True)
    need(wants.resolve()==wants,"unsafe Guest systemd wants")
    target=wants/service.name; need(not target.exists() and not target.is_symlink(),"Guest service already installed"); target.symlink_to("../"+service.name)
    out=rootfs/"opt/sub2api-full-recovery"; need(not out.exists() and not out.is_symlink(),"preexisting Guest evidence"); out.mkdir(mode=0o700)
    raw=rootfs.parent/"guest.raw.log"
    started=int(time.time()); clock_started=time.monotonic()
    with raw.open("xb") as stream:
        os.chmod(raw,0o600)
        child=subprocess.Popen(nspawn(rootfs,document),stdin=subprocess.DEVNULL,stdout=stream,stderr=subprocess.STDOUT,env={"PATH":"/usr/sbin:/usr/bin:/sbin:/bin","LANG":"C.UTF-8"})
        try: code=child.wait(timeout=timeout)
        except subprocess.TimeoutExpired:
            child.terminate()
            try: child.wait(timeout=60)
            except subprocess.TimeoutExpired: raise FullDrillBlocked("owned Guest could not be stopped; evidence preserved")
            raise FullDrillBlocked("owned Guest timed out; evidence preserved")
    need(code==0,"Guest process failed; evidence preserved")
    nomount(rootfs)
    path=out/"guest-measurements.json"
    if not path.is_file():
        failure=out/"failure.json"
        if failure.is_file():
            sf(failure,True)
            marker=read_json(failure,True)
            if set(marker)=={"schema","status","stage","error_type","reason_code"} and marker.get("status")=="blocked":
                raise FullDrillBlocked("Guest failed at %s (%s)" % (marker["stage"], marker["reason_code"]))
        raise FullDrillBlocked("Guest measurement missing; root-only diagnostics preserved")
    sf(path,True)
    receipt=read_json(path,True); validate_guest_receipt(receipt,document)
    isolated=receipt["measurements"]["isolation"]
    need(isolated["host_identity_sha256"]==proof["host_identity_sha256"] and isolated["rootfs_identity_sha256"]==proof["rootfs_identity_sha256"] and isolated["host_docker_engine_id_sha256"]==hashlib.sha256(proof["host_docker_engine_id"].encode()).hexdigest(),"independent host isolation binding differs")
    need(all(isolated["namespaces"][n]["host"]==proof["host_namespaces"][n] for n in NAMESPACES),"independent host namespaces differ")
    need(isolated["guest_identity_sha256"]==hashlib.sha256((rootfs/"etc/machine-id").read_bytes()).hexdigest(),"independent Guest identity differs")
    cp=rootfs/"opt/sub2api/releases"/document["release_id"]/"recovery-checkpoints"
    for phase,item in receipt["measurements"]["checkpoints"].items():
        for state in ("started","done"):
            file=cp/(phase+"."+state); sf(file,True)
            need(sha(file)==item[state+"_sha256"],"independent checkpoint readback differs")
    foreign_after=foreign_resources(rootfs.parent/"foreign-after.fingerprints.json"); need(foreign_before==foreign_after,"foreign host resources changed; Guest retained")
    # Preserve the actual Guest measurements before any owned destruction.
    private_write(rootfs.parent/"guest-measurements.json",canon(receipt))
    helper_log=out/"helpers.raw.log"; sf(helper_log,True)
    private_write(rootfs.parent/"helpers.raw.log",helper_log.read_bytes(),0o600)
    candidate_status=preserve_candidate(rootfs,out,receipt,document)
    validate_root_identity_for_cleanup(rootfs,document)
    need((rootfs.stat().st_dev,rootfs.stat().st_ino)==root_identity,"owned Guest inode changed")
    shutil.rmtree(rootfs)
    cleanup=receipt["measurements"]["cleanup"]
    cleanup.update(owned_guest_destroyed=not rootfs.exists() and not rootfs.is_symlink(),foreign_resources_unchanged=foreign_before==foreign_after,foreign_before_sha256=foreign_before,foreign_after_sha256=foreign_after)
    receipt["created_at"]=started; receipt["completed_at"]=int(time.time())
    receipt["measurements"]["rto"]={"started_at":started,"completed_at":receipt["completed_at"],"elapsed_ms":round((time.monotonic()-clock_started)*1000),"budget_ms":timeout*1000}
    mod("recovery_evidence").validate_measurements(receipt["measurements"],receipt)
    result=rootfs.parent/"measurements.json"; private_write(result,canon(receipt))
    return {"schema":1,"status":"measured","drill_id":document["drill_id"],"measurements_sha256":sha(result),"owned_guest_destroyed":cleanup["owned_guest_destroyed"],"signed_review":"pending","candidate_load_status":candidate_status}

def normalize_mounts(mounts):
    # Docker assembles this set from an internal map; its output order is not
    # an identity. Preserve every field while rejecting ambiguous destinations.
    need(isinstance(mounts,list),"invalid foreign container mounts")
    destinations=set()
    for mount in mounts:
        need(isinstance(mount,dict),"invalid foreign container mount")
        destination=mount.get('Destination')
        need(isinstance(destination,str) and destination.startswith('/') and '\x00' not in destination and destination not in destinations,"invalid or duplicate foreign mount destination")
        destinations.add(destination)
    return sorted(mounts,key=lambda mount:mount['Destination'])


def foreign_fingerprints(metadata):
    # Only fixed field names and hashes are persisted. Full resource documents,
    # including fields not named below, remain bound by each resource digest.
    def fingerprint(value): return hashlib.sha256(canon(value)).hexdigest()
    fields={
        'containers':('id','image','config_sha256','host_config_sha256','mounts','restart_count','state'),
        'volumes':('Name','Driver','Mountpoint','CreatedAt','Status','Labels','Scope','Options','UsageData'),
        'networks':('Name','Id','Created','Scope','Driver','EnableIPv4','EnableIPv6','IPAM','Internal','Attachable','Ingress','ConfigFrom','ConfigOnly','Containers','Options','Labels','Peers','Services'),
    }
    result={'schema':1,'type':'sub2api-foreign-resources-fingerprints','metadata_sha256':fingerprint(metadata),'identities':{kind:fingerprint(values) for kind,values in metadata['identities'].items()},'resources':{}}
    for kind in fields:
        resources={}
        for item in metadata[kind]:
            identity=item['id' if kind=='containers' else 'Name' if kind=='volumes' else 'Id']
            key=fingerprint(identity)
            need(key not in resources,"duplicate foreign resource identity")
            resources[key]={'metadata_sha256':fingerprint(item),'fields':{name:fingerprint(item[name]) for name in fields[kind] if name in item}}
            if kind=='containers':
                resources[key]['state_fields']={name:fingerprint(item['state'][name]) for name in ('Status','Running','Paused','Restarting','OOMKilled','Dead','Pid','ExitCode','StartedAt','FinishedAt')}
        result['resources'][kind]=resources
    return result


def foreign_resources(fingerprint_path=None):
    # Only hashes leave this function. Container environment/config bytes may
    # contain secrets; they are never logged. This covers Docker metadata and
    # process identity, not arbitrary writes to mounted volume contents.
    identities={"containers":sorted(set(observed(["docker","ps","-aq","--no-trunc"]).decode().splitlines())),"images":sorted(set(observed(["docker","image","ls","-q","--no-trunc"]).decode().splitlines())),"volumes":sorted(set(observed(["docker","volume","ls","-q"]).decode().splitlines())),"networks":sorted(set(observed(["docker","network","ls","-q","--no-trunc"]).decode().splitlines()))}
    metadata={"identities":identities,"containers":[],"volumes":[],"networks":[]}
    for identity in identities['containers']:
        item=json.loads(observed(['docker','inspect','--type','container',identity]))[0]
        state=item['State']
        metadata['containers'].append({'id':item['Id'],'image':item['Image'],'config_sha256':hashlib.sha256(canon(item['Config'])).hexdigest(),'host_config_sha256':hashlib.sha256(canon(item['HostConfig'])).hexdigest(),'mounts':normalize_mounts(item['Mounts']),'restart_count':item['RestartCount'],'state':{key:state[key] for key in ('Status','Running','Paused','Restarting','OOMKilled','Dead','Pid','ExitCode','StartedAt','FinishedAt')}})
    for kind,command in (('volumes','volume'),('networks','network')):
        for identity in identities[kind]:
            metadata[kind].append(json.loads(observed(['docker',command,'inspect',identity]))[0])
    fingerprints=foreign_fingerprints(metadata)
    if fingerprint_path is not None:
        private_write(fingerprint_path,canon(fingerprints),0o400)
    return fingerprints['metadata_sha256']

def validate_root_identity_for_cleanup(rootfs,document):
    need(rootfs==ROOT/document["drill_id"]/"rootfs" and rootfs.resolve()==rootfs,"owned Guest identity changed")
    sd(rootfs); sd(rootfs.parent); nomount(rootfs)

def validate_guest_receipt(receipt,d):
    # Validate every Guest field before destroying the owned rootfs. RTO and
    # host cleanup are added only after their actual observations exist.
    api=mod("recovery_evidence")
    shape=api.shape; digest=api.sha; integer=api.integer
    need(isinstance(receipt,dict) and set(receipt)==RECEIPT,"Guest receipt fields")
    need(type(receipt["schema"]) is int and receipt["schema"]==1 and receipt["type"]=="sub2api-full-recovery-measurements" and receipt["drill_id"]==d["drill_id"] and receipt["recovery_helper_bundle_sha256"]==d["recovery_helper_bundle_sha256"],"Guest receipt identity")
    need(integer(receipt["created_at"],1) and integer(receipt["completed_at"],1) and receipt["created_at"]<=receipt["completed_at"],"Guest receipt timestamps")
    source=shape(receipt["source"],{"kind","artifact_sha256","observed_artifact_sha256","artifact_bytes","identity_available","decryption_verified","decrypted_sha256","release_id","recovery_point_sha256","pre_image_id","recipient_type","approved_recipient_sha256","derived_recipient_sha256","approved_identity_sha256","observed_identity_sha256","archive_inventory","archive_inventory_sha256"},"Guest source")
    for key in ("artifact_sha256","release_id","pre_image_id","approved_identity_sha256","approved_recipient_sha256"):
        need(source[key]==d[key],"Guest source differs")
    need(source["kind"]=="encrypted-production-baseline" and source["identity_available"] is True and source["decryption_verified"] is True and integer(source["artifact_bytes"],1),"actual source decryption missing")
    for a,b in (("artifact_sha256","observed_artifact_sha256"),("approved_identity_sha256","observed_identity_sha256"),("approved_recipient_sha256","derived_recipient_sha256")):
        need(digest(source[a]) and source[a]==source[b],"actual source fingerprint differs")
    need(source["recipient_type"] in {"age1","ssh-ed25519"} and digest(source["decrypted_sha256"]) and digest(source["recovery_point_sha256"]),"source recipient or plaintext fingerprint")
    inventory=shape(source["archive_inventory"],{"artifact_sha256","decrypted_sha256","recovery_point_sha256","postgres_dump_sha256","redis_rdb_sha256"},"Guest archive")
    need(all(digest(v) for v in inventory.values()) and all(inventory[k]==source[k] for k in ("artifact_sha256","decrypted_sha256","recovery_point_sha256")) and source["archive_inventory_sha256"]==api.checksum(inventory),"archive fingerprint differs")
    measured=shape(receipt["measurements"],{"isolation","compose","postgres","redis","faults","checkpoints","cleanup"},"Guest measurements")
    isolated=shape(measured["isolation"],{"backend","drill_id","release_id","recovery_helper_bundle_sha256","namespaces","host_identity_sha256","guest_identity_sha256","rootfs_identity_sha256","host_docker_engine_id_sha256","guest_docker_engine_id_sha256","docker_root_dir","docker_socket","default_routes_absent","host_facing_interfaces_absent","uid0_host","uid0_range","unified_cgroup2"},"Guest isolation")
    need(isolated["backend"]==BACKEND and all(isolated[k]==d[k] for k in ("drill_id","release_id","recovery_helper_bundle_sha256")),"Guest isolation belongs to another drill")
    need(all(digest(isolated[k]) for k in ("host_identity_sha256","guest_identity_sha256","rootfs_identity_sha256","host_docker_engine_id_sha256","guest_docker_engine_id_sha256")) and isolated["host_identity_sha256"]!=isolated["guest_identity_sha256"] and isolated["host_docker_engine_id_sha256"]!=isolated["guest_docker_engine_id_sha256"],"Guest identities not private")
    need(isolated["docker_root_dir"]=="/var/lib/docker" and isolated["docker_socket"]=="/run/docker.sock" and all(isolated[k] is True for k in ("default_routes_absent","host_facing_interfaces_absent","unified_cgroup2")) and integer(isolated["uid0_host"],65536) and integer(isolated["uid0_range"],65536),"Guest actual isolation missing")
    for n,item in shape(isolated["namespaces"],set(NAMESPACES),"Guest namespaces").items():
        shape(item,{"host","guest","guest_pid1"},"Guest namespace")
        need(all(isinstance(v,str) and re.fullmatch(n+r":\[[1-9][0-9]*\]",v) for v in item.values()) and item["host"]!=item["guest"]==item["guest_pid1"],"Guest namespace differs")
    api.validate_redis_measurement(measured["redis"],isolation=isolated,rdb_sha256=inventory["redis_rdb_sha256"],created_at=receipt["created_at"],completed_at=receipt["completed_at"],health_budget_max=180000)
    compose=shape(measured["compose"],{"closure_restored","render_before_sha256","render_after_sha256","old_image_id","restored_image_id"},"Guest Compose")
    need(digest(compose["render_before_sha256"]) and compose["render_before_sha256"]==compose["render_after_sha256"] and compose["closure_restored"] is True and compose["old_image_id"]==compose["restored_image_id"]==d["pre_image_id"],"Compose recovery differs")
    pg=shape(measured["postgres"],{"restore_verified","dump_sha256","migrations_before_sha256","migrations_after_sha256","rowcounts_before_sha256","rowcounts_after_sha256"},"Guest PostgreSQL")
    need(pg["restore_verified"] is True and all(digest(v) for k,v in pg.items() if k!="restore_verified") and pg["dump_sha256"]==inventory["postgres_dump_sha256"] and pg["migrations_before_sha256"]==pg["migrations_after_sha256"] and pg["rowcounts_before_sha256"]==pg["rowcounts_after_sha256"],"PostgreSQL recovery differs")
    checkpoints=shape(measured["checkpoints"],set(PHASES),"Guest checkpoints")
    for phase in PHASES:
        item=shape(checkpoints[phase],{"completed","live_reverified","apply_count","resume_apply_count","binding_sha256","started_base64","started_sha256","done_base64","done_sha256"},"Guest checkpoint")
        need(item["completed"] is True and item["live_reverified"] is True and type(item["apply_count"]) is int and item["apply_count"]==1 and type(item["resume_apply_count"]) is int and item["resume_apply_count"]==0 and item["binding_sha256"]==hashlib.sha256(api.checkpoint_identity_bytes(receipt)).hexdigest(),"checkpoint applied twice or not verified")
        for state in ("started","done"): api.verify_checkpoint_bytes(item[state+"_base64"],api.checkpoint_marker_bytes(receipt,phase),item[state+"_sha256"])
    faults=shape(measured["faults"],{"app_healthy_interrupt","cleanup_before_interrupt","stale_ingress"},"Guest faults")
    for name in ("app_healthy_interrupt","cleanup_before_interrupt"):
        f=shape(faults[name],{"exit_code","resume_verified","postgres_oid_before","postgres_oid_after","redis_run_before_sha256","redis_run_after_sha256"},"Guest fault")
        need(type(f["exit_code"]) is int and f["exit_code"]==79 and f["resume_verified"] is True and integer(f["postgres_oid_before"],1) and f["postgres_oid_before"]==f["postgres_oid_after"] and integer(f["postgres_oid_after"],1) and digest(f["redis_run_before_sha256"]) and f["redis_run_before_sha256"]==f["redis_run_after_sha256"],"actual fault resume differs")
    stale=shape(faults["stale_ingress"],{"recovery_restored_and_applied_observed","live_health_reverified","applied_removed"},"Guest stale ingress")
    need(all(v is True for v in stale.values()),"Guest stale ingress incomplete")
    clean=shape(measured["cleanup"],{"plaintext_destroyed","active_claim_cleared","stale_ingress_removed"},"Guest cleanup")
    need(all(v is True for v in clean.values()),"Guest cleanup incomplete")

def main() -> None:
    parser = argparse.ArgumentParser()
    sub = parser.add_subparsers(dest="command", required=True)
    sub.add_parser("probe")
    sub.add_parser("guest-run")
    for command in ("plan", "run"):
        item = sub.add_parser(command); item.add_argument("config", type=Path); item.add_argument("rootfs", type=Path)
        if command == "run": item.add_argument("--timeout", type=int, default=7200)
    args = parser.parse_args()
    try:
        if args.command == "probe": result = probe()
        elif args.command == "guest-run": result = guest_run()
        else:
            document = cfg(read_json(args.config))
            result = plan(args.rootfs, document) if args.command == "plan" else execute(args.rootfs, document, args.timeout)
        print(json.dumps(result, sort_keys=True))
    except (FullDrillBlocked, OSError, RuntimeError, ValueError, TypeError, KeyError, subprocess.SubprocessError) as error:
        if args.command=="guest-run": _write_failure_marker(Path("/opt/sub2api-full-recovery"),CURRENT_GUEST_STAGE,error)
        print(json.dumps({"schema": 1, "status": "blocked", "reason": "full_recovery_drill_failed; root-only diagnostics preserved"}))
        raise SystemExit(1)

if __name__ == "__main__": main()
