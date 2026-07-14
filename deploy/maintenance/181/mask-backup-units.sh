#!/usr/bin/env bash
set -Eeuo pipefail

state_root=${STATE_ROOT:-/opt/sub2api/backups}
state_dir=${STATE_DIR:?STATE_DIR is required}
units=(sub2api-backup.service sub2api-backup.timer)

report_masked() {
  set +e
  printf 'backup_units_masked=true\n'
  exit 0
}

validate_state_path() {
  local base expected root_real
  [[ -d $state_root && ! -L $state_root ]]
  root_real=$(realpath -e -- "$state_root")
  [[ $root_real == "$state_root" ]]
  base=$(basename -- "$state_dir")
  [[ $base =~ ^release181-state-[0-9]{8}T[0-9]{6}Z$ || $base =~ ^182-[0-9a-f]{12}-[0-9]+-[0-9a-f]{8}$ ]]
  expected="$state_root/$base"
  [[ $state_dir == "$expected" && $(dirname -- "$state_dir") == "$state_root" ]]
  if [[ -e $state_dir || -L $state_dir ]]; then
    [[ -d $state_dir && ! -L $state_dir ]]
    [[ $(realpath -e -- "$state_dir") == "$expected" ]]
  fi
}

restore_original_state() {
  (
    local unit enabled_state active_state unit_path actual_state
    local failed=0
    set +e
    systemctl stop "${units[@]}" || failed=1
    for unit in "${units[@]}"; do
      unit_path="/etc/systemd/system/$unit"
      rm -f -- "$unit_path" || failed=1
      install -m 644 "$state_dir/$unit" "$unit_path" || failed=1
    done
    systemctl daemon-reload || failed=1
    for unit in "${units[@]}"; do
      enabled_state=$(<"$state_dir/$unit.enabled")
      active_state=$(<"$state_dir/$unit.active")
      case "$enabled_state" in
        enabled) systemctl enable "$unit" >/dev/null || failed=1 ;;
        disabled) systemctl disable "$unit" >/dev/null || failed=1 ;;
        static|indirect|generated|alias) ;;
        *) failed=1 ;;
      esac
      if [[ $active_state == active ]]; then
        systemctl start "$unit" || failed=1
      else
        systemctl stop "$unit" || failed=1
      fi
    done
    for unit in "${units[@]}"; do
      unit_path="/etc/systemd/system/$unit"
      [[ -f $unit_path && ! -L $unit_path ]] || failed=1
      [[ $(sha256sum "$unit_path" 2>/dev/null | awk '{print $1}') == $(sha256sum "$state_dir/$unit" | awk '{print $1}') ]] || failed=1
      enabled_state=$(<"$state_dir/$unit.enabled")
      active_state=$(<"$state_dir/$unit.active")
      actual_state=$(systemctl is-enabled "$unit" 2>/dev/null || true)
      [[ $enabled_state != enabled || $actual_state == enabled ]] || failed=1
      actual_state=$(systemctl is-active "$unit" 2>/dev/null || true)
      if [[ $active_state == active ]]; then
        [[ $actual_state == active ]] || failed=1
      else
        [[ $actual_state != active ]] || failed=1
      fi
    done
    (( failed == 0 ))
  )
}

verify_snapshot() {
  local unit
  [[ -f $state_dir/backup-units.sha256 && ! -L $state_dir/backup-units.sha256 ]]
  for unit in "${units[@]}"; do
    [[ -f $state_dir/$unit && ! -L $state_dir/$unit ]]
    [[ -f $state_dir/$unit.enabled && ! -L $state_dir/$unit.enabled ]]
    [[ -f $state_dir/$unit.active && ! -L $state_dir/$unit.active ]]
  done
  [[ -z $(find "$state_dir" -mindepth 1 -maxdepth 1 -type l -print -quit) ]]
  (cd "$state_dir" && sha256sum -c backup-units.sha256 >/dev/null)
}

