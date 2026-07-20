#!/usr/bin/env bash
set -Eeuo pipefail

gate=${1:?gate path is required}
signature=${2:?signature path is required}
private_key=/opt/sub2api-release-signer/vm-gate-ed25519.pem
[[ $(id -u) == 0 ]]
unit_lock=${SUB2API_UNIT_LOCK_PATH:-/usr/local/libexec/.sub2api-release-unit.lock}
if [[ ${SUB2API_HELPER_TEST_MODE:-false} == true ]]; then
  lock_parent=${unit_lock%/*}
  [[ $lock_parent =~ ^/opt/sub2api-deploy/release-input/[A-Za-z0-9]+([.-][A-Za-z0-9]+)*/libexec$ ]]
  [[ -d $lock_parent && ! -L $lock_parent && $(realpath -e -- "$lock_parent") == "$lock_parent" && $(stat -c '%U:%G:%a' "$lock_parent") == root:root:700 ]]
else
  [[ $unit_lock == /usr/local/libexec/.sub2api-release-unit.lock ]]
fi
[[ -f $unit_lock && ! -L $unit_lock && $(stat -c '%U:%G:%a:%h' "$unit_lock") == root:root:600:1 ]]
exec 8<>"$unit_lock"
[[ $(stat -Lc '%U:%G:%a:%h' /proc/self/fd/8) == root:root:600:1 ]]
flock -s 8
[[ $gate =~ ^/opt/sub2api-deploy/release-gates/(182|187|191|192|194|195|197)-[0-9a-f]{12}-[0-9]+-[0-9a-f]{8}/output/gate\.json$ ]]
expected_signature=${gate%/gate.json}/gate.sig
[[ $signature == "$expected_signature" ]]
[[ -f $gate && ! -L $gate && $(stat -c '%U:%G:%a' "$gate") == root:root:400 ]]
[[ -f $private_key && ! -L $private_key && $(stat -c '%U:%G:%a' "$private_key") == root:root:600 ]]
[[ ! -e $signature && ! -L $signature ]]
signature_tmp="$signature.tmp.$$"
trap 'rm -f -- "$signature_tmp"' EXIT
openssl pkeyutl -sign -inkey "$private_key" -rawin -in "$gate" -out "$signature_tmp"
chmod 400 "$signature_tmp"
chown root:root "$signature_tmp"
mv -T -- "$signature_tmp" "$signature"
trap - EXIT
