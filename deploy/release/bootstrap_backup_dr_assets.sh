#!/usr/bin/env bash
set -Eeuo pipefail
umask 077

verifier_source=${VERIFIER_SOURCE:?VERIFIER_SOURCE is required}
promoter_source=${PROMOTER_SOURCE:?PROMOTER_SOURCE is required}
trust_source=${TRUST_SOURCE:?TRUST_SOURCE is required}
signed_test_source=${SIGNED_TEST_SOURCE:?SIGNED_TEST_SOURCE is required}
signed_test_signature=${SIGNED_TEST_SIGNATURE:?SIGNED_TEST_SIGNATURE is required}
verifier_expected_sha256=${VERIFIER_SHA256:?VERIFIER_SHA256 is required}
promoter_expected_sha256=${PROMOTER_SHA256:?PROMOTER_SHA256 is required}
trust_expected_sha256=${TRUST_SHA256:?TRUST_SHA256 is required}
test_mode=${SUB2API_BACKUP_BOOTSTRAP_TEST_MODE:-false}
test_fail_after_verifier=${SUB2API_TEST_FAIL_AFTER_VERIFIER_ACTIVATION:-false}
test_root=${BACKUP_TEST_ROOT:-}
if [[ $test_mode == true || $test_fail_after_verifier == true || -n $test_root ]]; then
  [[ $test_mode == true && -n $test_root ]]
  test_name=${test_root##*/}
  [[ $test_name =~ ^dr-(assets|promotion)-test\.[A-Za-z0-9]{8}$ ]]
  [[ $test_root == /srv/sub2api-backups/$test_name ]]
  [[ -d $test_root && ! -L $test_root && $(realpath -e -- "$test_root") == "$test_root" && $(stat -c '%U:%G:%a' "$test_root") == root:root:700 ]]
  target_libexec_dir="$test_root/libexec"
  trust_dir="$test_root/trust"
else
  target_libexec_dir=/usr/local/libexec
  trust_dir=/opt/sub2api-dr-trust
fi
target_verifier="$target_libexec_dir/sub2api-verify-dr-evidence"
target_promoter="$target_libexec_dir/sub2api-promote-dr-baseline"
target_trust="$trust_dir/vm-gate-ed25519.pub"
[[ $(id -u) == 0 ]]
for value in "$verifier_expected_sha256" "$promoter_expected_sha256" "$trust_expected_sha256"; do [[ $value =~ ^[0-9a-f]{64}$ ]]; done
for source in "$verifier_source" "$promoter_source" "$trust_source" "$signed_test_source" "$signed_test_signature"; do
  [[ -f $source && ! -L $source ]]
done
[[ $(sha256sum "$verifier_source" | awk '{print $1}') == "$verifier_expected_sha256" ]]
[[ $(sha256sum "$promoter_source" | awk '{print $1}') == "$promoter_expected_sha256" ]]
[[ $(sha256sum "$trust_source" | awk '{print $1}') == "$trust_expected_sha256" ]]
bash -n "$promoter_source"
promoter_constant() {
  local key=$1
  awk -F= -v key="$key" '$1 == key {sub(/^[^=]*=/, ""); print; found++} END {exit found == 1 ? 0 : 1}' "$promoter_source"
}
[[ $(promoter_constant verifier_sha256) == "$verifier_expected_sha256" ]]
[[ $(promoter_constant trust_sha256) == "$trust_expected_sha256" ]]

if [[ -e $target_libexec_dir || -L $target_libexec_dir ]]; then
  [[ -d $target_libexec_dir && ! -L $target_libexec_dir && $(realpath -e -- "$target_libexec_dir") == "$target_libexec_dir" ]]
  if [[ $test_mode == true ]]; then
    [[ $(stat -c '%U:%G:%a' "$target_libexec_dir") == root:root:700 ]]
  else
    [[ $(stat -c '%U:%G:%a' "$target_libexec_dir") == root:root:755 ]]
  fi
else
  if [[ $test_mode == true ]]; then
    install -d -o root -g root -m 700 "$target_libexec_dir"
  else
    install -d -o root -g root -m 755 "$target_libexec_dir"
  fi
fi
activation_dir=$(mktemp -d "$target_libexec_dir/.sub2api-dr-assets.XXXXXXXX")
activation_started=false
activation_complete=false
trust_created=false
cleanup() {
  if [[ $activation_started == true && $activation_complete != true ]]; then
    for asset in verifier promoter; do
      case $asset in verifier) target=$target_verifier ;; promoter) target=$target_promoter ;; esac
      if [[ -f $activation_dir/previous-$asset ]]; then
        install -o root -g root -m 700 "$activation_dir/previous-$asset" "$target.rollback"
        mv -T -- "$target.rollback" "$target"
      else
        rm -f -- "$target"
      fi
    done
  fi
  if [[ $trust_created == true && $activation_complete != true ]]; then rm -f -- "$target_trust"; fi
  rm -f -- "$target_verifier.new" "$target_promoter.new"
  rm -rf -- "$activation_dir"
}
trap cleanup EXIT
install -o root -g root -m 700 "$verifier_source" "$activation_dir/verifier"
install -o root -g root -m 700 "$promoter_source" "$activation_dir/promoter"
install -o root -g root -m 400 "$trust_source" "$activation_dir/trust.pub"
install -o root -g root -m 400 "$signed_test_source" "$activation_dir/valid.json"
install -o root -g root -m 400 "$signed_test_signature" "$activation_dir/valid.sig"
"$activation_dir/verifier" "$activation_dir/trust.pub" "$activation_dir/valid.json" "$activation_dir/valid.sig" >/dev/null
install -o root -g root -m 400 "$activation_dir/valid.json" "$activation_dir/tampered.json"
printf '\n' >> "$activation_dir/tampered.json"
if "$activation_dir/verifier" "$activation_dir/trust.pub" "$activation_dir/tampered.json" "$activation_dir/valid.sig" >/dev/null 2>&1; then exit 1; fi
install -o root -g root -m 400 "$activation_dir/valid.sig" "$activation_dir/tampered.sig"
printf X | dd of="$activation_dir/tampered.sig" bs=1 seek=0 conv=notrunc status=none
if "$activation_dir/verifier" "$activation_dir/trust.pub" "$activation_dir/valid.json" "$activation_dir/tampered.sig" >/dev/null 2>&1; then exit 1; fi
ln -s "$activation_dir/valid.json" "$activation_dir/symlink.json"
if "$activation_dir/verifier" "$activation_dir/trust.pub" "$activation_dir/symlink.json" "$activation_dir/valid.sig" >/dev/null 2>&1; then exit 1; fi
dd if=/dev/zero of="$activation_dir/oversize.json" bs=1048577 count=1 status=none
if "$activation_dir/verifier" "$activation_dir/trust.pub" "$activation_dir/oversize.json" "$activation_dir/valid.sig" >/dev/null 2>&1; then exit 1; fi

