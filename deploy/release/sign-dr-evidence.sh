#!/usr/bin/env bash
set -Eeuo pipefail

evidence=${1:?evidence path is required}
signature=${2:?signature path is required}
private_key=/opt/sub2api-release-signer/vm-gate-ed25519.pem
evidence_root=/opt/sub2api-deploy/dr-evidence
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
[[ $evidence =~ ^$evidence_root/(195-[0-9a-f]{12}-[0-9]+-[0-9a-f]{8})/(dr-195-[0-9]{8}T[0-9]{6}Z)/evidence\.json$ ]]
path_release_id=${BASH_REMATCH[1]}
path_drill_id=${BASH_REMATCH[2]}
expected_signature=${evidence%/evidence.json}/evidence.sig
[[ $signature == "$expected_signature" ]]
[[ $(realpath -e -- "$evidence") == "$evidence" ]]
[[ -d $evidence_root && ! -L $evidence_root && $(stat -c '%U:%G:%a' "$evidence_root") == root:root:700 ]]
release_dir="$evidence_root/$path_release_id"
drill_dir="$release_dir/$path_drill_id"
[[ -d $release_dir && ! -L $release_dir && $(stat -c '%U:%G:%a' "$release_dir") == root:root:700 ]]
[[ -d $drill_dir && ! -L $drill_dir && $(stat -c '%U:%G:%a' "$drill_dir") == root:root:700 ]]
[[ -f $evidence && ! -L $evidence && $(stat -c '%U:%G:%a' "$evidence") == root:root:400 ]]
[[ -f $private_key && ! -L $private_key && $(stat -c '%U:%G:%a' "$private_key") == root:root:600 ]]
[[ ! -e $signature && ! -L $signature ]]

jq -e \
  --arg release_id "$path_release_id" \
  --arg drill_id "$path_drill_id" '
    type == "object" and
    (keys | sort) == ([
      "artifact_sha256",
      "candidate_archive_sha256",
      "candidate_bundle_sha256",
      "candidate_image_id",
      "completed_at",
      "config_manifest_check",
      "counts_and_migrations",
      "created_at",
      "drill_id",
      "image_load_id_check",
      "migration_checksum",
      "postgres_restore",
      "redis_backup_dbsize",
      "redis_backup_expiring_keys",
      "redis_restore",
      "redis_restored_dbsize",
      "redis_restored_expiring_keys",
      "redis_ttl_reconciliation",
      "release_id",
      "schema",
      "temporary_material_destroyed"
    ] | sort) and
    .schema == 1 and
    .release_id == $release_id and
    .drill_id == $drill_id and
    (.created_at | gsub("[-:]"; "")) == ($drill_id | ltrimstr("dr-195-")) and
    (.artifact_sha256 | type == "string" and test("^[0-9a-f]{64}$")) and
    (.candidate_bundle_sha256 | type == "string" and test("^[0-9a-f]{64}$")) and
    (.candidate_archive_sha256 | type == "string" and test("^[0-9a-f]{64}$")) and
    (.candidate_image_id | type == "string" and test("^sha256:[0-9a-f]{64}$")) and
    (.migration_checksum | type == "string" and test("^[0-9a-f]{64}$")) and
    (.created_at | type == "string" and test("^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$")) and
    (.completed_at | type == "string" and test("^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$")) and
    ((.created_at | fromdateiso8601) as $created |
      (.completed_at | fromdateiso8601) as $completed |
      $created <= $completed and $completed >= (now - 86400) and $completed <= (now + 300)) and
    .image_load_id_check == "pass" and
    .config_manifest_check == "pass" and
    .postgres_restore == "pass" and
    .redis_restore == "pass" and
    .redis_ttl_reconciliation == "pass" and
    .counts_and_migrations == "pass" and
    .temporary_material_destroyed == "pass" and
    ([.redis_backup_dbsize, .redis_backup_expiring_keys, .redis_restored_dbsize, .redis_restored_expiring_keys] | all(type == "number" and . >= 0 and floor == .)) and
    .redis_backup_expiring_keys <= .redis_backup_dbsize and
    .redis_restored_expiring_keys <= .redis_restored_dbsize and
    .redis_backup_dbsize >= .redis_restored_dbsize and
    .redis_backup_expiring_keys >= .redis_restored_expiring_keys and
    (.redis_backup_dbsize - .redis_restored_dbsize) == (.redis_backup_expiring_keys - .redis_restored_expiring_keys)
  ' "$evidence" >/dev/null

signature_tmp="$signature.tmp.$$"
trap 'rm -f -- "$signature_tmp"' EXIT
openssl pkeyutl -sign -inkey "$private_key" -rawin -in "$evidence" -out "$signature_tmp"
chmod 400 "$signature_tmp"
chown root:root "$signature_tmp"
mv -T -- "$signature_tmp" "$signature"
trap - EXIT
