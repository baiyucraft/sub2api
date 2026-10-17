"""One immutable helper closure for production recovery and isolated drills.

This module also runs standalone in a flattened execution directory. The
repository paths remain in the manifest and in sources/; only explicitly
selected entry points receive an execution copy at the bundle root.
"""
from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import re
import shlex
import stat
import subprocess
import tarfile
import tempfile
from typing import Any

PREFIX = '.agents/skills/sub2api-production-deploy/scripts/'
SHA = re.compile(r'[0-9a-f]{64}')
COMMIT = re.compile(r'[0-9a-f]{40}')
BUNDLE_NAME = 'recovery-helper-bundle.json'
REQUIRED = frozenset({
    'context.sh', 'compose-contract.sh', 'recovery-compose.sh',
    'recovery-checkpoint.sh', 'recovery-isolation.sh', 'restore.sh',
    'recovery-finish.sh', 'cleanup-state.sh', 'reconcile.sh',
    'nginx-ingress-contract.sh',
    'restore-backup-units.sh', 'recovery_bundle.py', 'recovery_drill.py',
})
FULL_RECOVERY_REQUIRED = frozenset({
    'full_recovery_drill.py', 'full_recovery_drill_authorization.py',
    'full_recovery_authorization.py', 'full_recovery_source.py', 'full_recovery_guest.py',
    'baseline_candidate_load.py', 'process.py',
})


def canonical(value: Any) -> bytes:
    return json.dumps(value, ensure_ascii=True, sort_keys=True, separators=(',', ':')).encode('utf-8')


def validate_bundle(document: dict[str, Any]) -> dict[str, Any]:
    if not isinstance(document, dict) or set(document) != {'schema', 'commit_sha', 'files'}:
        raise RuntimeError('helper bundle fields are invalid')
    if type(document['schema']) is not int or document['schema'] != 1:
        raise RuntimeError('helper bundle schema is invalid')
    if not isinstance(document['commit_sha'], str) or not COMMIT.fullmatch(document['commit_sha']):
        raise RuntimeError('helper bundle commit is invalid')
    files = document['files']
    if not isinstance(files, dict) or not files:
        raise RuntimeError('helper bundle closure is empty')
    for path, checksum in files.items():
        if not isinstance(path, str) or not path.startswith(PREFIX) or '\\' in path:
            raise RuntimeError('helper bundle path is invalid')
        parts = path.split('/')
        if any(part in {'', '.', '..'} for part in parts) or str(PurePosixPath(path)) != path:
            raise RuntimeError('helper bundle path is not canonical')
        relative = path[len(PREFIX):]
        # Match manifest.py's exact release-asset namespace, not a new exemption.
        permitted = (
            relative == 'release.py'
            or re.fullmatch(r'(release|maintenance/release|maintenance/181|logging/release_logging|windows)/[^/]+', relative)
            or re.fullmatch(r'release/(trust|drverify)/[^/]+', relative)
        )
        if not permitted or not isinstance(checksum, str) or not SHA.fullmatch(checksum):
            raise RuntimeError('helper bundle asset is invalid')
    aliases = execution_paths(files)
    if not REQUIRED.issubset(aliases):
        raise RuntimeError('formal recovery helper closure is incomplete')
    if 'full_recovery_drill.py' in aliases and not FULL_RECOVERY_REQUIRED.issubset(aliases):
        raise RuntimeError('full recovery helper closure is incomplete')
    return document


def execution_paths(files: dict[str, str]) -> dict[str, str]:
    result: dict[str, str] = {}
    for path in sorted(files):
        relative = path[len(PREFIX):]
        selected = (
            relative.startswith('maintenance/release/')
            or relative in {'maintenance/181/restore-backup-units.sh', 'maintenance/181/mask-backup-units.sh', 'release/recovery_bundle.py', 'release/recovery_drill.py', 'release/full_recovery_drill.py', 'release/full_recovery_drill_authorization.py', 'release/full_recovery_authorization.py', 'release/full_recovery_source.py', 'release/full_recovery_guest.py', 'release/baseline_candidate_load.py', 'release/process.py'}
        )
        if not selected:
            continue
        name = path.rsplit('/', 1)[1]
        if name in result or not re.fullmatch(r'[A-Za-z0-9_.-]+', name):
            raise RuntimeError('helper execution basename collision')
        result[name] = path
    return result


