#!/usr/bin/env bash
set -Eeuo pipefail

signer_dir=${SIGNER_DIR:-/opt/sub2api-release-signer}
validator_source=${VALIDATOR_SOURCE:?VALIDATOR_SOURCE is required}
gate_signer_source=${GATE_SIGNER_SOURCE:?GATE_SIGNER_SOURCE is required}
dr_signer_source=${DR_SIGNER_SOURCE:?DR_SIGNER_SOURCE is required}
validator_expected_sha256=${VALIDATOR_SHA256:?VALIDATOR_SHA256 is required}
gate_signer_expected_sha256=${GATE_SIGNER_SHA256:?GATE_SIGNER_SHA256 is required}
dr_signer_expected_sha256=${DR_SIGNER_SHA256:?DR_SIGNER_SHA256 is required}
target_libexec_dir=${TARGET_LIBEXEC_DIR:-/usr/local/libexec}
test_mode=${SUB2API_BOOTSTRAP_TEST_MODE:-false}
test_fail_after_validator=${SUB2API_TEST_FAIL_AFTER_VALIDATOR_ACTIVATION:-false}
target_libexec_mode=755
if [[ $test_mode == true || $test_fail_after_validator == true ]]; then
  [[ $test_mode == true && $target_libexec_dir != /usr/local/libexec ]]
  target_libexec_mode=700
elif [[ $target_libexec_dir != /usr/local/libexec ]]; then
  exit 1
