from __future__ import annotations

import copy
import hashlib
import json
from pathlib import Path
import subprocess
import sys
import os
from unittest import mock

import pytest

sys.path.insert(0, str(Path(__file__).resolve().parents[2]))
from release import recovery_bundle as bundle


def fixture_files() -> dict[str, bytes]:
    files = {}
    for name in bundle.REQUIRED:
        category = 'release' if name.endswith('.py') else 'maintenance/181' if name == 'restore-backup-units.sh' else 'maintenance/release'
        files[bundle.PREFIX + category + '/' + name] = ('fixture ' + name + '\n').encode()
    # Repeated package basenames cannot collapse the complete source closure.
    files[bundle.PREFIX + 'release/__init__.py'] = b'release package\n'
    files[bundle.PREFIX + 'logging/release_logging/__init__.py'] = b'logging package\n'
    return files


def materialize(directory: Path):
    directory.mkdir()
    files = fixture_files()
    document = bundle.recovery_bundle_document('a' * 40, {name: hashlib.sha256(data).hexdigest() for name, data in files.items()})
    for relative in bundle.expected_files(document):
        target = directory / relative
        target.parent.mkdir(parents=True, exist_ok=True)
        if relative == bundle.BUNDLE_NAME:
            data = bundle.canonical(document)
        elif relative.startswith('sources/'):
            data = files[relative[len('sources/'):]]
        else:
            data = files[bundle.execution_paths(document['files'])[relative]]
        target.write_bytes(data)
        if os.name != 'nt':
            target.chmod(0o500 if relative.endswith('.sh') and '/' not in relative else 0o400)
    if os.name != 'nt':
        for root, _, _ in os.walk(directory):
            Path(root).chmod(0o700)
    bundle.verify_recovery_bundle(directory, document)
    return document


def test_full_closure_preserves_duplicate_nonexecution_basenames(tmp_path):
    document = materialize(tmp_path / 'bundle')
    assert len(document['files']) > len(bundle.execution_paths(document['files']))
    assert bundle.recovery_bundle_sha256(document) == hashlib.sha256(json.dumps(document, sort_keys=True, separators=(',', ':'), ensure_ascii=True).encode()).hexdigest()


@pytest.mark.parametrize('mutation', ['source', 'execution', 'extra_file', 'extra_dir', 'hardlink', 'symlink', 'manifest_newline'])
def test_bundle_rejects_any_byte_inventory_or_link_drift(tmp_path, mutation):
    directory = tmp_path / 'bundle'
    document = materialize(directory)
    if mutation == 'source':
        target = directory / 'sources' / next(iter(document['files']))
        target.write_bytes(b'drift')
    elif mutation == 'execution':
        (directory / 'restore.sh').write_bytes(b'drift')
    elif mutation == 'extra_file':
        (directory / 'unknown').write_bytes(b'')
    elif mutation == 'extra_dir':
        (directory / 'unknown').mkdir()
    elif mutation == 'manifest_newline':
        (directory / bundle.BUNDLE_NAME).write_bytes(bundle.canonical(document) + b'\n')
    else:
        target = directory / 'restore.sh'
        target.unlink()
        original = directory / 'sources' / bundle.execution_paths(document['files'])['restore.sh']
        if mutation == 'hardlink':
            target.hardlink_to(original)
        else:
            try:
                target.symlink_to(original)
            except OSError as error:
                if os.name == 'nt' and getattr(error, 'winerror', None) == 1314:
                    pytest.skip('Windows process lacks symlink privilege; live Linux integration covers links')
                raise
    with pytest.raises(RuntimeError):
        bundle.verify_recovery_bundle(directory, document)


@pytest.mark.parametrize('mutation', ['missing_helper', 'escape', 'bad_hash', 'unknown_schema', 'short_commit', 'unknown_field'])
def test_bundle_rejects_invalid_binding(tmp_path, mutation):
    document = materialize(tmp_path / 'bundle')
    broken = copy.deepcopy(document)
    if mutation == 'missing_helper':
        del broken['files'][bundle.PREFIX + 'maintenance/release/restore.sh']
    elif mutation == 'escape':
        broken['files'][bundle.PREFIX + 'maintenance/release/../../escape.sh'] = 'a' * 64
    elif mutation == 'bad_hash':
        broken['files'][next(iter(broken['files']))] = 'A' * 64
    elif mutation == 'unknown_schema':
        broken['schema'] = True
    elif mutation == 'short_commit':
        broken['commit_sha'] = 'a' * 12
    else:
        broken['pass'] = True
    with pytest.raises(RuntimeError):
        bundle.recovery_bundle_sha256(broken)


