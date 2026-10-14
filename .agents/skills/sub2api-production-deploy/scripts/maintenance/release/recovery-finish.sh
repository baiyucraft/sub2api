#!/usr/bin/env bash
set -Eeuo pipefail

release_dir=${RELEASE_DIR:?RELEASE_DIR is required}
bundle_assets_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
export RECOVERY_ASSETS_DIR=$bundle_assets_dir
if [[ -d $release_dir/.recovered && ! -L $release_dir/.recovered ]]; then
  export ACTIVE_CLAIM="$release_dir/.recovered"
fi
source "$bundle_assets_dir/context.sh"
source "$bundle_assets_dir/recovery-checkpoint.sh"
if [[ -n ${RECOVERY_INTERRUPT_AFTER_PHASE:-} ]]; then source "$bundle_assets_dir/recovery-isolation.sh"; fi
unset COMPOSE_FILE COMPOSE_PROFILES COMPOSE_ENV_FILES COMPOSE_PATH_SEPARATOR DOCKER_HOST DOCKER_CONTEXT
export RECOVERY_HELPER_BUNDLE_SHA256=${RECOVERY_HELPER_BUNDLE_SHA256:?verified helper bundle SHA is required}
export RELEASE_DIR=$release_dir
if [[ -n ${RECOVERY_DRILL_AUTHORIZATION:-} ]]; then
  # context.sh must have authenticated the distinct offline domain and run
  # the real isolation guard before the Gate-free Guest path can proceed.
  [[ ${recovery_drill_context_verified:-} == 1 && ${RECOVERY_DRILL_MODE:-} == 1 ]] || exit 1
  export RECOVERY_DRILL_AUTHORIZATION
  unset RECOVERY_HELPER_GATE RECOVERY_HELPER_SIGNATURE
else
  export RECOVERY_HELPER_GATE=${RECOVERY_HELPER_GATE:?signed helper authorization Gate is required}
  export RECOVERY_HELPER_SIGNATURE=${RECOVERY_HELPER_SIGNATURE:?helper authorization signature is required}
fi

recovery_checkpoint_assert_directory "$release_dir"
checkpoint_root="$release_dir/recovery-checkpoints"
recovery_checkpoint_assert_directory "$checkpoint_root"
recovery_checkpoint_assert_file "$checkpoint_root/identity"
# The existing bound identity survives plaintext cleanup. Do not invent a new
# point or image identity from current runtime state after deleting the dump.
point_sha=$(sed -n 's/^recovery_point_sha256=//p' "$checkpoint_root/identity")
pre_image_id=$(sed -n 's/^pre_image_id=//p' "$checkpoint_root/identity")
recovery_checkpoint_init "$checkpoint_root" "$release_id" "$point_sha" "$pre_image_id" "$RECOVERY_HELPER_BUNDLE_SHA256"
recovery_checkpoint_completed nginx_restored
if [[ -e $state_dir/recovery-point.tar || -L $state_dir/recovery-point.tar ]]; then
  [[ -f $state_dir/recovery-point.tar && ! -L $state_dir/recovery-point.tar ]]
  [[ $(sha256sum "$state_dir/recovery-point.tar" | awk '{print $1}') == "$point_sha" ]]
fi

log_directory="$release_dir/logs"
if [[ ! -e $log_directory && ! -L $log_directory ]]; then install -d -o root -g root -m 700 -- "$log_directory"; fi
recovery_checkpoint_assert_directory "$log_directory"
raw_log=${SUB2API_RELEASE_RAW_LOG:-$log_directory/production.raw.log}
[[ $raw_log == "$log_directory/production.raw.log" ]]
if [[ ! -e $raw_log && ! -L $raw_log ]]; then install -o root -g root -m 600 /dev/null "$raw_log"; fi
[[ -f $raw_log && ! -L $raw_log && $(stat -c '%u:%g:%a:%h' "$raw_log") == 0:0:600:1 ]]
exec 2>>"$raw_log"
exec 9>/run/lock/sub2api-backup-global.lock
flock -n 9

