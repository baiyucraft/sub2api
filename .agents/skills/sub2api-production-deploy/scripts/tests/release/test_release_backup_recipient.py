"""The signed full review selects a per-release public recipient only."""
import hashlib
from pathlib import Path
import sys
import tempfile
from types import SimpleNamespace
import unittest
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parents[2]))
from release.production import ProductionRelease


class ReleaseBackupRecipientTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.release = ProductionRelease.__new__(ProductionRelease)
        self.release.gate_dir = Path(self.temp.name)
        recipient = "age1" + "a" * 58
        self.metadata = {"recipient": recipient, "recipient_sha256": hashlib.sha256(recipient.encode()).hexdigest(), "identity_sha256": "1" * 64}
        self.source = {"recipient_type": "age1", "approved_recipient_sha256": self.metadata["recipient_sha256"], "approved_identity_sha256": self.metadata["identity_sha256"]}
        self.release.manifest = {"recovery_gate": {"schema": 2, "full_recovery_evidence": {"payload": {"source": self.source}}}}

    def test_public_asset_is_selected_from_the_approved_persistent_identity(self):
        with mock.patch("release.backup_keys.prepare_identity", return_value=SimpleNamespace(metadata=self.metadata)) as prepare:
            path = self.release._backup_recipient_asset()
        prepare.assert_called_once_with(expected_recipient_sha256=self.metadata["recipient_sha256"])
        self.assertEqual(path.read_text(), self.metadata["recipient"] + "\n")
        self.assertEqual(path.parent, self.release.gate_dir)
        self.assertNotIn("identity", path.name)

    def test_another_identity_with_the_same_recipient_is_rejected(self):
        self.source["approved_identity_sha256"] = "2" * 64
        with mock.patch("release.backup_keys.prepare_identity", return_value=SimpleNamespace(metadata=self.metadata)):
            with self.assertRaisesRegex(RuntimeError, "identity differs"):
                self.release._backup_recipient_asset()
        self.assertFalse((self.release.gate_dir / "backup-recipient.txt").exists())

    def test_recipient_value_is_rehashed(self):
        self.metadata["recipient"] = "age1" + "b" * 58
        with mock.patch("release.backup_keys.prepare_identity", return_value=SimpleNamespace(metadata=self.metadata)):
            with self.assertRaisesRegex(RuntimeError, "fingerprint differs"):
                self.release._backup_recipient_asset()

    def test_historical_release_does_not_prepare_or_replace_recipient(self):
        self.release.manifest = {"recovery_gate": {"schema": 1}}
        with mock.patch("release.backup_keys.prepare_identity") as prepare:
            self.assertIsNone(self.release._backup_recipient_asset())
        prepare.assert_not_called()


if __name__ == "__main__":
    unittest.main()