fi
if [[ $test_mode == true ]]; then
  test_parent=${target_libexec_dir%/libexec}
  test_name=${test_parent##*/}
  [[ $test_name =~ ^[A-Za-z0-9]+([.-][A-Za-z0-9]+)*$ ]]
  [[ $test_parent == /opt/sub2api-deploy/release-input/$test_name ]]
  [[ $(realpath -e -- "$test_parent") == "$test_parent" ]]
  [[ -d $test_parent && ! -L $test_parent && $(stat -c '%U:%G:%a' "$test_parent") == root:root:700 ]]
  if [[ -e $target_libexec_dir ]]; then
    [[ $(realpath -e -- "$target_libexec_dir") == "$target_libexec_dir" ]]
    [[ -d $target_libexec_dir && ! -L $target_libexec_dir && $(stat -c '%U:%G:%a' "$target_libexec_dir") == "root:root:$target_libexec_mode" ]]
  fi
fi
validator_target="$target_libexec_dir/sub2api-vm-validate"
gate_signer_target="$target_libexec_dir/sub2api-sign-gate"
dr_signer_target="$target_libexec_dir/sub2api-sign-dr-evidence"
private_key="$signer_dir/vm-gate-ed25519.pem"
public_key="$signer_dir/vm-gate-ed25519.pub"
[[ $(id -u) == 0 ]]
[[ -f $validator_source && ! -L $validator_source ]]
[[ -f $gate_signer_source && ! -L $gate_signer_source ]]
[[ -f $dr_signer_source && ! -L $dr_signer_source ]]
[[ $validator_expected_sha256 =~ ^[0-9a-f]{64}$ && $(sha256sum "$validator_source" | awk '{print $1}') == "$validator_expected_sha256" ]]
[[ $gate_signer_expected_sha256 =~ ^[0-9a-f]{64}$ && $(sha256sum "$gate_signer_source" | awk '{print $1}') == "$gate_signer_expected_sha256" ]]
[[ $dr_signer_expected_sha256 =~ ^[0-9a-f]{64}$ && $(sha256sum "$dr_signer_source" | awk '{print $1}') == "$dr_signer_expected_sha256" ]]
install -d -m 700 "$signer_dir"
install -d -m "$target_libexec_mode" "$target_libexec_dir"
if [[ ${REQUIRE_EXISTING_SIGNER_KEYS:-false} == true ]]; then
  [[ -f $private_key && ! -L $private_key && -f $public_key && ! -L $public_key ]]
  [[ $(stat -c '%U:%G:%a' "$private_key") == root:root:600 ]]
  [[ $(stat -c '%U:%G:%a' "$public_key") == root:root:644 ]]
elif [[ ! -e $private_key && ! -e $public_key ]]; then
  umask 077
  openssl genpkey -algorithm ED25519 -out "$private_key"
  openssl pkey -in "$private_key" -pubout -out "$public_key"
fi
[[ -f $private_key && ! -L $private_key && -f $public_key && ! -L $public_key ]]
[[ $(stat -c '%U:%G' "$private_key") == root:root && $(stat -c '%U:%G' "$public_key") == root:root ]]
chmod 600 "$private_key"
chmod 644 "$public_key"
openssl pkey -pubin -in "$public_key" -noout
derived_public=$(mktemp "$signer_dir/.derived-public.XXXXXX")
openssl pkey -in "$private_key" -pubout -out "$derived_public"
cmp -s "$derived_public" "$public_key"
rm -f -- "$derived_public"

activation_dir=$(mktemp -d "$target_libexec_dir/.sub2api-release-unit.XXXXXX")
activation_started=false
activation_complete=false
selftest_suffix=$(openssl rand -hex 4)
selftest_now=$(date -u +%Y-%m-%dT%H:%M:%SZ)
selftest_releases=()
cleanup() {
  if [[ $activation_started == true && $activation_complete != true ]]; then
    for asset in validator gate-signer dr-signer; do
      case $asset in
        validator) target=$validator_target ;;
        gate-signer) target=$gate_signer_target ;;
        dr-signer) target=$dr_signer_target ;;
      esac
      if [[ -f $activation_dir/previous-$asset ]]; then
        install -o root -g root -m 700 "$activation_dir/previous-$asset" "$target.rollback"
        mv -T -- "$target.rollback" "$target"
      else
        rm -f -- "$target"
      fi
    done
  fi
  rm -f -- "$validator_target.new" "$gate_signer_target.new" "$dr_signer_target.new"
  rm -rf -- "$activation_dir"
  for selftest_release in "${selftest_releases[@]}"; do
    rm -rf -- "/opt/sub2api-deploy/release-gates/$selftest_release" "/opt/sub2api-deploy/dr-evidence/$selftest_release"
  done
}
trap cleanup EXIT
install -o root -g root -m 700 "$validator_source" "$activation_dir/sub2api-vm-validate"
install -o root -g root -m 700 "$gate_signer_source" "$activation_dir/sub2api-sign-gate"
install -o root -g root -m 700 "$dr_signer_source" "$activation_dir/sub2api-sign-dr-evidence"
bash -n "$activation_dir/sub2api-vm-validate" "$activation_dir/sub2api-sign-gate" "$activation_dir/sub2api-sign-dr-evidence"
if [[ -e /opt/sub2api-deploy/dr-evidence ]]; then
  [[ -d /opt/sub2api-deploy/dr-evidence && ! -L /opt/sub2api-deploy/dr-evidence && $(stat -c '%U:%G:%a' /opt/sub2api-deploy/dr-evidence) == root:root:700 ]]
else
  install -d -o root -g root -m 700 /opt/sub2api-deploy/dr-evidence
