"""Prepare the owned offline Guest for a full recovery drill.

This helper only consumes a signed drill manifest and exact source bytes. It
does not issue a VM Gate, create a production claim, or start a runner when an
input is missing. Host preparation stages immutable image/pre-state files;
Guest preparation loads images and creates the drill-only active claim.
"""
from __future__ import annotations

import argparse, hashlib, json, os, re, shutil, stat, subprocess, sys
from pathlib import Path, PurePosixPath
from typing import Any

SHA = re.compile(r"[0-9a-f]{64}\Z")
IMAGE = re.compile(r"sha256:[0-9a-f]{64}\Z")
DRILL = re.compile(r"full-dr-[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}\Z")
RELEASE = re.compile(r"[0-9]{3}-[0-9a-f]{12}-[0-9]+-[0-9a-f]{8}\Z")
ROLES = ("app", "postgres", "redis")
ROOT = Path("/var/lib/sub2api-full-recovery-drills")
CONTAINERD_SOCKET = "/run/containerd/containerd.sock"
# Docker starts restored services inside a systemd-nspawn private-user Guest.
# runc otherwise tries to create a new kernel session keyring for each nested
# container; the VM kernel rejects that operation from the private user
# namespace before the container process starts. Keep the workaround scoped
# to this isolated runtime instead of weakening the host Docker daemon.
# Docker reserves the built-in ``runc`` runtime name and rejects a config that
# tries to redefine it.  Use a private runtime name and make it the default so
# every nested container still receives the keyring workaround.  With the
# containerd snapshotter Docker uses the shim-v2 runtime path; ``runtimeArgs``
# are legacy top-level runc arguments and are passed at the wrong layer.  The
# shim's typed options map is the supported way to set ``NoNewKeyring``.
OFFLINE_RUNTIME_NAME='runc-no-keyring'
DAEMON_CONFIG={'features':{'containerd-snapshotter':True},'storage-driver':'overlayfs','iptables':False,'ip6tables':False,'bridge':'none','ip-forward':False,'ip-masq':False,'default-runtime':OFFLINE_RUNTIME_NAME,'runtimes':{OFFLINE_RUNTIME_NAME:{'runtimeType':'io.containerd.runc.v2','options':{'NoNewKeyring':True}}}}
DAEMON_BYTES=json.dumps(DAEMON_CONFIG,sort_keys=True,separators=(',',':')).encode()
NGINX_MIME_BYTES = 3957
NGINX_MIME_SHA256 = '632f37cafbdce8ebfba27f7d8264da1ea462880c4961c32354455c8ab722f7c3'


class GuestPreparationBlocked(RuntimeError):
    pass


def require(value: bool, reason: str) -> None:
    if not value:
        raise GuestPreparationBlocked(reason)


def digest(path: Path) -> str:
    h = hashlib.sha256()
    with path.open("rb") as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b""):
            h.update(block)
    return h.hexdigest()


def regular(path: Path, *, mode: int | None = None) -> None:
    info = path.lstat()
    require(stat.S_ISREG(info.st_mode) and info.st_nlink == 1 and not path.is_symlink(), "regular private file required")
    require(path.resolve() == path.absolute(), "noncanonical path")
    if mode is not None:
        require(stat.S_IMODE(info.st_mode) == mode and info.st_uid == 0 and info.st_gid == 0, "private file ownership")


def private_directory(path: Path) -> None:
    """Require an owned, canonical root-only directory for recovery state."""
    require(path.is_absolute(), "absolute directory required")
    try:
        info = path.lstat()
    except OSError as error:
        raise GuestPreparationBlocked("private directory unavailable") from error
    require(stat.S_ISDIR(info.st_mode) and not path.is_symlink(), "private directory required")
    require(info.st_uid == 0 and info.st_gid == 0 and stat.S_IMODE(info.st_mode) == 0o700,
            "private directory ownership")


def strict_json(path: Path) -> dict[str, Any]:
    regular(path, mode=0o400)
    require(path.stat().st_size<=4*1024*1024,"manifest size limit")
    def unique(pairs):
        out = {}
        for key, value in pairs:
            require(key not in out, "duplicate manifest field")
            out[key] = value
        return out
    try:
        value = json.loads(path.read_bytes(), object_pairs_hook=unique, parse_constant=lambda _: (_ for _ in ()).throw(ValueError()))
    except (ValueError, UnicodeDecodeError, RecursionError) as error:
        raise GuestPreparationBlocked("invalid manifest JSON") from error
    require(isinstance(value, dict), "manifest object required")
    return value


