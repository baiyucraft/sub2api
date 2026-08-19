#!/usr/bin/env bash
set -Eeuo pipefail
umask 077

mode=${1:-dry-run}
plan_arg=${2:-}
shift 2 || true
backup_root=${BACKUP_ROOT:-/srv/sub2api-backups}
release_root=${RELEASE_ROOT:-/srv/sub2api-backups/releases}
retention_days=${RELEASE_RETENTION_DAYS:-30}
keep_recent=${KEEP_RECENT_RELEASES:-2}
keep_recent_per_profile=${KEEP_RECENT_PER_PROFILE:-$keep_recent}
failed_retention_days=${FAILED_RELEASE_RETENTION_DAYS:-7}

[[ $mode == dry-run || $mode == apply ]]
[[ $backup_root == /srv/sub2api-backups ]]
[[ $release_root == /srv/sub2api-backups/releases ]]
[[ $retention_days =~ ^[0-9]+$ && $retention_days -ge 30 ]]
[[ $keep_recent =~ ^[0-9]+$ && $keep_recent -ge 2 ]]
[[ $keep_recent_per_profile =~ ^[0-9]+$ && $keep_recent_per_profile -ge 1 ]]
[[ $failed_retention_days =~ ^[0-9]+$ && $failed_retention_days -ge 1 ]]
[[ -d $backup_root && ! -L $backup_root ]]
[[ -d $release_root && ! -L $release_root ]]
[[ $(realpath -e -- "$backup_root") == "$backup_root" ]]
[[ $(realpath -e -- "$release_root") == "$release_root" ]]
promotion_lock="$backup_root/.release-promotion.lock"
[[ -f $promotion_lock && ! -L $promotion_lock && $(stat -c '%h' "$promotion_lock") == 1 ]]

work=$(mktemp -d /tmp/sub2api-backup-release-retention.XXXXXX)
cleanup() { rm -f -- "$work"/* 2>/dev/null || true; rmdir -- "$work" 2>/dev/null || true; }
trap cleanup EXIT
all="$work/all"
recent="$work/recent"
protected="$work/protected"
plan="$work/plan"
candidate_ids="$work/candidate-ids"
: > "$all"; : > "$recent"; : > "$protected"; : > "$plan"; : > "$candidate_ids"
release_device=$(stat -c '%d' "$release_root")
now_epoch=$(date +%s)
cutoff=$(( now_epoch - retention_days * 86400 ))
failed_cutoff=$(( now_epoch - failed_retention_days * 86400 ))

verify_bundle() {
  local bundle=$1 release_id=$2 entry_count manifest_release transport sha expected_bundle_checksum
  [[ -d $bundle && ! -L $bundle ]]
  [[ $(realpath -e -- "$bundle") == "$bundle" ]]
  [[ $(stat -c '%d' "$bundle") == "$release_device" ]]
  entry_count=$(find "$bundle" -mindepth 1 -maxdepth 1 -print | wc -l | tr -d '[:space:]')
  [[ $entry_count == 4 ]]
  for name in artifact.tar.age artifact.tar.age.sha256 manifest bundle.sha256; do
    [[ -f "$bundle/$name" && ! -L "$bundle/$name" && $(stat -c '%h' "$bundle/$name") == 1 ]]
  done
  manifest_release=$(sed -n 's/^release_id=//p' "$bundle/manifest")
  transport=$(sed -n 's/^transport_artifact=//p' "$bundle/manifest")
  sha=$(sed -n 's/^sha256=//p' "$bundle/manifest")
  [[ $manifest_release == "$release_id" ]]
  [[ $transport =~ ^sub2api-[0-9]{8}T[0-9]{6}Z\.tar\.age$ ]]
  [[ $sha =~ ^[0-9a-f]{64}$ ]]
  [[ $(grep -c '^release_id=' "$bundle/manifest") == 1 ]]
  [[ $(grep -c '^transport_artifact=' "$bundle/manifest") == 1 ]]
  [[ $(grep -c '^sha256=' "$bundle/manifest") == 1 ]]
  [[ $(<"$bundle/artifact.tar.age.sha256") == "$sha  artifact.tar.age" ]]
  [[ $(sha256sum "$bundle/artifact.tar.age" | awk '{print $1}') == "$sha" ]]
  expected_bundle_checksum=$(cd "$bundle" && sha256sum artifact.tar.age artifact.tar.age.sha256 manifest)
  [[ $(<"$bundle/bundle.sha256") == "$expected_bundle_checksum" ]]
  (cd "$bundle" && sha256sum -c bundle.sha256 >/dev/null)
}

release_is_explicitly_failed() {
  local profile=$1 release_id=$2 log_file="$backup_root/release-logs/$release_id/remote.raw.log"
  [[ -f $log_file && ! -L $log_file ]] || return 1
  grep -Eq '(^|[^0-9])exit=[1-9][0-9]*([^0-9]|$)|stage failed|status=failed' "$log_file" || return 1
  ! grep -Eq 'production_verified|verify-result=verified|status=pass' "$log_file"
}

mapfile -t release_dirs < <(find "$release_root" -mindepth 2 -maxdepth 2 -type d ! -name '.*' -print | LC_ALL=C sort)
for bundle in "${release_dirs[@]}"; do
  profile_dir=${bundle%/*}; profile=${profile_dir##*/}; release_id=${bundle##*/}
  case "$release_id" in
    candidates|promotion-input|verified-bundles) continue ;;
  esac
  [[ $profile =~ ^[0-9]+$ ]]
  [[ $release_id =~ ^[0-9]+-[0-9a-f]{12}-[0-9]+-[0-9a-f]{8}$ ]]
  [[ $(realpath -e -- "$bundle") == "$release_root/$profile/$release_id" ]]
  [[ $(stat -c '%d' "$bundle") == "$release_device" ]]
  IFS=- read -r _ _ release_epoch _ <<< "$release_id"
  [[ $release_epoch =~ ^[0-9]+$ ]]
  printf '%s\t%s\t%s\t%s\n' "$profile" "$release_epoch" "$release_id" "$bundle" >> "$all"
