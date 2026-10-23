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

report_masked() {
  set +e
  printf 'backup_units_masked=true\n'
  exit 0
}

validate_state_path() {
  local base expected root_real
  [[ -d $state_root && ! -L $state_root ]] || return 1
  _unit_required_1=$(realpath -e -- "$state_root") || return 1
  root_real=${_unit_required_1}
  [[ $root_real == "$state_root" ]] || return 1
  _unit_required_2=$(basename -- "$state_dir") || return 1
  base=${_unit_required_2}
  [[ $base =~ ^release181-state-[0-9]{8}T[0-9]{6}Z$ || $base =~ ^(182|187|191|192|194|195|197|198|199|202|206|207|208|209|210|212|213|215|232|233|234|235|236|237|238|239|240|241|242|243|244|245|246|247|248|249|250|251|252|253|254|255|256|257|258|259|260|261|262|263|264|265|266|267|268|269|270|271|272|273|274|275)-[0-9a-f]{12}-[0-9]+-[0-9a-f]{8}$ ]] || return 1
  expected="$state_root/$base"
  _unit_required_3=$(dirname -- "$state_dir") || return 1
  [[ $state_dir == "$expected" && ${_unit_required_3} == "$state_root" ]] || return 1
  if [[ -e $state_dir || -L $state_dir ]]; then
    [[ -d $state_dir && ! -L $state_dir ]] || return 1
    _unit_required_4=$(realpath -e -- "$state_dir") || return 1
    [[ ${_unit_required_4} == "$expected" ]] || return 1
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
      _unit_required_5=$(cat -- "$state_dir/$unit.enabled") || failed=1
      enabled_state=${_unit_required_5}
      _unit_required_6=$(cat -- "$state_dir/$unit.active") || failed=1
      active_state=${_unit_required_6}
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
      _unit_required_7=$(sha256sum "$unit_path" 2>/dev/null | awk '{print $1}') || failed=1
      _unit_required_8=$(sha256sum "$state_dir/$unit" | awk '{print $1}') || failed=1
      [[ ${_unit_required_7} == ${_unit_required_8} ]] || failed=1
      _unit_required_9=$(cat -- "$state_dir/$unit.enabled") || failed=1
      enabled_state=${_unit_required_9}
      _unit_required_10=$(cat -- "$state_dir/$unit.active") || failed=1
      active_state=${_unit_required_10}
      _unit_required_11=$(observed_unit_state is-enabled "$unit") || failed=1
      actual_state=${_unit_required_11}
      [[ $enabled_state != enabled || $actual_state == enabled ]] || failed=1
      _unit_required_12=$(observed_unit_state is-active "$unit") || failed=1
      actual_state=${_unit_required_12}
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
  [[ -f $state_dir/backup-units.sha256 && ! -L $state_dir/backup-units.sha256 ]] || return 1
  for unit in "${units[@]}"; do
    [[ -f $state_dir/$unit && ! -L $state_dir/$unit ]] || return 1
    [[ -f $state_dir/$unit.enabled && ! -L $state_dir/$unit.enabled ]] || return 1
    [[ -f $state_dir/$unit.active && ! -L $state_dir/$unit.active ]] || return 1
  done
  _unit_required_13=$(find "$state_dir" -mindepth 1 -maxdepth 1 -type l -print -quit) || return 1
  [[ -z ${_unit_required_13} ]] || return 1
  (cd "$state_dir" && sha256sum -c backup-units.sha256 >/dev/null)
}

verify_masked() {
  local unit
  for unit in "${units[@]}"; do
    _unit_required_14=$(observed_unit_state is-active "$unit") || return 1
    [[ ${_unit_required_14} != active ]] || return 1
    _unit_required_15=$(observed_unit_state is-enabled "$unit") || return 1
    [[ ${_unit_required_15} == masked ]] || return 1
    [[ -L /etc/systemd/system/$unit ]] || return 1
    _unit_required_16=$(readlink -f "/etc/systemd/system/$unit") || return 1
    [[ ${_unit_required_16} == /dev/null ]] || return 1
  done
}

validate_state_path
_unit_required_17=$(basename -- "$state_dir") || exit 1
staging_dir="$state_root/.${_unit_required_17}.staging.$$"
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
  _unit_required_18=$(realpath -e -- "$staging_dir") || exit 1
  [[ ${_unit_required_18} == "$staging_dir" ]]
  for unit in "${units[@]}"; do
    unit_path="/etc/systemd/system/$unit"
    [[ -f $unit_path && ! -L $unit_path ]]
    install -m 600 "$unit_path" "$staging_dir/$unit"
    _unit_required_19=$(observed_unit_state is-enabled "$unit") || exit 1
    enabled_state=${_unit_required_19}
    _unit_required_20=$(observed_unit_state is-active "$unit") || exit 1
    active_state=${_unit_required_20}
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
      _unit_required_21=$(readlink -f "$unit_path") || failed=1
      [[ -L $unit_path && ${_unit_required_21} == /dev/null ]] || failed=1
      _unit_required_22=$(observed_unit_state is-active "$unit") || failed=1
      [[ ${_unit_required_22} != active ]] || failed=1
      _unit_required_23=$(observed_unit_state is-enabled "$unit") || failed=1
      [[ ${_unit_required_23} == masked ]] || failed=1
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
_unit_required_24=$(date -u +%Y-%m-%dT%H:%M:%SZ) || exit 1
printf 'masked_at=%s\n' "${_unit_required_24}" > "$marker_tmp"
[[ ! -e $state_dir/masked.committed && ! -L $state_dir/masked.committed ]]
mv -T -- "$marker_tmp" "$state_dir/masked.committed"
trap - ERR EXIT
report_masked
