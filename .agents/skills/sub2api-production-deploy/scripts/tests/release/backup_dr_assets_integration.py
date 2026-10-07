from __future__ import annotations

import hashlib
import secrets
import shlex
import sys
import tempfile
import time
from pathlib import Path


DEPLOY_ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(DEPLOY_ROOT))

from release.paths import WORKSPACE
from release.process import run_hidden
from release.ssh import SSHRunner


ROOT = WORKSPACE


def sha256_file(path: Path) -> str:
    return hashlib.sha256(path.read_bytes()).hexdigest()


def verifier_binary() -> Path:
    binary = ROOT / ".tmp" / "sub2api-verify-dr-evidence"
    expected = (DEPLOY_ROOT / "release" / "drverify" / "linux-amd64.sha256").read_text(encoding="ascii").split()[0]
    if not binary.is_file() or sha256_file(binary) != expected:
        run_hidden(
            [sys.executable, str(DEPLOY_ROOT / "release" / "drverify" / "build.py"), "--output", str(binary)],
            cwd=ROOT,
            check=True,
        )
    if sha256_file(binary) != expected:
        raise RuntimeError("DR verifier fixture does not match the repository checksum")
    return binary


def cleanup_remote_temp(runner: SSHRunner, host: str, path: str) -> None:
    quoted = shlex.quote(path)
    script = f"""
if [[ -e {quoted} || -L {quoted} ]]; then
  [[ -d {quoted} && ! -L {quoted} && $(realpath -e -- {quoted}) == {quoted} ]]
  rm -rf -- {quoted}
fi
printf 'cleanup=pass\\n'
"""
    runner.run(host, script, {"cleanup"})


def run(
    runner: SSHRunner,
    remote_temps: list[tuple[str, str]],
    local_temps: list[tempfile.TemporaryDirectory[str]],
) -> None:
    release_id = f"195-000000000000-{int(time.time())}-{secrets.token_hex(4)}"
    vm_clock = runner.run(
        "local_vm",
        """
now_epoch=$(date -u +%s)
printf 'drill_id=dr-195-%s\\n' "$(date -u -d "@$now_epoch" +%Y%m%dT%H%M%SZ)"
printf 'previous_drill_id=dr-195-%s\\n' "$(date -u -d "@$((now_epoch - 1))" +%Y%m%dT%H%M%SZ)"
printf 'observed_at=%s\\n' "$(date -u -d "@$now_epoch" +%Y-%m-%dT%H:%M:%SZ)"
""",
        {"drill_id", "previous_drill_id", "observed_at"},
    ).values
    drill_id = vm_clock["drill_id"]
    previous_drill_id = vm_clock["previous_drill_id"]
    observed_at = vm_clock["observed_at"]
    vm_temp = runner.create_temp_dir("local_vm", "/opt/sub2api-deploy/release-input", "promotion-evidence")
    remote_temps.append(("local_vm", vm_temp))
    gate_release_dir = f"/opt/sub2api-deploy/release-gates/{release_id}"
    evidence_release_dir = f"/opt/sub2api-deploy/dr-evidence/{release_id}"
    remote_temps.extend((("local_vm", gate_release_dir), ("local_vm", evidence_release_dir)))
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
gate_release_dir={shlex.quote(gate_release_dir)}
gate_dir="$gate_release_dir/output"
[[ ! -e $gate_release_dir && ! -L $gate_release_dir ]]
cleanup() {{ rm -rf -- "$gate_release_dir"; }}
trap cleanup EXIT
install -d -o root -g root -m 700 "$gate_dir"
gate_signer="$vm_temp/libexec/sub2api-sign-gate"
[[ -f $gate_signer && ! -L $gate_signer && $(stat -c '%U:%G:%a:%h' "$gate_signer") == root:root:700:1 ]]
[[ $(sha256sum "$gate_signer" | awk '{{print $1}}') == {sha256_file(gate_signer)} ]]
printf 'profile-195-candidate-fixture\n' > "$gate_dir/candidate.tar.gz"
chmod 400 "$gate_dir/candidate.tar.gz"
archive_sha=$(sha256sum "$gate_dir/candidate.tar.gz" | awk '{{print $1}}')
image_id=sha256:$(printf 'd%.0s' {{1..64}})
migration_sha=$(printf '1%.0s' {{1..64}})
jq -n --arg release_id "$release_id" --arg archive_sha "$archive_sha" --arg image_id "$image_id" --arg migration_sha "$migration_sha" \
  '{{manifest:{{release_id:$release_id,profile:"195",schema:1,migration_sha256:{{"195_upstream_scheduling_monitor_rates.sql":$migration_sha}}}},evidence:{{candidate_archive_sha256:$archive_sha,candidate_image_id:$image_id}}}}' > "$gate_dir/gate.json"