def path_field(value: Any, prefix: str) -> Path:
    require(isinstance(value, str) and value.startswith(prefix), "manifest path")
    require("\\" not in value and "\x00" not in value and ".." not in PurePosixPath(value).parts, "unsafe manifest path")
    require(str(PurePosixPath(value))==value,"canonical manifest path")
    result = Path(value)
    return result


def validate_manifest(manifest: dict[str, Any]) -> dict[str, Any]:
    validate_daemon_runtime_config()
    fields = {"schema", "drill_id", "release_id", "pre_image_id", "images", "backup_units", "active_app", "source_snapshot_path", "compose_plugin_sha256", "docker_daemon_config_sha256", "docker_version"}
    require(set(manifest) == fields and type(manifest["schema"]) is int and manifest["schema"] == 1, "manifest fields")
    require(isinstance(manifest['drill_id'],str) and DRILL.fullmatch(manifest["drill_id"]) and isinstance(manifest['release_id'],str) and RELEASE.fullmatch(manifest["release_id"]), "manifest identity")
    require(isinstance(manifest['pre_image_id'],str) and IMAGE.fullmatch(manifest["pre_image_id"]), "manifest old image")
    require(isinstance(manifest['compose_plugin_sha256'],str) and SHA.fullmatch(manifest['compose_plugin_sha256']),"Compose plugin fingerprint")
    require(manifest['docker_daemon_config_sha256']==hashlib.sha256(DAEMON_BYTES).hexdigest(),"offline containerd Docker configuration required")
    require(isinstance(manifest['docker_version'],str) and re.fullmatch(r'[0-9]+\.[0-9]+\.[0-9]+',manifest['docker_version']),"Docker version contract")
    images = manifest["images"]
    require(isinstance(images, list) and len(images) == 3, "three exact images required")
    roles = set()
    for item in images:
        require(isinstance(item, dict) and set(item) == {"role", "image_id", "path", "sha256", "bytes"}, "image manifest fields")
        require(item["role"] in ROLES and item["role"] not in roles, "image role")
        roles.add(item["role"]); require(isinstance(item['image_id'],str) and IMAGE.fullmatch(item["image_id"]), "image identity")
        path_field(item["path"], "/opt/sub2api-full-recovery-input/images/")
        require(isinstance(item['sha256'],str) and SHA.fullmatch(item["sha256"]) and type(item["bytes"]) is int and item["bytes"] > 0, "image archive digest")
    require(roles == set(ROLES), "app/postgres/redis images required")
    prestate = manifest["backup_units"]
    require(isinstance(prestate, list) and len(prestate)==2, "two actual backup units required")
    seen = set()
    for item in prestate:
        require(isinstance(item, dict) and set(item) == {"name", "path", "sha256", "bytes"}, "pre-state fields")
        require(item["name"] in {"sub2api-backup.service","sub2api-backup.timer"} and item["name"] not in seen, "backup unit name")
        seen.add(item["name"]); path_field(item["path"], "/opt/sub2api-full-recovery-input/units/")
        require(isinstance(item['sha256'],str) and SHA.fullmatch(item["sha256"]) and type(item["bytes"]) is int and item["bytes"] > 0, "pre-state digest")
    path_field(manifest["source_snapshot_path"],"/opt/sub2api-full-recovery-input/")
    slot=manifest["active_app"]
    require(isinstance(slot,dict) and set(slot)=={"container","port","instance_id","release_id"},"active app fields")
    require(isinstance(slot["container"],str) and re.fullmatch(r"[A-Za-z0-9_.-]{1,80}",slot["container"]),"active container")
    require(type(slot["port"]) is int and slot["port"] in {18080,18081},"active port")
    for key in ("instance_id","release_id"):
        require(isinstance(slot[key],str) and (not slot[key] or re.fullmatch(r"[A-Za-z0-9_.-]{1,128}",slot[key])),"active slot identity")
    return manifest


