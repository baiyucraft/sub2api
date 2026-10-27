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
  {
    required_query_1=$(realpath -e -- "$test_root") || exit $?
    required_query_2=$(stat -c '%U:%G:%a' "$test_root") || exit $?
    [[ -d $test_root && ! -L $test_root && ${required_query_1} == "$test_root" && ${required_query_2} == root:root:700 ]]
  }
  target_libexec_dir="$test_root/libexec"
  trust_dir="$test_root/trust"
else
  target_libexec_dir=/usr/local/libexec
  trust_dir=/opt/sub2api-dr-trust
fi
target_verifier="$target_libexec_dir/sub2api-verify-dr-evidence"
target_promoter="$target_libexec_dir/sub2api-promote-dr-baseline"
target_trust="$trust_dir/vm-gate-ed25519.pub"
{
  required_query_3=$(id -u)
  [[ ${required_query_3} == 0 ]]
}
for value in "$verifier_expected_sha256" "$promoter_expected_sha256" "$trust_expected_sha256"; do [[ $value =~ ^[0-9a-f]{64}$ ]]; done
for source in "$verifier_source" "$promoter_source" "$trust_source" "$signed_test_source" "$signed_test_signature"; do
  [[ -f $source && ! -L $source ]]
done
{
  required_query_4=$(sha256sum "$verifier_source" | awk '{print $1}')
  [[ ${required_query_4} == "$verifier_expected_sha256" ]]
}
{
  required_query_5=$(sha256sum "$promoter_source" | awk '{print $1}')
  [[ ${required_query_5} == "$promoter_expected_sha256" ]]
}
{
  required_query_6=$(sha256sum "$trust_source" | awk '{print $1}')
  [[ ${required_query_6} == "$trust_expected_sha256" ]]
}
bash -n "$promoter_source"
promoter_constant() {
  local key=$1
  awk -F= -v key="$key" '$1 == key {sub(/^[^=]*=/, ""); print; found++} END {exit found == 1 ? 0 : 1}' "$promoter_source"
}
{
  required_query_7=$(promoter_constant verifier_sha256)
  [[ ${required_query_7} == "$verifier_expected_sha256" ]]
}
{
  required_query_8=$(promoter_constant trust_sha256)
  [[ ${required_query_8} == "$trust_expected_sha256" ]]
}

if [[ -e $target_libexec_dir || -L $target_libexec_dir ]]; then
  {
    required_query_9=$(realpath -e -- "$target_libexec_dir") || exit $?
    [[ -d $target_libexec_dir && ! -L $target_libexec_dir && ${required_query_9} == "$target_libexec_dir" ]]
  }
  if [[ $test_mode == true ]]; then
    {
      required_query_10=$(stat -c '%U:%G:%a' "$target_libexec_dir") || exit $?
      [[ ${required_query_10} == root:root:700 ]]
    }
  else
    {
      required_query_11=$(stat -c '%U:%G:%a' "$target_libexec_dir") || exit $?
      [[ ${required_query_11} == root:root:755 ]]
    }
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
  releases_root="$test_root/releases"
  install -d -o root -g root -m 700 "$releases_root"
else
  releases_root=/srv/sub2api-backups/releases
  {
    required_query_12=$(realpath -e -- "$releases_root") || exit $?
    required_query_13=$(stat -c '%U:%G:%a' "$releases_root") || exit $?
    [[ -d $releases_root && ! -L $releases_root && ${required_query_12} == "$releases_root" && ${required_query_13} == root:root:700 ]]
  }
fi
for profile in 195 199 202 206 207 208 209 210 212 213 215 232 233 234 235 236 237 238 239 240 241 242 243 244 245 246 247 248 249 250 251 252 253 254 255 256 257 258 259 260 261 262 263 264 265 266 267 268 269 270 271 272 273 274 275 276; do
  promotion_root="$releases_root/$profile"
  if [[ -e $promotion_root || -L $promotion_root ]]; then
    {
      required_query_14=$(realpath -e -- "$promotion_root") || exit $?
      required_query_15=$(stat -c '%U:%G:%a' "$promotion_root") || exit $?
      [[ -d $promotion_root && ! -L $promotion_root && ${required_query_14} == "$promotion_root" && ${required_query_15} == root:root:700 ]]
    }
  else
    install -d -o root -g root -m 700 "$promotion_root"
  fi
  for directory in "$promotion_root/promotion-input" "$promotion_root/verified-bundles"; do
    if [[ -e $directory || -L $directory ]]; then
      {
        required_query_16=$(realpath -e -- "$directory") || exit $?
        required_query_17=$(stat -c '%U:%G:%a' "$directory") || exit $?
        [[ -d $directory && ! -L $directory && ${required_query_16} == "$directory" && ${required_query_17} == root:root:700 ]]
      }
    else
      install -d -o root -g root -m 700 "$directory"
    fi
  done
