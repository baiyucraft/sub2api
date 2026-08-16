#!/usr/bin/env bash
set -Eeuo pipefail
umask 077

mode=${1:-dry-run}
plan_sha256=${2:-}
backup_root=${BACKUP_ROOT:-/srv/sub2api-backups}
journal_root=${JOURNAL_ROOT:-/var/log/journal}
journal_max_bytes=${JOURNAL_MAX_BYTES:-1073741824}
minimum_free_bytes=${MINIMUM_FREE_BYTES:-5368709120}
upload_reserve_bytes=${UPLOAD_RESERVE_BYTES:-536870912}

[[ $mode == dry-run || $mode == apply ]]
[[ $backup_root == /srv/sub2api-backups ]]
[[ $journal_root == /var/log/journal ]]
[[ $journal_max_bytes =~ ^[0-9]+$ && $journal_max_bytes -ge 268435456 ]]
[[ $minimum_free_bytes =~ ^[0-9]+$ ]]
[[ $upload_reserve_bytes =~ ^[0-9]+$ ]]
[[ -d $backup_root && ! -L $backup_root ]]
[[ -d $journal_root && ! -L $journal_root ]]
[[ $(realpath -e -- "$backup_root") == "$backup_root" ]]
[[ $(realpath -e -- "$journal_root") == "$journal_root" ]]

promotion_lock="$backup_root/.release-promotion.lock"
retention_lock="$backup_root/.backup-retention.lock"
host_cleanup_lock="$backup_root/.backup-host-space-clean.lock"
[[ -f $promotion_lock && ! -L $promotion_lock && $(stat -c '%h' "$promotion_lock") == 1 ]]
[[ ! -e $retention_lock || ! -L $retention_lock ]]
[[ ! -e $host_cleanup_lock || ! -L $host_cleanup_lock ]]

backup_device=$(stat -c '%d' "$backup_root")
journal_device=$(stat -c '%d' "$journal_root")
[[ $backup_device == "$journal_device" ]]

required_free_bytes=$((minimum_free_bytes + upload_reserve_bytes))
free_before=$(df -PB1 "$backup_root" | awk 'NR==2 {print $4}')
journal_before=$(du -sbx "$journal_root" | awk '{print $1}')
[[ $free_before =~ ^[0-9]+$ && $journal_before =~ ^[0-9]+$ ]]

plan_document=$(printf '%s\n' \
  'schema=backup-host-space-clean-v1' \
  'action=journal-vacuum' \
  "backup_device=$backup_device" \
  "journal_device=$journal_device" \
  "journal_max_bytes=$journal_max_bytes" \
  "minimum_free_bytes=$minimum_free_bytes" \
  "upload_reserve_bytes=$upload_reserve_bytes")
plan_sha=$(printf '%s' "$plan_document" | sha256sum | awk '{print $1}')

if [[ $mode == apply ]]; then
  [[ $plan_sha256 =~ ^[0-9a-f]{64}$ && $plan_sha256 == "$plan_sha" ]]
  exec 9>"$host_cleanup_lock"
  [[ -f $host_cleanup_lock && ! -L $host_cleanup_lock && $(stat -c '%h' "$host_cleanup_lock") == 1 ]]
  flock -n 9
  exec 8>"$promotion_lock"
  flock -n 8
  exec 7>"$retention_lock"
  [[ -f $retention_lock && ! -L $retention_lock && $(stat -c '%h' "$retention_lock") == 1 ]]
  flock -n 7

  journalctl --rotate >/dev/null
  journalctl --vacuum-size="$journal_max_bytes" >/dev/null

  free_after=$(df -PB1 "$backup_root" | awk 'NR==2 {print $4}')
  journal_after=$(du -sbx "$journal_root" | awk '{print $1}')
  [[ $free_after =~ ^[0-9]+$ && $journal_after =~ ^[0-9]+$ ]]
  (( free_after >= required_free_bytes ))
  (( journal_after <= journal_max_bytes + 134217728 ))
  printf 'cleanup_mode=apply\ncleanup_status=completed\nplan_sha256=%s\nfree_before=%s\nfree_after=%s\njournal_before=%s\njournal_after=%s\nrequired_free_bytes=%s\n' \
    "$plan_sha" "$free_before" "$free_after" "$journal_before" "$journal_after" "$required_free_bytes"
else
  reclaimable_bytes=0
  if (( journal_before > journal_max_bytes )); then
    reclaimable_bytes=$((journal_before - journal_max_bytes))
  fi
  projected_free_bytes=$((free_before + reclaimable_bytes))
  printf 'cleanup_mode=dry-run\ncleanup_status=ready\nplan_sha256=%s\nfree_before=%s\nprojected_free_bytes=%s\njournal_before=%s\njournal_target_bytes=%s\nrequired_free_bytes=%s\n' \
    "$plan_sha" "$free_before" "$projected_free_bytes" "$journal_before" "$journal_max_bytes" "$required_free_bytes"
fi