def validate_daemon_runtime_config() -> None:
    """Catch Docker runtime-name mistakes before booting the Guest.

    Docker treats ``runc`` as a reserved built-in runtime.  The manifest
    checksum binds the bytes, while this semantic check prevents a future
    helper edit from producing a daemon that can never start.
    """
    require(OFFLINE_RUNTIME_NAME != 'runc', 'offline runtime name is reserved')
    runtime = DAEMON_CONFIG.get('runtimes', {}).get(OFFLINE_RUNTIME_NAME)
    require(DAEMON_CONFIG.get('default-runtime') == OFFLINE_RUNTIME_NAME, 'offline runtime is not default')
    require(runtime == {'runtimeType': 'io.containerd.runc.v2', 'options': {'NoNewKeyring': True}}, 'offline runtime config differs')


def stage(rootfs: Path, manifest_path: Path) -> dict[str, Any]:
    """Stage only owned image/pre-state inputs and the exact manifest."""
    manifest = validate_manifest(strict_json(manifest_path))
    expected = ROOT / manifest["drill_id"] / "rootfs"
    require(rootfs == expected and rootfs.resolve() == rootfs, "owned rootfs")
    require(rootfs.is_dir() and rootfs.stat().st_uid == 0 and stat.S_IMODE(rootfs.stat().st_mode) == 0o700, "rootfs ownership")
    input_dir = rootfs / "opt/sub2api-full-recovery-input"
    require(input_dir.is_dir() and not input_dir.is_symlink(), "Guest input directory")
    require(not any(line.split()[4]==str(rootfs) or line.split()[4].startswith(str(rootfs)+'/') for line in Path('/proc/self/mountinfo').read_text().splitlines()),"Guest rootfs is mounted")
    verify_nginx_runtime(rootfs)
    plugin=rootfs/'usr/libexec/docker/cli-plugins/docker-compose'
    if not plugin.exists():
        candidates=[Path(p) for p in ('/usr/libexec/docker/cli-plugins/docker-compose','/usr/lib/docker/cli-plugins/docker-compose','/usr/local/lib/docker/cli-plugins/docker-compose','/root/.docker/cli-plugins/docker-compose')]
        sources=[p for p in candidates if p.is_file() and not p.is_symlink() and digest(p)==manifest['compose_plugin_sha256']]
        require(len(sources)>=1,"approved Compose plugin unavailable")
        plugin.parent.mkdir(mode=0o755,parents=True,exist_ok=True)
        require(plugin.parent.resolve()==plugin.parent,"unsafe plugin directory")
        shutil.copyfile(sources[0],plugin); plugin.chmod(0o755)
    regular(plugin); require(digest(plugin)==manifest['compose_plugin_sha256'] and plugin.stat().st_uid==0 and stat.S_IMODE(plugin.stat().st_mode)==0o755,"Guest Compose plugin differs")
    config=rootfs/'etc/docker/daemon.json'; regular(config)
    config.write_bytes(DAEMON_BYTES); config.chmod(0o600)
    for item in [*manifest["images"], *manifest["backup_units"]]:
        guest_path = Path(item["path"])
        source = rootfs/str(guest_path).lstrip('/')
        regular(source, mode=0o400)
        require(source.stat().st_size == item["bytes"], "input byte count")
        require(digest(source) == item["sha256"], "input digest")
    target_manifest = input_dir / "guest-manifest.json"
    if target_manifest.exists():
        regular(target_manifest,mode=0o400)
        require(target_manifest.read_bytes()==manifest_path.read_bytes(),"staged manifest differs")
    else:
        shutil.copyfile(manifest_path, target_manifest); os.chmod(target_manifest, 0o400)
    return {"schema": 1, "status": "staged", "drill_id": manifest["drill_id"], "guest_manifest_sha256": digest(target_manifest)}


def approved_manifest(manifest_path: Path, approval: dict[str,Any]) -> dict[str,Any]:
    regular(manifest_path,mode=0o400)
    require(digest(manifest_path)==approval["guest_manifest_sha256"],"signed manifest bytes differ")
    manifest=validate_manifest(strict_json(manifest_path))
    require(all(manifest[k]==approval[k] for k in ("drill_id","release_id","pre_image_id")),"signed Guest identity differs")
    snapshot=Path(manifest["source_snapshot_path"]); regular(snapshot,mode=0o400)
    require(digest(snapshot)==approval["drill_context"]["source_snapshot_sha256"],"source snapshot differs")
    snapshot_data=strict_json(snapshot)
    context=approval['drill_context']
    require(snapshot_data['production_current_commit_sha']==context['commit_sha'] and snapshot_data['current_image_id']==context['candidate_image_id']==manifest['pre_image_id'],"actual source identity differs")
    if 'profile' in snapshot_data: require(snapshot_data['profile']==context['profile'],"source profile differs")
    if 'version' in snapshot_data: require(snapshot_data['version']==context['version'],"source version differs")
    return manifest