def recovery_bundle_document(commit: str, asset_checksums: dict[str, str]) -> dict[str, Any]:
    return validate_bundle({'schema': 1, 'commit_sha': commit, 'files': dict(asset_checksums)})


def recovery_bundle_sha256(document: dict[str, Any]) -> str:
    return hashlib.sha256(canonical(validate_bundle(document))).hexdigest()


def file_sha256(path: Path) -> str:
    value = hashlib.sha256()
    with path.open('rb') as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b''):
            value.update(chunk)
    return value.hexdigest()


def assert_canonical_path(path: Path) -> None:
    if path.is_symlink() or path.resolve() != path.absolute():
        raise RuntimeError('helper local path is unsafe')


def assert_local_file(path: Path) -> None:
    assert_canonical_path(path)
    if not path.is_file() or path.stat().st_nlink != 1:
        raise RuntimeError('helper local file is unsafe')


def read_local_file(path: Path) -> bytes:
    assert_local_file(path)
    fd = os.open(path, os.O_RDONLY | getattr(os, 'O_NOFOLLOW', 0))
    with os.fdopen(fd, 'rb') as stream:
        info = os.fstat(stream.fileno())
        observed = path.lstat()
        if not stat.S_ISREG(info.st_mode) or info.st_nlink != 1 or (info.st_dev, info.st_ino) != (observed.st_dev, observed.st_ino):
            raise RuntimeError('helper local file changed while opening')
        return stream.read()


def prepare_local_root(directory: Path) -> None:
    # Resolve every existing ancestor before mkdir can follow a link.
    assert_canonical_path(directory)
    directory.mkdir(mode=0o700, parents=True, exist_ok=True)
    assert_canonical_path(directory)
    if not directory.is_dir():
        raise RuntimeError('helper local directory is unsafe')
    info = directory.stat()
    if os.name != 'nt' and (info.st_uid != 0 or info.st_gid != 0 or stat.S_IMODE(info.st_mode) != 0o700):
        raise RuntimeError('helper local directory permissions are unsafe')


def validate_authorization(gate: dict[str, Any], document: dict[str, Any]) -> None:
    if not isinstance(gate, dict) or bundle_from_manifest(gate.get('manifest', {})) != document:
        raise RuntimeError('helper bundle differs from signed authorization')
    evidence = gate.get('evidence', {})
    if not isinstance(evidence, dict) or evidence.get('integration_verified') is not True or evidence.get('vm_restore_verified') is not True:
        raise RuntimeError('helper authorization lacks verified VM recovery')


def expected_files(document: dict[str, Any]) -> dict[str, str]:
    validate_bundle(document)
    result = {'sources/' + path: checksum for path, checksum in document['files'].items()}
    result.update({name: document['files'][source] for name, source in execution_paths(document['files']).items()})
    result[BUNDLE_NAME] = recovery_bundle_sha256(document)
    return result


def verify_recovery_bundle(directory: Path, document: dict[str, Any]) -> None:
    validate_bundle(document)
    if not directory.is_dir() or directory.is_symlink() or directory.resolve() != directory.absolute():
        raise RuntimeError('helper bundle directory is unsafe')
    expected = expected_files(document)
    expected_directories = {str(parent) for relative in expected for parent in PurePosixPath(relative).parents if str(parent) != '.'}
    actual: set[str] = set()
    for root, subdirs, names in os.walk(directory, followlinks=False):
        for name in subdirs + names:
            path = Path(root) / name
            if path.is_symlink():
                raise RuntimeError('helper bundle symlink rejected')
            info = path.stat()
            if path.is_dir() and path.relative_to(directory).as_posix() not in expected_directories:
                raise RuntimeError('helper bundle directory inventory differs')
            if os.name != 'nt' and (info.st_uid != 0 or info.st_gid != 0 or stat.S_IMODE(info.st_mode) not in ({0o700} if path.is_dir() else {0o400, 0o500})):
                raise RuntimeError('helper bundle permissions are unsafe')
        for name in names:
            path = Path(root) / name
            relative = path.relative_to(directory).as_posix()
            if not path.is_file() or path.stat().st_nlink != 1 or relative not in expected:
                raise RuntimeError('helper bundle inventory differs')
            if file_sha256(path) != expected[relative]:
                raise RuntimeError('helper bundle bytes differ')
            actual.add(relative)
    if os.name != 'nt' and (directory.stat().st_uid != 0 or directory.stat().st_gid != 0 or stat.S_IMODE(directory.stat().st_mode) != 0o700):
        raise RuntimeError('helper bundle root permissions are unsafe')
    if actual != set(expected) or (directory / BUNDLE_NAME).read_bytes() != canonical(document):
        raise RuntimeError('helper bundle closure differs')


