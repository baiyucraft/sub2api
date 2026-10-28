#!/usr/bin/env bash
set -Eeuo pipefail

state_root=${STATE_ROOT:-/opt/sub2api/backups}
state_dir=${STATE_DIR:?STATE_DIR is required}
units=(sub2api-backup.service sub2api-backup.timer)

# Validate systemctl's value together with its documented exit status. A failed
# observer must never be treated as a stopped, disabled or masked unit.
observed_unit_state() {
  local _unit_value _unit_code=0
  _unit_value=$(systemctl "$@" 2>/dev/null) || _unit_code=$?
  case "$1:$_unit_value:$_unit_code" in
    is-active:active:0|is-active:inactive:3|is-active:failed:3|is-active:activating:3|is-active:deactivating:3|is-enabled:enabled:0|is-enabled:disabled:1|is-enabled:static:0|is-enabled:indirect:0|is-enabled:generated:0|is-enabled:alias:0|is-enabled:masked:1) ;;
    *) return 1 ;;
  esac
  printf '%s\n' "$_unit_value"
}

report_restored() {
  set +e
  printf 'backup_units_restored=true\n'
  exit 0
}

_unit_required_1=$(basename -- "$state_dir") || exit 1
base=${_unit_required_1}
[[ -d $state_root && ! -L $state_root ]]
_unit_required_2=$(realpath -e -- "$state_root") || exit 1
[[ ${_unit_required_2} == "$state_root" ]]
# Profile admission belongs to the signed release context; this helper validates path syntax only.
[[ $base != *$'\n'* && $base != *$'\r'* && ( $base =~ ^release181-state-[0-9]{8}T[0-9]{6}Z$ || $base =~ ^[1-9][0-9]*-[0-9a-f]{12}-[0-9]+-[0-9a-f]{8}$ ) ]]
_unit_required_3=$(dirname -- "$state_dir") || exit 1
[[ $state_dir == "$state_root/$base" && ${_unit_required_3} == "$state_root" ]]
[[ -d $state_dir && ! -L $state_dir ]]
_unit_required_4=$(realpath -e -- "$state_dir") || exit 1
[[ ${_unit_required_4} == "$state_root/$base" ]]
[[ -f $state_dir/masked.committed && ! -L $state_dir/masked.committed ]]
[[ -f $state_dir/backup-units.sha256 && ! -L $state_dir/backup-units.sha256 ]]
for unit in "${units[@]}"; do
  [[ -f $state_dir/$unit && ! -L $state_dir/$unit ]]
  [[ -f $state_dir/$unit.enabled && ! -L $state_dir/$unit.enabled ]]
  [[ -f $state_dir/$unit.active && ! -L $state_dir/$unit.active ]]
done
_unit_required_5=$(find "$state_dir" -mindepth 1 -maxdepth 1 -type l -print -quit) || exit 1
[[ -z ${_unit_required_5} ]]
(cd "$state_dir" && sha256sum -c backup-units.sha256 >/dev/null)

verify_restored() {
  local unit enabled_state active_state unit_path
  for unit in "${units[@]}"; do
    unit_path="/etc/systemd/system/$unit"
    [[ -f $unit_path && ! -L $unit_path ]] || return 1
    _unit_required_6=$(sha256sum "$unit_path" | awk '{print $1}') || return 1
    _unit_required_7=$(sha256sum "$state_dir/$unit" | awk '{print $1}') || return 1
    [[ ${_unit_required_6} == ${_unit_required_7} ]] || return 1
    _unit_required_8=$(cat -- "$state_dir/$unit.enabled") || return 1
    enabled_state=${_unit_required_8}
    _unit_required_9=$(cat -- "$state_dir/$unit.active") || return 1
    active_state=${_unit_required_9}
    if [[ $enabled_state == enabled ]]; then
      _unit_required_10=$(observed_unit_state is-enabled "$unit") || return 1
      [[ ${_unit_required_10} == enabled ]] || return 1
    fi
    if [[ $active_state == active ]]; then
      _unit_required_11=$(observed_unit_state is-active "$unit") || return 1
      [[ ${_unit_required_11} == active ]] || return 1
    else
      _unit_required_12=$(observed_unit_state is-active "$unit") || return 1
      [[ ${_unit_required_12} != active ]] || return 1
    fi
  done
}

if [[ -f $state_dir/restored.committed ]]; then
  verify_restored
  report_restored
fi

for unit in "${units[@]}"; do
  unit_path="/etc/systemd/system/$unit"
  _unit_required_13=$(readlink -f "$unit_path") || exit 1
  [[ -L $unit_path && ${_unit_required_13} == /dev/null ]]
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
    _unit_required_14=$(readlink -f "$unit_path") || failed=1
    [[ -L $unit_path && ${_unit_required_14} == /dev/null ]] || failed=1
    _unit_required_15=$(observed_unit_state is-active "$unit") || failed=1
    [[ ${_unit_required_15} != active ]] || failed=1
    _unit_required_16=$(observed_unit_state is-enabled "$unit") || failed=1
    [[ ${_unit_required_16} == masked ]] || failed=1
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
  _unit_required_17=$(cat -- "$state_dir/$unit.enabled") || exit 1
  enabled_state=${_unit_required_17}
  _unit_required_18=$(cat -- "$state_dir/$unit.active") || exit 1
  active_state=${_unit_required_18}
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
_unit_required_19=$(date -u +%Y-%m-%dT%H:%M:%SZ) || exit 1
printf 'restored_at=%s\n' "${_unit_required_19}" > "$marker_tmp"
[[ ! -e $state_dir/restored.committed && ! -L $state_dir/restored.committed ]]
mv -T -- "$marker_tmp" "$state_dir/restored.committed"
trap - ERR EXIT
report_restored