def verify_inputs(manifest,root=Path('/')):
    for item in [*manifest['images'],*manifest['backup_units']]:
        path=root/item['path'].lstrip('/')
        regular(path,mode=0o400)
        require(path.stat().st_size==item['bytes'] and digest(path)==item['sha256'],'preparation input bytes differ')


def command(argv,raw,env=None):
    result=subprocess.run(argv,capture_output=True,env=env,timeout=600,check=False)
    with raw.open('ab') as stream: stream.write(result.stdout+result.stderr)
    require(result.returncode==0,"owned Guest preparation command failed")
    return result.stdout


def verify_nginx_runtime(root=Path('/')):
    """Require package runtime data before restoring the source configuration."""
    mime = root / 'etc/nginx/mime.types'
    regular(mime, mode=0o644)
    require(mime.stat().st_size == NGINX_MIME_BYTES and digest(mime) == NGINX_MIME_SHA256,
            'Guest Nginx MIME data unavailable or differs from approved package')


def restore_nginx_include_closure(snapshot: Path, root: Path = Path('/')) -> None:
    """Restore exact runtime includes from the authenticated source nginx -T.

    The recovery archive separately preserves managed files, while nginx -T
    also contains package data and unmanaged includes. Recreate those bytes in
    the offline Guest rather than inventing a replacement configuration.
    """
    dump = snapshot / 'nginx-T.txt'
    regular(dump)
    require(dump.stat().st_size <= 8 * 1024 * 1024, 'Nginx dump size limit')
    raw = dump.read_bytes()
    headers = list(re.finditer(rb'^# configuration file ([^\r\n]+):\n', raw, re.MULTILINE))
    require(headers, 'Nginx source include closure unavailable')
    records = {}
    for index, header in enumerate(headers):
        try:
            name = header[1].decode('ascii')
        except UnicodeDecodeError as error:
            raise GuestPreparationBlocked('Nginx include path encoding') from error
        path = PurePosixPath(name)
        require(re.fullmatch(r'/etc/nginx/[A-Za-z0-9_./-]+', name) is not None
                and '..' not in path.parts and str(path) == name and name not in records,
                'Nginx include path or duplicate section')
        content = raw[header.end():headers[index + 1].start() if index + 1 < len(headers) else len(raw)]
        # nginx -T adds exactly one separator LF after each original file.
        require(content.endswith(b'\n'), 'Nginx source section separator missing')
        records[name] = content[:-1]
    require('/etc/nginx/nginx.conf' in records, 'Nginx source main config missing')
    captured = {'/etc/nginx/nginx.conf': snapshot / 'nginx.conf'}
    for directory in ('conf.d', 'sites-enabled', 'snippets'):
        for file in (snapshot / directory).iterdir():
            if not file.name.startswith('.'):
                captured['/etc/nginx/' + directory + '/' + file.name] = file
    observability = snapshot / 'observability.conf'
    if observability.exists():
        captured['/etc/nginx/conf.d/sub2api-release-observability.conf'] = observability
    # Cross-check every separately archived managed file before writing anything.
    for name, file in captured.items():
        regular(file)
        require(name in records and records[name] == file.read_bytes(), 'Nginx dump differs from captured config')
    # Guest packages can enable optional dynamic modules absent on the source.
    # Disable only their package links; never delete module binaries or source
    # configuration, and keep the final nginx -T comparison exact.
    extra_modules = []
    modules = root / 'etc/nginx/modules-enabled'
    if modules.exists():
        require(not modules.is_symlink() and modules.resolve() == modules.absolute(), 'Nginx module directory escaped Guest')
        for link in modules.iterdir():
            name = '/etc/nginx/modules-enabled/' + link.name
            if name in records:
                continue
            require(link.is_symlink() and re.fullmatch(r'[0-9]+-mod-[A-Za-z0-9_-]+\.conf', link.name) is not None,
                    'Unexpected Guest Nginx module entry')
            raw_target = os.readlink(link)
            target = root / raw_target.lstrip('/') if raw_target.startswith('/') else link.parent / raw_target
            package_modules = (root / 'usr/share/nginx/modules-available').resolve()
            require(package_modules.is_relative_to(root.resolve())
                    and target.resolve().parent == package_modules and target.resolve().is_file(),
                    'Nginx module link is not package owned')
            extra_modules.append(link)
    targets = []
    for name, content in records.items():
        target = root / name.lstrip('/')
        require(target.absolute().is_relative_to(root.absolute()) and target.resolve() == target.absolute(),
                'Nginx include target escaped Guest')
        if target.exists():
            regular(target)
        targets.append((target, content, stat.S_IMODE(captured[name].stat().st_mode) if name in captured else 0o644))
    for target, content, mode in targets:
        target.parent.mkdir(parents=True, exist_ok=True, mode=0o755)
        require(target.parent.resolve() == target.parent.absolute(), 'Nginx include parent changed')
        target.write_bytes(content)
        target.chmod(mode)
    for link in extra_modules:
        link.unlink()


