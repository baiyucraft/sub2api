from __future__ import annotations

import hashlib
import os
import shutil
import subprocess
import tempfile
from pathlib import Path


ROOT = Path(__file__).resolve().parents[3]
SCRIPT = ROOT / "deploy" / "maintenance" / "release" / "promote-backup.sh"
RELEASE_ID = "195-aaaaaaaaaaaa-1-aaaaaaaa"
TRANSPORT = "sub2api-20260719T120000Z.tar.age"


def sha256(path: Path) -> str:
    return hashlib.sha256(path.read_bytes()).hexdigest()


def run_promoter(root: Path, digest: str, *, expect_ok: bool) -> subprocess.CompletedProcess[str]:
    env = os.environ.copy()
    env.update(
        {
            "BACKUP_ROOT": str(root),
            "RELEASE_ID": RELEASE_ID,
            "TRANSPORT_ARTIFACT_NAME": TRANSPORT,
            "ARTIFACT_SHA256": digest,
            "MINIMUM_FREE_BYTES": "1",
        }
    )
    completed = subprocess.run(
        ["bash", str(SCRIPT)],
        env=env,
        text=True,
        capture_output=True,
        check=False,
    )
    if expect_ok and completed.returncode != 0:
        raise RuntimeError(f"promotion failed with exit {completed.returncode}")
    if not expect_ok and completed.returncode == 0:
        raise RuntimeError("invalid bundle was accepted")
    return completed


def main() -> None:
    if shutil.which("bash") is None:
        raise RuntimeError("bash is required")
    with tempfile.TemporaryDirectory(prefix="sub2api-promote-backup-") as temp:
        root = Path(temp).resolve()
        source_dir = root / "incoming"
        source_dir.mkdir()
        artifact = source_dir / TRANSPORT
        artifact.write_bytes(b"encrypted-test-artifact")
        digest = sha256(artifact)
        (source_dir / f"{TRANSPORT}.sha256").write_text(
            f"{digest}  {TRANSPORT}\n", encoding="ascii"
        )

        first = run_promoter(root, digest, expect_ok=True)
        if "backup_promotion=verified" not in first.stdout:
            raise RuntimeError("success output is incomplete")
        run_promoter(root, digest, expect_ok=True)

        target = root / "releases" / "195" / RELEASE_ID
        extra = target / "extra"
        extra.write_text("unexpected", encoding="ascii")
        run_promoter(root, digest, expect_ok=False)
        extra.unlink()

        bundle = target / "bundle.sha256"
        valid_bundle = bundle.read_text(encoding="ascii")
        bundle.write_text(valid_bundle + valid_bundle.splitlines()[0] + "\n", encoding="ascii")
        run_promoter(root, digest, expect_ok=False)
        bundle.write_text(valid_bundle, encoding="ascii")

        manifest = target / "manifest"
        saved_manifest = target / "manifest.saved"
        manifest.rename(saved_manifest)
        manifest.symlink_to(saved_manifest.name)
        run_promoter(root, digest, expect_ok=False)
        manifest.unlink()
        saved_manifest.rename(manifest)

        lock = root / ".release-promotion.lock"
        lock.unlink()
        sentinel = root / "lock-sentinel"
        sentinel.write_text("unchanged", encoding="ascii")
        lock.symlink_to(sentinel.name)
        run_promoter(root, digest, expect_ok=False)
        if sentinel.read_text(encoding="ascii") != "unchanged":
            raise RuntimeError("lock symlink target changed")

    print("promote_backup_integration=pass checks=6")


if __name__ == "__main__":
    main()
