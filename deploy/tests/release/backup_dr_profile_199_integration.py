from __future__ import annotations

import hashlib
import secrets
import shlex
import subprocess
import sys
import tempfile
import time
from datetime import datetime, timezone
from pathlib import Path


DEPLOY_ROOT = Path(__file__).resolve().parents[2]
ROOT = DEPLOY_ROOT.parent
sys.path.insert(0, str(DEPLOY_ROOT))

from release.ssh import SSHRunner


MIGRATION_195 = "195_upstream_scheduling_monitor_rates.sql"
MIGRATION_199 = "199_group_reasoning_effort_policy.sql"
MIGRATION_195_SHA = "1" * 64
MIGRATION_199_SHA = "2" * 64


def sha256_file(path: Path) -> str:
    return hashlib.sha256(path.read_bytes()).hexdigest()


def verifier_binary() -> Path:
    binary = ROOT / ".tmp" / "sub2api-verify-dr-evidence"
    expected = (DEPLOY_ROOT / "release" / "drverify" / "linux-amd64.sha256").read_text(encoding="ascii").split()[0]
    if not binary.is_file() or sha256_file(binary) != expected:
        subprocess.run(
            [sys.executable, str(DEPLOY_ROOT / "release" / "drverify" / "build.py"), "--output", str(binary)],
            cwd=ROOT,
            check=True,
        )
    if sha256_file(binary) != expected:
        raise RuntimeError("DR verifier fixture does not match the repository checksum")
    return binary


def cleanup_remote_temp(runner: SSHRunner, host: str, path: str) -> None:
    quoted = shlex.quote(path)
    runner.run(
        host,
        f"""
if [[ -e {quoted} || -L {quoted} ]]; then
  [[ -d {quoted} && ! -L {quoted} && $(realpath -e -- {quoted}) == {quoted} ]]
  rm -rf -- {quoted}
fi
printf 'cleanup=pass\\n'
""",
        {"cleanup"},
    )