def verify_oci_content(digest_ref, raw, env):
    """Verify the captured OCI index and its linux/amd64 manifest by content digest."""
    require(IMAGE.fullmatch(digest_ref), 'invalid OCI content digest')
    ctr = ['ctr', '--address', CONTAINERD_SOCKET, '--namespace', 'moby', 'content', 'get', digest_ref]
    blob = command(ctr, raw, env)
    require(hashlib.sha256(blob).hexdigest() == digest_ref.split(':', 1)[1],
            'OCI content digest differs')
    try:
        index = json.loads(blob)
    except (ValueError, UnicodeDecodeError) as error:
        raise GuestPreparationBlocked('OCI index content is not JSON') from error
    require(isinstance(index, dict) and set(index) == {'schemaVersion', 'mediaType', 'manifests'} and index['schemaVersion'] == 2,
            'OCI index fields')
    require(index['mediaType'] in {
        'application/vnd.oci.image.index.v1+json',
        'application/vnd.docker.distribution.manifest.list.v2+json',
    }, 'OCI index media type')
    manifests = index['manifests']
    require(isinstance(manifests, list), 'OCI manifest list')
    candidates = [item for item in manifests if isinstance(item, dict) and item.get('platform', {}).get('os') == 'linux' and item.get('platform', {}).get('architecture') == 'amd64']
    require(len(candidates) == 1, 'linux amd64 manifest is not unique')
    selected = candidates[0]
    manifest_digest = selected.get('digest')
    require(isinstance(manifest_digest, str) and IMAGE.fullmatch(manifest_digest), 'OCI manifest digest')
    manifest_blob = command(['ctr', '--address', CONTAINERD_SOCKET, '--namespace', 'moby', 'content', 'get', manifest_digest], raw, env)
    require(hashlib.sha256(manifest_blob).hexdigest() == manifest_digest.split(':', 1)[1],
            'OCI manifest content digest differs')
    try:
        manifest = json.loads(manifest_blob)
    except (ValueError, UnicodeDecodeError) as error:
        raise GuestPreparationBlocked('OCI manifest content is not JSON') from error
    require(isinstance(manifest, dict) and manifest.get('schemaVersion') == 2 and manifest.get('mediaType') in {
        'application/vnd.oci.image.manifest.v1+json',
        'application/vnd.docker.distribution.manifest.v2+json',
    } and isinstance(manifest.get('config'), dict), 'OCI manifest config missing')
    config_digest = manifest['config'].get('digest')
    require(isinstance(config_digest, str) and IMAGE.fullmatch(config_digest), 'OCI config digest')
    config_blob = command(['ctr', '--address', CONTAINERD_SOCKET, '--namespace', 'moby', 'content', 'get', config_digest], raw, env)
    require(hashlib.sha256(config_blob).hexdigest() == config_digest.split(':', 1)[1],
            'OCI config content digest differs')
    try:
        config = json.loads(config_blob)
    except (ValueError, UnicodeDecodeError) as error:
        raise GuestPreparationBlocked('OCI config content is not JSON') from error
    require(isinstance(config, dict) and config.get('os') == 'linux' and config.get('architecture') == 'amd64',
            'OCI config platform differs')


