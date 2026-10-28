#!/usr/bin/env bash
set -Eeuo pipefail

gate=${1:?gate path is required}
signature=${2:?signature path is required}
private_key=/opt/sub2api-release-signer/vm-gate-ed25519.pem
# Legacy Gate v1 profile allowlist retained for audit/recovery assets:
# (182|187|191|192|194|195|197|198|199|202|206|207|208|209|210|212|213|215|232|233|234|235|236|237|238|239|240|241|242|243|244|245|246|247|248|249|250|251|252|253|254|255)
{
  required_query_1=$(id -u)
  [[ ${required_query_1} == 0 ]]
}
unit_lock=${SUB2API_UNIT_LOCK_PATH:-/usr/local/libexec/.sub2api-release-unit.lock}
if [[ ${SUB2API_HELPER_TEST_MODE:-false} == true ]]; then
  lock_parent=${unit_lock%/*}
  [[ $lock_parent =~ ^/opt/sub2api-deploy/release-input/[A-Za-z0-9]+([.-][A-Za-z0-9]+)*/libexec$ ]]
  {
    required_query_2=$(realpath -e -- "$lock_parent") || exit $?
    required_query_3=$(stat -c '%U:%G:%a' "$lock_parent") || exit $?
    [[ -d $lock_parent && ! -L $lock_parent && ${required_query_2} == "$lock_parent" && ${required_query_3} == root:root:700 ]]
  }
else
  [[ $unit_lock == /usr/local/libexec/.sub2api-release-unit.lock ]]
fi
{
  required_query_4=$(stat -c '%U:%G:%a:%h' "$unit_lock")
  [[ -f $unit_lock && ! -L $unit_lock && ${required_query_4} == root:root:600:1 ]]
}
exec 8<>"$unit_lock"
{
  required_query_5=$(stat -Lc '%U:%G:%a:%h' /proc/self/fd/8)
  [[ ${required_query_5} == root:root:600:1 ]]
}
flock -s 8
[[ $gate =~ ^/opt/sub2api-deploy/release-gates/(182|187|191|192|194|195|197|198|199|202|206|207|208|209|210|212|213|215|232|233|234|235|236|237|238|239|240|241|242|243|244|245|246|247|248|249|250|251|252|253|254|255|256|257|258|259|260|261|262|263|264|265|266|267|268|269|270|271|272|273|274|275|276|277|278|279|280|281)-[0-9a-f]{12}-[0-9]+-[0-9a-f]{8}/output/gate\.json$ ]]
gate_release_dir=${gate%/output/gate.json}
gate_release_id=${gate_release_dir##*/}
gate_profile=${gate_release_id%%-*}
{
  required_query_6=$(jq -er '.manifest.release_id' "$gate")
  [[ ${required_query_6} == "$gate_release_id" ]]
}
{
  required_query_7=$(jq -er '.manifest.profile' "$gate")
  [[ ${required_query_7} == "$gate_profile" ]]
}
gate_schema=$(jq -er '.manifest.schema' "$gate")
if [[ $gate_profile == 242 || $gate_profile == 243 || $gate_profile == 244 || $gate_profile == 245 || $gate_profile == 246 || $gate_profile == 247 || $gate_profile == 248 || $gate_profile == 249 || $gate_profile == 250 || $gate_profile == 251 || $gate_profile == 252 || $gate_profile == 253 || $gate_profile == 254 || $gate_profile == 255 || $gate_profile == 256 || $gate_profile == 257 || $gate_profile == 258 || $gate_profile == 259 || $gate_profile == 260 || $gate_profile == 261 || $gate_profile == 262 || $gate_profile == 263 || $gate_profile == 264 || $gate_profile == 265 || $gate_profile == 266 || $gate_profile == 267 || $gate_profile == 268 || $gate_profile == 269 || $gate_profile == 270 || $gate_profile == 271 || $gate_profile == 272 || $gate_profile == 273 || $gate_profile == 274 || $gate_profile == 275 || $gate_profile == 276 || $gate_profile == 277 || $gate_profile == 278 || $gate_profile == 279 || $gate_profile == 280 || $gate_profile == 281 ]]; then
  [[ $gate_schema == 2 ]]
  {
    required_query_8=$(jq -er '.gate_version' "$gate") || exit $?
    [[ ${required_query_8} == 2 ]]
  }
  {
    required_query_9=$(jq -er '.profile_id' "$gate") || exit $?
    [[ ${required_query_9} == "$gate_profile" ]]
  }
else
  [[ $gate_schema == 1 ]]
fi
expected_signature=${gate%/gate.json}/gate.sig
[[ $signature == "$expected_signature" ]]
{
  required_query_10=$(stat -c '%U:%G:%a' "$gate")
  [[ -f $gate && ! -L $gate && ${required_query_10} == root:root:400 ]]
}
{
  required_query_11=$(stat -c '%U:%G:%a' "$private_key")
  [[ -f $private_key && ! -L $private_key && ${required_query_11} == root:root:600 ]]
}
[[ ! -e $signature && ! -L $signature ]]
signature_tmp="$signature.tmp.$$"
trap 'rm -f -- "$signature_tmp"' EXIT
openssl pkeyutl -sign -inkey "$private_key" -rawin -in "$gate" -out "$signature_tmp"
chmod 400 "$signature_tmp"
chown root:root "$signature_tmp"
mv -T -- "$signature_tmp" "$signature"
trap - EXIT
