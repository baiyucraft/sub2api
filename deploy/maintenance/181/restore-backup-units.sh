#!/usr/bin/env bash
set -Eeuo pipefail

state_root=${STATE_ROOT:-/opt/sub2api/backups}
state_dir=${STATE_DIR:?STATE_DIR is required}
units=(sub2api-backup.service sub2api-backup.timer)

report_restored() {
  set +e
  printf 'backup_units_restored=true\n'
  exit 0
}

base=$(basename -- "$state_dir")
[[ -d $state_root && ! -L $state_root ]]
[[ $(realpath -e -- "$state_root") == "$state_root" ]]
[[ $base =~ ^release181-state-[0-9]{8}T[0-9]{6}Z$ || $base =~ ^182-[0-9a-f]{12}-[0-9]+-[0-9a-f]{8}$ ]]
[[ $state_dir == "$state_root/$base" && $(dirname -- "$state_dir") == "$state_root" ]]
[[ -d $state_dir && ! -L $state_dir ]]
[[ $(realpath -e -- "$state_dir") == "$state_root/$base" ]]
[[ -f $state_dir/masked.committed && ! -L $state_dir/masked.committed ]]
[[ -f $state_dir/backup-units.sha256 && ! -L $state_dir/backup-units.sha256 ]]
for unit in "${units[@]}"; do
  [[ -f $state_dir/$unit && ! -L $state_dir/$unit ]]
  [[ -f $state_dir/$unit.enabled && ! -L $state_dir/$unit.enabled ]]
  [[ -f $state_dir/$unit.active && ! -L $state_dir/$unit.active ]]
done
[[ -z $(find "$state_dir" -mindepth 1 -maxdepth 1 -type l -print -quit) ]]
(cd "$state_dir" && sha256sum -c backup-units.sha256 >/dev/null)

verify_restored() {
  local unit enabled_state active_state unit_path
  for unit in "${units[@]}"; do
    unit_path="/etc/systemd/system/$unit"
    [[ -f $unit_path && ! -L $unit_path ]]
    [[ $(sha256sum "$unit_path" | awk '{print $1}') == $(sha256sum "$state_dir/$unit" | awk '{print $1}') ]]
    enabled_state=$(<"$state_dir/$unit.enabled")
    active_state=$(<"$state_dir/$unit.active")
    if [[ $enabled_state == enabled ]]; then
      [[ $(systemctl is-enabled "$unit" 2>/dev/null || true) == enabled ]]
    fi
    if [[ $active_state == active ]]; then
      [[ $(systemctl is-active "$unit" 2>/dev/null || true) == active ]]
    else
      [[ $(systemctl is-active "$unit" 2>/dev/null || true) != active ]]
    fi
  done
}

if [[ -f $state_dir/restored.committed ]]; then
  verify_restored
  report_restored
fi

for unit in "${units[@]}"; do
  unit_path="/etc/systemd/system/$unit"
  [[ -L $unit_path && $(readlink -f "$unit_path") == /dev/null ]]
done

rollback_restore() {
  local rc=$?
  local unit unit_path failed=0
  trap - ERR EXIT
  set +e
  systemctl stop "${units[@]}" || failed=1
  for unit in "${units[@]}"; do
    unit_path="/etc/systemd/system/$unit"
    rm -f -- "/etc/systemd/system/.$unit.restore.$$" || failed=1
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
  if (( failed != 0 )); then
    printf 'failed to re-mask backup units after restore failure\n' >&2
    exit 125
  fi
  exit "$rc"
}
trap rollback_restore ERR EXIT

systemctl stop "${units[@]}"
for unit in "${units[@]}"; do
  unit_path="/etc/systemd/system/$unit"
  temp_path="/etc/systemd/system/.$unit.restore.$$"
  install -m 644 "$state_dir/$unit" "$temp_path"
  rm -f -- "$unit_path"
  mv -Tn -- "$temp_path" "$unit_path"
  [[ -f $unit_path && ! -L $unit_path && ! -e $temp_path ]]
done
systemctl daemon-reload

for unit in "${units[@]}"; do
  enabled_state=$(<"$state_dir/$unit.enabled")
  active_state=$(<"$state_dir/$unit.active")
  case "$enabled_state" in
    enabled) systemctl enable "$unit" >/dev/null ;;
    disabled) systemctl disable "$unit" >/dev/null ;;
    static|indirect|generated|alias) ;;
    *) false ;;
  esac
  if [[ $active_state == active ]]; then
    systemctl start "$unit"
  else
    systemctl stop "$unit"
  fi
done
verify_restored

marker_tmp="$state_dir/.restored.committed.tmp.$$"
printf 'restored_at=%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" > "$marker_tmp"
[[ ! -e $state_dir/restored.committed && ! -L $state_dir/restored.committed ]]
mv -T -- "$marker_tmp" "$state_dir/restored.committed"
trap - ERR EXIT
report_restored