chmod 400 "$gate_dir/gate.json"
export SUB2API_HELPER_TEST_MODE=true
export SUB2API_UNIT_LOCK_PATH="$vm_temp/libexec/.sub2api-release-unit.lock"
"$gate_signer" "$gate_dir/gate.json" "$gate_dir/gate.sig"
(cd "$gate_dir" && sha256sum "$gate_dir/gate.json" "$gate_dir/gate.sig" "$gate_dir/candidate.tar.gz" > SHA256SUMS)
chmod 400 "$gate_dir/SHA256SUMS"
# Profile 195 retains its single-migration checksum rather than a normalized map hash.
[[ $(jq -er '.manifest.migration_sha256["195_upstream_scheduling_monitor_rates.sql"]' "$gate_dir/gate.json") == "$migration_sha" ]]
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

    remote = runner.create_temp_dir("backup", "/srv/sub2api-backups", "dr-promotion-test")
    remote_temps.append(("backup", remote))
    (ROOT / ".tmp").mkdir(exist_ok=True)
    local_temp = tempfile.TemporaryDirectory(dir=ROOT / ".tmp")
    local_temps.append(local_temp)
    local_root = Path(local_temp.name)
    for name in ("gate.json", "gate.sig", "candidate.tar.gz", "SHA256SUMS"):
        local_path = local_root / name
        runner.download_file("local_vm", f"{vm_temp}/{name}", local_path)
        runner.upload_file("backup", local_path, f"{remote}/{name}", 0o400)
    files = {
        "verifier": verifier_binary(),
        "promoter": DEPLOY_ROOT / "release" / "promote-dr-baseline.sh",
        "bootstrap": DEPLOY_ROOT / "release" / "bootstrap_backup_dr_assets.sh",
        "trust.pub": DEPLOY_ROOT / "release" / "trust" / "vm-gate-ed25519.pub",
    }
    for name, path in files.items():
        runner.upload_file("backup", path, f"{remote}/{name}", 0o700 if name != "trust.pub" else 0o400)

    # Assemble a synthetic candidate only under the isolated backup test root.
    # The final evidence must bind its actual artifact and six-file bundle hashes.
    compact_time = drill_id.rsplit("-", 1)[1]
    binding = runner.run(
        "backup",
        rf'''
set -Eeuo pipefail
remote={shlex.quote(remote)}
release_id={release_id}
candidate_dir="$remote/releases/195/candidates/$release_id"
install -d -o root -g root -m 700 "$remote/releases" "$remote/releases/195" "$remote/releases/195/candidates" "$candidate_dir"
for file in candidate.tar.gz gate.json gate.sig SHA256SUMS; do
  install -o root -g root -m 600 "$remote/$file" "$candidate_dir/$file"
done
printf 'encrypted-profile-195-fixture\n' > "$candidate_dir/artifact.tar.age"
chmod 600 "$candidate_dir/artifact.tar.age"
chown root:root "$candidate_dir/artifact.tar.age"
artifact_sha=$(sha256sum "$candidate_dir/artifact.tar.age" | awk '{{print $1}}')
archive_sha=$(sha256sum "$candidate_dir/candidate.tar.gz" | awk '{{print $1}}')
image_id=$(jq -er '.evidence.candidate_image_id' "$candidate_dir/gate.json")
migration_sha=$(jq -er '.manifest.migration_sha256["195_upstream_scheduling_monitor_rates.sql"]' "$candidate_dir/gate.json")
[[ $archive_sha == {candidate['archive_sha256']} && $image_id == {candidate['candidate_image_id']} && $migration_sha == {candidate['migration_sha256']} ]]
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
ln -s "candidates/$release_id" "$remote/releases/195/candidate"
printf 'artifact_sha256=%s\n' "$artifact_sha"
printf 'bundle_sha256=%s\n' "$(sha256sum "$candidate_dir/bundle.sha256" | awk '{{print $1}}')"
''',
        {"artifact_sha256", "bundle_sha256"},
        timeout=300,
    ).values
    runner.run(
        "local_vm",
        rf'''
set -Eeuo pipefail
vm_temp={shlex.quote(vm_temp)}
release_id={release_id}
drill_id={shlex.quote(drill_id)}
observed_at={shlex.quote(observed_at)}
release_dir={shlex.quote(evidence_release_dir)}
evidence_dir="$release_dir/$drill_id"
[[ ! -e $release_dir && ! -L $release_dir ]]
cleanup() {{ rm -rf -- "$release_dir"; }}
trap cleanup EXIT
dr_root=/opt/sub2api-deploy/dr-evidence
if [[ -e $dr_root || -L $dr_root ]]; then
  [[ -d $dr_root && ! -L $dr_root && $(realpath -e -- "$dr_root") == "$dr_root" && $(stat -c '%U:%G:%a' "$dr_root") == root:root:700 ]]
else
  install -d -o root -g root -m 700 "$dr_root"
fi
install -d -o root -g root -m 700 "$release_dir" "$evidence_dir"
signer="$vm_temp/libexec/sub2api-sign-dr-evidence"
[[ -f $signer && ! -L $signer && $(stat -c '%U:%G:%a:%h' "$signer") == root:root:700:1 ]]
[[ $(sha256sum "$signer" | awk '{{print $1}}') == {sha256_file(dr_signer)} ]]
export SUB2API_HELPER_TEST_MODE=true
export SUB2API_UNIT_LOCK_PATH="$vm_temp/libexec/.sub2api-release-unit.lock"
write_bound() {{
  local artifact_sha=$1 output_name=$2
  jq -n --arg release_id "$release_id" --arg drill_id "$drill_id" --arg now "$observed_at" \
    --arg artifact_sha "$artifact_sha" --arg bundle_sha {binding['bundle_sha256']} \
    --arg archive_sha {candidate['archive_sha256']} --arg image_id {candidate['candidate_image_id']} --arg migration_sha {candidate['migration_sha256']} \
    '{{schema:1,release_id:$release_id,drill_id:$drill_id,created_at:$now,completed_at:$now,artifact_sha256:$artifact_sha,candidate_bundle_sha256:$bundle_sha,candidate_archive_sha256:$archive_sha,candidate_image_id:$image_id,migration_checksum:$migration_sha,image_load_id_check:"pass",config_manifest_check:"pass",postgres_restore:"pass",redis_restore:"pass",redis_ttl_reconciliation:"pass",counts_and_migrations:"pass",temporary_material_destroyed:"pass",redis_backup_dbsize:10,redis_backup_expiring_keys:2,redis_restored_dbsize:9,redis_restored_expiring_keys:1}}' > "$evidence_dir/evidence.json"
  chmod 400 "$evidence_dir/evidence.json"
  rm -f -- "$evidence_dir/evidence.sig"
  "$signer" "$evidence_dir/evidence.json" "$evidence_dir/evidence.sig"
  install -o root -g root -m 400 "$evidence_dir/evidence.json" "$vm_temp/$output_name.json"
  install -o root -g root -m 400 "$evidence_dir/evidence.sig" "$vm_temp/$output_name.sig"
}}
write_bound {binding['artifact_sha256']} evidence
write_bound $(printf '0%.0s' {{1..64}}) mismatch
printf 'bound_evidence_ready=pass\n'
''',
        {"bound_evidence_ready"},
        timeout=300,
    )
    for name in ("evidence.json", "evidence.sig", "mismatch.json", "mismatch.sig"):
        local_path = local_root / name
        runner.download_file("local_vm", f"{vm_temp}/{name}", local_path)
        runner.upload_file("backup", local_path, f"{remote}/{name}", 0o400)
    verifier_sha = sha256_file(files["verifier"])
    promoter_sha = sha256_file(files["promoter"])
    trust_sha = sha256_file(files["trust.pub"])
    script = rf'''
set -Eeuo pipefail
remote={shlex.quote(remote)}
asset_state() {{ if [[ -e $1 ]]; then sha256sum "$1" | awk '{{print $1}}'; else printf absent; fi; }}
before_verifier=$(asset_state /usr/local/libexec/sub2api-verify-dr-evidence)
before_promoter=$(asset_state /usr/local/libexec/sub2api-promote-dr-baseline)
before_trust=$(asset_state /opt/sub2api-dr-trust/vm-gate-ed25519.pub)
cleanup() {{ rm -rf -- "$remote"; }}
trap cleanup EXIT
run_bootstrap() {{
  local promoter_source=$1 promoter_expected=$2 fail_after=${{3:-false}}
  SUB2API_BACKUP_BOOTSTRAP_TEST_MODE=true SUB2API_TEST_FAIL_AFTER_VERIFIER_ACTIVATION="$fail_after" BACKUP_TEST_ROOT="$remote" \
    VERIFIER_SOURCE="$remote/verifier" PROMOTER_SOURCE="$promoter_source" TRUST_SOURCE="$remote/trust.pub" \
    SIGNED_TEST_SOURCE="$remote/gate.json" SIGNED_TEST_SIGNATURE="$remote/gate.sig" \
    VERIFIER_SHA256={verifier_sha} PROMOTER_SHA256="$promoter_expected" TRUST_SHA256={trust_sha} \
    "$remote/bootstrap" >/dev/null 2>&1
}}
run_bootstrap "$remote/promoter" {promoter_sha}
[[ $(asset_state "$remote/libexec/sub2api-verify-dr-evidence") == {verifier_sha} ]]
[[ $(asset_state "$remote/libexec/sub2api-promote-dr-baseline") == {promoter_sha} ]]
[[ $(asset_state "$remote/trust/vm-gate-ed25519.pub") == {trust_sha} ]]
backup_bootstrap_successful_activation=pass
run_bootstrap "$remote/promoter" {promoter_sha}
backup_bootstrap_idempotent_reinstall=pass
printf sentinel > "$remote/lock-sentinel"
sentinel_sha=$(sha256sum "$remote/lock-sentinel" | awk '{{print $1}}')
rm -f -- "$remote/libexec/.sub2api-dr-assets.lock"
ln -s "$remote/lock-sentinel" "$remote/libexec/.sub2api-dr-assets.lock"
if run_bootstrap "$remote/promoter" {promoter_sha}; then exit 80; fi
[[ $(sha256sum "$remote/lock-sentinel" | awk '{{print $1}}') == "$sentinel_sha" ]]
rm -f -- "$remote/libexec/.sub2api-dr-assets.lock"
install -o root -g root -m 600 /dev/null "$remote/libexec/.sub2api-dr-assets.lock"
backup_bootstrap_lock_symlink_rejected=pass
sed 's/^verifier_sha256=.*/verifier_sha256=0000000000000000000000000000000000000000000000000000000000000000/' \
  "$remote/promoter" > "$remote/promoter-unbound"
chmod 700 "$remote/promoter-unbound"
unbound_sha=$(sha256sum "$remote/promoter-unbound" | awk '{{print $1}}')
if run_bootstrap "$remote/promoter-unbound" "$unbound_sha"; then exit 81; fi
[[ $(asset_state "$remote/libexec/sub2api-verify-dr-evidence") == {verifier_sha} ]]
[[ $(asset_state "$remote/libexec/sub2api-promote-dr-baseline") == {promoter_sha} ]]
[[ $(asset_state "$remote/trust/vm-gate-ed25519.pub") == {trust_sha} ]]
backup_bootstrap_version_mismatch_rejected=pass
cp "$remote/promoter" "$remote/promoter-mutated"
printf '\n' >> "$remote/promoter-mutated"
chmod 700 "$remote/promoter-mutated"
mutated_sha=$(sha256sum "$remote/promoter-mutated" | awk '{{print $1}}')
if run_bootstrap "$remote/promoter-mutated" "$mutated_sha" true; then exit 81; fi
[[ $(asset_state "$remote/libexec/sub2api-verify-dr-evidence") == {verifier_sha} ]]
[[ $(asset_state "$remote/libexec/sub2api-promote-dr-baseline") == {promoter_sha} ]]
[[ $(asset_state "$remote/trust/vm-gate-ed25519.pub") == {trust_sha} ]]
backup_bootstrap_post_activation_rollback=pass

release_id={release_id}
drill_id={drill_id}
promotion_root="$remote/releases/195"
candidate_root="$promotion_root/candidates"
input_root="$promotion_root/promotion-input"
verified_root="$promotion_root/verified-bundles"
[[ -d "$candidate_root/$release_id" && ! -L "$candidate_root/$release_id" && $(stat -c '%U:%G:%a' "$candidate_root/$release_id") == root:root:700 ]]
[[ -L $promotion_root/candidate && $(readlink "$promotion_root/candidate") == "candidates/$release_id" ]]
[[ $(realpath -e -- "$promotion_root/candidate") == "$candidate_root/$release_id" ]]
old_target_name="$release_id--{previous_drill_id}"
old_target="$verified_root/$old_target_name"
install -d -o root -g root -m 700 "$old_target"
for file in SHA256SUMS artifact.tar.age bundle.sha256 candidate.tar.gz gate.json gate.sig manifest; do
  install -o root -g root -m 400 "$candidate_root/$release_id/$file" "$old_target/$file"
done
install -o root -g root -m 400 "$remote/evidence.json" "$old_target/evidence.json"
install -o root -g root -m 400 "$remote/evidence.sig" "$old_target/evidence.sig"
(cd "$old_target" && sha256sum SHA256SUMS artifact.tar.age bundle.sha256 candidate.tar.gz evidence.json evidence.sig gate.json gate.sig manifest > VERIFIED_SHA256SUMS)
chmod 400 "$old_target/VERIFIED_SHA256SUMS"
chown root:root "$old_target/VERIFIED_SHA256SUMS"
ln -s "verified-bundles/$old_target_name" "$promotion_root/verified"
old_fingerprint=$(cd "$old_target" && sha256sum SHA256SUMS VERIFIED_SHA256SUMS artifact.tar.age bundle.sha256 candidate.tar.gz evidence.json evidence.sig gate.json gate.sig manifest | sha256sum | awk '{{print $1}}')
candidate_fingerprint=$(cd "$candidate_root/$release_id" && sha256sum SHA256SUMS artifact.tar.age bundle.sha256 candidate.tar.gz gate.json gate.sig manifest | sha256sum | awk '{{print $1}}')
bundle_files=(artifact.tar.age candidate.tar.gz gate.json gate.sig manifest SHA256SUMS)
[[ $(wc -l < "$candidate_root/$release_id/bundle.sha256") == ${{#bundle_files[@]}} ]]
line=1
for file in "${{bundle_files[@]}}"; do
  recorded_sha=$(awk -v line="$line" -v expected="$file" 'NR == line && NF == 2 && $2 == expected {{print $1}}' "$candidate_root/$release_id/bundle.sha256")
  [[ ${{#recorded_sha}} == 64 && $recorded_sha =~ ^[0-9a-f]{{64}}$ ]]
  [[ $recorded_sha == "$(sha256sum "$candidate_root/$release_id/$file" | awk '{{print $1}}')" ]]
  ((line += 1))
done
promotion_bundle_contract=pass

valid_input="$input_root/$release_id--$drill_id.VALID123"
mismatch_input="$input_root/$release_id--$drill_id.BAD00001"
for input in "$valid_input" "$mismatch_input"; do install -d -o root -g root -m 700 "$input"; done
install -o root -g root -m 400 "$remote/evidence.json" "$valid_input/evidence.json"
install -o root -g root -m 400 "$remote/evidence.sig" "$valid_input/evidence.sig"
install -o root -g root -m 400 "$remote/mismatch.json" "$mismatch_input/evidence.json"
install -o root -g root -m 400 "$remote/mismatch.sig" "$mismatch_input/evidence.sig"
input_fingerprint=$(sha256sum "$valid_input/evidence.json" "$valid_input/evidence.sig" "$mismatch_input/evidence.json" "$mismatch_input/evidence.sig" | sha256sum | awk '{{print $1}}')
target_name="$release_id--$drill_id"
target="$verified_root/$target_name"
run_promoter() {{
  SUB2API_PROMOTION_TEST_MODE=true PROMOTION_TEST_ROOT="$remote" \
    "$remote/libexec/sub2api-promote-dr-baseline" "$release_id" "$drill_id" "$1"
}}
assert_sources_unchanged() {{
  [[ $(readlink "$promotion_root/candidate") == "candidates/$release_id" ]]
  [[ $(cd "$candidate_root/$release_id" && sha256sum SHA256SUMS artifact.tar.age bundle.sha256 candidate.tar.gz gate.json gate.sig manifest | sha256sum | awk '{{print $1}}') == "$candidate_fingerprint" ]]
  [[ $(readlink "$promotion_root/verified") == "verified-bundles/$old_target_name" ]]
  [[ $(cd "$old_target" && sha256sum SHA256SUMS VERIFIED_SHA256SUMS artifact.tar.age bundle.sha256 candidate.tar.gz evidence.json evidence.sig gate.json gate.sig manifest | sha256sum | awk '{{print $1}}') == "$old_fingerprint" ]]
}}
assert_no_promotion_temps() {{
  [[ -z $(find "$verified_root" -mindepth 1 -maxdepth 1 -name '.staging-*' -print -quit) ]]
  [[ -z $(find "$promotion_root" -mindepth 1 -maxdepth 1 -name '.verified.*.tmp' -print -quit) ]]
}}
printf sentinel > "$remote/promotion-lock-sentinel"
promotion_lock_sentinel_sha=$(sha256sum "$remote/promotion-lock-sentinel" | awk '{{print $1}}')
ln -s "$remote/promotion-lock-sentinel" "$promotion_root/.promotion.lock"
if run_promoter "$mismatch_input" >/dev/null 2>&1; then exit 83; fi
[[ $(sha256sum "$remote/promotion-lock-sentinel" | awk '{{print $1}}') == "$promotion_lock_sentinel_sha" ]]
assert_sources_unchanged
rm -f -- "$promotion_root/.promotion.lock"
install -o root -g root -m 600 /dev/null "$promotion_root/.promotion.lock"
promotion_lock_symlink_rejected=pass
if run_promoter "$mismatch_input" >/dev/null 2>&1; then exit 83; fi
assert_sources_unchanged
[[ ! -e $target && ! -L $target ]]
assert_no_promotion_temps
promotion_candidate_evidence_mismatch_rejected=pass

install -d -o root -g root -m 700 "$target"
install -o root -g root -m 400 "$remote/gate.json" "$target/conflict-marker"
if run_promoter "$valid_input" >/dev/null 2>&1; then exit 84; fi
assert_sources_unchanged
[[ -f $target/conflict-marker ]]
assert_no_promotion_temps
promotion_conflict_target_rejected=pass
promotion_failure_preserved_old_verified=pass
rm -rf -- "$target"

install -d -o root -g root -m 700 "$target"
for file in SHA256SUMS artifact.tar.age bundle.sha256 candidate.tar.gz gate.json gate.sig manifest; do
  install -o root -g root -m 400 "$candidate_root/$release_id/$file" "$target/$file"
done
install -o root -g root -m 400 "$remote/evidence.json" "$target/evidence.json"
install -o root -g root -m 400 "$remote/evidence.sig" "$target/evidence.sig"
(cd "$target" && sha256sum SHA256SUMS artifact.tar.age bundle.sha256 candidate.tar.gz evidence.json evidence.sig gate.json gate.sig manifest > VERIFIED_SHA256SUMS)
chmod 400 "$target/VERIFIED_SHA256SUMS"
chown root:root "$target/VERIFIED_SHA256SUMS"
printf '\n' >> "$target/evidence.json"
(cd "$target" && sha256sum SHA256SUMS artifact.tar.age bundle.sha256 candidate.tar.gz evidence.json evidence.sig gate.json gate.sig manifest > VERIFIED_SHA256SUMS)
chmod 400 "$target/VERIFIED_SHA256SUMS"
if run_promoter "$valid_input" >/dev/null 2>&1; then exit 85; fi
assert_sources_unchanged
[[ -f $target/evidence.json && $(stat -c '%U:%G:%a:%h' "$target/evidence.json") == root:root:400:1 ]]
assert_no_promotion_temps
promotion_content_conflict_rejected=pass
rm -rf -- "$target"

promotion_output=$(run_promoter "$valid_input")
[[ $(printf '%s\n' "$promotion_output" | wc -l) == 5 ]]
promotion_value() {{ awk -F= -v key="$1" '$1 == key {{sub(/^[^=]*=/, ""); print; found++}} END {{exit found == 1 ? 0 : 1}}' <<< "$promotion_output"; }}
[[ $(promotion_value promotion_status) == verified ]]
[[ $(promotion_value verified_target) == "$target_name" ]]
[[ $(promotion_value evidence_sha256) == "$(sha256sum "$valid_input/evidence.json" | awk '{{print $1}}')" ]]
[[ $(promotion_value signature_sha256) == "$(sha256sum "$valid_input/evidence.sig" | awk '{{print $1}}')" ]]
[[ $(promotion_value verified_bundle_sha256) == "$(sha256sum "$target/VERIFIED_SHA256SUMS" | awk '{{print $1}}')" ]]
[[ $(find "$target" -mindepth 1 -maxdepth 1 -printf '%y %f\n' | LC_ALL=C sort) == $'f SHA256SUMS\nf VERIFIED_SHA256SUMS\nf artifact.tar.age\nf bundle.sha256\nf candidate.tar.gz\nf evidence.json\nf evidence.sig\nf gate.json\nf gate.sig\nf manifest' ]]
for file in "$target"/*; do [[ $(stat -c '%U:%G:%a:%h' "$file") == root:root:400:1 ]]; done
(cd "$target" && sha256sum -c VERIFIED_SHA256SUMS >/dev/null)
for file in SHA256SUMS artifact.tar.age bundle.sha256 candidate.tar.gz gate.json gate.sig manifest; do cmp -s "$candidate_root/$release_id/$file" "$target/$file"; done
cmp -s "$valid_input/evidence.json" "$target/evidence.json"
cmp -s "$valid_input/evidence.sig" "$target/evidence.sig"
[[ $(readlink "$promotion_root/verified") == "verified-bundles/$target_name" ]]
[[ $(realpath -e -- "$promotion_root/verified") == "$target" ]]
[[ $(cd "$candidate_root/$release_id" && sha256sum SHA256SUMS artifact.tar.age bundle.sha256 candidate.tar.gz gate.json gate.sig manifest | sha256sum | awk '{{print $1}}') == "$candidate_fingerprint" ]]
[[ $(cd "$old_target" && sha256sum SHA256SUMS VERIFIED_SHA256SUMS artifact.tar.age bundle.sha256 candidate.tar.gz evidence.json evidence.sig gate.json gate.sig manifest | sha256sum | awk '{{print $1}}') == "$old_fingerprint" ]]
[[ $(sha256sum "$valid_input/evidence.json" "$valid_input/evidence.sig" "$mismatch_input/evidence.json" "$mismatch_input/evidence.sig" | sha256sum | awk '{{print $1}}') == "$input_fingerprint" ]]
target_fingerprint=$(cd "$target" && sha256sum SHA256SUMS VERIFIED_SHA256SUMS artifact.tar.age bundle.sha256 candidate.tar.gz evidence.json evidence.sig gate.json gate.sig manifest | sha256sum | awk '{{print $1}}')
promotion_successful_activation=pass
promotion_pointer_and_checksum_verified=pass

promotion_output_second=$(run_promoter "$valid_input")
[[ $promotion_output_second == "$promotion_output" ]]
[[ $(cd "$target" && sha256sum SHA256SUMS VERIFIED_SHA256SUMS artifact.tar.age bundle.sha256 candidate.tar.gz evidence.json evidence.sig gate.json gate.sig manifest | sha256sum | awk '{{print $1}}') == "$target_fingerprint" ]]
[[ $(cd "$candidate_root/$release_id" && sha256sum SHA256SUMS artifact.tar.age bundle.sha256 candidate.tar.gz gate.json gate.sig manifest | sha256sum | awk '{{print $1}}') == "$candidate_fingerprint" ]]
[[ $(cd "$old_target" && sha256sum SHA256SUMS VERIFIED_SHA256SUMS artifact.tar.age bundle.sha256 candidate.tar.gz evidence.json evidence.sig gate.json gate.sig manifest | sha256sum | awk '{{print $1}}') == "$old_fingerprint" ]]
[[ $(sha256sum "$valid_input/evidence.json" "$valid_input/evidence.sig" "$mismatch_input/evidence.json" "$mismatch_input/evidence.sig" | sha256sum | awk '{{print $1}}') == "$input_fingerprint" ]]
[[ $(find "$verified_root" -mindepth 1 -maxdepth 1 -type d | wc -l) == 2 ]]
[[ -d $old_target && -d $candidate_root/$release_id && -d $valid_input && -d $mismatch_input ]]
assert_no_promotion_temps
promotion_idempotent_replay=pass
promotion_sources_retained=pass
[[ $(asset_state /usr/local/libexec/sub2api-verify-dr-evidence) == "$before_verifier" ]]
[[ $(asset_state /usr/local/libexec/sub2api-promote-dr-baseline) == "$before_promoter" ]]
[[ $(asset_state /opt/sub2api-dr-trust/vm-gate-ed25519.pub) == "$before_trust" ]]
backup_production_assets_unchanged=pass
cleanup
trap - EXIT
test ! -e "$remote"
cleanup_verified=pass
printf 'backup_bootstrap_successful_activation=pass\n'
printf 'backup_bootstrap_idempotent_reinstall=pass\n'
printf 'backup_bootstrap_lock_symlink_rejected=pass\n'
printf 'backup_bootstrap_version_mismatch_rejected=pass\n'
printf 'backup_bootstrap_post_activation_rollback=pass\n'
printf 'backup_production_assets_unchanged=pass\n'
printf 'promotion_bundle_contract=pass\n'
printf 'promotion_candidate_evidence_mismatch_rejected=pass\n'
printf 'promotion_lock_symlink_rejected=pass\n'
printf 'promotion_conflict_target_rejected=pass\n'
printf 'promotion_content_conflict_rejected=pass\n'
printf 'promotion_failure_preserved_old_verified=pass\n'
printf 'promotion_successful_activation=pass\n'
printf 'promotion_pointer_and_checksum_verified=pass\n'
printf 'promotion_idempotent_replay=pass\n'
printf 'promotion_sources_retained=pass\n'
printf 'cleanup_verified=pass\n'
'''
    fields = {
        "backup_bootstrap_successful_activation",
        "backup_bootstrap_idempotent_reinstall",
        "backup_bootstrap_lock_symlink_rejected",
        "backup_bootstrap_version_mismatch_rejected",
        "backup_bootstrap_post_activation_rollback",
        "backup_production_assets_unchanged",
        "promotion_bundle_contract",
        "promotion_candidate_evidence_mismatch_rejected",
        "promotion_lock_symlink_rejected",
        "promotion_conflict_target_rejected",
        "promotion_content_conflict_rejected",
        "promotion_failure_preserved_old_verified",
        "promotion_successful_activation",
        "promotion_pointer_and_checksum_verified",
        "promotion_idempotent_replay",
        "promotion_sources_retained",
        "cleanup_verified",
    }
    result = runner.run("backup", script, fields, timeout=300).values
    if set(result) != fields or set(result.values()) != {"pass"}:
        raise RuntimeError("backup DR asset integration test did not pass")
    print(f"backup_dr_assets_integration=pass checks={len(result)}")


def main() -> None:
    runner = SSHRunner()
    remote_temps: list[tuple[str, str]] = []
    local_temps: list[tempfile.TemporaryDirectory[str]] = []
    primary_error: BaseException | None = None
    try:
        run(runner, remote_temps, local_temps)
    except BaseException as exc:
        primary_error = exc

    cleanup_errors: list[BaseException] = []
    for host, path in reversed(remote_temps):
        try:
            cleanup_remote_temp(runner, host, path)
        except BaseException as exc:
            cleanup_errors.append(exc)
    for local_temp in reversed(local_temps):
        try:
            local_temp.cleanup()
        except BaseException as exc:
            cleanup_errors.append(exc)

    if primary_error is not None:
        if cleanup_errors:
            note = "one or more registered temporary directories could not be cleaned"
            if hasattr(primary_error, "add_note"):
                primary_error.add_note(note)
            else:
                # Keep the primary failure on Python versions before PEP 678.
                primary_error.__notes__ = [*getattr(primary_error, "__notes__", []), note]
        raise primary_error.with_traceback(primary_error.__traceback__)
    if cleanup_errors:
        raise cleanup_errors[0]


if __name__ == "__main__":
    main()