recovery_finish_run_helper() {
  local helper=${1:?helper basename is required}
  case "$helper" in restore-backup-units.sh|cleanup-state.sh|reconcile.sh) ;; *) return 1 ;; esac
  [[ -f $bundle_assets_dir/$helper && ! -L $bundle_assets_dir/$helper ]] || return 1
  STATE_ROOT=/opt/sub2api/backups/release-state STATE_DIR="$state_dir" \
    RELEASE_LOCK_HELD=true bash "$bundle_assets_dir/$helper" >>"$raw_log" 2>&1
}

recovery_finish_verify_application() {
  [[ -f $active_slot_file && ! -L $active_slot_file ]] || return 1
  [[ $(stat -c '%u:%g:%a:%h' "$active_slot_file") == 0:0:600:1 ]] || return 1
  local container port image
  container=$(sed -n 's/^container=//p' "$active_slot_file")
  port=$(sed -n 's/^port=//p' "$active_slot_file")
  image=$(sed -n 's/^image_id=//p' "$active_slot_file")
  [[ $container == sub2api && ($port == 18080 || $port == 18081) && $image == "$pre_image_id" ]] || return 1
  [[ $(docker inspect -f '{{.Image}}' "$container") == "$pre_image_id" ]] || return 1
  [[ $(docker inspect -f '{{.State.Health.Status}}' "$container") == healthy ]] || return 1
  [[ $(systemctl is-active nginx 2>/dev/null) == active ]] || return 1
  nginx -t >/dev/null 2>&1 || return 1
  recovery_checkpoint_assert_file "$checkpoint_root/nginx.fingerprint" || return 1
  local current_nginx_fingerprint
  current_nginx_fingerprint=$(nginx -T 2>&1 | sha256sum | awk '{print $1}') || return 1
  [[ $current_nginx_fingerprint == "$(<"$checkpoint_root/nginx.fingerprint")" ]] || return 1
  local upstream=${NGINX_MANAGED_UPSTREAM:-/etc/nginx/conf.d/sub2api-release-upstream.conf}
  [[ -f $upstream && ! -L $upstream ]] || return 1
  grep -Fq "server 127.0.0.1:$port;" "$upstream" || return 1
}

recovery_finish_backup_fingerprint() (
  set -Eeuo pipefail
  local unit path enabled active digest
  for unit in sub2api-backup.service sub2api-backup.timer; do
    path="/etc/systemd/system/$unit"
    [[ -f $path && ! -L $path ]] || return 1
    [[ $(stat -c '%u:%g:%a:%h' "$path") == 0:0:644:1 ]] || return 1
    digest=$(sha256sum "$path" | awk '{print $1}') || return 1
    enabled=$(systemctl is-enabled "$unit" 2>/dev/null || true)
    active=$(systemctl is-active "$unit" 2>/dev/null || true)
    [[ $enabled =~ ^(enabled|disabled|static|indirect|generated|alias)$ ]] || return 1
    [[ $active =~ ^(active|inactive|failed)$ ]] || return 1
    printf '%s|%s|%s|%s\n' "$unit" "$digest" "$enabled" "$active"
  done
)

