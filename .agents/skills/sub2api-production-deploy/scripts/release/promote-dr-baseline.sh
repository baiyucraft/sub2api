#!/usr/bin/env bash
set -Eeuo pipefail
umask 077

release_id=${1:?release ID is required}
drill_id=${2:?drill ID is required}
input_dir=${3:?promotion input directory is required}
test_mode=${SUB2API_PROMOTION_TEST_MODE:-false}
test_root=${PROMOTION_TEST_ROOT:-}
# Keep the immutable legacy profile contract visible for audit tooling. Gate
# v2 profiles 242-264 (historical) and 265 (current) are accepted below.
# ^(195|199|202|206|207|208|209|210|212|213|215|232|233|234|235|236|237|238|239|240|241)-
# ^dr-(195|199|202|206|207|208|209|210|212|213|215|232|233|234|235|236|237|238|239|240|241)-
[[ $release_id =~ ^(195|199|202|206|207|208|209|210|212|213|215|232|233|234|235|236|237|238|239|240|241|242|243|244|245|246|247|248|249|250|251|252|253|254|255|256|257|258|259|260|261|262|263|264|265|266|267|268|269|270|271|272|273|274|275|276)-[0-9a-f]{12}-[0-9]+-[0-9a-f]{8}$ ]]
profile=${BASH_REMATCH[1]}
[[ $drill_id =~ ^dr-(195|199|202|206|207|208|209|210|212|213|215|232|233|234|235|236|237|238|239|240|241|242|243|244|245|246|247|248|249|250|251|252|253|254|255|256|257|258|259|260|261|262|263|264|265|266|267|268|269|270|271|272|273|274|275|276)-[0-9]{8}T[0-9]{6}Z$ ]]
drill_profile=${BASH_REMATCH[1]}
[[ $drill_profile == "$profile" ]]
if [[ $test_mode == true || -n $test_root ]]; then
  [[ $test_mode == true && -n $test_root ]]
  test_name=${test_root##*/}
  [[ $test_name =~ ^dr-promotion-test\.[A-Za-z0-9]{8}$ ]]
  [[ $test_root == /srv/sub2api-backups/$test_name ]]
  {
    required_query_1=$(realpath -e -- "$test_root") || exit $?
    required_query_2=$(stat -c '%U:%G:%a' "$test_root") || exit $?
    [[ -d $test_root && ! -L $test_root && ${required_query_1} == "$test_root" && ${required_query_2} == root:root:700 ]]
  }
  root="$test_root/releases/$profile"
  trust_key="$test_root/trust/vm-gate-ed25519.pub"
  verifier="$test_root/libexec/sub2api-verify-dr-evidence"
  asset_lock="$test_root/libexec/.sub2api-dr-assets.lock"
  asset_lock_dir_mode=700
else
  root="/srv/sub2api-backups/releases/$profile"
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
{
  required_query_3=$(id -u)
  [[ ${required_query_3} == 0 ]]
}

validate_directory() {
  local path=$1 mode=$2
  {
    required_query_4=$(realpath -e -- "$path") || return $?
    required_query_5=$(stat -c '%U:%G:%a' "$path") || return $?
    [[ -d $path && ! -L $path && ${required_query_4} == "$path" && ${required_query_5} == "root:root:$mode" ]]
  }
}
prepare_lock() {
  local path=$1
  if [[ -e $path || -L $path ]]; then
    {
      required_query_6=$(stat -c '%U:%G:%a:%h' "$path") || return $?
      [[ -f $path && ! -L $path && ${required_query_6} == root:root:600:1 ]]
    }
  else
    (set -o noclobber; umask 077; : > "$path") || {
      {
        required_query_7=$(stat -c '%U:%G:%a:%h' "$path") || return $?
        [[ -f $path && ! -L $path && ${required_query_7} == root:root:600:1 ]]
      }
    }
  fi
}
validate_directory "${asset_lock%/*}" "$asset_lock_dir_mode"
prepare_lock "$asset_lock"
exec 8<>"$asset_lock"
{
  required_query_8=$(stat -Lc '%U:%G:%a:%h' /proc/self/fd/8)
  [[ ${required_query_8} == root:root:600:1 ]]
}
flock -s 8
validate_directory "$root" 700
promotion_lock="$root/.promotion.lock"
prepare_lock "$promotion_lock"
exec 9<>"$promotion_lock"
{
  required_query_9=$(stat -Lc '%U:%G:%a:%h' /proc/self/fd/9)
  [[ ${required_query_9} == root:root:600:1 ]]
}
flock 9
for path in "$candidate_root" "$input_root" "$verified_root"; do
  validate_directory "$path" 700
done
{
  required_query_10=$(stat -c '%U:%G:%a:%h' "$trust_key")
  [[ -f $trust_key && ! -L $trust_key && ${required_query_10} == root:root:644:1 ]]
}
{
  required_query_11=$(realpath -e -- "${trust_key%/*}")
  required_query_12=$(stat -c '%U:%G:%a' "${trust_key%/*}")
  [[ -d ${trust_key%/*} && ! -L ${trust_key%/*} && ${required_query_11} == "${trust_key%/*}" && ${required_query_12} == root:root:755 ]]
}
{
  required_query_13=$(sha256sum "$trust_key" | awk '{print $1}')
  [[ ${required_query_13} == "$trust_sha256" ]]
}
{
  required_query_14=$(stat -c '%U:%G:%a:%h' "$verifier")
  [[ -f $verifier && ! -L $verifier && ${required_query_14} == root:root:700:1 ]]
}
{
  required_query_15=$(sha256sum "$verifier" | awk '{print $1}')
  [[ ${required_query_15} == "$verifier_sha256" ]]
}

input_name=${input_dir##*/}
[[ $input_name =~ ^$release_id--$drill_id\.[A-Za-z0-9]{8}$ ]]
[[ $input_dir == "$input_root/$input_name" ]]
{
  required_query_16=$(realpath -e -- "$input_dir")
  required_query_17=$(stat -c '%U:%G:%a' "$input_dir")
  [[ -d $input_dir && ! -L $input_dir && ${required_query_16} == "$input_dir" && ${required_query_17} == root:root:700 ]]
}
evidence="$input_dir/evidence.json"
signature="$input_dir/evidence.sig"
for path in "$evidence" "$signature"; do
  {
    required_query_18=$(stat -c '%U:%G:%a:%h' "$path")
    [[ -f $path && ! -L $path && ${required_query_18} == root:root:400:1 ]]
  }
done

{
  required_query_19=$(readlink "$candidate_link")
  [[ -L $candidate_link && ${required_query_19} == "candidates/$release_id" ]]
}
candidate_dir="$candidate_root/$release_id"
{
  required_query_20=$(realpath -e -- "$candidate_link")
  [[ ${required_query_20} == "$candidate_dir" ]]
}
[[ -d $candidate_dir && ! -L $candidate_dir ]]
{
  required_query_21=$(realpath -e -- "$candidate_dir")
  [[ ${required_query_21} == "$candidate_dir" ]]
}
candidate_dir_mode=$(stat -c '%U:%G:%a' "$candidate_dir")
if [[ $candidate_dir_mode != root:root:700 ]]; then
  false
fi
assert_file_set() {
  local directory=$1 expected_count=$2 file entry
  shift 2
  {
    required_query_22=$(find "$directory" -mindepth 1 -maxdepth 1 -printf '%y\n' | wc -l) || return $?
    [[ ${required_query_22} == "$expected_count" ]]
  }
  for file in "$@"; do
    entry=$(find "$directory" -mindepth 1 -maxdepth 1 -name "$file" -printf '%y %f\n')
    [[ $entry == "f $file" ]]
  done
}
candidate_files=(SHA256SUMS artifact.tar.age bundle.sha256 candidate.tar.gz gate.json gate.sig manifest)
assert_file_set "$candidate_dir" 7 "${candidate_files[@]}"
candidate_file_contract() {
  {
    required_query_23=$(stat -c '%U:%G:%a:%h' "$candidate_dir/SHA256SUMS") || return $?
    [[ ${required_query_23} == root:root:600:1 ]]
  }
  {
    required_query_24=$(stat -c '%U:%G:%a:%h' "$candidate_dir/artifact.tar.age") || return $?
    [[ ${required_query_24} == root:root:600:1 ]]
  }
  {
    required_query_25=$(stat -c '%U:%G:%a:%h' "$candidate_dir/bundle.sha256") || return $?
    [[ ${required_query_25} == root:root:644:1 ]]
  }
  {
    required_query_26=$(stat -c '%U:%G:%a:%h' "$candidate_dir/candidate.tar.gz") || return $?
    [[ ${required_query_26} == root:root:600:1 ]]
  }
  {
    required_query_27=$(stat -c '%U:%G:%a:%h' "$candidate_dir/gate.json") || return $?
    [[ ${required_query_27} == root:root:600:1 ]]
  }
  {
    required_query_28=$(stat -c '%U:%G:%a:%h' "$candidate_dir/gate.sig") || return $?
    [[ ${required_query_28} == root:root:600:1 ]]
  }
  {
    required_query_29=$(stat -c '%U:%G:%a:%h' "$candidate_dir/manifest") || return $?
    [[ ${required_query_29} == root:root:644:1 ]]
  }
}
candidate_file_contract

candidate_fingerprint() {
  (cd "$candidate_dir" && sha256sum SHA256SUMS artifact.tar.age bundle.sha256 candidate.tar.gz gate.json gate.sig manifest | sha256sum | awk '{print $1}')
}
initial_candidate_fingerprint=$(candidate_fingerprint)
assert_candidate_unchanged() {
  {
    required_query_30=$(readlink "$candidate_link") || return $?
    [[ -L $candidate_link && ${required_query_30} == "candidates/$release_id" ]]
  }
  {
    required_query_31=$(realpath -e -- "$candidate_link") || return $?
    [[ ${required_query_31} == "$candidate_dir" ]]
  }
  candidate_file_contract
  {
    required_query_32=$(candidate_fingerprint) || return $?
    [[ ${required_query_32} == "$initial_candidate_fingerprint" ]]
  }
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
manifest_keys=$(sed -n 's/^\([a-z0-9_]*\)=.*/\1/p' "$manifest" | LC_ALL=C sort)
if [[ $manifest_keys != $'artifact_name\nartifact_sha256\ncandidate_archive_sha256\ncandidate_image_id\nrelease_id\nstate' ]]; then
  false
fi
manifest_value() { awk -F= -v key="$1" '$1 == key {sub(/^[^=]*=/, ""); print; found++} END {exit found == 1 ? 0 : 1}' "$manifest"; }
{
  required_query_33=$(manifest_value release_id)
  [[ ${required_query_33} == "$release_id" ]]
}
{
  required_query_34=$(manifest_value state)
  [[ ${required_query_34} == restore_pending ]]
}
artifact_name=$(manifest_value artifact_name)
[[ $artifact_name =~ ^sub2api-[0-9]{8}T[0-9]{6}Z\.tar\.age$ ]]
artifact_sha=$(sha256sum "$staging/artifact.tar.age" | awk '{print $1}')
archive_sha=$(sha256sum "$staging/candidate.tar.gz" | awk '{print $1}')
bundle_sha=$(sha256sum "$staging/bundle.sha256" | awk '{print $1}')
{
  required_query_35=$(manifest_value artifact_sha256)
  [[ $artifact_sha == "${required_query_35}" ]]
}
{
  required_query_36=$(manifest_value candidate_archive_sha256)
  [[ $archive_sha == "${required_query_36}" ]]
}
checksum_entry() {
  local document=$1 line=$2 expected_name=$3
  awk -v line="$line" -v expected="$expected_name" '
    NR == line && NF == 2 {
      if ($2 == expected) print $1
    }
  ' "$document"
}
bundle_files=(artifact.tar.age candidate.tar.gz gate.json gate.sig manifest SHA256SUMS)
{
  required_query_37=$(wc -l < "$staging/bundle.sha256")
  [[ ${required_query_37} == ${#bundle_files[@]} ]]
}
line=1
for file in "${bundle_files[@]}"; do
  recorded_sha=$(checksum_entry "$staging/bundle.sha256" "$line" "$file")
  [[ ${#recorded_sha} == 64 && $recorded_sha =~ ^[0-9a-f]{64}$ ]]
  {
    required_query_38=$(sha256sum "$staging/$file" | awk '{print $1}')
    [[ $recorded_sha == "${required_query_38}" ]]
  }
  ((line += 1))
done
{
  required_query_39=$(wc -l < "$staging/SHA256SUMS")
  [[ ${required_query_39} == 3 ]]
}
gate_output_path="/opt/sub2api-deploy/release-gates/$release_id/output"
gate_recorded_sha=$(checksum_entry "$staging/SHA256SUMS" 1 "$gate_output_path/gate.json")
signature_recorded_sha=$(checksum_entry "$staging/SHA256SUMS" 2 "$gate_output_path/gate.sig")
archive_recorded_sha=$(checksum_entry "$staging/SHA256SUMS" 3 "$gate_output_path/candidate.tar.gz")
{
  required_query_40=$(sha256sum "$staging/gate.json" | awk '{print $1}')
  [[ $gate_recorded_sha == "${required_query_40}" && ${#gate_recorded_sha} == 64 && $gate_recorded_sha =~ ^[0-9a-f]+$ ]]
}
{
  required_query_41=$(sha256sum "$staging/gate.sig" | awk '{print $1}')
  [[ $signature_recorded_sha == "${required_query_41}" && ${#signature_recorded_sha} == 64 && $signature_recorded_sha =~ ^[0-9a-f]+$ ]]
}
[[ $archive_recorded_sha == "$archive_sha" && ${#archive_recorded_sha} == 64 && $archive_recorded_sha =~ ^[0-9a-f]+$ ]]
"$verifier" "$trust_key" "$staging/gate.json" "$staging/gate.sig" >/dev/null

gate_release=$(jq -er '.manifest.release_id' "$staging/gate.json")
gate_image=$(jq -er '.evidence.candidate_image_id' "$staging/gate.json")
gate_archive=$(jq -er '.evidence.candidate_archive_sha256' "$staging/gate.json")
gate_profile=$(jq -er '.manifest.profile' "$staging/gate.json")
gate_schema=$(jq -er '.manifest.schema' "$staging/gate.json")
if [[ $gate_schema == 2 ]]; then
  gate_migration=$(jq -cS '{catalog_sha256:.manifest.catalog_sha256,checksum_policy_sha256:.manifest.checksum_policy_sha256,pending:.evidence.migration_evidence.pending}' "$staging/gate.json" | tr -d '\n' | sha256sum | awk '{print $1}')
elif [[ $profile == 195 ]]; then
  gate_migration=$(jq -er '.manifest.migration_sha256["195_upstream_scheduling_monitor_rates.sql"]' "$staging/gate.json")
else
  gate_migration=$(jq -cS '.manifest.migration_sha256' "$staging/gate.json" | sha256sum | awk '{print $1}')
fi
[[ $gate_release == "$release_id" && $gate_profile == "$profile" && $gate_archive == "$archive_sha" ]]
{
  required_query_42=$(manifest_value candidate_image_id)
  [[ $gate_image == "${required_query_42}" ]]
}

"$verifier" "$trust_key" "$staging/evidence.json" "$staging/evidence.sig" >/dev/null
jq -e --arg release_id "$release_id" --arg drill_id "$drill_id" --arg drill_prefix "dr-$profile-" --arg artifact_sha "$artifact_sha" --arg bundle_sha "$bundle_sha" --arg archive_sha "$archive_sha" --arg image_id "$gate_image" --arg migration_sha "$gate_migration" '
  type == "object" and
  (keys | sort) == (["artifact_sha256","candidate_archive_sha256","candidate_bundle_sha256","candidate_image_id","completed_at","config_manifest_check","counts_and_migrations","created_at","drill_id","image_load_id_check","migration_checksum","postgres_restore","redis_backup_dbsize","redis_backup_expiring_keys","redis_restore","redis_restored_dbsize","redis_restored_expiring_keys","redis_ttl_reconciliation","release_id","schema","temporary_material_destroyed"] | sort) and
  .schema == 1 and .release_id == $release_id and .drill_id == $drill_id and
  (.created_at | type == "string" and test("^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$")) and
  (.created_at | gsub("[-:]"; "")) == ($drill_id | ltrimstr($drill_prefix)) and
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
  {
    required_query_43=$(stat -c '%U:%G:%a' "$target") || exit $?
    [[ -d $target && ! -L $target && ${required_query_43} == root:root:700 ]]
  }
  verified_files=(SHA256SUMS VERIFIED_SHA256SUMS artifact.tar.age bundle.sha256 candidate.tar.gz evidence.json evidence.sig gate.json gate.sig manifest)
  assert_file_set "$target" 10 "${verified_files[@]}"
  verified_checksum_files=(SHA256SUMS artifact.tar.age bundle.sha256 candidate.tar.gz evidence.json evidence.sig gate.json gate.sig manifest)
  {
    required_query_44=$(wc -l < "$target/VERIFIED_SHA256SUMS") || exit $?
    [[ ${required_query_44} == ${#verified_checksum_files[@]} ]]
  }
  line=1
  for file in "${verified_checksum_files[@]}"; do
    {
      required_query_45=$(checksum_entry "$target/VERIFIED_SHA256SUMS" "$line" "$file") || exit $?
      required_query_46=$(sha256sum "$target/$file" | awk '{print $1}') || exit $?
      [[ ${required_query_45} == "${required_query_46}" ]]
    }
    ((line += 1))
  done
  for file in SHA256SUMS VERIFIED_SHA256SUMS artifact.tar.age bundle.sha256 candidate.tar.gz evidence.json evidence.sig gate.json gate.sig manifest; do
    {
      required_query_47=$(stat -c '%U:%G:%a:%h' "$target/$file") || exit $?
      [[ ${required_query_47} == root:root:400:1 ]]
    }
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
  [[ $old_target =~ ^verified-bundles/$profile-[0-9a-f]{12}-[0-9]+-[0-9a-f]{8}--dr-$profile-[0-9]{8}T[0-9]{6}Z$ ]]
  old_target_path="$root/$old_target"
  {
    required_query_48=$(realpath -e -- "$old_target_path") || exit $?
    required_query_49=$(stat -c '%U:%G:%a' "$old_target_path") || exit $?
    [[ -d $old_target_path && ! -L $old_target_path && ${required_query_48} == "$old_target_path" && ${required_query_49} == root:root:700 ]]
  }
  old_verified_files=(SHA256SUMS VERIFIED_SHA256SUMS artifact.tar.age bundle.sha256 candidate.tar.gz evidence.json evidence.sig gate.json gate.sig manifest)
  assert_file_set "$old_target_path" 10 "${old_verified_files[@]}"
  old_verified_checksum_files=(SHA256SUMS artifact.tar.age bundle.sha256 candidate.tar.gz evidence.json evidence.sig gate.json gate.sig manifest)
  {
    required_query_50=$(wc -l < "$old_target_path/VERIFIED_SHA256SUMS") || exit $?
    [[ ${required_query_50} == ${#old_verified_checksum_files[@]} ]]
  }
  line=1
  for file in "${old_verified_checksum_files[@]}"; do
    {
      required_query_51=$(checksum_entry "$old_target_path/VERIFIED_SHA256SUMS" "$line" "$file") || exit $?
      required_query_52=$(sha256sum "$old_target_path/$file" | awk '{print $1}') || exit $?
      [[ ${required_query_51} == "${required_query_52}" ]]
    }
    {
      required_query_53=$(stat -c '%U:%G:%a:%h' "$old_target_path/$file") || exit $?
      [[ ${required_query_53} == root:root:400:1 ]]
    }
    ((line += 1))
  done
  {
    required_query_54=$(stat -c '%U:%G:%a:%h' "$old_target_path/VERIFIED_SHA256SUMS") || exit $?
    [[ ${required_query_54} == root:root:400:1 ]]
  }
fi
ln -s "verified-bundles/$target_name" "$link_tmp"
mv -T -- "$link_tmp" "$verified_link"
sync -f "$root"
{
  required_query_55=$(readlink "$verified_link")
  required_query_56=$(realpath -e -- "$verified_link")
  [[ ${required_query_55} == "verified-bundles/$target_name" && ${required_query_56} == "$target" ]]
}
evidence_sha=$(sha256sum "$target/evidence.json" | awk '{print $1}')
signature_sha=$(sha256sum "$target/evidence.sig" | awk '{print $1}')
verified_sha=$(sha256sum "$target/VERIFIED_SHA256SUMS" | awk '{print $1}')
printf 'promotion_status=verified\n'
printf 'verified_target=%s\n' "$target_name"
printf 'evidence_sha256=%s\n' "$evidence_sha"
printf 'signature_sha256=%s\n' "$signature_sha"
printf 'verified_bundle_sha256=%s\n' "$verified_sha"