fi
for selftest_profile in 195 199; do
  selftest_release="$selftest_profile-000000000000-0-$selftest_suffix"
  selftest_drill="dr-$selftest_profile-$(tr -d ':-' <<<"$selftest_now")"
  selftest_gate_dir="/opt/sub2api-deploy/release-gates/$selftest_release/output"
  selftest_dr_dir="/opt/sub2api-deploy/dr-evidence/$selftest_release/$selftest_drill"
  selftest_releases+=("$selftest_release")
  install -d -o root -g root -m 700 "$selftest_gate_dir" "/opt/sub2api-deploy/dr-evidence/$selftest_release" "$selftest_dr_dir"
  printf '{"selftest":true}\n' > "$selftest_gate_dir/gate.json"
  chmod 400 "$selftest_gate_dir/gate.json"
  "$activation_dir/sub2api-sign-gate" "$selftest_gate_dir/gate.json" "$selftest_gate_dir/gate.sig"
  openssl pkeyutl -verify -pubin -inkey "$public_key" -rawin -in "$selftest_gate_dir/gate.json" -sigfile "$selftest_gate_dir/gate.sig" >/dev/null
  jq -n --arg release_id "$selftest_release" --arg drill_id "$selftest_drill" --arg now "$selftest_now" '{schema:1,release_id:$release_id,drill_id:$drill_id,created_at:$now,completed_at:$now,artifact_sha256:("a"*64),candidate_bundle_sha256:("b"*64),candidate_archive_sha256:("c"*64),candidate_image_id:("sha256:"+("d"*64)),migration_checksum:("e"*64),image_load_id_check:"pass",config_manifest_check:"pass",postgres_restore:"pass",redis_restore:"pass",redis_ttl_reconciliation:"pass",counts_and_migrations:"pass",temporary_material_destroyed:"pass",redis_backup_dbsize:576,redis_backup_expiring_keys:101,redis_restored_dbsize:526,redis_restored_expiring_keys:51}' > "$selftest_dr_dir/evidence.json"
  chmod 400 "$selftest_dr_dir/evidence.json"
  "$activation_dir/sub2api-sign-dr-evidence" "$selftest_dr_dir/evidence.json" "$selftest_dr_dir/evidence.sig"
  openssl pkeyutl -verify -pubin -inkey "$public_key" -rawin -in "$selftest_dr_dir/evidence.json" -sigfile "$selftest_dr_dir/evidence.sig" >/dev/null
done
unit_lock="$target_libexec_dir/.sub2api-release-unit.lock"
if [[ -e $unit_lock || -L $unit_lock ]]; then
  [[ -f $unit_lock && ! -L $unit_lock && $(stat -c '%U:%G:%a:%h' "$unit_lock") == root:root:600:1 ]]
else
  (set -o noclobber; umask 077; : > "$unit_lock") || {
    [[ -f $unit_lock && ! -L $unit_lock && $(stat -c '%U:%G:%a:%h' "$unit_lock") == root:root:600:1 ]]
  }
fi
exec 9<>"$unit_lock"
[[ $(stat -Lc '%U:%G:%a:%h' /proc/self/fd/9) == root:root:600:1 ]]
flock 9
for asset in validator gate-signer dr-signer; do
  case $asset in
    validator) target=$validator_target ;;
    gate-signer) target=$gate_signer_target ;;
    dr-signer) target=$dr_signer_target ;;
  esac
  if [[ -e $target ]]; then
    [[ -f $target && ! -L $target && $(stat -c '%U:%G:%a' "$target") == root:root:700 ]]
    install -o root -g root -m 700 "$target" "$activation_dir/previous-$asset"
  fi
done
install -o root -g root -m 700 "$activation_dir/sub2api-vm-validate" "$validator_target.new"
install -o root -g root -m 700 "$activation_dir/sub2api-sign-gate" "$gate_signer_target.new"
install -o root -g root -m 700 "$activation_dir/sub2api-sign-dr-evidence" "$dr_signer_target.new"
activation_started=true
mv -T -- "$validator_target.new" "$validator_target"
if [[ $test_mode == true && $test_fail_after_validator == true ]]; then
  false
fi
mv -T -- "$gate_signer_target.new" "$gate_signer_target"
mv -T -- "$dr_signer_target.new" "$dr_signer_target"
[[ $(sha256sum "$validator_target" | awk '{print $1}') == "$validator_expected_sha256" ]]
[[ $(sha256sum "$gate_signer_target" | awk '{print $1}') == "$gate_signer_expected_sha256" ]]
[[ $(sha256sum "$dr_signer_target" | awk '{print $1}') == "$dr_signer_expected_sha256" ]]
activation_complete=true
printf 'signer_status=ready\n'
printf 'public_key_sha256=%s\n' "$(sha256sum "$public_key" | awk '{print $1}')"
printf 'validator_sha256=%s\n' "$(sha256sum "$validator_target" | awk '{print $1}')"
printf 'gate_signer_sha256=%s\n' "$(sha256sum "$gate_signer_target" | awk '{print $1}')"
printf 'dr_signer_sha256=%s\n' "$(sha256sum "$dr_signer_target" | awk '{print $1}')"