def test_builder_uses_committed_bytes_preserves_dirty_source(tmp_path):
    workspace = tmp_path / 'repo'
    workspace.mkdir()
    for relative, data in fixture_files().items():
        target = workspace / relative
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_bytes(data)
    for command in [
        ['git', 'init', '-q'], ['git', 'add', '.'],
        ['git', '-c', 'user.name=Recovery test', '-c', 'user.email=recovery-test@invalid', 'commit', '-qm', 'fixture'],
    ]:
        subprocess.run(command, cwd=workspace, check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    commit = subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=workspace, text=True).strip()
    checksums = {name: hashlib.sha256(data).hexdigest() for name, data in fixture_files().items()}
    document = bundle.recovery_bundle_document(commit, checksums)
    dirty = workspace / bundle.PREFIX / 'maintenance/release/restore.sh'
    dirty.write_bytes(b'user edits remain\n')
    directory = tmp_path / 'built'
    bundle.build_recovery_bundle(directory, document, workspace)
    assert (directory / 'restore.sh').read_bytes() == fixture_files()[bundle.PREFIX + 'maintenance/release/restore.sh']
    assert dirty.read_bytes() == b'user edits remain\n'


def test_manifest_binding_must_match_full_asset_closure(tmp_path):
    document = materialize(tmp_path / 'bundle')
    manifest = {'commit_sha': document['commit_sha'], 'release_asset_sha256': document['files'], 'recovery_helper_bundle_sha256': bundle.recovery_bundle_sha256(document)}
    assert bundle.bundle_from_manifest(manifest) == document
    manifest['recovery_helper_bundle_sha256'] = 'b' * 64
    with pytest.raises(RuntimeError):
        bundle.bundle_from_manifest(manifest)


def test_formal_entry_never_uses_old_claim_assets_or_whole_restore_shortcut():
    identifier = '270-' + 'a' * 12 + '-1791990000-' + 'b' * 8
    release = '/opt/sub2api/releases/' + identifier
    checksum = 'c' * 64
    command = bundle.recovery_command(release + '/recovery-helpers/' + checksum, checksum, release)
    assert '/.active-release/assets/' not in command
    assert 'nginx_restored.done' in command
    assert 'recovery-finish.sh' in command
    assert 'recovery_helper_bundle_sha256' in command
    assert 'recovery-point.tar' in command


def staging_fixture(tmp_path):
    local = tmp_path / 'bundles'
    local.mkdir(mode=0o700)
    document = bundle.recovery_bundle_document('a' * 40, {
        name: hashlib.sha256(data).hexdigest() for name, data in fixture_files().items()})
    checksum = bundle.recovery_bundle_sha256(document)
    materialize(local / checksum)
    manifest = {'commit_sha': document['commit_sha'], 'release_asset_sha256': document['files'],
                'recovery_helper_bundle_sha256': checksum}
    gate = tmp_path / 'gate'
    gate.mkdir()
    (gate / 'gate.json').write_bytes(bundle.canonical({'manifest': manifest, 'evidence': {
        'integration_verified': True, 'vm_restore_verified': True}}))
    (gate / 'gate.sig').write_bytes(b'fixture signature'.ljust(64, b'.'))
    ssh = mock.Mock()
    ssh.create_temp_dir.return_value = '/opt/sub2api/releases/.recovery-helpers-owned'
    ssh.run.return_value.values = {'recovery_helper_bundle_verified': 'true'}
    release = '/opt/sub2api/releases/270-aaaaaaaaaaaa-1-deadbeef'
    return local, gate, document, manifest, checksum, ssh, release


