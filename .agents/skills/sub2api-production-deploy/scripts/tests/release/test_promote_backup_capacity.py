from __future__ import annotations

import hashlib
import os
from pathlib import Path
import shutil
import subprocess

import pytest


SCRIPT = Path(__file__).resolve().parents[2] / "maintenance/release/promote-backup.sh"
RELEASE_ID = "284-aaaaaaaaaaaa-1000-aaaaaaaa"
TRANSPORT = "sub2api-20261112T120000Z.tar.age"
MINIMUM = 1000
ARTIFACT_BYTES = 128


@pytest.fixture
def promoter(tmp_path):
    git_bash = Path("C:/Program Files/Git/bin/bash.exe")
    bash = str(git_bash) if git_bash.is_file() else shutil.which("bash")
    if not bash:
        pytest.skip("Bash is required for the promotion capacity behavior fixture")
    root = tmp_path / "backup"
    incoming = root / "incoming"
    incoming.mkdir(parents=True)
    artifact = incoming / TRANSPORT
    artifact.write_bytes(b"x" * ARTIFACT_BYTES)
    digest = hashlib.sha256(artifact.read_bytes()).hexdigest()
    (incoming / (TRANSPORT + ".sha256")).write_bytes(f"{digest}  {TRANSPORT}\n".encode("ascii"))
    sentinel = root / "protected"
    sentinel.write_bytes(b"preserved")
    target = root / "releases/284" / RELEASE_ID

    def run(free=(1128, 1000), *, fail_df=0, fail_stat=False, fail_install=False):
        # Only measurements and locking are simulated. The production script
        # performs real checksums, copies, verification, rename and cleanup.
        # Git Bash has no flock and cannot set Unix install modes on NTFS.
        # Windows uses real mkdir/cp; this fixture makes no lock/mode claim.
        wrapper = r'''
set -Eeuo pipefail
export BACKUP_ROOT=$(cd "$2" && pwd -P)
export CAPACITY_COUNT="$BACKUP_ROOT/.capacity-count"
printf '0\n' > "$CAPACITY_COUNT"
flock() { return 0; }
df() {
  local count value
  count=$(<"$CAPACITY_COUNT"); count=$((count + 1))
  printf '%s\n' "$count" > "$CAPACITY_COUNT"
  if [[ "$count" == "$FAIL_DF" ]]; then return 37; fi
  if (( count == 1 )); then value=$FREE_FIRST; else value=$FREE_LATER; fi
  printf 'Filesystem 1-blocks Used Available Use%% Mounted\nfixture 99999 1 %s 1%% /\n' "$value"
}
stat() {
  if [[ "$FAIL_STAT" == true && "$1" == -c && "$2" == %s ]]; then return 39; fi
  command stat "$@"
}
install() {
  if [[ "$1" == -d ]]; then
    if [[ "${OS:-}" == Windows_NT ]]; then command mkdir -p -- "$4"; else command install "$@"; fi
  else
    local destination=$4
    if [[ "$FAIL_INSTALL" == true ]]; then destination="$4/missing-parent/artifact"; fi
    if [[ "${OS:-}" == Windows_NT ]]; then command cp -- "$3" "$destination"; else command install -m "$2" "$3" "$destination"; fi
  fi
}
source "$1"
'''
        result = subprocess.run(
            [bash, "--noprofile", "--norc", "-c", wrapper, "promotion-test", str(SCRIPT), str(root)],
            env={**os.environ, "RELEASE_ID": RELEASE_ID, "TRANSPORT_ARTIFACT_NAME": TRANSPORT,
                 "ARTIFACT_SHA256": digest, "MINIMUM_FREE_BYTES": str(MINIMUM),
                 "FREE_FIRST": str(free[0]), "FREE_LATER": str(free[1]),
                 "FAIL_DF": str(fail_df), "FAIL_STAT": str(fail_stat).lower(),
                 "FAIL_INSTALL": str(fail_install).lower()},
            capture_output=True, text=True, encoding="utf-8", timeout=20,
        )
        assert artifact.read_bytes() == b"x" * ARTIFACT_BYTES
        assert sentinel.read_bytes() == b"preserved"
        assert not list((root / "releases/284").glob(".*.staging.*"))
        return result

    return run, target


def test_copy_reserves_exact_size_and_reports_actual_postcopy_space(promoter):
    run, target = promoter
    result = run()
    assert result.returncode == 0, result.stderr
    assert target.is_dir()
    assert "backup_promotion=verified\n" in result.stdout
    assert "release_free_bytes=1000\n" in result.stdout
    assert "release_free_bytes=1128\n" not in result.stdout


def test_insufficient_copy_headroom_never_commits(promoter):
    run, target = promoter
    result = run((1127, 1000))
    assert result.returncode != 0
    assert not target.exists()
    assert "backup_promotion=verified" not in result.stdout


def test_postcopy_drift_cleans_staging_without_committing(promoter):
    run, target = promoter
    result = run((1128, 999))
    assert result.returncode != 0
    assert not target.exists()
    assert "backup_promotion=verified" not in result.stdout


@pytest.mark.parametrize("fail_df", [1, 2])
def test_df_failure_propagates_without_success(promoter, fail_df):
    run, target = promoter
    result = run(fail_df=fail_df)
    assert result.returncode == 37, result.stderr
    assert not target.exists()
    assert "backup_promotion=verified" not in result.stdout


def test_artifact_stat_failure_stops_before_copy(promoter):
    run, target = promoter
    result = run(fail_stat=True)
    assert result.returncode == 39, result.stderr
    assert not target.exists()
    assert "backup_promotion=verified" not in result.stdout


def test_copy_write_failure_cleans_staging_without_success(promoter):
    run, target = promoter
    result = run(fail_install=True)
    assert result.returncode != 0
    assert "missing-parent" in result.stderr
    assert not target.exists()
    assert "backup_promotion=verified" not in result.stdout


@pytest.mark.parametrize("invalid", ["08", "-1", "nan", "1000000000000000000"])
def test_malformed_measurement_is_rejected(promoter, invalid):
    run, target = promoter
    result = run((invalid, 1000))
    assert result.returncode != 0
    assert not target.exists()
    assert "backup_promotion=verified" not in result.stdout


def test_replay_needs_only_minimum_space_and_retains_bundle(promoter):
    run, target = promoter
    assert run().returncode == 0
    before = {p.name: p.read_bytes() for p in target.iterdir()}
    result = run((1000, 1000))
    assert result.returncode == 0, result.stderr
    assert "release_free_bytes=1000\n" in result.stdout
    assert {p.name: p.read_bytes() for p in target.iterdir()} == before


def test_replay_rechecks_drift_and_preserves_existing_bundle(promoter):
    run, target = promoter
    assert run().returncode == 0
    before = {p.name: p.read_bytes() for p in target.iterdir()}
    result = run((1000, 999))
    assert result.returncode != 0
    assert "backup_promotion=verified" not in result.stdout
    assert {p.name: p.read_bytes() for p in target.iterdir()} == before


def test_replay_still_rejects_tampered_bundle(promoter):
    run, target = promoter
    assert run().returncode == 0
    (target / "artifact.tar.age").write_bytes(b"tampered")
    result = run((1000, 1000))
    assert result.returncode != 0
    assert "backup_promotion=verified" not in result.stdout
    assert (target / "artifact.tar.age").read_bytes() == b"tampered"