done

LC_ALL=C sort -t $'\t' -k1,1 -k2,2nr -k3,3 "$all" \
  | awk -F '\t' -v keep="$keep_recent_per_profile" 'count[$1]++ < keep {print $4}' > "$recent"
while IFS= read -r -d '' link; do
  target=$(realpath -e -- "$link" 2>/dev/null || true)
  [[ -n $target && $target == "$release_root/"* && -d $target && ! -L $target ]] || continue
  printf '%s\n' "$target" >> "$protected"
done < <(find "$release_root" -mindepth 1 -maxdepth 2 -type l \( -name candidate -o -name verified -o -name current -o -name previous -o -name recovery -o -name baseline \) -print0)
LC_ALL=C sort -u "$protected" -o "$protected"

while IFS=$'\t' read -r profile release_epoch release_id bundle; do
  [[ -n $bundle ]] || continue
  grep -Fxq "$bundle" "$recent" && continue
  grep -Fxq "$bundle" "$protected" && continue
  bundle_mtime=$(stat -c '%Y' "$bundle")
  if (( bundle_mtime >= cutoff )); then
    (( bundle_mtime < failed_cutoff )) || continue
    release_is_explicitly_failed "$profile" "$release_id" || continue
  fi
  find "$bundle" -mindepth 1 -maxdepth 1 \( -name '.active' -o -name '.prepared' -o -name '.consumed' -o -name '.recovered' -o -name '.reconciliation' -o -name 'recovery-point.*' -o -name 'production-result.json' \) -print -quit | grep -q . && continue
  verify_bundle "$bundle" "$release_id" || continue
  bundle_bytes=$(find "$bundle" -mindepth 1 -maxdepth 1 -type f -printf '%s\n' | awk '{s += $1} END {printf "%.0f\n", s+0}')
  bundle_digest=$(cd "$bundle" && sha256sum artifact.tar.age artifact.tar.age.sha256 manifest bundle.sha256 | sha256sum | awk '{print $1}')
  printf '%s\t%s\t%s\t%s\t%s\t%s\n' "$release_id" "$bundle" "$(stat -c '%Y' "$bundle")" "$bundle_bytes" "$bundle_digest" "$release_epoch" >> "$plan"
  printf '%s\n' "$release_id" >> "$candidate_ids"