def run(runner: SSHRunner, remote_temps: list[tuple[str, str]]) -> None:
    release_id = f"199-000000000000-{int(time.time())}-{secrets.token_hex(4)}"
    now = datetime.now(timezone.utc)
    drill_id = "dr-199-" + now.strftime("%Y%m%dT%H%M%SZ")
    observed_at = now.strftime("%Y-%m-%dT%H:%M:%SZ")

    vm_temp = runner.create_temp_dir("local_vm", "/opt/sub2api-deploy/release-input", "promotion-evidence")
    remote_temps.append(("local_vm", vm_temp))
    runner.run(
        "local_vm",
        f"install -d -o root -g root -m 700 {shlex.quote(vm_temp + '/libexec')} && "
        f"install -o root -g root -m 600 /dev/null {shlex.quote(vm_temp + '/libexec/.sub2api-release-unit.lock')} && "
        "printf 'helper_root_ready=pass\\n'",
        {"helper_root_ready"},
    )
    gate_signer = DEPLOY_ROOT / "release" / "sign-gate.sh"
    dr_signer = DEPLOY_ROOT / "release" / "sign-dr-evidence.sh"
    runner.upload_file("local_vm", gate_signer, f"{vm_temp}/libexec/sub2api-sign-gate", 0o700)
    runner.upload_file("local_vm", dr_signer, f"{vm_temp}/libexec/sub2api-sign-dr-evidence", 0o700)
    candidate = runner.run(
        "local_vm",
        rf'''
set -Eeuo pipefail
vm_temp={shlex.quote(vm_temp)}
release_id={release_id}
drill_id={drill_id}
observed_at={observed_at}
gate_root=/opt/sub2api-deploy/release-gates
gate_dir="$gate_root/$release_id/output"
dr_root=/opt/sub2api-deploy/dr-evidence
release_dir="$dr_root/$release_id"
evidence_dir="$release_dir/$drill_id"
cleanup() {{ rm -rf -- "$gate_root/$release_id" "$release_dir"; }}
trap cleanup EXIT
install -d -o root -g root -m 700 "$gate_dir"
if [[ -e $dr_root ]]; then
  [[ -d $dr_root && ! -L $dr_root && $(stat -c '%U:%G:%a' "$dr_root") == root:root:700 ]]
else
  install -d -o root -g root -m 700 "$dr_root"
fi
install -d -o root -g root -m 700 "$release_dir" "$evidence_dir"
printf 'profile-199-candidate-fixture\n' > "$gate_dir/candidate.tar.gz"
chmod 400 "$gate_dir/candidate.tar.gz"
archive_sha=$(sha256sum "$gate_dir/candidate.tar.gz" | awk '{{print $1}}')
image_id=sha256:$(printf 'd%.0s' {{1..64}})
jq -n --arg release_id "$release_id" --arg archive_sha "$archive_sha" --arg image_id "$image_id" \
  --arg migration_195 {MIGRATION_195} --arg migration_199 {MIGRATION_199} \
  --arg sha_195 {MIGRATION_195_SHA} --arg sha_199 {MIGRATION_199_SHA} \
  '{{manifest:{{release_id:$release_id,profile:"199",migration_sha256:{{($migration_195):$sha_195,($migration_199):$sha_199}}}},evidence:{{candidate_archive_sha256:$archive_sha,candidate_image_id:$image_id}}}}' > "$gate_dir/gate.json"
chmod 400 "$gate_dir/gate.json"
export SUB2API_HELPER_TEST_MODE=true
export SUB2API_UNIT_LOCK_PATH="$vm_temp/libexec/.sub2api-release-unit.lock"
"$vm_temp/libexec/sub2api-sign-gate" "$gate_dir/gate.json" "$gate_dir/gate.sig"
(cd "$gate_dir" && sha256sum "$gate_dir/gate.json" "$gate_dir/gate.sig" "$gate_dir/candidate.tar.gz" > SHA256SUMS)
chmod 400 "$gate_dir/SHA256SUMS"
migration_sha=$(jq -cS '.manifest.migration_sha256' "$gate_dir/gate.json" | sha256sum | awk '{{print $1}}')
write_evidence() {{
  local evidence_migration=$1 output_name=$2
  jq -n --arg release_id "$release_id" --arg drill_id "$drill_id" --arg now "$observed_at" \
    --arg archive_sha "$archive_sha" --arg image_id "$image_id" --arg migration_sha "$evidence_migration" \
    '{{schema:1,release_id:$release_id,drill_id:$drill_id,created_at:$now,completed_at:$now,artifact_sha256:("a"*64),candidate_bundle_sha256:("b"*64),candidate_archive_sha256:$archive_sha,candidate_image_id:$image_id,migration_checksum:$migration_sha,image_load_id_check:"pass",config_manifest_check:"pass",postgres_restore:"pass",redis_restore:"pass",redis_ttl_reconciliation:"pass",counts_and_migrations:"pass",temporary_material_destroyed:"pass",redis_backup_dbsize:10,redis_backup_expiring_keys:2,redis_restored_dbsize:9,redis_restored_expiring_keys:1}}' > "$evidence_dir/evidence.json"
  chmod 400 "$evidence_dir/evidence.json"
  rm -f -- "$evidence_dir/evidence.sig"
  "$vm_temp/libexec/sub2api-sign-dr-evidence" "$evidence_dir/evidence.json" "$evidence_dir/evidence.sig"
  install -o root -g root -m 400 "$evidence_dir/evidence.json" "$vm_temp/$output_name.json"
  install -o root -g root -m 400 "$evidence_dir/evidence.sig" "$vm_temp/$output_name.sig"
}}
write_evidence "$migration_sha" valid
write_evidence $(printf 'f%.0s' {{1..64}}) mismatch
for file in gate.json gate.sig candidate.tar.gz SHA256SUMS; do
  install -o root -g root -m 400 "$gate_dir/$file" "$vm_temp/$file"
done
printf 'archive_sha256=%s\n' "$archive_sha"
printf 'candidate_image_id=%s\n' "$image_id"
printf 'migration_sha256=%s\n' "$migration_sha"
''',
        {"archive_sha256", "candidate_image_id", "migration_sha256"},
        timeout=300,
    ).values
    print("profile_199_stage=signed_fixtures_ready", flush=True)

    backup_temp = runner.create_temp_dir("backup", "/srv/sub2api-backups", "dr-promotion-test")
    remote_temps.append(("backup", backup_temp))
    with tempfile.TemporaryDirectory(dir=ROOT / ".tmp") as local_temp:
        local_root = Path(local_temp)
        generated_names = (
            "gate.json",
            "gate.sig",
            "candidate.tar.gz",
            "SHA256SUMS",
            "valid.json",
            "valid.sig",
            "mismatch.json",
            "mismatch.sig",
        )
        for name in generated_names:
            local_path = local_root / name
            runner.download_file("local_vm", f"{vm_temp}/{name}", local_path)
            runner.upload_file("backup", local_path, f"{backup_temp}/{name}", 0o400)

    files = {
        "verifier": verifier_binary(),
        "promoter": DEPLOY_ROOT / "release" / "promote-dr-baseline.sh",
        "bootstrap": DEPLOY_ROOT / "release" / "bootstrap_backup_dr_assets.sh",
        "trust.pub": DEPLOY_ROOT / "release" / "trust" / "vm-gate-ed25519.pub",
    }
    for name, path in files.items():
        runner.upload_file("backup", path, f"{backup_temp}/{name}", 0o700 if name != "trust.pub" else 0o400)
    print("profile_199_stage=assets_uploaded", flush=True)

    verifier_sha = sha256_file(files["verifier"])
    promoter_sha = sha256_file(files["promoter"])
    trust_sha = sha256_file(files["trust.pub"])
    compact_time = now.strftime("%Y%m%dT%H%M%SZ")
    result = runner.run(
        "backup",
        rf'''
set -Eeuo pipefail
remote={shlex.quote(backup_temp)}
release_id={release_id}
drill_id={drill_id}
SUB2API_BACKUP_BOOTSTRAP_TEST_MODE=true BACKUP_TEST_ROOT="$remote" \
  VERIFIER_SOURCE="$remote/verifier" PROMOTER_SOURCE="$remote/promoter" TRUST_SOURCE="$remote/trust.pub" \
  SIGNED_TEST_SOURCE="$remote/gate.json" SIGNED_TEST_SIGNATURE="$remote/gate.sig" \
  VERIFIER_SHA256={verifier_sha} PROMOTER_SHA256={promoter_sha} TRUST_SHA256={trust_sha} \
  "$remote/bootstrap" >/dev/null
root="$remote/releases/199"
candidate_dir="$root/candidates/$release_id"
install -d -o root -g root -m 700 "$root/candidates" "$candidate_dir"
for file in candidate.tar.gz gate.json gate.sig SHA256SUMS; do
  install -o root -g root -m 600 "$remote/$file" "$candidate_dir/$file"
done
printf 'encrypted-profile-199-fixture\n' > "$candidate_dir/artifact.tar.age"
chmod 600 "$candidate_dir/artifact.tar.age"
chown root:root "$candidate_dir/artifact.tar.age"
artifact_sha=$(sha256sum "$candidate_dir/artifact.tar.age" | awk '{{print $1}}')
archive_sha=$(sha256sum "$candidate_dir/candidate.tar.gz" | awk '{{print $1}}')
image_id=$(jq -er '.evidence.candidate_image_id' "$candidate_dir/gate.json")
cat > "$candidate_dir/manifest" <<EOF
release_id=$release_id
state=restore_pending
artifact_name=sub2api-{compact_time}.tar.age
artifact_sha256=$artifact_sha
candidate_image_id=$image_id
candidate_archive_sha256=$archive_sha
EOF
chmod 644 "$candidate_dir/manifest"
chown root:root "$candidate_dir/manifest"
(cd "$candidate_dir" && sha256sum artifact.tar.age candidate.tar.gz gate.json gate.sig manifest SHA256SUMS > bundle.sha256)
chmod 644 "$candidate_dir/bundle.sha256"
chown root:root "$candidate_dir/bundle.sha256"
ln -s "candidates/$release_id" "$root/candidate"
bundle_sha=$(sha256sum "$candidate_dir/bundle.sha256" | awk '{{print $1}}')
migration_sha=$(jq -cS '.manifest.migration_sha256' "$candidate_dir/gate.json" | sha256sum | awk '{{print $1}}')
[[ $archive_sha == {candidate['archive_sha256']} && $image_id == {candidate['candidate_image_id']} && $migration_sha == {candidate['migration_sha256']} ]]
''',
        set(),
        timeout=300,
    ).values
    if result:
        raise RuntimeError("profile 199 candidate assembly returned unexpected fields")

    # Artifact and bundle hashes are only known after the backup-side candidate is assembled.
    # Re-sign the final evidence using those two white-listed hashes.
    binding = runner.run(
        "backup",
        rf'''
set -Eeuo pipefail
candidate_dir={shlex.quote(backup_temp)}/releases/199/candidates/{release_id}
printf 'artifact_sha256=%s\n' "$(sha256sum "$candidate_dir/artifact.tar.age" | awk '{{print $1}}')"
printf 'bundle_sha256=%s\n' "$(sha256sum "$candidate_dir/bundle.sha256" | awk '{{print $1}}')"
''',
        {"artifact_sha256", "bundle_sha256"},
    ).values
    runner.run(
        "local_vm",
        rf'''
set -Eeuo pipefail
vm_temp={shlex.quote(vm_temp)}
release_id={release_id}
drill_id={drill_id}
observed_at={observed_at}
dr_root=/opt/sub2api-deploy/dr-evidence
release_dir="$dr_root/$release_id"
evidence_dir="$release_dir/$drill_id"
install -d -o root -g root -m 700 "$release_dir" "$evidence_dir"
export SUB2API_HELPER_TEST_MODE=true
export SUB2API_UNIT_LOCK_PATH="$vm_temp/libexec/.sub2api-release-unit.lock"
write_bound() {{
  local migration_sha=$1 output_name=$2
  jq -n --arg release_id "$release_id" --arg drill_id "$drill_id" --arg now "$observed_at" \
    --arg artifact_sha {binding['artifact_sha256']} --arg bundle_sha {binding['bundle_sha256']} \
    --arg archive_sha {candidate['archive_sha256']} --arg image_id {candidate['candidate_image_id']} --arg migration_sha "$migration_sha" \
    '{{schema:1,release_id:$release_id,drill_id:$drill_id,created_at:$now,completed_at:$now,artifact_sha256:$artifact_sha,candidate_bundle_sha256:$bundle_sha,candidate_archive_sha256:$archive_sha,candidate_image_id:$image_id,migration_checksum:$migration_sha,image_load_id_check:"pass",config_manifest_check:"pass",postgres_restore:"pass",redis_restore:"pass",redis_ttl_reconciliation:"pass",counts_and_migrations:"pass",temporary_material_destroyed:"pass",redis_backup_dbsize:10,redis_backup_expiring_keys:2,redis_restored_dbsize:9,redis_restored_expiring_keys:1}}' > "$evidence_dir/evidence.json"
  chmod 400 "$evidence_dir/evidence.json"
  rm -f -- "$evidence_dir/evidence.sig"
  "$vm_temp/libexec/sub2api-sign-dr-evidence" "$evidence_dir/evidence.json" "$evidence_dir/evidence.sig"
  install -o root -g root -m 400 "$evidence_dir/evidence.json" "$vm_temp/$output_name.json"
  install -o root -g root -m 400 "$evidence_dir/evidence.sig" "$vm_temp/$output_name.sig"
}}
write_bound {candidate['migration_sha256']} valid-bound
write_bound $(printf 'f%.0s' {{1..64}}) mismatch-bound
rm -rf -- "$release_dir"
printf 'bound_evidence_ready=pass\n'
''',
        {"bound_evidence_ready"},
    )
    with tempfile.TemporaryDirectory(dir=ROOT / ".tmp") as local_temp:
        local_root = Path(local_temp)
        for name in ("valid-bound.json", "valid-bound.sig", "mismatch-bound.json", "mismatch-bound.sig"):
            local_path = local_root / name
            runner.download_file("local_vm", f"{vm_temp}/{name}", local_path)
            runner.upload_file("backup", local_path, f"{backup_temp}/{name}", 0o400)

    result = runner.run(
        "backup",
        rf'''
set -Eeuo pipefail
remote={shlex.quote(backup_temp)}
release_id={release_id}
drill_id={drill_id}
root="$remote/releases/199"
input_root="$root/promotion-input"
valid_input="$input_root/$release_id--$drill_id.VALID199"
mismatch_input="$input_root/$release_id--$drill_id.BAD01999"
install -d -o root -g root -m 700 "$valid_input" "$mismatch_input"
install -o root -g root -m 400 "$remote/valid-bound.json" "$valid_input/evidence.json"
install -o root -g root -m 400 "$remote/valid-bound.sig" "$valid_input/evidence.sig"
install -o root -g root -m 400 "$remote/mismatch-bound.json" "$mismatch_input/evidence.json"
install -o root -g root -m 400 "$remote/mismatch-bound.sig" "$mismatch_input/evidence.sig"
run_promoter() {{
  SUB2API_PROMOTION_TEST_MODE=true PROMOTION_TEST_ROOT="$remote" \
    "$remote/libexec/sub2api-promote-dr-baseline" "$release_id" "$drill_id" "$1"
}}
if run_promoter "$mismatch_input" >/dev/null 2>&1; then exit 80; fi
[[ ! -e $root/verified && ! -L $root/verified ]]
run_promoter "$valid_input" >/dev/null
target_name="$release_id--$drill_id"
target="$root/verified-bundles/$target_name"
[[ -L $root/verified && $(readlink "$root/verified") == "verified-bundles/$target_name" ]]
[[ $(realpath -e -- "$root/verified") == "$target" ]]
[[ -d $target && ! -L $target && $(stat -c '%U:%G:%a' "$target") == root:root:700 ]]
[[ $(jq -er '.migration_checksum' "$target/evidence.json") == {candidate['migration_sha256']} ]]
[[ $(jq -er '.manifest.profile' "$target/gate.json") == 199 ]]
printf 'profile_199_migration_mismatch_rejected=pass\n'
printf 'profile_199_promotion=pass\n'
printf 'profile_199_path_isolation=pass\n'
''',
        {"profile_199_migration_mismatch_rejected", "profile_199_promotion", "profile_199_path_isolation"},
        timeout=300,
    ).values
    if set(result.values()) != {"pass"}:
        raise RuntimeError("profile 199 DR promotion integration did not pass")
    print("backup_dr_profile_199_integration=pass checks=3")


def main() -> None:
    runner = SSHRunner()
    remote_temps: list[tuple[str, str]] = []
    primary_error: BaseException | None = None
    try:
        run(runner, remote_temps)
    except BaseException as exc:
        primary_error = exc

    cleanup_errors: list[BaseException] = []
    for host, path in reversed(remote_temps):
        try:
            cleanup_remote_temp(runner, host, path)
        except BaseException as exc:
            cleanup_errors.append(exc)
    if primary_error is not None:
        if cleanup_errors:
            primary_error.add_note("one or more registered remote temporary directories could not be cleaned")
        raise primary_error.with_traceback(primary_error.__traceback__)
    if cleanup_errors:
        raise cleanup_errors[0]


if __name__ == "__main__":
    main()