def build_recovery_bundle(directory: Path, document: dict[str, Any], workspace: Path) -> None:
    """Materialize exact committed bytes; never substitute dirty workspace files."""
    validate_bundle(document)
    assert_canonical_path(directory)
    if directory.exists() or directory.is_symlink():
        raise RuntimeError('helper bundle output already exists')
    directory.mkdir(mode=0o700, parents=True)
    for source, checksum in sorted(document['files'].items()):
        process = subprocess.run(['git', 'show', document['commit_sha'] + ':' + source], cwd=workspace, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, check=True,
                                 creationflags=getattr(subprocess, 'CREATE_NO_WINDOW', 0))
        if hashlib.sha256(process.stdout).hexdigest() != checksum:
            raise RuntimeError('committed helper asset differs from signed closure')
        target = directory / 'sources' / source
        target.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
        target.write_bytes(process.stdout)
        if os.name != 'nt':
            target.chmod(0o400)
    for name, source in execution_paths(document['files']).items():
        target = directory / name
        target.write_bytes((directory / 'sources' / source).read_bytes())
        if os.name != 'nt':
            target.chmod(0o500 if name.endswith('.sh') else 0o400)
    (directory / BUNDLE_NAME).write_bytes(canonical(document))
    if os.name != 'nt':
        (directory / BUNDLE_NAME).chmod(0o400)
        # mkdir(parents=True) uses default mode for intermediate directories.
        for root, dirs, _ in os.walk(directory):
            Path(root).chmod(0o700)
    verify_recovery_bundle(directory, document)


def bundle_from_manifest(manifest: dict[str, Any]) -> dict[str, Any]:
    try:
        document = recovery_bundle_document(manifest['commit_sha'], manifest['release_asset_sha256'])
    except (KeyError, TypeError) as error:
        raise RuntimeError('signed recovery helper manifest is invalid') from error
    if manifest.get('recovery_helper_bundle_sha256') != recovery_bundle_sha256(document):
        raise RuntimeError('signed recovery helper binding is absent or mismatched')
    return document


