"""Archive attack cases and approved encrypted source binding."""
from copy import deepcopy
import hashlib
import io
import os
from pathlib import Path
import subprocess
import sys
import tarfile
import tempfile
import unittest
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parents[2]))
from release import full_recovery_source as source
from release.atomic import canonical_json


RELEASE = "270-" + "1" * 12 + "-1792000000-aabbccdd"
IMAGE = "sha256:" + "2" * 64


class FullRecoverySourceTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name).resolve()
        os.chmod(self.root, 0o700)
        self.files = {
            "database/sub2api.dump": b"PGDMP fixture",
            "database/globals.sql": b"CREATE ROLE fixture;",
            "redis/dump.rdb": b"REDIS fixture",
            "config/app/docker-compose.yml": b"services: {}",
            "config/app/.env": b"secret fixture",
            "config/nginx/nginx.conf": b"events {}",
            "metadata/manifest.txt": ("release_id=" + RELEASE + "\npre_switch_image_id=" + IMAGE + "\n").encode(),
        }

    def archive(self, *, extras=(), mutate_files=None, modes=None, owners=None, missing_checksum=False):
        files = deepcopy(self.files)
        if mutate_files:
            mutate_files(files)
        if not missing_checksum:
            files["SHA256SUMS"] = b"".join(hashlib.sha256(data).hexdigest().encode() + b"  ./" + name.encode() + b"\n" for name, data in sorted(files.items()))
        path = self.root / "source.tar"
        with tarfile.open(path, "w") as bundle:
            top = tarfile.TarInfo(".")
            top.type = tarfile.DIRTYPE
            top.mode = 0o700
            bundle.addfile(top)
            for name, data in files.items():
                member = tarfile.TarInfo("./" + name)
                member.size = len(data)
                member.mode = (modes or {}).get(name, 0o600)
                member.uid, member.gid = (owners or {}).get(name, (0, 0))
                bundle.addfile(member, io.BytesIO(data))
            for member, data in extras:
                bundle.addfile(member, io.BytesIO(data) if data else None)
        os.chmod(path, 0o600)
        return path

    def inspect(self, archive):
        return source.inspect_archive(archive, release_id=RELEASE, pre_image_id=IMAGE)

    def test_inventory_measures_actual_pg_and_rdb_bytes(self):
        archive = self.archive()
        result = self.inspect(archive)
        self.assertEqual(result["postgres_dump_sha256"], hashlib.sha256(self.files["database/sub2api.dump"]).hexdigest())
        self.assertEqual(result["redis_rdb_sha256"], hashlib.sha256(self.files["redis/dump.rdb"]).hexdigest())
        self.assertEqual(result["recovery_point_sha256"], hashlib.sha256(archive.read_bytes()).hexdigest())

    def test_restore_extracts_sensitive_bytes_privately(self):
        archive = self.archive()
        destination = self.root / "restored"
        result = source.extract_archive(archive, destination, release_id=RELEASE, pre_image_id=IMAGE)
        self.assertEqual((destination / "config/app/.env").read_bytes(), self.files["config/app/.env"])
        self.assertEqual(result, self.inspect(archive))
        if os.name != "nt":
            self.assertEqual((destination / "config/app/.env").stat().st_mode & 0o777, 0o600)

    def test_wrong_current_production_image_is_rejected(self):
        archive = self.archive()
        with self.assertRaisesRegex(RuntimeError, "old image differs"):
            source.inspect_archive(archive, release_id=RELEASE, pre_image_id="sha256:" + "3" * 64)

    def test_wrong_source_release_is_rejected(self):
        archive = self.archive()
        with self.assertRaisesRegex(RuntimeError, "release identity differs"):
            source.inspect_archive(archive, release_id=RELEASE.replace("aabbccdd", "eeff0011"), pre_image_id=IMAGE)

    def test_links_and_devices_are_rejected(self):
        for kind in (tarfile.SYMTYPE, tarfile.LNKTYPE, tarfile.FIFOTYPE, tarfile.CHRTYPE, tarfile.BLKTYPE):
            with self.subTest(kind=kind):
                member = tarfile.TarInfo("./config/app/injected")
                member.type = kind
                member.linkname = "../../outside"
                member.mode = 0o600
                with self.assertRaisesRegex(RuntimeError, "special files"):
                    self.inspect(self.archive(extras=[(member, None)]))

    def test_traversal_and_alternative_name_spellings_are_rejected(self):
        for name in ("../outside", "/absolute", "./config/../outside", "./config//app/file", "././config/file", "config\\file", "config/C:drive", "config/x\nfile", "unknown/file"):
            with self.subTest(name=name):
                member = tarfile.TarInfo(name)
                member.mode = 0o600
                with self.assertRaises(RuntimeError):
                    self.inspect(self.archive(extras=[(member, None)]))

    def test_duplicate_members_are_rejected(self):
        member = tarfile.TarInfo("./redis/dump.rdb")
        member.mode = 0o600
        with self.assertRaisesRegex(RuntimeError, "duplicate tar"):
            self.inspect(self.archive(extras=[(member, None)]))

    def test_file_cannot_be_parent_of_another_member(self):
        def mutation(files):
            files["config/app/parent"] = b"file"
            files["config/app/parent/child"] = b"child"
        with self.assertRaisesRegex(RuntimeError, "parent directory"):
            self.inspect(self.archive(mutate_files=mutation))

    def test_duplicate_manifest_identity_is_rejected(self):
        def mutation(files):
            files["metadata/manifest.txt"] += ("release_id=" + RELEASE + "\n").encode()
        with self.assertRaisesRegex(RuntimeError, "manifest field"):
            self.inspect(self.archive(mutate_files=mutation))

    def test_hardlinked_archive_file_is_rejected(self):
        archive = self.archive()
        os.link(archive, self.root / "second-link.tar")
        with self.assertRaisesRegex(RuntimeError, "single-link"):
            self.inspect(archive)

    def test_world_writable_setuid_or_executable_permissions_are_rejected(self):
        for mode in (0o666, 0o640 | 0o4000, 0o755):
            with self.subTest(mode=mode), self.assertRaisesRegex(RuntimeError, "permissions"):
                self.inspect(self.archive(modes={"database/sub2api.dump": mode}))

    def test_non_root_archive_owner_is_preserved_as_metadata(self):
        result = self.inspect(self.archive(owners={"redis/dump.rdb": (999, 1000)}))
        self.assertIn("recovery_point_sha256", result)

    def test_unbounded_owner_is_rejected(self):
        with self.assertRaisesRegex(RuntimeError, "ownership metadata"):
            self.inspect(self.archive(owners={"redis/dump.rdb": (2 ** 32 - 1, 1000)}))

    def test_preserved_owner_is_not_applied_during_extraction(self):
        archive = self.archive(owners={"redis/dump.rdb": (999, 1000)})
        destination = self.root / "restored"
        source.extract_archive(archive, destination, release_id=RELEASE, pre_image_id=IMAGE)
        copied = destination / "redis/dump.rdb"
        self.assertEqual(copied.read_bytes(), self.files["redis/dump.rdb"])
        if os.name != "nt":
            self.assertEqual(copied.stat().st_uid, os.getuid())
            self.assertEqual(copied.stat().st_gid, os.getgid())

    def test_checksum_closure_rejects_uninventoried_file(self):
        member = tarfile.TarInfo("config/app/injected")
        member.mode = 0o600
        with self.assertRaisesRegex(RuntimeError, "checksum closure"):
            self.inspect(self.archive(extras=[(member, None)]))

    def test_checksum_content_is_reverified(self):
        archive = self.archive()
        raw = archive.read_bytes()
        archive.write_bytes(raw.replace(b"PGDMP fixture", b"PGDMP corrupt"))
        with self.assertRaisesRegex(RuntimeError, "checksum closure"):
            self.inspect(archive)

    def test_missing_core_file_or_empty_pg_is_rejected(self):
        for mutation in (lambda files: files.pop("redis/dump.rdb"), lambda files: files.__setitem__("database/sub2api.dump", b"")):
            with self.assertRaises(RuntimeError):
                self.inspect(self.archive(mutate_files=mutation))

    def test_source_cannot_decrypt_on_host(self):
        archive, identity, expected = self.decrypt_inputs()
        with mock.patch.object(source, "run_hidden") as child:
            with self.assertRaisesRegex(RuntimeError, "Guest|namespace"):
                source.decrypt_source(archive, identity, self.root / "output.tar", expected=expected, host_namespaces={})
        child.assert_not_called()

    def decrypt_inputs(self):
        artifact = self.root / "encrypted.age"
        artifact.write_bytes(b"age-encryption.org/v1 fixture")
        identity = self.root / "identity.age"
        identity.write_bytes(b"AGE-SECRET-KEY-FIXTURE")
        for path in (artifact, identity):
            os.chmod(path, 0o600)
        self.recipient = "age1" + "a" * 58
        expected = {
            "artifact_sha256": hashlib.sha256(artifact.read_bytes()).hexdigest(),
            "artifact_bytes": artifact.stat().st_size,
            "approved_identity_sha256": hashlib.sha256(identity.read_bytes()).hexdigest(),
            "approved_recipient_sha256": hashlib.sha256(self.recipient.encode()).hexdigest(),
            "recipient_type": "age1", "release_id": RELEASE, "pre_image_id": IMAGE,
        }
        return artifact, identity, expected

    def fake_age(self, command, **kwargs):
        if "-y" in command:
            return subprocess.CompletedProcess(command, 0, (self.recipient + "\n").encode(), b"")
        destination = Path(command[command.index("-o") + 1])
        destination.write_bytes(self.fixture_archive.read_bytes())
        os.chmod(destination, 0o600)
        return subprocess.CompletedProcess(command, 0, b"", b"")

    def test_actual_decryption_binds_approved_inputs_and_redacted_inventory(self):
        artifact, identity, expected = self.decrypt_inputs()
        self.fixture_archive = self.archive()
        with mock.patch.object(source, "assert_isolated_guest"), mock.patch.object(source, "run_hidden", side_effect=self.fake_age):
            result = source.decrypt_source(artifact, identity, self.root / "output.tar", expected=expected, host_namespaces={})
        self.assertEqual(result["artifact_sha256"], result["observed_artifact_sha256"])
        self.assertEqual(result["approved_recipient_sha256"], result["derived_recipient_sha256"])
        self.assertEqual(result["archive_inventory_sha256"], hashlib.sha256(canonical_json(result["archive_inventory"])).hexdigest())
        self.assertNotIn("AGE-SECRET", str(result))
        self.assertNotIn(self.recipient, str(result))

    @unittest.skipUnless(os.name == "posix", "child umask requires the Linux Guest API")
    def test_decryption_creates_private_plaintext_without_changing_caller_umask(self):
        artifact, identity, expected = self.decrypt_inputs()
        fixture = self.archive()
        output = self.root / "output.tar"
        real_run = source.run_hidden
        # Exercise the actual subprocess umask with age's output-creation
        # semantics; the fixture is synthetic and the child never chmods it.
        program = "import os,sys; fd=os.open(sys.argv[1],os.O_WRONLY|os.O_CREAT|os.O_EXCL,0o666); f=os.fdopen(fd,'wb'); f.write(open(sys.argv[2],'rb').read()); f.close()"

        def simulated_age(command, **kwargs):
            if "-y" in command:
                return subprocess.CompletedProcess(command, 0, (self.recipient + "\n").encode(), b"")
            return real_run([sys.executable, "-c", program, str(output), str(fixture)], **kwargs)

        original_mask = os.umask(0o022)
        try:
            with mock.patch.object(source, "assert_isolated_guest"), mock.patch.object(source, "run_hidden", side_effect=simulated_age):
                result = source.decrypt_source(artifact, identity, output, expected=expected, host_namespaces={})
            self.assertEqual(output.stat().st_mode & 0o777, 0o600)
            self.assertTrue(result["decryption_verified"])
            self.assertEqual(os.umask(0o022), 0o022)
        finally:
            os.umask(original_mask)

    def test_identity_or_encrypted_hash_drift_stops_before_decrypt(self):
        for key in ("artifact_sha256", "approved_identity_sha256"):
            artifact, identity, expected = self.decrypt_inputs()
            expected[key] = "f" * 64
            with mock.patch.object(source, "assert_isolated_guest"), mock.patch.object(source, "run_hidden") as child:
                with self.assertRaisesRegex(RuntimeError, "differs"):
                    source.decrypt_source(artifact, identity, self.root / "output.tar", expected=expected, host_namespaces={})
            child.assert_not_called()

    def test_derived_recipient_drift_stops_before_age_decrypt(self):
        artifact, identity, expected = self.decrypt_inputs()
        expected["approved_recipient_sha256"] = "f" * 64
        with mock.patch.object(source, "assert_isolated_guest"), mock.patch.object(source, "run_hidden", side_effect=self.fake_age) as child:
            with self.assertRaisesRegex(RuntimeError, "recipient differs"):
                source.decrypt_source(artifact, identity, self.root / "output.tar", expected=expected, host_namespaces={})
        self.assertEqual(child.call_count, 1)

    def test_failed_decryption_removes_only_owned_partial_plaintext(self):
        artifact, identity, expected = self.decrypt_inputs()
        foreign = self.root / "keep-file"
        foreign.write_bytes(b"foreign")
        output = self.root / "output.tar"
        def failure(command, **kwargs):
            if "-y" in command:
                return subprocess.CompletedProcess(command, 0, (self.recipient + "\n").encode(), b"")
            output.write_bytes(b"partial sensitive fixture")
            return subprocess.CompletedProcess(command, 1, b"", b"secret stderr fixture")
        with mock.patch.object(source, "assert_isolated_guest"), mock.patch.object(source, "run_hidden", side_effect=failure):
            with self.assertRaisesRegex(RuntimeError, "age decryption failed"):
                source.decrypt_source(artifact, identity, output, expected=expected, host_namespaces={})
        self.assertFalse(output.exists())
        self.assertEqual(foreign.read_bytes(), b"foreign")

    def test_existing_plaintext_never_overwritten(self):
        artifact, identity, expected = self.decrypt_inputs()
        output = self.root / "output.tar"
        output.write_bytes(b"existing")
        with mock.patch.object(source, "assert_isolated_guest"), mock.patch.object(source, "run_hidden") as child:
            with self.assertRaisesRegex(RuntimeError, "output already exists"):
                source.decrypt_source(artifact, identity, output, expected=expected, host_namespaces={})
        child.assert_not_called()
        self.assertEqual(output.read_bytes(), b"existing")


if __name__ == "__main__":
    unittest.main()
