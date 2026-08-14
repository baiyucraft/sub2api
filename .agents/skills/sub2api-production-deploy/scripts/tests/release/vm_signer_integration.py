from __future__ import annotations

import sys
import shlex
from pathlib import Path


DEPLOY_ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(DEPLOY_ROOT))

from release.ssh import SSHRunner


def cleanup_remote_temp(runner: SSHRunner, path: str) -> None:
    quoted = shlex.quote(path)
    script = f"""
if [[ -e {quoted} || -L {quoted} ]]; then
  [[ -d {quoted} && ! -L {quoted} && $(realpath -e -- {quoted}) == {quoted} ]]
  rm -rf -- {quoted}
fi
printf 'cleanup=pass\\n'
"""
    runner.run("local_vm", script, {"cleanup"})


def run(runner: SSHRunner, remote_temps: list[str]) -> None:
    base = "/opt/sub2api-deploy/release-input"
    remote = runner.create_temp_dir("local_vm", base, "dr-signer-test")
    remote_temps.append(remote)
    assets = {
        "validator": DEPLOY_ROOT / "release" / "vm-validate.sh",
        "bootstrap": DEPLOY_ROOT / "release" / "bootstrap_vm_signer.sh",
        "sign-gate": DEPLOY_ROOT / "release" / "sign-gate.sh",
        "sign-dr": DEPLOY_ROOT / "release" / "sign-dr-evidence.sh",
    }
    for name, path in assets.items():
        runner.upload_file("local_vm", path, f"{remote}/{name}", 0o700)

    script = rf'''
set -Eeuo pipefail
remote={shlex.quote(remote)}
release=199-000000000000-0-deadbeef
now=$(date -u +%Y-%m-%dT%H:%M:%SZ)
drill="dr-199-$(tr -d ':-' <<<"$now")"
gate_dir=/opt/sub2api-deploy/release-gates/$release/output
dr_root=/opt/sub2api-deploy/dr-evidence
dr_dir=$dr_root/$release/$drill
asset_state() {{
  if [[ -e $1 ]]; then sha256sum "$1" | awk '{{print $1}}'; else printf absent; fi
}}
before_public=$(sha256sum /opt/sub2api-release-signer/vm-gate-ed25519.pub | awk '{{print $1}}')
before_validator=$(asset_state /usr/local/libexec/sub2api-vm-validate)
before_gate=$(asset_state /usr/local/libexec/sub2api-sign-gate)
before_dr=$(asset_state /usr/local/libexec/sub2api-sign-dr-evidence)
cleanup() {{
  rm -rf -- "/opt/sub2api-deploy/release-gates/$release" "$dr_root/$release" "$remote"
}}
trap cleanup EXIT
bash -n "$remote/validator" "$remote/bootstrap" "$remote/sign-gate" "$remote/sign-dr"
test_libexec="$remote/libexec"
validator_sha=$(sha256sum "$remote/validator" | awk '{{print $1}}')
gate_sha=$(sha256sum "$remote/sign-gate" | awk '{{print $1}}')
dr_sha=$(sha256sum "$remote/sign-dr" | awk '{{print $1}}')
run_test_bootstrap() {{
  local validator_source=$1 validator_expected=$2 fail_after=${{3:-false}}
  REQUIRE_EXISTING_SIGNER_KEYS=true SUB2API_BOOTSTRAP_TEST_MODE=true \
    SUB2API_TEST_FAIL_AFTER_VALIDATOR_ACTIVATION="$fail_after" TARGET_LIBEXEC_DIR="$test_libexec" \
    VALIDATOR_SOURCE="$validator_source" GATE_SIGNER_SOURCE="$remote/sign-gate" DR_SIGNER_SOURCE="$remote/sign-dr" \
    VALIDATOR_SHA256="$validator_expected" GATE_SIGNER_SHA256="$gate_sha" DR_SIGNER_SHA256="$dr_sha" \
    "$remote/bootstrap" >/dev/null 2>&1
}}
run_test_bootstrap "$remote/validator" "$validator_sha"
[[ $(asset_state "$test_libexec/sub2api-vm-validate") == "$validator_sha" ]]
[[ $(asset_state "$test_libexec/sub2api-sign-gate") == "$gate_sha" ]]
[[ $(asset_state "$test_libexec/sub2api-sign-dr-evidence") == "$dr_sha" ]]
bootstrap_successful_activation=pass
run_test_bootstrap "$remote/validator" "$validator_sha"
[[ $(asset_state "$test_libexec/sub2api-vm-validate") == "$validator_sha" ]]
[[ $(asset_state "$test_libexec/sub2api-sign-gate") == "$gate_sha" ]]
[[ $(asset_state "$test_libexec/sub2api-sign-dr-evidence") == "$dr_sha" ]]
bootstrap_idempotent_reinstall=pass
printf sentinel > "$remote/lock-sentinel"
sentinel_sha=$(sha256sum "$remote/lock-sentinel" | awk '{{print $1}}')
rm -f -- "$test_libexec/.sub2api-release-unit.lock"
ln -s "$remote/lock-sentinel" "$test_libexec/.sub2api-release-unit.lock"
if run_test_bootstrap "$remote/validator" "$validator_sha"; then exit 80; fi
[[ $(sha256sum "$remote/lock-sentinel" | awk '{{print $1}}') == "$sentinel_sha" ]]
rm -f -- "$test_libexec/.sub2api-release-unit.lock"
install -o root -g root -m 600 /dev/null "$test_libexec/.sub2api-release-unit.lock"
bootstrap_lock_symlink_rejected=pass
cp "$remote/validator" "$remote/validator-mutated"
printf '\n' >> "$remote/validator-mutated"
chmod 700 "$remote/validator-mutated"
mutated_validator_sha=$(sha256sum "$remote/validator-mutated" | awk '{{print $1}}')
if run_test_bootstrap "$remote/validator-mutated" "$mutated_validator_sha" true; then exit 81; fi
[[ $(asset_state "$test_libexec/sub2api-vm-validate") == "$validator_sha" ]]
[[ $(asset_state "$test_libexec/sub2api-sign-gate") == "$gate_sha" ]]
[[ $(asset_state "$test_libexec/sub2api-sign-dr-evidence") == "$dr_sha" ]]
bootstrap_post_activation_rollback=pass
[[ $(asset_state /usr/local/libexec/sub2api-vm-validate) == "$before_validator" ]]
[[ $(asset_state /usr/local/libexec/sub2api-sign-gate) == "$before_gate" ]]
[[ $(asset_state /usr/local/libexec/sub2api-sign-dr-evidence) == "$before_dr" ]]
production_unit_unchanged=pass
[[ $(sha256sum /opt/sub2api-release-signer/vm-gate-ed25519.pub | awk '{{print $1}}') == "$before_public" ]]
trust_root_unchanged=pass

fixed_gate_signer=/usr/local/libexec/sub2api-sign-gate
fixed_dr_signer=/usr/local/libexec/sub2api-sign-dr-evidence
[[ -f $fixed_gate_signer && ! -L $fixed_gate_signer && $(stat -c '%U:%G:%a:%h' "$fixed_gate_signer") == root:root:700:1 ]]
[[ -f $fixed_dr_signer && ! -L $fixed_dr_signer && $(stat -c '%U:%G:%a:%h' "$fixed_dr_signer") == root:root:700:1 ]]
[[ $(sha256sum "$fixed_gate_signer" | awk '{{print $1}}') == "$before_gate" ]]
[[ $(sha256sum "$fixed_dr_signer" | awk '{{print $1}}') == "$before_dr" ]]
fixed_helpers_verified=pass
helper_lock="$test_libexec/.helper-lock"
printf sentinel > "$remote/helper-lock-sentinel"
helper_sentinel_sha=$(sha256sum "$remote/helper-lock-sentinel" | awk '{{print $1}}')
ln -s "$remote/helper-lock-sentinel" "$helper_lock"
if SUB2API_HELPER_TEST_MODE=true SUB2API_UNIT_LOCK_PATH="$helper_lock" "$remote/sign-gate" "$remote/sign-gate" "$remote/bad.sig" >/dev/null 2>&1; then exit 97; fi
[[ $(sha256sum "$remote/helper-lock-sentinel" | awk '{{print $1}}') == "$helper_sentinel_sha" ]]
rm -f -- "$helper_lock"
install -o root -g root -m 600 /dev/null "$helper_lock"
rm -f -- "$helper_lock"
ln -s "$remote/helper-lock-sentinel" "$helper_lock"
if SUB2API_HELPER_TEST_MODE=true SUB2API_UNIT_LOCK_PATH="$helper_lock" "$remote/sign-dr" "$remote/sign-dr" "$remote/bad.sig" >/dev/null 2>&1; then exit 98; fi
[[ $(sha256sum "$remote/helper-lock-sentinel" | awk '{{print $1}}') == "$helper_sentinel_sha" ]]
rm -f -- "$helper_lock"
install -o root -g root -m 600 /dev/null "$helper_lock"
helper_lock_symlink_rejected=pass

gate_signer="$test_libexec/sub2api-sign-gate"
dr_signer="$test_libexec/sub2api-sign-dr-evidence"
export SUB2API_HELPER_TEST_MODE=true
export SUB2API_UNIT_LOCK_PATH="$test_libexec/.sub2api-release-unit.lock"

install -d -o root -g root -m 700 "$gate_dir"
if [[ -e $dr_root ]]; then
  [[ -d $dr_root && ! -L $dr_root && $(stat -c '%U:%G:%a' "$dr_root") == root:root:700 ]]
else
  install -d -o root -g root -m 700 "$dr_root"
fi
install -d -o root -g root -m 700 "$dr_root/$release" "$dr_dir"
printf '{{"selftest":true}}\n' > "$gate_dir/gate.json"
chmod 400 "$gate_dir/gate.json"
"$gate_signer" "$gate_dir/gate.json" "$gate_dir/gate.sig"
openssl pkeyutl -verify -pubin -inkey /opt/sub2api-release-signer/vm-gate-ed25519.pub -rawin -in "$gate_dir/gate.json" -sigfile "$gate_dir/gate.sig" >/dev/null
gate_valid=pass
if "$gate_signer" "$gate_dir/gate.json" "$gate_dir/gate.sig" >/dev/null 2>&1; then exit 82; fi
gate_existing_signature_rejected=pass
if "$gate_signer" "$remote/sign-gate" "$remote/bad.sig" >/dev/null 2>&1; then exit 83; fi
gate_wrong_path_rejected=pass

write_valid() {{
  jq -n --arg release_id "$release" --arg drill_id "$drill" --arg now "$now" '{{schema:1,release_id:$release_id,drill_id:$drill_id,created_at:$now,completed_at:$now,artifact_sha256:("a"*64),candidate_bundle_sha256:("b"*64),candidate_archive_sha256:("c"*64),candidate_image_id:("sha256:"+("d"*64)),migration_checksum:("e"*64),image_load_id_check:"pass",config_manifest_check:"pass",postgres_restore:"pass",redis_restore:"pass",redis_ttl_reconciliation:"pass",counts_and_migrations:"pass",temporary_material_destroyed:"pass",redis_backup_dbsize:576,redis_backup_expiring_keys:101,redis_restored_dbsize:526,redis_restored_expiring_keys:51}}' > "$dr_dir/evidence.json"
  chmod 400 "$dr_dir/evidence.json"
}}
reject_mutation() {{
  local filter=$1 exit_code=$2
  write_valid
  jq "$filter" "$dr_dir/evidence.json" > "$dr_dir/bad.json"
  mv "$dr_dir/bad.json" "$dr_dir/evidence.json"
  chmod 400 "$dr_dir/evidence.json"
  if "$dr_signer" "$dr_dir/evidence.json" "$dr_dir/evidence.sig" >/dev/null 2>&1; then exit "$exit_code"; fi
}}
write_valid
"$dr_signer" "$dr_dir/evidence.json" "$dr_dir/evidence.sig"
openssl pkeyutl -verify -pubin -inkey /opt/sub2api-release-signer/vm-gate-ed25519.pub -rawin -in "$dr_dir/evidence.json" -sigfile "$dr_dir/evidence.sig" >/dev/null
dr_valid=pass
if "$dr_signer" "$dr_dir/evidence.json" "$dr_dir/evidence.sig" >/dev/null 2>&1; then exit 84; fi
dr_existing_signature_rejected=pass
rm -f "$dr_dir/evidence.sig"
if "$dr_signer" "$remote/validator" "$remote/evidence.sig" >/dev/null 2>&1; then exit 85; fi
dr_outside_root_rejected=pass
write_valid
if "$dr_signer" "$dr_dir/evidence.json" "$dr_dir/wrong.sig" >/dev/null 2>&1; then exit 86; fi
dr_wrong_signature_path_rejected=pass
write_valid
chmod 755 "$dr_dir"
if "$dr_signer" "$dr_dir/evidence.json" "$dr_dir/evidence.sig" >/dev/null 2>&1; then exit 87; fi
dr_directory_mode_rejected=pass
chmod 700 "$dr_dir"
write_valid
chown 65534:65534 "$dr_root/$release"
if "$dr_signer" "$dr_dir/evidence.json" "$dr_dir/evidence.sig" >/dev/null 2>&1; then exit 88; fi
dr_directory_owner_rejected=pass
chown root:root "$dr_root/$release"
write_valid
chmod 600 "$dr_dir/evidence.json"
if "$dr_signer" "$dr_dir/evidence.json" "$dr_dir/evidence.sig" >/dev/null 2>&1; then exit 89; fi
dr_mode_rejected=pass
write_valid
chown 65534:65534 "$dr_dir/evidence.json"
if "$dr_signer" "$dr_dir/evidence.json" "$dr_dir/evidence.sig" >/dev/null 2>&1; then exit 90; fi
dr_owner_rejected=pass
chown root:root "$dr_dir/evidence.json"
reject_mutation '.postgres_restore="fail"' 87
dr_failed_assertion_rejected=pass
reject_mutation '.redis_restored_dbsize=525' 88
dr_ttl_mismatch_rejected=pass
reject_mutation '.redis_backup_dbsize=1|.redis_restored_dbsize=2|.redis_backup_expiring_keys=1|.redis_restored_expiring_keys=2' 89
dr_negative_delta_rejected=pass
reject_mutation '.extra="unsafe"' 90
dr_unknown_field_rejected=pass
reject_mutation '.artifact_sha256="bad"' 91
dr_malformed_sha_rejected=pass
reject_mutation 'del(.temporary_material_destroyed)' 92
dr_missing_assertion_rejected=pass
reject_mutation '.release_id="199-ffffffffffff-1-ffffffff"' 93
dr_release_mismatch_rejected=pass
reject_mutation '.drill_id="dr-199-20000101T000000Z"' 94
dr_drill_mismatch_rejected=pass
reject_mutation '.created_at="2020-01-01T00:00:00Z"|.completed_at="2020-01-01T00:00:00Z"' 95
dr_stale_time_rejected=pass
write_valid
mv "$dr_dir/evidence.json" "$dr_dir/real.json"
ln -s "$dr_dir/real.json" "$dr_dir/evidence.json"
if "$dr_signer" "$dr_dir/evidence.json" "$dr_dir/evidence.sig" >/dev/null 2>&1; then exit 96; fi
dr_symlink_rejected=pass
cleanup
trap - EXIT
test ! -e "$remote" && test ! -e "/opt/sub2api-deploy/release-gates/$release" && test ! -e "$dr_root/$release"
cleanup_verified=pass
for field in gate_valid gate_existing_signature_rejected gate_wrong_path_rejected dr_valid dr_existing_signature_rejected dr_outside_root_rejected dr_wrong_signature_path_rejected dr_directory_mode_rejected dr_directory_owner_rejected dr_mode_rejected dr_owner_rejected dr_failed_assertion_rejected dr_ttl_mismatch_rejected dr_negative_delta_rejected dr_unknown_field_rejected dr_malformed_sha_rejected dr_missing_assertion_rejected dr_release_mismatch_rejected dr_drill_mismatch_rejected dr_stale_time_rejected dr_symlink_rejected bootstrap_successful_activation bootstrap_idempotent_reinstall bootstrap_lock_symlink_rejected bootstrap_post_activation_rollback production_unit_unchanged trust_root_unchanged fixed_helpers_verified helper_lock_symlink_rejected cleanup_verified; do
  printf '%s=pass\n' "$field"
done
'''
    fields = {
        "gate_valid", "gate_existing_signature_rejected", "gate_wrong_path_rejected",
        "dr_valid", "dr_existing_signature_rejected", "dr_outside_root_rejected", "dr_wrong_signature_path_rejected",
        "dr_directory_mode_rejected", "dr_directory_owner_rejected", "dr_mode_rejected", "dr_owner_rejected",
        "dr_failed_assertion_rejected", "dr_ttl_mismatch_rejected", "dr_negative_delta_rejected",
        "dr_unknown_field_rejected", "dr_malformed_sha_rejected", "dr_missing_assertion_rejected",
        "dr_release_mismatch_rejected", "dr_drill_mismatch_rejected", "dr_stale_time_rejected", "dr_symlink_rejected",
        "bootstrap_successful_activation", "bootstrap_idempotent_reinstall", "bootstrap_post_activation_rollback",
        "bootstrap_lock_symlink_rejected",
        "production_unit_unchanged", "trust_root_unchanged",
        "fixed_helpers_verified",
        "helper_lock_symlink_rejected",
        "cleanup_verified",
    }
    result = runner.run("local_vm", script, fields, timeout=300).values
    if set(result.values()) != {"pass"}:
        raise RuntimeError("VM signer integration test did not pass")
    print(f"vm_signer_integration=pass checks={len(result)}")


def main() -> None:
    runner = SSHRunner()
    remote_temps: list[str] = []
    primary_error: BaseException | None = None
    try:
        run(runner, remote_temps)
    except BaseException as exc:
        primary_error = exc

    cleanup_errors: list[BaseException] = []
    for path in reversed(remote_temps):
        try:
            cleanup_remote_temp(runner, path)
        except BaseException as exc:
            cleanup_errors.append(exc)
    if primary_error is not None:
        if cleanup_errors:
            primary_error.add_note("the registered VM temporary directory could not be cleaned")
        raise primary_error.with_traceback(primary_error.__traceback__)
    if cleanup_errors:
        raise cleanup_errors[0]


if __name__ == "__main__":
    main()