def stage_recovery_bundle(ssh, manifest: dict[str, Any], release_dir: str, local_root: Path, workspace: Path, gate_dir: Path) -> tuple[str, str]:
    """Install a signed exact closure under the release, preserving failed evidence."""
    document = bundle_from_manifest(manifest)
    checksum = recovery_bundle_sha256(document)
    if not re.fullmatch(r'/opt/sub2api/releases/[0-9]+-[0-9a-f]{12}-[0-9]+-[0-9a-f]{8}', release_dir):
        raise RuntimeError('recovery release directory is invalid')
    # Validate the immutable local authorization before materializing/uploading
    # anything. Remote openssl verification still proves its trusted signature.
    assert_canonical_path(gate_dir)
    authorization = {}
    for name in ('gate.json', 'gate.sig'):
        source = gate_dir / name
        authorization[name] = read_local_file(source)
    validate_authorization(json.loads(authorization['gate.json']), document)
    if len(authorization['gate.sig']) != 64:
        raise RuntimeError('signed helper authorization is incomplete')
    prepare_local_root(local_root)
    directory = local_root / checksum
    if directory.exists() or directory.is_symlink():
        verify_recovery_bundle(directory, document)
    else:
        build_recovery_bundle(directory, document, workspace)
    archive_path = local_root / (checksum + '.tar')
    assert_canonical_path(archive_path)
    if archive_path.exists():
        assert_local_file(archive_path)
    # An exclusive descriptor prevents opening an existing tar symlink for
    # truncation. Publish the completed archive only inside the private root.
    fd, temporary_archive = tempfile.mkstemp(prefix=checksum + '.', suffix='.tar', dir=local_root)
    with os.fdopen(fd, 'wb') as output, tarfile.open(fileobj=output, mode='w', format=tarfile.PAX_FORMAT) as archive:
        for relative in sorted(expected_files(document)):
            path = directory / relative
            info = archive.gettarinfo(str(path), arcname=relative)
            info.uid = info.gid = 0
            info.uname = info.gname = 'root'
            info.mode = 0o500 if relative.endswith('.sh') and '/' not in relative else 0o400
            info.mtime = 0
            with path.open('rb') as stream:
                archive.addfile(info, stream)
    assert_canonical_path(archive_path)
    if archive_path.exists():
        assert_local_file(archive_path)
    os.replace(temporary_archive, archive_path)
    if os.name != 'nt':
        archive_path.chmod(0o400)
    archive_sha = file_sha256(archive_path)
    # Upload snapshots, not paths that can change after authorization checks.
    authorization_directory = Path(tempfile.mkdtemp(prefix=checksum + '.authorization-', dir=local_root))
    for name, data in authorization.items():
        target = authorization_directory / name
        with target.open('xb') as output:
            output.write(data)
        if os.name != 'nt':
            target.chmod(0o400)
    temp = ssh.create_temp_dir('racknerd', '/opt/sub2api/releases', 'recovery-helpers')
    remote_archive = temp + '/bundle.tar'
    ssh.upload_file('racknerd', archive_path, remote_archive, 0o400)
    for name in ('gate.json', 'gate.sig'):
        source = authorization_directory / name
        ssh.upload_file('racknerd', source, temp + '/' + name, 0o400)
    destination = release_dir + '/recovery-helpers/' + checksum
    verifier_sha = document['files'][PREFIX + 'release/recovery_bundle.py']
    script = f'''set -Eeuo pipefail
required_value=$(id -u)
test "$required_value" = 0
exec 8>/run/lock/sub2api-production-release.lock
flock -xn 8
release={shlex.quote(release_dir)}
parent="$release/recovery-helpers"
dest={shlex.quote(destination)}
temp={shlex.quote(temp)}
test -d "$release"
test ! -L "$release"
required_value=$(realpath -e "$release")
test "$required_value" = "$release"
required_value=$(stat -c '%u:%g:%a' "$release")
test "$required_value" = 0:0:700
required_value=$(stat -c '%u:%g:%a' "$temp")
test "$required_value" = 0:0:700
required_value=$(realpath -e "$temp")
test "$required_value" = "$temp"
required_value=$(sha256sum "$temp/bundle.tar" | awk '{{print $1}}')
test "$required_value" = {shlex.quote(archive_sha)}
required_value=$(sha256sum "$temp/gate.json" | awk '{{print $1}}')
test "$required_value" = {shlex.quote(hashlib.sha256(authorization['gate.json']).hexdigest())}
required_value=$(sha256sum "$temp/gate.sig" | awk '{{print $1}}')
test "$required_value" = {shlex.quote(hashlib.sha256(authorization['gate.sig']).hexdigest())}
for name in bundle.tar gate.json gate.sig; do
  test -f "$temp/$name"
  test ! -L "$temp/$name"
  required_value=$(stat -c '%u:%g:%a:%h' "$temp/$name")
  test "$required_value" = 0:0:400:1
done
trust_dir=/opt/sub2api-release-trust
trust="$trust_dir/vm-gate-ed25519.pub"
test -d "$trust_dir"
test ! -L "$trust_dir"
required_value=$(realpath -e "$trust_dir")
test "$required_value" = "$trust_dir"
required_value=$(stat -c '%u:%g:%a' "$trust_dir")
test "$required_value" = 0:0:755
test -f "$trust"
test ! -L "$trust"
required_value=$(stat -c '%u:%g:%a:%h' "$trust")
test "$required_value" = 0:0:644:1
openssl pkeyutl -verify -pubin -inkey "$trust" -rawin -in "$temp/gate.json" -sigfile "$temp/gate.sig" >/dev/null 2>&1
if test ! -e "$parent" && test ! -L "$parent"; then install -d -m 700 -o root -g root "$parent"; fi
test -d "$parent"
test ! -L "$parent"
required_value=$(realpath -e "$parent")
test "$required_value" = "$parent"
required_value=$(stat -c '%u:%g:%a' "$parent")
test "$required_value" = 0:0:700
if test ! -e "$dest" && test ! -L "$dest"; then
  install -d -m 700 -o root -g root "$temp/extracted"
  tar --no-same-permissions -xf "$temp/bundle.tar" -C "$temp/extracted"
  find "$temp/extracted" -type d -exec chmod 700 {{}} +
  find "$temp/extracted" -type f -exec chmod 400 {{}} +
  find "$temp/extracted" -maxdepth 1 -name '*.sh' -type f -exec chmod 500 {{}} +
  required_value=$(sha256sum "$temp/extracted/recovery_bundle.py" | awk '{{print $1}}')
  test "$required_value" = {shlex.quote(verifier_sha)}
  python3 "$temp/extracted/recovery_bundle.py" --verify "$temp/extracted" --expected {shlex.quote(checksum)} --gate "$temp/gate.json" >/dev/null
  mv -T -- "$temp/extracted" "$dest"
fi
test -d "$dest"
test ! -L "$dest"
required_value=$(sha256sum "$dest/recovery_bundle.py" | awk '{{print $1}}')
test "$required_value" = {shlex.quote(verifier_sha)}
python3 "$dest/recovery_bundle.py" --verify "$dest" --expected {shlex.quote(checksum)} --gate "$temp/gate.json" >/dev/null
auth="$parent/{checksum}.authorization"
if test ! -e "$auth" && test ! -L "$auth"; then install -d -m 700 -o root -g root "$auth"; fi
test -d "$auth"
test ! -L "$auth"
required_value=$(realpath -e "$auth")
test "$required_value" = "$auth"
required_value=$(stat -c '%u:%g:%a' "$auth")
test "$required_value" = 0:0:700
for name in gate.json gate.sig; do
  if test -e "$auth/$name" || test -L "$auth/$name"; then
    test -f "$auth/$name"
    test ! -L "$auth/$name"
    required_value=$(stat -c '%u:%g:%a:%h' "$auth/$name")
    test "$required_value" = 0:0:400:1
    cmp -s "$auth/$name" "$temp/$name"
  else
    install -m 400 -o root -g root "$temp/$name" "$auth/$name"
  fi
done
rm -rf -- "$temp"
printf 'recovery_helper_bundle_verified=true\\n'
'''
    values = ssh.run('racknerd', script, {'recovery_helper_bundle_verified'}, timeout=600).values
    if values.get('recovery_helper_bundle_verified') != 'true':
        raise RuntimeError('remote helper closure was not verified')
    return destination, checksum


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument('--verify', type=Path, required=True)
    parser.add_argument('--expected', required=True)
    parser.add_argument('--gate', type=Path)
    args = parser.parse_args()
    if not SHA.fullmatch(args.expected):
        raise RuntimeError('expected helper bundle identity is invalid')
    path = args.verify / BUNDLE_NAME
    if not path.is_file() or path.is_symlink() or path.stat().st_nlink != 1:
        raise RuntimeError('helper bundle manifest is unsafe')
    document = json.loads(path.read_bytes())
    if recovery_bundle_sha256(document) != args.expected:
        raise RuntimeError('expected helper bundle identity differs')
    if args.gate:
        if not args.gate.is_file() or args.gate.is_symlink() or args.gate.stat().st_nlink != 1:
            raise RuntimeError('helper Gate authorization is unsafe')
        gate = json.loads(args.gate.read_bytes())
        validate_authorization(gate, document)
    verify_recovery_bundle(args.verify, document)
    print('recovery_helper_bundle_verified=true')


