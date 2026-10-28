"""Exercise the production prepare entry with real signed Gates in a sandbox.

Only fixed host paths are redirected; Docker is an inert call recorder. Bash,
jq, OpenSSL, archive checks and the prepare failure trap execute for real.
"""
from __future__ import annotations

import copy
import gzip
import hashlib
import json
import os
import shutil
import subprocess
import tempfile
import time
import unittest
from pathlib import Path


SCRIPTS = Path(__file__).resolve().parents[2]


class PrepareGatePolicyTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls.bash = shutil.which("bash")
        if os.name == "nt":
            candidate = Path("C:/Program Files/Git/bin/bash.exe")
            if candidate.is_file():
                cls.bash = str(candidate)
        if not cls.bash:
            raise unittest.SkipTest("Bash is required")
        check = subprocess.run(
            [cls.bash, "-c", "command -v jq && command -v openssl && command -v flock"],
            capture_output=True, timeout=15,
        )
        if check.returncode:
            raise unittest.SkipTest("real jq, OpenSSL and flock are required; run on Linux")

    def setUp(self) -> None:
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.private = self.root / "signing.pem"
        self.public = self.root / "trusted.pem"
        self._openssl("genpkey", "-algorithm", "ED25519", "-out", str(self.private))
        self._openssl("pkey", "-in", str(self.private), "-pubout", "-out", str(self.public))
        self.archive = gzip.compress(b"inert candidate fixture", mtime=0)
        self.image = "sha256:" + "a" * 64
        self.source = (SCRIPTS / "maintenance/release/prepare.sh").read_text(encoding="utf-8")

    def _openssl(self, *args: str) -> None:
        subprocess.run(["openssl", *args], check=True, capture_output=True, timeout=15)

    def _document(self, mode="fast", *, schema=2, profile="276", suffix="aaaaaaaa") -> dict:
        restored = mode != "fast"
        document = {
            "manifest": {
                "schema": schema, "release_id": f"{profile}-{'b' * 12}-1-{suffix}",
                "profile": profile, "origin": "https://github.com/baiyucraft/sub2api.git",
                "vm_identity": "sub2api-dev", "expires_at": int(time.time()) + 600,
                "release_asset_layout": "skill-v1", "release_asset_sha256": {},
                "commit_sha": "b" * 40, "version": "0.2.18-baiyu",
                "recovery_gate": {"mode": mode},
            },
            "evidence": {
                "integration_verified": True, "vm_restore_verified": restored,
                "release_policy": {"restore_points_verified": restored},
                "candidate_image_id": self.image,
                "candidate_archive_sha256": hashlib.sha256(self.archive).hexdigest(),
            },
        }
        if schema == 2:
            document["gate_version"] = 2
            document["profile_id"] = int(profile)
        else:
            document["manifest"].pop("recovery_gate")
            document["evidence"]["vm_restore_verified"] = True
        return document

    def _prepare(self, document: dict, *, accepted: bool, bad_signature=False, free_bytes=1000000000000,
                 preserve_candidate_on_failure=False) -> None:
        # Every run gets its own releases/lock tree; no host path is touched.
        sandbox = self.root / str(len(list(self.root.iterdir())))
        sandbox.mkdir()
        releases = sandbox / "releases"
        release_id = document["manifest"]["release_id"]
        release = releases / release_id
        (release / "assets").mkdir(parents=True)
        asset = release / "assets/context.sh"
        asset.write_text("# inert fixture asset\n", encoding="utf-8")
        document = copy.deepcopy(document)
        source = ".agents/skills/sub2api-production-deploy/scripts/maintenance/release/context.sh"
        document["manifest"]["release_asset_sha256"][source] = hashlib.sha256(asset.read_bytes()).hexdigest()
        gate = release / "gate.json"
        gate.write_text(json.dumps(document, sort_keys=True), encoding="utf-8")
        signature = release / "gate.sig"
        self._openssl("pkeyutl", "-sign", "-inkey", str(self.private), "-rawin", "-in", str(gate), "-out", str(signature))
        if bad_signature:
            signature.write_bytes(b"bad signature")
        (release / "candidate.tar.gz").write_bytes(self.archive)
        names = ("gate.json", "gate.sig", "candidate.tar.gz", "assets/context.sh")
        (release / "ASSET_SHA256SUMS").write_text("".join(
            f"{hashlib.sha256((release / name).read_bytes()).hexdigest()}  {name}\n" for name in names
        ), encoding="utf-8")
        source = self.source.replace("/opt/sub2api/releases", releases.as_posix()).replace(
            "/run/lock/sub2api-production-release.lock", (sandbox / "release.lock").as_posix()
        )
        script = sandbox / "prepare.sh"
        script.write_text(source, encoding="utf-8", newline="\n")
        docker_calls = sandbox / "docker-calls"
        command = '''docker() {
 printf '%s\n' "$1" >> "$FIXTURE_DOCKER_CALLS"
 if [[ $1 == info ]]; then printf '%s\n' "$FIXTURE_DOCKER_ROOT";
 elif [[ $1 == load ]]; then cat >/dev/null;
 elif [[ $1 == image ]]; then printf '%s\n' "$FIXTURE_IMAGE"; fi
}
df() {
 printf 'Filesystem 1B-blocks Used Available Use%% Mounted on\nfixture 1000000000000 0 %s 0%% /\n' "$FIXTURE_FREE_BYTES"
}
export -f df
export -f docker
exec bash "$1"
'''
        environment = os.environ.copy()
        environment.update(RELEASE_ID=release_id, RELEASE_DIR=release.as_posix(), TRUST_KEY=self.public.as_posix(),
                           FIXTURE_DOCKER_CALLS=docker_calls.as_posix(), FIXTURE_IMAGE=self.image,
                           FIXTURE_DOCKER_ROOT="/fixture/docker", FIXTURE_FREE_BYTES=str(free_bytes),
                           MINIMUM_FREE_BYTES="1024")
        result = subprocess.run([self.bash, "-c", command, "prepare-test", str(script)], env=environment,
                                capture_output=True, text=True, timeout=30)
        if accepted:
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertTrue((release / ".prepared").is_file())
            self.assertIn("prepared=true", result.stdout)
            self.assertIn("load", docker_calls.read_text())
            self.assertTrue((releases / ".active-release/candidate.tar.gz").is_file())
        else:
            self.assertNotEqual(result.returncode, 0, result.stdout)
            self.assertFalse((release / ".prepared").exists())
            self.assertNotIn("prepared=true", result.stdout)
            self.assertNotIn("load\n", docker_calls.read_text() if docker_calls.exists() else "",
                             "rejected Gate reached docker load")
            self.assertFalse((releases / ".active-release").exists(), "failed claim was retained")
            if preserve_candidate_on_failure:
                self.assertEqual((release / "candidate.tar.gz").read_bytes(), self.archive)

    def test_signed_fast_gate_prepares_for_two_profiles_and_release_ids(self):
        for profile, suffix in (("276", "aaaaaaaa"), ("273", "bbbbbbbb")):
            with self.subTest(profile=profile):
                self._prepare(self._document(profile=profile, suffix=suffix), accepted=True)

    def test_signed_restore_modes_and_legacy_gates_still_prepare(self):
        for mode in ("specialized", "full"):
            with self.subTest(mode=mode):
                self._prepare(self._document(mode), accepted=True)
        legacy_v2 = self._document("specialized")
        legacy_v2["manifest"].pop("recovery_gate")
        self._prepare(legacy_v2, accepted=True)
        self._prepare(self._document(schema=1), accepted=True)

    def test_restore_flags_are_required_booleans_matching_the_signed_mode(self):
        for mode in ("fast", "specialized", "full"):
            for field in ("vm_restore_verified", "restore_points_verified"):
                expected = mode != "fast"
                for value in (not expected, str(expected).lower(), None):
                    with self.subTest(mode=mode, field=field, value=value):
                        doc = self._document(mode)
                        target = doc["evidence"] if field == "vm_restore_verified" else doc["evidence"]["release_policy"]
                        if value is None:
                            target.pop(field)
                        else:
                            target[field] = value
                        self._prepare(doc, accepted=False)

    def test_unknown_mode_schema_and_integration_failure_stop_before_docker(self):
        docs = [self._document("unknown"), self._document(schema=3)]
        for value in (False, "true", None):
            doc = self._document()
            doc["evidence"]["integration_verified"] = value
            docs.append(doc)
        legacy = self._document(schema=1)
        legacy["evidence"]["vm_restore_verified"] = False
        docs.append(legacy)
        wrong_version = self._document()
        wrong_version["gate_version"] = 3
        docs.append(wrong_version)
        wrong_profile = self._document()
        wrong_profile["profile_id"] = 274
        docs.append(wrong_profile)
        null_mode = self._document()
        null_mode["manifest"]["recovery_gate"]["mode"] = None
        docs.append(null_mode)
        for index, doc in enumerate(docs):
            with self.subTest(index=index):
                self._prepare(doc, accepted=False)

    def test_invalid_signature_stops_before_docker(self):
        self._prepare(self._document(), accepted=False, bad_signature=True)

    def test_insufficient_import_space_preserves_candidate_archive_for_retry(self):
        self._prepare(self._document(), accepted=False, free_bytes=1024, preserve_candidate_on_failure=True)


if __name__ == "__main__":
    unittest.main(verbosity=2)
