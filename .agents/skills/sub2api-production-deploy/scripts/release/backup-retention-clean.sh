#!/usr/bin/env bash
set -Eeuo pipefail
umask 077

mode=${1:-dry-run}
plan_sha256=${2:-}
backup_root=${BACKUP_ROOT:-/srv/sub2api-backups}
minimum_free_bytes=${MINIMUM_FREE_BYTES:-5368709120}
keep_daily=${KEEP_DAILY:-2}

[[ $mode == dry-run || $mode == apply ]]
[[ $backup_root == /srv/sub2api-backups ]]
[[ $keep_daily =~ ^[0-9]+$ && $keep_daily -ge 2 ]]
[[ $minimum_free_bytes =~ ^[0-9]+$ ]]
[[ -d $backup_root && ! -L $backup_root ]]
[[ $(realpath -e -- "$backup_root") == "$backup_root" ]]
daily="$backup_root/daily"
[[ -d $daily && ! -L $daily ]]
daily_owner=$(stat -c '%U:%G' "$daily")
[[ $daily_owner =~ ^[A-Za-z_][A-Za-z0-9_-]*:[A-Za-z_][A-Za-z0-9_-]*$ ]]
promotion_lock="$backup_root/.release-promotion.lock"
[[ -f $promotion_lock && ! -L $promotion_lock && $(stat -c '%h' "$promotion_lock") == 1 ]]

work=$(mktemp -d /tmp/sub2api-backup-retention.XXXXXX)
cleanup() { rm -rf -- "$work"; }
trap cleanup EXIT
entries="$work/entries"
plan="$work/plan"
: > "$entries"
: > "$plan"

for artifact in "$daily"/*.tar.age; do
  [[ -e $artifact ]] || continue
  [[ -f $artifact && ! -L $artifact && $(stat -c '%h' "$artifact") == 1 ]]
  checksum="$artifact.sha256"
  [[ -f $checksum && ! -L $checksum && $(stat -c '%h' "$checksum") == 1 ]]
  [[ $(stat -c '%U:%G' "$artifact") == "$daily_owner" ]]
  [[ $(stat -c '%U:%G' "$checksum") == "$daily_owner" ]]
  name=${artifact##*/}
  [[ $name =~ ^sub2api-[0-9]{8}T[0-9]{6}Z\.tar\.age$ ]]
  expected=$(sha256sum "$artifact" | awk '{print $1}')
  [[ $(cat "$checksum") == "$expected  $name" || $(cat "$checksum") == "$expected *$name" ]]
  printf '%s\t%s\t%s\t%s\n' \
    "$(stat -c '%Y' "$artifact")" "$name" "$(stat -c '%s' "$artifact")" "$expected" >> "$entries"
done

LC_ALL=C sort -nr "$entries" > "$work/sorted"
count=$(wc -l < "$work/sorted" | tr -d '[:space:]')
if (( count > keep_daily )); then
  tail -n +$((keep_daily + 1)) "$work/sorted" | while IFS=$'\t' read -r mtime name bytes digest; do
    artifact="$daily/$name"
    checksum="$artifact.sha256"
    printf '%s\t%s\t%s\t%s\n' "$artifact" "$bytes" "$mtime" "$digest" >> "$plan"
    printf '%s\t%s\t%s\t%s\n' "$checksum" "$(stat -c '%s' "$checksum")" "$(stat -c '%Y' "$checksum")" "$(sha256sum "$checksum" | awk '{print $1}')" >> "$plan"
  done
fi

plan_sha=$(sha256sum "$plan" | awk '{print $1}')
candidate_count=$(awk 'NF {n++} END {print n+0}' "$plan")
candidate_bytes=$(awk -F '\t' '{s += $2} END {printf "%.0f\n", s+0}' "$plan")
free_before=$(df -PB1 "$backup_root" | awk 'NR==2 {print $4}')

if [[ $mode == apply ]]; then
  [[ $plan_sha256 =~ ^[0-9a-f]{64}$ && $plan_sha256 == "$plan_sha" ]]
  lock_file="$backup_root/.backup-retention.lock"
  [[ ! -e $lock_file || ! -L $lock_file ]]
  exec 9>"$lock_file"
  [[ $(stat -c '%h' "$lock_file") == 1 ]]
  flock -n 9
  while IFS=$'\t' read -r path bytes mtime digest; do
    [[ -f $path && ! -L $path && $(stat -c '%h' "$path") == 1 ]]
    [[ $(stat -c '%s' "$path") == "$bytes" ]]
    [[ $(sha256sum "$path" | awk '{print $1}') == "$digest" ]]
    rm -- "$path"
  done < "$plan"
  free_after=$(df -PB1 "$backup_root" | awk 'NR==2 {print $4}')
  (( free_after >= minimum_free_bytes ))
  printf 'cleanup_mode=apply\ncleanup_status=completed\nplan_sha256=%s\ncandidate_count=%s\ncandidate_bytes=%s\nfree_before=%s\nfree_after=%s\n' \
    "$plan_sha" "$candidate_count" "$candidate_bytes" "$free_before" "$free_after"
else
  printf 'cleanup_mode=dry-run\ncleanup_status=ready\nplan_sha256=%s\ncandidate_count=%s\ncandidate_bytes=%s\nfree_before=%s\nfree_after=%s\n' \
    "$plan_sha" "$candidate_count" "$candidate_bytes" "$free_before" "$free_before"
fi