done

asset_lock="$target_libexec_dir/.sub2api-dr-assets.lock"
if [[ -e $asset_lock || -L $asset_lock ]]; then
  {
    required_query_18=$(stat -c '%U:%G:%a:%h' "$asset_lock") || exit $?
    [[ -f $asset_lock && ! -L $asset_lock && ${required_query_18} == root:root:600:1 ]]
  }
else
  (set -o noclobber; umask 077; : > "$asset_lock") || {
    {
      required_query_19=$(stat -c '%U:%G:%a:%h' "$asset_lock") || exit $?
      [[ -f $asset_lock && ! -L $asset_lock && ${required_query_19} == root:root:600:1 ]]
    }
  }
fi
exec 9<>"$asset_lock"
{
  required_query_20=$(stat -Lc '%U:%G:%a:%h' /proc/self/fd/9)
  [[ ${required_query_20} == root:root:600:1 ]]
}
flock 9
if [[ -e $trust_dir ]]; then
  {
    required_query_21=$(realpath -e -- "$trust_dir") || exit $?
    required_query_22=$(stat -c '%U:%G:%a' "$trust_dir") || exit $?
    [[ -d $trust_dir && ! -L $trust_dir && ${required_query_21} == "$trust_dir" && ${required_query_22} == root:root:755 ]]
  }
else
  install -d -o root -g root -m 755 "$trust_dir"
fi
if [[ -e $target_trust ]]; then
  {
    required_query_23=$(stat -c '%U:%G:%a' "$target_trust") || exit $?
    [[ -f $target_trust && ! -L $target_trust && ${required_query_23} == root:root:644 ]]
  }
  {
    required_query_24=$(sha256sum "$target_trust" | awk '{print $1}') || exit $?
    [[ ${required_query_24} == "$trust_expected_sha256" ]]
  }
else
  install -o root -g root -m 644 "$activation_dir/trust.pub" "$target_trust.new"
  mv -T -- "$target_trust.new" "$target_trust"
  trust_created=true
fi

for asset in verifier promoter; do
  case $asset in verifier) target=$target_verifier ;; promoter) target=$target_promoter ;; esac
  if [[ -e $target ]]; then
    {
      required_query_25=$(stat -c '%U:%G:%a' "$target") || exit $?
      [[ -f $target && ! -L $target && ${required_query_25} == root:root:700 ]]
    }
    install -o root -g root -m 700 "$target" "$activation_dir/previous-$asset"
  fi
done
install -o root -g root -m 700 "$activation_dir/verifier" "$target_verifier.new"
install -o root -g root -m 700 "$activation_dir/promoter" "$target_promoter.new"
activation_started=true
mv -T -- "$target_verifier.new" "$target_verifier"
if [[ $test_mode == true && $test_fail_after_verifier == true ]]; then false; fi
mv -T -- "$target_promoter.new" "$target_promoter"
{
  required_query_26=$(sha256sum "$target_verifier" | awk '{print $1}')
  [[ ${required_query_26} == "$verifier_expected_sha256" ]]
}
{
  required_query_27=$(sha256sum "$target_promoter" | awk '{print $1}')
  [[ ${required_query_27} == "$promoter_expected_sha256" ]]
}
"$target_verifier" "$target_trust" "$activation_dir/valid.json" "$activation_dir/valid.sig" >/dev/null
activation_complete=true
printf 'backup_dr_assets_status=ready\n'
{
  required_query_28=$(sha256sum "$target_verifier" | awk '{print $1}')
  printf 'verifier_sha256=%s\n' "${required_query_28}"
}
{
  required_query_29=$(sha256sum "$target_promoter" | awk '{print $1}')
  printf 'promoter_sha256=%s\n' "${required_query_29}"
}
{
  required_query_30=$(sha256sum "$target_trust" | awk '{print $1}')
  printf 'trust_sha256=%s\n' "${required_query_30}"
}
