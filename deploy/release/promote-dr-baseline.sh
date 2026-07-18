#!/usr/bin/env bash
set -Eeuo pipefail
umask 077

release_id=${1:?release ID is required}
drill_id=${2:?drill ID is required}
input_dir=${3:?promotion input directory is required}
test_mode=${SUB2API_PROMOTION_TEST_MODE:-false}
test_root=${PROMOTION_TEST_ROOT:-}
if [[ $test_mode == true || -n $test_root ]]; then
  [[ $test_mode == true && -n $test_root ]]
  test_name=${test_root##*/}
  [[ $test_name =~ ^dr-promotion-test\.[A-Za-z0-9]{8}$ ]]
  [[ $test_root == /srv/sub2api-backups/$test_name ]]
  [[ -d $test_root && ! -L $test_root && $(realpath -e -- "$test_root") == "$test_root" && $(stat -c '%U:%G:%a' "$test_root") == root:root:700 ]]
  root="$test_root/releases/195"
  trust_key="$test_root/trust/vm-gate-ed25519.pub"
  verifier="$test_root/libexec/sub2api-verify-dr-evidence"
  asset_lock="$test_root/libexec/.sub2api-dr-assets.lock"
  asset_lock_dir_mode=700
else
  root=/srv/sub2api-backups/releases/195
  trust_key=/opt/sub2api-dr-trust/vm-gate-ed25519.pub
  verifier=/usr/local/libexec/sub2api-verify-dr-evidence
  asset_lock=/usr/local/libexec/.sub2api-dr-assets.lock
  asset_lock_dir_mode=755
fi
candidate_link="$root/candidate"
candidate_root="$root/candidates"
input_root="$root/promotion-input"
verified_root="$root/verified-bundles"
verified_link="$root/verified"
trust_sha256=ea0b628532f8d85d0e57921b5b010c7f00ef8b0f9701da2b0d4ea31105553e08
verifier_sha256=f43b10ae4e9d52b970a9395487a3cf31ae2ccb0d869b5d0a5ee134fd0ca81d89
migration_sha256=e77566efef46748b4098a148659a97e021928bd4aae0a97cf26e122aadf85cf0
[[ $(id -u) == 0 ]]
[[ $release_id =~ ^195-[0-9a-f]{12}-[0-9]+-[0-9a-f]{8}$ ]]
[[ $drill_id =~ ^dr-195-[0-9]{8}T[0-9]{6}Z$ ]]

validate_directory() {
  local path=$1 mode=$2
  [[ -d $path && ! -L $path && $(realpath -e -- "$path") == "$path" && $(stat -c '%U:%G:%a' "$path") == "root:root:$mode" ]]
}
prepare_lock() {
  local path=$1
  if [[ -e $path || -L $path ]]; then
    [[ -f $path && ! -L $path && $(stat -c '%U:%G:%a:%h' "$path") == root:root:600:1 ]]
  else
    (set -o noclobber; umask 077; : > "$path") || {
      [[ -f $path && ! -L $path && $(stat -c '%U:%G:%a:%h' "$path") == root:root:600:1 ]]
    }
  fi
}
validate_directory "${asset_lock%/*}" "$asset_lock_dir_mode"
prepare_lock "$asset_lock"
exec 8<>"$asset_lock"
[[ $(stat -Lc '%U:%G:%a:%h' /proc/self/fd/8) == root:root:600:1 ]]
flock -s 8
validate_directory "$root" 700
promotion_lock="$root/.promotion.lock"
prepare_lock "$promotion_lock"
exec 9<>"$promotion_lock"
[[ $(stat -Lc '%U:%G:%a:%h' /proc/self/fd/9) == root:root:600:1 ]]
flock 9
for path in "$candidate_root" "$input_root" "$verified_root"; do
  validate_directory "$path" 700
done
[[ -f $trust_key && ! -L $trust_key && $(stat -c '%U:%G:%a:%h' "$trust_key") == root:root:644:1 ]]
[[ -d ${trust_key%/*} && ! -L ${trust_key%/*} && $(realpath -e -- "${trust_key%/*}") == "${trust_key%/*}" && $(stat -c '%U:%G:%a' "${trust_key%/*}") == root:root:755 ]]
[[ $(sha256sum "$trust_key" | awk '{print $1}') == "$trust_sha256" ]]
[[ -f $verifier && ! -L $verifier && $(stat -c '%U:%G:%a:%h' "$verifier") == root:root:700:1 ]]
[[ $(sha256sum "$verifier" | awk '{print $1}') == "$verifier_sha256" ]]

input_name=${input_dir##*/}
[[ $input_name =~ ^$release_id--$drill_id\.[A-Za-z0-9]{8}$ ]]
[[ $input_dir == "$input_root/$input_name" ]]
[[ -d $input_dir && ! -L $input_dir && $(realpath -e -- "$input_dir") == "$input_dir" && $(stat -c '%U:%G:%a' "$input_dir") == root:root:700 ]]
evidence="$input_dir/evidence.json"
signature="$input_dir/evidence.sig"
for path in "$evidence" "$signature"; do
  [[ -f $path && ! -L $path && $(stat -c '%U:%G:%a:%h' "$path") == root:root:400:1 ]]
done

[[ -L $candidate_link && $(readlink "$candidate_link") == "candidates/$release_id" ]]
candidate_dir="$candidate_root/$release_id"
[[ $(realpath -e -- "$candidate_link") == "$candidate_dir" ]]
[[ -d $candidate_dir && ! -L $candidate_dir ]]
[[ $(realpath -e -- "$candidate_dir") == "$candidate_dir" ]]
candidate_dir_mode=$(stat -c '%U:%G:%a' "$candidate_dir")
if [[ $candidate_dir_mode != root:root:700 ]]; then
  false
fi
assert_file_set() {
  local directory=$1 expected_count=$2 file entry
  shift 2
  [[ $(find "$directory" -mindepth 1 -maxdepth 1 -printf '%y\n' | wc -l) == "$expected_count" ]]
  for file in "$@"; do
    entry=$(find "$directory" -mindepth 1 -maxdepth 1 -name "$file" -printf '%y %f\n')
    [[ $entry == "f $file" ]]
  done
}
candidate_files=(SHA256SUMS artifact.tar.age bundle.sha256 candidate.tar.gz gate.json gate.sig manifest)
assert_file_set "$candidate_dir" 7 "${candidate_files[@]}"
candidate_file_contract() {
  [[ $(stat -c '%U:%G:%a:%h' "$candidate_dir/SHA256SUMS") == root:root:600:1 ]]
  [[ $(stat -c '%U:%G:%a:%h' "$candidate_dir/artifact.tar.age") == root:root:600:1 ]]
  [[ $(stat -c '%U:%G:%a:%h' "$candidate_dir/bundle.sha256") == root:root:644:1 ]]
  [[ $(stat -c '%U:%G:%a:%h' "$candidate_dir/candidate.tar.gz") == root:root:600:1 ]]
  [[ $(stat -c '%U:%G:%a:%h' "$candidate_dir/gate.json") == root:root:600:1 ]]
  [[ $(stat -c '%U:%G:%a:%h' "$candidate_dir/gate.sig") == root:root:600:1 ]]
  [[ $(stat -c '%U:%G:%a:%h' "$candidate_dir/manifest") == root:root:644:1 ]]
}
candidate_file_contract

candidate_fingerprint() {
  (cd "$candidate_dir" && sha256sum SHA256SUMS artifact.tar.age bundle.sha256 candidate.tar.gz gate.json gate.sig manifest | sha256sum | awk '{print $1}')
}
initial_candidate_fingerprint=$(candidate_fingerprint)
assert_candidate_unchanged() {
  [[ -L $candidate_link && $(readlink "$candidate_link") == "candidates/$release_id" ]]
  [[ $(realpath -e -- "$candidate_link") == "$candidate_dir" ]]
  candidate_file_contract
  [[ $(candidate_fingerprint) == "$initial_candidate_fingerprint" ]]
}

target_name="$release_id--$drill_id"
target="$verified_root/$target_name"
staging=$(mktemp -d "$verified_root/.staging-$target_name.XXXXXXXX")
link_tmp="$root/.verified.$$.tmp"
cleanup() { rm -rf -- "$staging"; rm -f -- "$link_tmp"; }
trap cleanup EXIT
for file in SHA256SUMS artifact.tar.age bundle.sha256 candidate.tar.gz gate.json gate.sig manifest; do
  install -o root -g root -m 400 "$candidate_dir/$file" "$staging/$file"
done
install -o root -g root -m 400 "$evidence" "$staging/evidence.json"
install -o root -g root -m 400 "$signature" "$staging/evidence.sig"
assert_candidate_unchanged

manifest="$staging/manifest"
manifest_lines=$(wc -l < "$manifest")
if [[ $manifest_lines != 6 ]]; then
  false
fi
manifest_keys=$(sed -n 's/^\([a-z0-9_]*\)=.*/\1/p' "$manifest" | sort)
if [[ $manifest_keys != $'artifact_name\nartifact_sha256\ncandidate_archive_sha256\ncandidate_image_id\nrelease_id\nstate' ]]; then
  false
fi
manifest_value() { awk -F= -v key="$1" '$1 == key {sub(/^[^=]*=/, ""); print; found++} END {exit found == 1 ? 0 : 1}' "$manifest"; }
[[ $(manifest_value release_id) == "$release_id" ]]
[[ $(manifest_value state) == restore_pending ]]
artifact_name=$(manifest_value artifact_name)
[[ $artifact_name =~ ^sub2api-[0-9]{8}T[0-9]{6}Z\.tar\.age$ ]]
artifact_sha=$(sha256sum "$staging/artifact.tar.age" | awk '{print $1}')
archive_sha=$(sha256sum "$staging/candidate.tar.gz" | awk '{print $1}')
bundle_sha=$(sha256sum "$staging/bundle.sha256" | awk '{print $1}')
[[ $artifact_sha == "$(manifest_value artifact_sha256)" ]]
[[ $archive_sha == "$(manifest_value candidate_archive_sha256)" ]]
checksum_entry() {
  local document=$1 line=$2 expected_name=$3
  awk -v line="$line" -v expected="$expected_name" '
    NR == line && NF == 2 {
      if ($2 == expected) print $1
    }
  ' "$document"
}
bundle_files=(artifact.tar.age candidate.tar.gz gate.json gate.sig manifest SHA256SUMS)
[[ $(wc -l < "$staging/bundle.sha256") == ${#bundle_files[@]} ]]
line=1
for file in "${bundle_files[@]}"; do
  recorded_sha=$(checksum_entry "$staging/bundle.sha256" "$line" "$file")
  [[ ${#recorded_sha} == 64 && $recorded_sha =~ ^[0-9a-f]{64}$ ]]
  [[ $recorded_sha == "$(sha256sum "$staging/$file" | awk '{print $1}')" ]]
  ((line += 1))
done
[[ $(wc -l < "$staging/SHA256SUMS") == 3 ]]
gate_output_path="/opt/sub2api-deploy/release-gates/$release_id/output"
gate_recorded_sha=$(checksum_entry "$staging/SHA256SUMS" 1 "$gate_output_path/gate.json")
signature_recorded_sha=$(checksum_entry "$staging/SHA256SUMS" 2 "$gate_output_path/gate.sig")
archive_recorded_sha=$(checksum_entry "$staging/SHA256SUMS" 3 "$gate_output_path/candidate.tar.gz")
[[ $gate_recorded_sha == "$(sha256sum "$staging/gate.json" | awk '{print $1}')" && ${#gate_recorded_sha} == 64 && $gate_recorded_sha =~ ^[0-9a-f]+$ ]]
[[ $signature_recorded_sha == "$(sha256sum "$staging/gate.sig" | awk '{print $1}')" && ${#signature_recorded_sha} == 64 && $signature_recorded_sha =~ ^[0-9a-f]+$ ]]
[[ $archive_recorded_sha == "$archive_sha" && ${#archive_recorded_sha} == 64 && $archive_recorded_sha =~ ^[0-9a-f]+$ ]]
"$verifier" "$trust_key" "$staging/gate.json" "$staging/gate.sig" >/dev/null

gate_release=$(jq -er '.manifest.release_id' "$staging/gate.json")
gate_image=$(jq -er '.evidence.candidate_image_id' "$staging/gate.json")
gate_archive=$(jq -er '.evidence.candidate_archive_sha256' "$staging/gate.json")
gate_migration=$(jq -er '.manifest.migration_sha256["195_upstream_scheduling_monitor_rates.sql"]' "$staging/gate.json")
[[ $gate_release == "$release_id" && $gate_archive == "$archive_sha" && $gate_migration == "$migration_sha256" ]]
[[ $gate_image == "$(manifest_value candidate_image_id)" ]]

"$verifier" "$trust_key" "$staging/evidence.json" "$staging/evidence.sig" >/dev/null
jq -e --arg release_id "$release_id" --arg drill_id "$drill_id" --arg artifact_sha "$artifact_sha" --arg bundle_sha "$bundle_sha" --arg archive_sha "$archive_sha" --arg image_id "$gate_image" --arg migration_sha "$migration_sha256" '
  type == "object" and
  (keys | sort) == (["artifact_sha256","candidate_archive_sha256","candidate_bundle_sha256","candidate_image_id","completed_at","config_manifest_check","counts_and_migrations","created_at","drill_id","image_load_id_check","migration_checksum","postgres_restore","redis_backup_dbsize","redis_backup_expiring_keys","redis_restore","redis_restored_dbsize","redis_restored_expiring_keys","redis_ttl_reconciliation","release_id","schema","temporary_material_destroyed"] | sort) and
  .schema == 1 and .release_id == $release_id and .drill_id == $drill_id and
  (.created_at | type == "string" and test("^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$")) and
  (.created_at | gsub("[-:]"; "")) == ($drill_id | ltrimstr("dr-195-")) and
  (.completed_at | type == "string" and test("^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$")) and
  ((.created_at | fromdateiso8601) as $created | (.completed_at | fromdateiso8601) as $completed | $created <= $completed and $completed >= (now - 86400) and $completed <= (now + 300)) and
  .artifact_sha256 == $artifact_sha and .candidate_bundle_sha256 == $bundle_sha and
  .candidate_archive_sha256 == $archive_sha and .candidate_image_id == $image_id and .migration_checksum == $migration_sha and
  .image_load_id_check == "pass" and .config_manifest_check == "pass" and .postgres_restore == "pass" and .redis_restore == "pass" and .redis_ttl_reconciliation == "pass" and .counts_and_migrations == "pass" and .temporary_material_destroyed == "pass" and
  ([.redis_backup_dbsize,.redis_backup_expiring_keys,.redis_restored_dbsize,.redis_restored_expiring_keys] | all(type == "number" and . >= 0 and floor == .)) and
  .redis_backup_expiring_keys <= .redis_backup_dbsize and .redis_restored_expiring_keys <= .redis_restored_dbsize and
  .redis_backup_dbsize >= .redis_restored_dbsize and .redis_backup_expiring_keys >= .redis_restored_expiring_keys and
  (.redis_backup_dbsize - .redis_restored_dbsize) == (.redis_backup_expiring_keys - .redis_restored_expiring_keys)
' "$staging/evidence.json" >/dev/null

(cd "$staging" && sha256sum SHA256SUMS artifact.tar.age bundle.sha256 candidate.tar.gz evidence.json evidence.sig gate.json gate.sig manifest > VERIFIED_SHA256SUMS)
chmod 400 "$staging/VERIFIED_SHA256SUMS"
chown root:root "$staging/VERIFIED_SHA256SUMS"
for file in "$staging"/*; do sync -f "$file"; done
sync -f "$staging"
assert_candidate_unchanged
if [[ -e $target || -L $target ]]; then
  [[ -d $target && ! -L $target && $(stat -c '%U:%G:%a' "$target") == root:root:700 ]]
  verified_files=(SHA256SUMS VERIFIED_SHA256SUMS artifact.tar.age bundle.sha256 candidate.tar.gz evidence.json evidence.sig gate.json gate.sig manifest)
  assert_file_set "$target" 10 "${verified_files[@]}"
  verified_checksum_files=(SHA256SUMS artifact.tar.age bundle.sha256 candidate.tar.gz evidence.json evidence.sig gate.json gate.sig manifest)
  [[ $(wc -l < "$target/VERIFIED_SHA256SUMS") == ${#verified_checksum_files[@]} ]]
  line=1
  for file in "${verified_checksum_files[@]}"; do
    [[ $(checksum_entry "$target/VERIFIED_SHA256SUMS" "$line" "$file") == "$(sha256sum "$target/$file" | awk '{print $1}')" ]]
    ((line += 1))
  done
  for file in SHA256SUMS VERIFIED_SHA256SUMS artifact.tar.age bundle.sha256 candidate.tar.gz evidence.json evidence.sig gate.json gate.sig manifest; do
    [[ $(stat -c '%U:%G:%a:%h' "$target/$file") == root:root:400:1 ]]
    cmp -s "$staging/$file" "$target/$file"
  done
  rm -rf -- "$staging"
else
  mv -T -- "$staging" "$target"
  sync -f "$verified_root"
fi
assert_candidate_unchanged
if [[ -e $verified_link && ! -L $verified_link ]]; then exit 1; fi
if [[ -L $verified_link ]]; then
  old_target=$(readlink "$verified_link")
  [[ $old_target =~ ^verified-bundles/195-[0-9a-f]{12}-[0-9]+-[0-9a-f]{8}--dr-195-[0-9]{8}T[0-9]{6}Z$ ]]
  old_target_path="$root/$old_target"
  [[ -d $old_target_path && ! -L $old_target_path && $(realpath -e -- "$old_target_path") == "$old_target_path" && $(stat -c '%U:%G:%a' "$old_target_path") == root:root:700 ]]
  old_verified_files=(SHA256SUMS VERIFIED_SHA256SUMS artifact.tar.age bundle.sha256 candidate.tar.gz evidence.json evidence.sig gate.json gate.sig manifest)
  assert_file_set "$old_target_path" 10 "${old_verified_files[@]}"
  old_verified_checksum_files=(SHA256SUMS artifact.tar.age bundle.sha256 candidate.tar.gz evidence.json evidence.sig gate.json gate.sig manifest)
  [[ $(wc -l < "$old_target_path/VERIFIED_SHA256SUMS") == ${#old_verified_checksum_files[@]} ]]
  line=1
  for file in "${old_verified_checksum_files[@]}"; do
    [[ $(checksum_entry "$old_target_path/VERIFIED_SHA256SUMS" "$line" "$file") == "$(sha256sum "$old_target_path/$file" | awk '{print $1}')" ]]
    [[ $(stat -c '%U:%G:%a:%h' "$old_target_path/$file") == root:root:400:1 ]]
    ((line += 1))
  done
  [[ $(stat -c '%U:%G:%a:%h' "$old_target_path/VERIFIED_SHA256SUMS") == root:root:400:1 ]]
fi
ln -s "verified-bundles/$target_name" "$link_tmp"
mv -T -- "$link_tmp" "$verified_link"
sync -f "$root"
[[ $(readlink "$verified_link") == "verified-bundles/$target_name" && $(realpath -e -- "$verified_link") == "$target" ]]
evidence_sha=$(sha256sum "$target/evidence.json" | awk '{print $1}')
signature_sha=$(sha256sum "$target/evidence.sig" | awk '{print $1}')
verified_sha=$(sha256sum "$target/VERIFIED_SHA256SUMS" | awk '{print $1}')
printf 'promotion_status=verified\n'
printf 'verified_target=%s\n' "$target_name"
printf 'evidence_sha256=%s\n' "$evidence_sha"
printf 'signature_sha256=%s\n' "$signature_sha"
printf 'verified_bundle_sha256=%s\n' "$verified_sha"