def verify_containerd_runtime(raw, env):
    """Bind ctr to the Docker service's configured system containerd."""
    socket = Path(CONTAINERD_SOCKET)
    info = socket.lstat()
    require(not socket.is_symlink() and stat.S_ISSOCK(info.st_mode) and info.st_uid == 0,
            'Docker containerd socket is not a root-owned Unix socket')
    require(stat.S_IMODE(info.st_mode) == 0o660, 'Docker containerd socket mode differs')
    unit = command(['systemctl', 'show', 'docker.service', '--property=ExecStart', '--value'], raw, env).decode(errors='replace')
    require(re.search(r'(?:^|\s)--containerd=' + re.escape(CONTAINERD_SOCKET) + r'(?:\s|;|$)', unit),
            'Docker service containerd socket differs')


def restore_image_references(image_lines, expected, raw, env):
    """Rebuild captured digest references in the owned Docker image store."""
    require({line.split()[-1] for line in image_lines} == expected,
            'archived image inventory differs')
    ctr = ['ctr', '--address', CONTAINERD_SOCKET, '--namespace', 'moby', 'images']
    for line in image_lines:
        parts = line.split(); require(len(parts) == 3, 'archive image metadata')
        if '@sha256:' in parts[1]:
            require(parts[1].split('@', 1)[1] == parts[2],
                    'source registry digest differs from OCI descriptor')
            require(re.fullmatch(r'(?:docker\.io/)?(?:library/)?(?:postgres|redis)@sha256:[0-9a-f]{64}', parts[1]),
                    'unsupported immutable source image reference')
            verify_oci_content(parts[2], raw, env)
            refs = command([*ctr, 'list', '--quiet'], raw, env).decode().splitlines()
            repository = parts[1].split('@', 1)[0]
            if not repository.startswith('docker.io/'):
                repository = 'docker.io/' + ('library/' if '/' not in repository else '') + repository
            reference = repository + '@' + parts[2]
            if reference not in refs:
                matched = next((ref for ref in refs if ref.endswith('@' + parts[2])), None)
                require(matched, 'exact OCI image reference missing from containerd')
                command([*ctr, 'tag', matched, reference], raw, env)
        else:
            verify_oci_content(parts[2], raw, env)
        actual = command(['docker', 'image', 'inspect', '--format', '{{.Id}}', parts[1]], raw, env).decode().strip()
        require(actual == parts[2], 'original Compose image reference unavailable offline')


def prepare_system(manifest,approval,helpers,env,raw):
    verify_inputs(manifest)
    verify_nginx_runtime()
    plugin=Path('/usr/libexec/docker/cli-plugins/docker-compose'); regular(plugin)
    require(digest(plugin)==manifest['compose_plugin_sha256'],"Guest Compose plugin differs")
    config=Path('/etc/docker/daemon.json'); regular(config,mode=0o600)
    require(digest(config)==manifest['docker_daemon_config_sha256'],"Guest Docker daemon configuration differs")
    require(command(['docker','version','--format','{{.Server.Version}}'],raw,env).decode().strip()==manifest['docker_version'],'actual Guest Docker version differs')
    driver=command(['docker','info','--format','{{json .DriverStatus}}'],raw,env)
    require(['driver-type','io.containerd.snapshotter.v1'] in json.loads(driver),'actual Docker containerd image store required')
    command(['docker','compose','version','--short'],raw,env)
    loaded=set()
    for item in manifest["images"]:
        archive = Path(item["path"])
        regular(archive, mode=0o400); require(archive.stat().st_size == item["bytes"] and digest(archive) == item["sha256"], "staged image drift")
        if str(archive) not in loaded:
            command(["docker","load","--input",str(archive)],raw,env); loaded.add(str(archive))
        observed=command(["docker","image","inspect","--format","{{.Id}}",item["image_id"]],raw,env)
        require(observed.decode().strip()==item["image_id"], "loaded image identity differs")
    require(manifest["images"][[item["role"] for item in manifest["images"]].index("app")]["image_id"] == manifest["pre_image_id"], "old app image binding")
    for item in manifest["backup_units"]:
        source=Path(item["path"]); regular(source,mode=0o400)
        require(source.stat().st_size==item["bytes"] and digest(source)==item["sha256"],"backup unit source differs")
        target=Path('/etc/systemd/system')/item["name"]
        require(not target.exists() and not target.is_symlink(),"backup unit already installed")
        shutil.copyfile(source,target); target.chmod(0o644)
    command(['systemctl','daemon-reload'],raw,env)
    command(['systemctl','enable','sub2api-backup.timer'],raw,env)
    command(['systemctl','start','sub2api-backup.timer'],raw,env)
    # A persistent source timer may request an immediate catch-up invocation.
    # Stop that owned Guest job before the formal mask helper observes its
    # original state; no backup success or historical timer event is invented.
    command(['systemctl','stop','sub2api-backup.service'],raw,env)
    command(['systemctl','reset-failed','sub2api-backup.service'],raw,env)
    require(command(['systemctl','is-active','sub2api-backup.timer'],raw,env).decode().strip()=='active','actual Guest backup timer inactive')
    service=subprocess.run(['systemctl','is-active','sub2api-backup.service'],capture_output=True,env=env,check=False)
    require(service.stdout.decode().strip()=='inactive','actual Guest backup service not inactive')
    state=Path('/opt/sub2api/backups/release-state')/manifest['release_id']
    state.parent.mkdir(parents=True,mode=0o700,exist_ok=True)
    require(not state.exists() and not state.is_symlink(),"preexisting recovery state")
    maskenv=dict(env,STATE_ROOT=str(state.parent),STATE_DIR=str(state))
    command(['bash',str(helpers/'mask-backup-units.sh')],raw,maskenv)
    private_directory(state)
    return state