RECOVERY_RESULT_FIELDS = frozenset({
    'coordinated_restore', 'restored_image_id', 'application_health',
    'backup_units_restored', 'plaintext_state_removed', 'release_claim_reconciled',
    'recovery_helper_bundle_sha256',
})


def recovery_command(assets: str, checksum: str, release_dir: str) -> str:
    if not SHA.fullmatch(checksum) or assets != release_dir + '/recovery-helpers/' + checksum:
        raise RuntimeError('formal recovery entry identity is invalid')
    if not re.fullmatch(r'/opt/sub2api/releases/[0-9]+-[0-9a-f]{12}-[0-9]+-[0-9a-f]{8}', release_dir):
        raise RuntimeError('formal recovery release identity is invalid')
    identifier = release_dir.rsplit('/', 1)[1]
    return f'''set -Eeuo pipefail
export RELEASE_DIR={shlex.quote(release_dir)}
export RECOVERY_ASSETS_DIR={shlex.quote(assets)}
export RECOVERY_HELPER_BUNDLE_SHA256={shlex.quote(checksum)}
export RECOVERY_HELPER_GATE={shlex.quote(assets + '.authorization/gate.json')}
export RECOVERY_HELPER_SIGNATURE={shlex.quote(assets + '.authorization/gate.sig')}
export RELEASE_LOCK_HELD=true
exec 8>/run/lock/sub2api-production-release.lock
flock -xn 8
test -d "$RELEASE_DIR"
test ! -L "$RELEASE_DIR"
logdir="$RELEASE_DIR/logs"
if test ! -e "$logdir" && test ! -L "$logdir"; then install -d -m 700 -o root -g root "$logdir"; fi
test -d "$logdir"
test ! -L "$logdir"
required_value=$(realpath -e "$logdir")
test "$required_value" = "$logdir"
required_value=$(stat -c '%u:%g:%a' "$logdir")
test "$required_value" = 0:0:700
export SUB2API_RELEASE_RAW_LOG="$logdir/production.raw.log"
if test ! -e "$SUB2API_RELEASE_RAW_LOG" && test ! -L "$SUB2API_RELEASE_RAW_LOG"; then install -m 600 -o root -g root /dev/null "$SUB2API_RELEASE_RAW_LOG"; fi
test -f "$SUB2API_RELEASE_RAW_LOG"
test ! -L "$SUB2API_RELEASE_RAW_LOG"
required_value=$(stat -c '%u:%g:%a:%h' "$SUB2API_RELEASE_RAW_LOG")
test "$required_value" = 0:0:600:1
state=/opt/sub2api/backups/release-state/{identifier}
# After cleanup has consumed the plaintext point, only finish can continue.
# It validates the surviving checkpoint identity and every final live invariant.
if test -f "$state/recovery-point.tar" && test ! -L "$state/recovery-point.tar"; then
  bash "$RECOVERY_ASSETS_DIR/restore.sh" >>"$SUB2API_RELEASE_RAW_LOG" 2>&1
else
  test ! -e "$state/recovery-point.tar"
  test ! -L "$state/recovery-point.tar"
  test -f "$RELEASE_DIR/recovery-checkpoints/nginx_restored.done"
  test ! -L "$RELEASE_DIR/recovery-checkpoints/nginx_restored.done"
fi
bash "$RECOVERY_ASSETS_DIR/recovery-finish.sh" >>"$SUB2API_RELEASE_RAW_LOG" 2>&1
identity="$RELEASE_DIR/recovery-checkpoints/identity"
test -f "$identity"
test ! -L "$identity"
required_value=$(stat -c '%u:%g:%a:%h' "$identity")
test "$required_value" = 0:0:400:1
image=$(sed -n 's/^pre_image_id=//p' "$identity")
printf 'coordinated_restore=verified\\nrestored_image_id=%s\\napplication_health=pass\\n' "$image"
printf 'backup_units_restored=true\\nplaintext_state_removed=true\\nrelease_claim_reconciled=true\\n'
printf 'recovery_helper_bundle_sha256=%s\\n' "$RECOVERY_HELPER_BUNDLE_SHA256"
'''


if __name__ == '__main__':
    main()