for command_name in awk cmp dd find flock install jq ln mv realpath sed sha256sum sort stat sync wc; do command -v "$command_name" >/dev/null; done
if [[ $test_mode == true ]]; then
  install -d -o root -g root -m 700 "$test_root/releases" "$test_root/releases/195"
  promotion_root="$test_root/releases/195"
else
  promotion_root=/srv/sub2api-backups/releases/195
  [[ -d $promotion_root && ! -L $promotion_root && $(realpath -e -- "$promotion_root") == "$promotion_root" && $(stat -c '%U:%G:%a' "$promotion_root") == root:root:700 ]]
fi
for directory in "$promotion_root/promotion-input" "$promotion_root/verified-bundles"; do
  if [[ -e $directory ]]; then
    [[ -d $directory && ! -L $directory && $(realpath -e -- "$directory") == "$directory" && $(stat -c '%U:%G:%a' "$directory") == root:root:700 ]]
  else
    install -d -o root -g root -m 700 "$directory"
  fi
done

asset_lock="$target_libexec_dir/.sub2api-dr-assets.lock"
if [[ -e $asset_lock || -L $asset_lock ]]; then
  [[ -f $asset_lock && ! -L $asset_lock && $(stat -c '%U:%G:%a:%h' "$asset_lock") == root:root:600:1 ]]
else
  (set -o noclobber; umask 077; : > "$asset_lock") || {
    [[ -f $asset_lock && ! -L $asset_lock && $(stat -c '%U:%G:%a:%h' "$asset_lock") == root:root:600:1 ]]
  }
fi
exec 9<>"$asset_lock"
[[ $(stat -Lc '%U:%G:%a:%h' /proc/self/fd/9) == root:root:600:1 ]]
flock 9
if [[ -e $trust_dir ]]; then
  [[ -d $trust_dir && ! -L $trust_dir && $(realpath -e -- "$trust_dir") == "$trust_dir" && $(stat -c '%U:%G:%a' "$trust_dir") == root:root:755 ]]
else
  install -d -o root -g root -m 755 "$trust_dir"
fi
if [[ -e $target_trust ]]; then
  [[ -f $target_trust && ! -L $target_trust && $(stat -c '%U:%G:%a' "$target_trust") == root:root:644 ]]
  [[ $(sha256sum "$target_trust" | awk '{print $1}') == "$trust_expected_sha256" ]]
else
  install -o root -g root -m 644 "$activation_dir/trust.pub" "$target_trust.new"
  mv -T -- "$target_trust.new" "$target_trust"
  trust_created=true
fi

for asset in verifier promoter; do
  case $asset in verifier) target=$target_verifier ;; promoter) target=$target_promoter ;; esac
  if [[ -e $target ]]; then
    [[ -f $target && ! -L $target && $(stat -c '%U:%G:%a' "$target") == root:root:700 ]]
    install -o root -g root -m 700 "$target" "$activation_dir/previous-$asset"
  fi
done
install -o root -g root -m 700 "$activation_dir/verifier" "$target_verifier.new"
install -o root -g root -m 700 "$activation_dir/promoter" "$target_promoter.new"
activation_started=true
mv -T -- "$target_verifier.new" "$target_verifier"
if [[ $test_mode == true && $test_fail_after_verifier == true ]]; then false; fi
mv -T -- "$target_promoter.new" "$target_promoter"
[[ $(sha256sum "$target_verifier" | awk '{print $1}') == "$verifier_expected_sha256" ]]
[[ $(sha256sum "$target_promoter" | awk '{print $1}') == "$promoter_expected_sha256" ]]
"$target_verifier" "$target_trust" "$activation_dir/valid.json" "$activation_dir/valid.sig" >/dev/null
activation_complete=true
printf 'backup_dr_assets_status=ready\n'
printf 'verifier_sha256=%s\n' "$(sha256sum "$target_verifier" | awk '{print $1}')"
printf 'promoter_sha256=%s\n' "$(sha256sum "$target_promoter" | awk '{print $1}')"
printf 'trust_sha256=%s\n' "$(sha256sum "$target_trust" | awk '{print $1}')"