@pytest.mark.parametrize('mutation', ['root_noncanonical', 'root_symlink', 'root_ancestor_symlink',
                                    'archive_symlink', 'archive_hardlink', 'gate_symlink',
                                    'gate_hardlink', 'authorization_mismatch', 'missing_vm_evidence',
                                    'empty_signature'])
def test_staging_rejects_unsafe_local_paths_or_authorization_before_remote_actions(tmp_path, mutation):
    local, gate, _, manifest, checksum, ssh, release = staging_fixture(tmp_path)
    outside = tmp_path / 'outside'
    outside.write_bytes(b'preserved outside evidence')
    try:
        if mutation == 'root_noncanonical':
            local = local / '..' / local.name
        elif mutation == 'root_symlink':
            link = tmp_path / 'linked-root'
            link.symlink_to(local, target_is_directory=True)
            local = link
        elif mutation == 'root_ancestor_symlink':
            link = tmp_path / 'linked-parent'
            link.symlink_to(tmp_path, target_is_directory=True)
            local = link / local.name
        elif mutation == 'archive_symlink':
            (local / (checksum + '.tar')).symlink_to(outside)
        elif mutation == 'archive_hardlink':
            (local / (checksum + '.tar')).hardlink_to(outside)
        elif mutation in {'gate_symlink', 'gate_hardlink'}:
            source = gate / 'gate.sig'
            source.unlink()
            if mutation == 'gate_symlink':
                source.symlink_to(outside)
            else:
                source.hardlink_to(outside)
        elif mutation == 'empty_signature':
            (gate / 'gate.sig').write_bytes(b'')
        else:
            document = json.loads((gate / 'gate.json').read_bytes())
            if mutation == 'authorization_mismatch':
                document['manifest']['commit_sha'] = 'b' * 40
            else:
                document['evidence']['vm_restore_verified'] = False
            (gate / 'gate.json').write_bytes(bundle.canonical(document))
    except OSError as error:
        if os.name == 'nt' and getattr(error, 'winerror', None) == 1314:
            pytest.skip('Windows process lacks symlink privilege; live Linux integration covers links')
        raise
    with pytest.raises(RuntimeError):
        bundle.stage_recovery_bundle(ssh, manifest, release, local, tmp_path, gate)
    ssh.create_temp_dir.assert_not_called()
    ssh.upload_file.assert_not_called()
    ssh.run.assert_not_called()
    assert outside.read_bytes() == b'preserved outside evidence'


def test_staging_uploads_bound_authorization_snapshot_and_verifies_signature_before_install(tmp_path):
    local, gate, _, manifest, checksum, ssh, release = staging_fixture(tmp_path)
    original_gate = (gate / 'gate.json').read_bytes()
    original_signature = (gate / 'gate.sig').read_bytes()

    def mutate_original_after_snapshot(*args):
        (gate / 'gate.json').write_bytes(b'original changed after snapshot')
        (gate / 'gate.sig').unlink()
        return '/opt/sub2api/releases/.recovery-helpers-owned'

    ssh.create_temp_dir.side_effect = mutate_original_after_snapshot
    destination, actual = bundle.stage_recovery_bundle(ssh, manifest, release, local, tmp_path, gate)
    assert actual == checksum
    assert destination == release + '/recovery-helpers/' + checksum
    uploads = {Path(call.args[2]).name: call.args[1] for call in ssh.upload_file.call_args_list}
    assert uploads['gate.json'].read_bytes() == original_gate
    assert uploads['gate.sig'].read_bytes() == original_signature
    assert uploads['gate.json'].parent != gate
    script = ssh.run.call_args.args[1]
    assert hashlib.sha256(original_gate).hexdigest() in script
    assert hashlib.sha256(original_signature).hexdigest() in script
    assert script.index('openssl pkeyutl -verify') < script.index('install -d -m 700 -o root -g root "$parent"')
    assert script.index('openssl pkeyutl -verify') < script.index('tar --no-same-permissions')
    assert script.index('test -f "$trust"\ntest ! -L "$trust"') < script.index('openssl pkeyutl -verify')
    assert '0:0:644:1' in script
    assert '--gate "$temp/gate.json"' in script
    assert "0:0:400:1" in script