def write_private(path,data,mode=0o400):
    require(not path.exists() and not path.is_symlink(),"private output already exists")
    with path.open('xb') as stream: stream.write(data)
    path.chmod(mode)


def prepare_source(manifest,approval,inventory,source,helpers,env,raw):
    deploy=Path('/opt/sub2api')
    archive=inventory/'config/app'
    for item in archive.iterdir():
        target=deploy/item.name
        require(not target.exists() and not target.is_symlink(),"deployment input already exists")
        if item.is_dir(): shutil.copytree(item,target,copy_function=shutil.copy2)
        else: shutil.copy2(item,target)
    state=deploy/'backups/release-state'/manifest['release_id']
    private_directory(state)
    slot=manifest['active_app']
    slot_bytes=(f"container={slot['container']}\nport={slot['port']}\nimage_id={manifest['pre_image_id']}\nrelease_id={slot['release_id']}\ninstance_id={slot['instance_id']}\n").encode()
    write_private(state/'pre-active-app',slot_bytes,0o600)
    write_private(deploy/'active-app',slot_bytes,0o600)
    write_private(state/'pre-image-id',(manifest['pre_image_id']+'\n').encode(),0o600)
    migration_file=inventory/'metadata/migrations.txt'
    if not migration_file.exists():
        lines=[line for line in (inventory/'metadata/postgres.txt').read_text().splitlines() if '|' in line and line.split('|',1)[0].endswith('.sql')]
        require(lines,"archive migration evidence missing")
        migration_file=state/'pre-migrations.tsv'; write_private(migration_file,('\n'.join(lines)+'\n').encode(),0o600)
    else: write_private(state/'pre-migrations.tsv',migration_file.read_bytes(),0o600)
    files=sorted(p for p in state.iterdir() if p.is_file())
    write_private(state/'SHA256SUMS',''.join(digest(p)+'  '+p.name+'\n' for p in files).encode(),0o600)
    release = Path("/opt/sub2api/releases") / manifest["release_id"]
    write_private(release/'.prepared',(f"release_id={manifest['release_id']}\ncandidate_image_id={manifest['pre_image_id']}\n").encode())
    claim = Path("/opt/sub2api/releases") / ".active-release"
    require(not claim.exists() and not claim.is_symlink(), "recovery claim already exists")
    claim.mkdir(mode=0o700)
    write_private(claim/'drill-metadata.json',json.dumps(approval['drill_context'],sort_keys=True,separators=(',',':')).encode())
    write_private(claim/'release_id',(f"release_id={manifest['release_id']}\n").encode())
    shutil.copytree(helpers,claim/'assets',copy_function=shutil.copy2)
    claim_files=sorted(p for p in claim.rglob('*') if p.is_file())
    write_private(claim/'CLAIM_SHA256SUMS',''.join(digest(p)+'  '+str(p.relative_to(claim))+'\n' for p in claim_files).encode())
    # Exact source configuration stays inside the proven offline Guest.
    ng=inventory/'config/nginx'
    targets={'nginx.conf':Path('/etc/nginx/nginx.conf'),'sites-enabled':Path('/etc/nginx/sites-enabled'),'conf.d':Path('/etc/nginx/conf.d'),'snippets':Path('/etc/nginx/snippets'),'release-backups':Path('/etc/nginx/sub2api-release-backups'),'logrotate':Path('/etc/logrotate.d')}
    default=Path('/etc/nginx/sites-enabled/default')
    if default.is_symlink() or default.is_file(): default.unlink()
    for name,target in targets.items():
        src=ng/name
        if src.is_file(): shutil.copy2(src,target); continue
        target.mkdir(mode=0o700 if name=='release-backups' else 0o755,exist_ok=True)
        for p in src.iterdir():
            if p.name.startswith('.'): continue
            dst=target/p.name; require(not dst.is_symlink(),"Nginx target symlink")
            shutil.copy2(p,dst)
    restore_nginx_include_closure(ng)
    if (ng/'observability.conf').exists(): shutil.copy2(ng/'observability.conf',Path('/etc/nginx/conf.d/sub2api-release-observability.conf'))
    cert=inventory/'config/certbot'
    Path('/etc/letsencrypt').mkdir(mode=0o755,exist_ok=True)
    for item in cert.iterdir():
        target=Path('/etc/letsencrypt')/item.name
        require(not target.exists() and not target.is_symlink(),"certificate target exists")
        shutil.copytree(item,target,copy_function=shutil.copy2)
    image_lines=(inventory/'metadata/images.txt').read_text().splitlines()
    expected={item['image_id'] for item in manifest['images']}
    # Compose may refer to immutable repo digests that docker-save does not
    # preserve as RepoDigests. Require the original references to resolve to
    # their exact captured image IDs before starting anything; never pull.
    verify_containerd_runtime(raw, env)
    restore_image_references(image_lines, expected, raw, env)
    shell='set -Eeuo pipefail; source "$1/recovery-compose.sh"; recovery_compose_verify_live "$2/config/app" /opt/sub2api "$3"; cd /opt/sub2api; load_release_compose_files /opt/sub2api; docker compose "${release_compose_args[@]}" up -d --pull never postgres redis; for x in sub2api-postgres sub2api-redis; do for n in {1..90}; do [[ $(docker inspect -f "{{.State.Health.Status}}" "$x") == healthy ]] && break; sleep 2; done; [[ $(docker inspect -f "{{.State.Health.Status}}" "$x") == healthy ]]; done'
    command(['bash','-c',shell,'prepare',str(helpers),str(inventory),manifest['pre_image_id']],raw,env)
    command(['nginx','-t'],raw,env); command(['systemctl','start','nginx'],raw,env)
    # This file records the actual encrypted source measurement, not a Gate.
    result=json.dumps(source,sort_keys=True,separators=(',',':')).encode()
    write_private(state/'backup-result',result,0o600)
    write_private(state/'backup-result.sha256',(digest(state/'backup-result')+'  backup-result\n').encode(),0o600)
    command(['bash',str(helpers/'apply-nginx-ingress.sh')],raw,env)
    return {'schema':1,'status':'prepared','drill_id':manifest['drill_id']}


def main() -> None:
    parser = argparse.ArgumentParser(); sub = parser.add_subparsers(dest="command", required=True)
    p = sub.add_parser("stage"); p.add_argument("rootfs", type=Path); p.add_argument("manifest", type=Path)
    args = parser.parse_args()
    try:
        result = stage(args.rootfs, args.manifest)
        print(json.dumps(result, sort_keys=True))
    except (GuestPreparationBlocked, OSError, ValueError, KeyError, subprocess.SubprocessError):
        print(json.dumps({"schema": 1, "status": "blocked", "reason": "guest_preparation_failed; owned diagnostics preserved"}))
        raise SystemExit(1)


if __name__ == "__main__":
    main()