done < <(LC_ALL=C sort -nr -k1,1 -k2,2 "$all")

plan_sha=$(sha256sum "$plan" | awk '{print $1}')
candidate_count=$(wc -l < "$plan" | tr -d '[:space:]')
candidate_bytes=$(awk -F '\t' '{s += $4} END {printf "%.0f\n", s+0}' "$plan")
candidate_release_ids=$(paste -sd, "$candidate_ids")
free_before=$(df -PB1 "$backup_root" | awk 'NR==2 {print $4}')

if [[ $mode == dry-run ]]; then
  printf 'cleanup_mode=dry-run\ncleanup_status=ready\nplan_sha256=%s\ncandidate_count=%s\ncandidate_bytes=%s\ncandidate_release_ids=%s\nfree_before=%s\nkeep_recent=%s\nretention_days=%s\n' "$plan_sha" "$candidate_count" "$candidate_bytes" "$candidate_release_ids" "$free_before" "$keep_recent" "$retention_days"
  exit 0
fi

[[ $plan_arg =~ ^[0-9a-f]{64}$ && $plan_arg == "$plan_sha" ]]
[[ $# -ge 1 ]]
declare -A approved=()
for release_id in "$@"; do
  [[ $release_id =~ ^[0-9]+-[0-9a-f]{12}-[0-9]+-[0-9a-f]{8}$ ]]
  [[ -z ${approved[$release_id]+x} ]]
  approved[$release_id]=1
  grep -Fxq "$release_id" "$candidate_ids"
done
retention_lock="$backup_root/.backup-release-retention.lock"
[[ ! -e $retention_lock || ! -L $retention_lock ]]
exec 9>"$retention_lock"
[[ $(stat -c '%h' "$retention_lock") == 1 ]]
flock -n 9
exec 8<"$promotion_lock"
flock -n 8

deleted=0
deleted_bytes=0
while IFS=$'\t' read -r release_id bundle bundle_mtime bundle_bytes bundle_digest release_epoch; do
  [[ ${approved[$release_id]+x} == x ]] || continue
  [[ -d "$bundle" && ! -L "$bundle" ]]
  [[ $(stat -c '%Y' "$bundle") == "$bundle_mtime" ]]
  [[ $(find "$bundle" -mindepth 1 -maxdepth 1 -type f -printf '%s\n' | awk '{s += $1} END {printf "%.0f\n", s+0}') == "$bundle_bytes" ]]
  [[ $(cd "$bundle" && sha256sum artifact.tar.age artifact.tar.age.sha256 manifest bundle.sha256 | sha256sum | awk '{print $1}') == "$bundle_digest" ]]
  verify_bundle "$bundle" "$release_id"
  while IFS= read -r file; do
    [[ -f "$file" && ! -L "$file" && $(stat -c '%h' "$file") == 1 ]]
    rm -- "$file"
  done < <(find "$bundle" -mindepth 1 -maxdepth 1 -type f -print | LC_ALL=C sort)
  rmdir -- "$bundle"
  deleted=$((deleted + 1))
  deleted_bytes=$((deleted_bytes + bundle_bytes))
done < "$plan"

free_after=$(df -PB1 "$backup_root" | awk 'NR==2 {print $4}')
printf 'cleanup_mode=apply\ncleanup_status=completed\nplan_sha256=%s\ndeleted_count=%s\ndeleted_bytes=%s\nfree_before=%s\nfree_after=%s\n' "$plan_sha" "$deleted" "$deleted_bytes" "$free_before" "$free_after"
