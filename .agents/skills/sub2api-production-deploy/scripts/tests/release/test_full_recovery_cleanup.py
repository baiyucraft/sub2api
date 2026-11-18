from contextlib import nullcontext
import hashlib
import json
from pathlib import Path
import sys
from unittest.mock import MagicMock

import pytest

sys.path.insert(0, str(Path(__file__).resolve().parents[2]))
from release import full_recovery_cleanup as cleanup


@pytest.fixture
def adapter(tmp_path, monkeypatch):
    value = cleanup.FailureAdapter.__new__(cleanup.FailureAdapter)
    value.directory = tmp_path
    value.owner_path = tmp_path / 'owner.json'
    value.settings = object()
    value.runner = MagicMock()
    value.identity = {'target_commit': 'a' * 40, 'bundle_sha256': 'b' * 64, 'pid': 7, 'process_token': 'win:123'}
    trusted = tmp_path / 'trusted.pub'
    trusted.write_bytes(b'pinned public key fixture')
    monkeypatch.setattr(cleanup, 'TRUSTED_VM_PUBLIC_KEY', trusted)
    manifest = b'frozen guest manifest fixture'
    (tmp_path / 'guest-manifest.json').write_bytes(manifest)
    documents = {
        'helper/recovery-helper-bundle.json': {'commit_sha': 'a' * 40},
        'offline-request.json': {'trusted_public_key_sha256': hashlib.sha256(trusted.read_bytes()).hexdigest(),
                                 'target_commit': 'a' * 40, 'bundle_sha256': 'b' * 64,
                                 'guest_manifest_sha256': hashlib.sha256(manifest).hexdigest(),
                                 'recovery_helper_bundle_sha256': 'b' * 64},
        'helper-stage.json': {'remote': '/opt/sub2api-full-recovery-input/review-stage.ABC123'},
        'drill-launch-receipt.json': {'started_at': 100},
    }
    monkeypatch.setattr(cleanup, '_json', lambda _, name: documents[name])
    monkeypatch.setattr(cleanup, 'recovery_bundle_sha256', lambda _: 'b' * 64)
    monkeypatch.setattr(cleanup, 'verify_recovery_bundle', lambda *_: None)
    monkeypatch.setattr(cleanup, 'expected_files', lambda _: {'test': 'f' * 64})
    return value, documents


def test_payload_binds_current_pinned_inputs(adapter):
    value, documents = adapter
    payload = value._payload()
    assert payload['manifest_sha256'] == documents['offline-request.json']['guest_manifest_sha256']
    assert payload['helper_path'].endswith('/review-stage.ABC123/helper')
    assert payload['authorization'] == documents['offline-request.json']


@pytest.mark.parametrize(('document', 'field', 'corrupt', 'error'), [
    ('helper/recovery-helper-bundle.json', 'commit_sha', 'f' * 40, 'full_dr_original_bundle_changed'),
    ('offline-request.json', 'trusted_public_key_sha256', 'f' * 64, 'full_dr_pinned_trust_or_target_changed'),
    ('offline-request.json', 'target_commit', 'f' * 40, 'full_dr_pinned_trust_or_target_changed'),
    ('offline-request.json', 'guest_manifest_sha256', 'f' * 64, 'full_dr_signed_closure_changed'),
    ('offline-request.json', 'recovery_helper_bundle_sha256', 'f' * 64, 'full_dr_signed_closure_changed'),
    ('helper-stage.json', 'remote', '/tmp/candidate', 'full_dr_helper_stage_invalid'),
])
def test_payload_blocks_changed_identity(adapter, document, field, corrupt, error):
    value, documents = adapter
    documents[document][field] = corrupt
    with pytest.raises(RuntimeError, match=error):
        value._payload()


def test_ready_failure_closes_transport_without_unlock_request(adapter, monkeypatch):
    value, _ = adapter
    monkeypatch.setattr(cleanup, 'RunLock', lambda *_, **__: nullcontext())
    monkeypatch.setattr(cleanup.vm_lifecycle, 'shared_lock', lambda *_: nullcontext())
    monkeypatch.setattr(cleanup, 'process_identity', lambda _: None)
    client = value.runner.connect.return_value
    stdin, stdout, stderr = MagicMock(), MagicMock(), MagicMock()
    client.exec_command.return_value = stdin, stdout, stderr
    stdout.readline.side_effect = OSError('read failed during ready')
    with pytest.raises(OSError, match='read failed during ready'):
        with value.locks():
            pytest.fail('startup failure reached protected operation')
    client.close.assert_called_once()
    assert stdin.write.call_count == 1


@pytest.fixture
def remote_cleanup(adapter, monkeypatch):
    value, _ = adapter
    value.expected_diagnostics = {'failure.json': {'sha256': 'c' * 64, 'bytes': 123}}
    root = {'path': '/var/lib/sub2api-full-recovery-drills/id/rootfs', 'device': 1, 'inode': 10}
    receipt = {'schema': 1, 'rootfs': root.copy(), 'status': 'deleted', 'archive_sha256': 'a' * 64,
               'foreign_before_sha256': 'b' * 64, 'foreign_after_sha256': 'b' * 64, 'completed_at': 10}
    monkeypatch.setattr(value, '_call', MagicMock(return_value=receipt))
    return value, root, receipt


@pytest.mark.parametrize(('field', 'corrupt'), [('archive_sha256', 'bad'), ('rootfs', {}), ('status', 'blocked'),
                                                ('foreign_after_sha256', 'a' * 64), ('schema', True)])
def test_bad_remote_receipt_cannot_prove_cleanup(remote_cleanup, field, corrupt):
    value, root, receipt = remote_cleanup
    receipt[field] = corrupt
    with pytest.raises(RuntimeError, match='full_dr_remote_cleanup_receipt_invalid'):
        value.cleanup(root)
    assert not (value.directory / 'failed-closure-remote-receipt.json').exists()


def test_verified_remote_receipt_is_preserved_before_normalizing(remote_cleanup):
    value, root, receipt = remote_cleanup
    observed = value.cleanup(root)
    assert observed == {'schema': 1, 'rootfs': root, 'status': 'deleted',
                        'remote_receipt_sha256': hashlib.sha256(cleanup.canonical_json(receipt)).hexdigest()}
    value._call.assert_called_once_with('cleanup', rootfs=root, diagnostics=value.expected_diagnostics)
    assert json.loads((value.directory / 'failed-closure-remote-receipt.json').read_bytes()) == receipt


def test_inline_vm_program_compiles():
    compile(cleanup.REMOTE, '<offline cleanup VM program>', 'exec')