recovery_finish_apply_backup_units() {
  recovery_finish_run_helper restore-backup-units.sh || return 1
  local restored_fingerprint
  restored_fingerprint=$(recovery_finish_backup_fingerprint) || return 1
  recovery_checkpoint_store backup-units.fingerprint "$restored_fingerprint" || return 1
}
recovery_finish_verify_backup_units() {
  recovery_finish_verify_application || return 1
  [[ $(systemctl is-active sub2api-backup.service 2>/dev/null || true) != active ]] || return 1
  [[ $(systemctl is-enabled sub2api-backup.timer 2>/dev/null || true) == enabled ]] || return 1
  [[ $(systemctl is-active sub2api-backup.timer 2>/dev/null || true) == active ]] || return 1
  if [[ ! -e $checkpoint_root/backup-units.fingerprint && ! -L $checkpoint_root/backup-units.fingerprint ]]; then
    # A process can die after the unit helper's atomic commit but before the
    # checkpoint write. Its committed branch verifies without restoring again.
    [[ ! -e $checkpoint_root/backup_units_restored.done && ! -L $checkpoint_root/backup_units_restored.done ]] || return 1
    [[ -f $state_dir/restored.committed && ! -L $state_dir/restored.committed ]] || return 1
    recovery_finish_run_helper restore-backup-units.sh || return 1
    local adopted_fingerprint
    adopted_fingerprint=$(recovery_finish_backup_fingerprint) || return 1
    recovery_checkpoint_store backup-units.fingerprint "$adopted_fingerprint" || return 1
  fi
  recovery_checkpoint_assert_file "$checkpoint_root/backup-units.fingerprint" || return 1
  local current_fingerprint
  current_fingerprint=$(recovery_finish_backup_fingerprint) || return 1
  [[ $current_fingerprint == "$(<"$checkpoint_root/backup-units.fingerprint")" ]] || return 1
}

recovery_finish_apply_state_cleanup() {
  recovery_finish_run_helper cleanup-state.sh || return 1
}
recovery_finish_verify_state_cleanup() {
  recovery_finish_verify_backup_units || return 1
  recovery_checkpoint_assert_file "$active_claim/plaintext-cleaned" || return 1
  if [[ -e $state_dir || -L $state_dir ]]; then
    recovery_checkpoint_assert_directory "$state_dir" || return 1
    local file
    while IFS= read -r -d '' file; do
      case "${file##*/}" in
        recovery-point.age|recovery-point.age.sha256|pre-image-id|backup-result|backup-result.sha256|backup-failure) ;;
        *) return 1 ;;
      esac
      [[ -f $file && ! -L $file ]] || return 1
      case "$(stat -c '%u:%g:%a:%h' "$file")" in 0:0:400:1|0:0:600:1) ;; *) return 1 ;; esac
    done < <(find "$state_dir" -mindepth 1 -maxdepth 1 -print0)
    [[ -f $state_dir/recovery-point.age && -f $state_dir/recovery-point.age.sha256 ]] || return 1
    (cd -- "$state_dir" && sha256sum -c recovery-point.age.sha256 >/dev/null) || return 1
    [[ $(<"$state_dir/pre-image-id") == "$pre_image_id" ]] || return 1
  fi
}

recovery_finish_apply_claim_reconcile() {
  [[ $active_claim == /opt/sub2api/releases/.active-release ]] || return 1
  recovery_finish_run_helper reconcile.sh || return 1
  active_claim="$release_dir/.recovered"
  export ACTIVE_CLAIM=$active_claim
}
recovery_finish_verify_claim_reconcile() {
  [[ -d $release_dir/.recovered && ! -L $release_dir/.recovered ]] || return 1
  [[ ! -e /opt/sub2api/releases/.active-release && ! -L /opt/sub2api/releases/.active-release ]] || return 1
  active_claim="$release_dir/.recovered"
  recovery_checkpoint_assert_file "$active_claim/marker" || return 1
  grep -Fxq "release_id=$release_id" "$active_claim/marker" || return 1
  recovery_finish_verify_state_cleanup || return 1
}

# Invoke directly. Callbacks also propagate failures explicitly when a replay
# caller uses a conditional, and verifiers never modify application data.
recovery_finish_verify_application
recovery_checkpoint_run backup_units_restored recovery_finish_verify_backup_units recovery_finish_apply_backup_units
recovery_checkpoint_run state_cleanup recovery_finish_verify_state_cleanup recovery_finish_apply_state_cleanup
recovery_checkpoint_run claim_reconciled recovery_finish_verify_claim_reconcile recovery_finish_apply_claim_reconcile
printf 'backup_units_restored=true\nplaintext_state_removed=true\nrelease_claim_reconciled=true\n'
printf 'recovery_helper_bundle_sha256=%s\n' "$RECOVERY_HELPER_BUNDLE_SHA256"