verify_masked() {
  local unit
  for unit in "${units[@]}"; do
    [[ $(systemctl is-active "$unit" 2>/dev/null || true) != active ]]
    [[ $(systemctl is-enabled "$unit" 2>/dev/null || true) == masked ]]
    [[ -L /etc/systemd/system/$unit ]]
    [[ $(readlink -f "/etc/systemd/system/$unit") == /dev/null ]]
  done
}

validate_state_path
staging_dir="$state_root/.$(basename -- "$state_dir").staging.$$"
[[ ! -e $staging_dir && ! -L $staging_dir ]]

if [[ -d $state_dir ]]; then
  verify_snapshot
  if [[ -f $state_dir/masked.committed ]]; then
    verify_masked
    report_masked
  fi
  restore_original_state
else
  cleanup_staging() {
    trap - ERR EXIT
    rm -f -- "$staging_dir"/* 2>/dev/null || true
    rmdir -- "$staging_dir" 2>/dev/null || true
  }
  trap cleanup_staging ERR EXIT
  install -d -m 700 "$staging_dir"
  [[ $(realpath -e -- "$staging_dir") == "$staging_dir" ]]
  for unit in "${units[@]}"; do
    unit_path="/etc/systemd/system/$unit"
    [[ -f $unit_path && ! -L $unit_path ]]
    install -m 600 "$unit_path" "$staging_dir/$unit"
    enabled_state=$(systemctl is-enabled "$unit" 2>/dev/null || true)
    active_state=$(systemctl is-active "$unit" 2>/dev/null || true)
    [[ $enabled_state =~ ^(enabled|disabled|static|indirect|generated|alias)$ ]]
    [[ $active_state =~ ^(active|inactive|failed|activating|deactivating)$ ]]
    printf '%s\n' "$enabled_state" > "$staging_dir/$unit.enabled"
    printf '%s\n' "$active_state" > "$staging_dir/$unit.active"
  done
  (cd "$staging_dir" && sha256sum "${units[@]}" "${units[@]/%/.enabled}" "${units[@]/%/.active}" > backup-units.sha256)
  (cd "$staging_dir" && sha256sum -c backup-units.sha256 >/dev/null)
  mv -T -- "$staging_dir" "$state_dir"
  [[ -d $state_dir && ! -L $state_dir && ! -e $staging_dir ]]
  trap - ERR EXIT
fi

rollback_mask() {
  local rc=$?
  trap - ERR EXIT
  if ! restore_original_state; then
    printf 'failed to restore backup units; forcing fail-closed mask\n' >&2
    if ! force_mask_state; then
      printf 'failed to force fail-closed backup unit mask\n' >&2
      exit 126
    fi
    exit 125
  fi
  exit "$rc"
}

force_mask_state() {
  (
    local unit unit_path failed=0
    set +e
    systemctl stop "${units[@]}" || failed=1
    for unit in "${units[@]}"; do
      unit_path="/etc/systemd/system/$unit"
      rm -f -- "$unit_path" || failed=1
      ln -s /dev/null "$unit_path" || failed=1
    done
    systemctl daemon-reload || failed=1
    for unit in "${units[@]}"; do
      unit_path="/etc/systemd/system/$unit"
      [[ -L $unit_path && $(readlink -f "$unit_path") == /dev/null ]] || failed=1
      [[ $(systemctl is-active "$unit" 2>/dev/null || true) != active ]] || failed=1
      [[ $(systemctl is-enabled "$unit" 2>/dev/null || true) == masked ]] || failed=1
    done
    (( failed == 0 ))
  )
}
trap rollback_mask ERR EXIT

systemctl stop "${units[@]}"
for unit in "${units[@]}"; do
  unit_path="/etc/systemd/system/$unit"
  rm -f -- "$unit_path"
  ln -s /dev/null "$unit_path"
done
systemctl daemon-reload
verify_masked

marker_tmp="$state_dir/.masked.committed.tmp.$$"
printf 'masked_at=%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" > "$marker_tmp"
[[ ! -e $state_dir/masked.committed && ! -L $state_dir/masked.committed ]]
mv -T -- "$marker_tmp" "$state_dir/masked.committed"
trap - ERR EXIT
report_masked
