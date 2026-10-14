from __future__ import annotations

import hashlib
import json
import os
from pathlib import Path
import sys
import tempfile
import unittest
from unittest import mock

SCRIPTS = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(SCRIPTS))
from release import backup_keys as keys


class BackupKeyBoundaryTest(unittest.TestCase):
    def test_workspace_private_root_rejected_before_creation(self):
        with self.assertRaises(keys.KeyPreparationError):
            keys.prepare_root(keys.WORKSPACE / '.tmp/forbidden-private-key')

    def test_hardlinked_identity_rejected(self):
        with tempfile.TemporaryDirectory() as temp:
            original = Path(temp) / 'identity'
            original.write_bytes(b'private-placeholder')
            linked = Path(temp) / 'linked'
            os.link(original, linked)
            with self.assertRaises(keys.KeyPreparationError):
                keys.no_links(linked)

    def test_exclusive_creation_preserves_existing_identity(self):
        with tempfile.TemporaryDirectory() as temp:
            path = Path(temp) / 'identity'
            path.write_bytes(b'preserve')
            with self.assertRaises(keys.KeyPreparationError):
                keys.exclusive_write(path, b'overwrite')
            self.assertEqual(path.read_bytes(), b'preserve')

    def test_failed_tool_output_withheld(self):
        with tempfile.TemporaryDirectory() as temp:
            tool = Path(temp) / 'tool'
            tool.write_bytes(b'executable-placeholder')
            with mock.patch.object(keys, 'run_hidden', return_value=mock.Mock(returncode=1, stdout=b'AGE-SECRET-KEY', stderr=b'private-path')):
                with self.assertRaises(keys.KeyPreparationError) as raised:
                    keys.tool_run(tool, [])
            self.assertNotIn('SECRET', str(raised.exception))
            self.assertNotIn('private-path', str(raised.exception))

    def test_public_result_never_exposes_storage_or_recipient(self):
        private = keys.PreparedIdentity(Path('/private/identity'), {'version':'version', 'created_at':'time', 'recipient':'age1public', 'identity_sha256':'a'*64, 'recipient_sha256':'b'*64})
        result = private.public()
        self.assertEqual(result['full_recovery_status'], 'not_checked')
        self.assertNotIn('identity_path', result)
        self.assertNotIn('recipient', result)
        self.assertNotIn('/private', json.dumps(result))

    def test_wrong_selftest_readback_cleans_owned_material(self):
        with tempfile.TemporaryDirectory() as temp:
            directory = Path(temp)
            identity = directory / 'identity'
            identity.write_bytes(b'keep')
            def fake_tool(_tool, args, **_):
                output = Path(args[args.index('-o') + 1])
                output.write_bytes(b'incorrect-readback')
                return b''
            with mock.patch.object(keys, 'private_permissions'), mock.patch.object(keys, 'tool_run', side_effect=fake_tool):
                with self.assertRaises(keys.KeyPreparationError):
                    keys.selftest(directory, identity, 'recipient', Path('/tool'))
            self.assertEqual(list(directory.iterdir()), [identity])
            self.assertEqual(identity.read_bytes(), b'keep')

    def test_existing_registry_mismatch_fails_before_decryption(self):
        with tempfile.TemporaryDirectory() as temp:
            directory = Path(temp) / 'age-x25519-20261015T010203Z-0123456789abcdef'
            directory.mkdir()
            (directory / 'identity.agekey').write_bytes(b'identity')
            metadata = {'schema':1,'version':directory.name,'created_at':'time','recipient':'recipient','recipient_sha256':'b'*64,'identity_sha256':'c'*64,'tool_sha256':{}}
            (directory / 'registry.json').write_text(json.dumps(metadata))
            with mock.patch.object(keys, 'private_permissions'), mock.patch.object(keys, 'tool_run') as tool:
                with self.assertRaises(keys.KeyPreparationError):
                    keys._validate_existing(directory, Path('/age'), Path('/keygen'))
                tool.assert_not_called()

    def test_missing_historical_key_cannot_generate_replacement_for_old_ciphertext(self):
        with tempfile.TemporaryDirectory() as temp:
            directory = Path(temp)
            tool_dir = directory / 'tools'
            tool_dir.mkdir()
            for name in ('age', 'age-keygen'):
                (tool_dir / (name + ('.exe' if os.name == 'nt' else ''))).write_bytes(b'tool')
            root = directory / 'private'
            root.mkdir()
            with mock.patch.object(keys,'prepare_root'), mock.patch.object(keys,'private_permissions'), mock.patch.object(keys,'verify_tool_version'), mock.patch.object(keys,'tool_run') as tool:
                with self.assertRaisesRegex(keys.KeyPreparationError, 'matching historical identity unavailable'):
                    keys.prepare_identity(root=root,tool_dir=tool_dir,expected_recipient_sha256='a'*64)
                tool.assert_not_called()
                self.assertEqual(list(root.iterdir()), [])

    def test_original_tool_junction_is_rejected_before_resolve(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp) / 'private'
            tools = Path(temp) / 'linked-tools'
            with mock.patch.object(keys, 'prepare_root'), mock.patch.object(keys, 'no_links', side_effect=keys.KeyPreparationError('linked path')):
                with self.assertRaisesRegex(keys.KeyPreparationError, 'linked path'):
                    keys.prepare_identity(root=root, tool_dir=tools)

    def test_pinned_version_failure_never_generates_identity(self):
        with tempfile.TemporaryDirectory() as temp:
            tools = Path(temp) / 'tools'
            tools.mkdir()
            for name in ('age', 'age-keygen'):
                (tools / (name + ('.exe' if os.name == 'nt' else ''))).write_bytes(b'tool')
            with mock.patch.object(keys, 'prepare_root'), mock.patch.object(keys, 'verify_tool_version', side_effect=keys.KeyPreparationError('version differs')), mock.patch.object(keys, 'tool_run') as run:
                with self.assertRaisesRegex(keys.KeyPreparationError, 'version differs'):
                    keys.prepare_identity(root=Path(temp) / 'private', tool_dir=tools)
                run.assert_not_called()

    def test_tool_version_output_is_not_exposed(self):
        with mock.patch.object(keys, 'tool_run', return_value=b'wrong-version-sensitive-placeholder'):
            with self.assertRaises(keys.KeyPreparationError) as raised:
                keys.verify_tool_version(Path('/tool'))
            self.assertNotIn('sensitive', str(raised.exception))

    def test_unix_tool_mode_retains_owner_execute(self):
        if os.name == 'nt':
            self.skipTest('Unix executable mode only')
        with tempfile.TemporaryDirectory() as temp:
            tool = Path(temp) / 'age'
            tool.write_bytes(b'tool')
            keys.private_permissions(tool, initialize=True, executable=True)
            self.assertEqual(tool.stat().st_mode & 0o777, 0o700)

    def test_build_failure_summary_is_fixed_allowlisted_code(self):
        self.assertEqual(keys.install_failure_category(b'https://private.invalid failed TLS handshake timeout secret'), 'tls_timeout')
        self.assertEqual(keys.install_failure_category(b'unknown private-path credentials'), 'unclassified_install_failure')


if __name__ == '__main__':
    unittest.main()
