#!/usr/bin/env bash
# Source-only recovery state machine. A completed marker is an attestation of a
# verified apply; every replay must still run the caller's live verifier.
# Cleanup must precede claim release, so a concurrent release cannot acquire
# the claim while recovery plaintext still exists.
RECOVERY_CHECKPOINT_PHASES=(compose_restored postgres_restored redis_restored app_healthy nginx_restored backup_units_restored state_cleanup claim_reconciled)

recovery_checkpoint_assert_directory() {
  local path=${1:?checkpoint directory is required}
  [[ $path == /* && -d $path && ! -L $path ]] || return 1
  [[ $(realpath -e -- "$path") == "$path" ]] || return 1
  [[ $(stat -c '%u:%g:%a' "$path") == 0:0:700 ]] || return 1
}

recovery_checkpoint_assert_file() {
  local path=${1:?checkpoint file is required}
  [[ -f $path && ! -L $path ]] || return 1
  [[ $(stat -c '%u:%g:%a:%h' "$path") == 0:0:400:1 ]] || return 1
}

recovery_checkpoint_init() {
  local root=${1:?checkpoint root is required}
  local release=${2:?release identity is required}
  local point=${3:?recovery point SHA is required}
  local image=${4:?pre image ID is required}
  local bundle=${5:?helper bundle SHA is required}
  [[ $release =~ ^[A-Za-z0-9_.-]{1,160}$ ]] || return 1
  [[ $point =~ ^[0-9a-f]{64}$ && $bundle =~ ^[0-9a-f]{64}$ && $image =~ ^sha256:[0-9a-f]{64}$ ]] || return 1
  [[ $root == /* && $root != *$'\n'* && $root != *'/../'* && $root != */.. ]] || return 1
  recovery_checkpoint_assert_directory "${root%/*}" || return 1
  if [[ ! -e $root && ! -L $root ]]; then install -d -o root -g root -m 700 -- "$root" || return 1; fi
  recovery_checkpoint_assert_directory "$root" || return 1
  RECOVERY_CHECKPOINT_ROOT=$root
  RECOVERY_CHECKPOINT_IDENTITY=$(printf 'schema=1\nrelease_id=%s\nrecovery_point_sha256=%s\npre_image_id=%s\nrecovery_helper_bundle_sha256=%s' "$release" "$point" "$image" "$bundle")
  if [[ -e $root/identity || -L $root/identity ]]; then
    recovery_checkpoint_assert_file "$root/identity" || return 1
    [[ $(<"$root/identity") == "$RECOVERY_CHECKPOINT_IDENTITY" ]] || return 1
  else
    [[ -z $(find "$root" -mindepth 1 -maxdepth 1 -print -quit) ]] || return 1
    recovery_checkpoint_store identity "$RECOVERY_CHECKPOINT_IDENTITY" || return 1
  fi
  local phase seen_gap=false entry
  for phase in "${RECOVERY_CHECKPOINT_PHASES[@]}"; do
    if [[ -e $root/$phase.started || -L $root/$phase.started ]]; then
      [[ $seen_gap == false ]] || return 1
      recovery_checkpoint_assert_file "$root/$phase.started" || return 1
      [[ $(<"$root/$phase.started") == "$RECOVERY_CHECKPOINT_IDENTITY"$'\n'"phase=$phase" ]] || return 1
    fi
    if [[ -e $root/$phase.done || -L $root/$phase.done ]]; then
      [[ $seen_gap == false ]] || return 1
      [[ -f $root/$phase.started && ! -L $root/$phase.started ]] || return 1
      recovery_checkpoint_assert_file "$root/$phase.done" || return 1
      [[ $(<"$root/$phase.done") == "$RECOVERY_CHECKPOINT_IDENTITY"$'\n'"phase=$phase" ]] || return 1
    else
      seen_gap=true
    fi
  done
  while IFS= read -r -d '' entry; do
    case "${entry##*/}" in
      identity|postgres.fingerprint|postgres.metadata|redis.persistent|redis.health-wait-seconds|redis.run-id.sha256|backup-units.fingerprint|nginx.fingerprint) ;;
      *.done|*.started)
        phase=${entry##*/}; phase=${phase%.*}
        [[ " ${RECOVERY_CHECKPOINT_PHASES[*]} " == *" $phase "* ]] || return 1 ;;
      *) return 1 ;;
    esac
    recovery_checkpoint_assert_file "$entry" || return 1
    if [[ $entry == *.started ]]; then
      [[ $(<"$entry") == "$RECOVERY_CHECKPOINT_IDENTITY"$'\n'"phase=$phase" ]] || return 1
    fi
  done < <(find "$root" -mindepth 1 -maxdepth 1 -print0)
  if [[ -n ${RECOVERY_INTERRUPT_AFTER_PHASE:-} ]]; then
    [[ " ${RECOVERY_CHECKPOINT_PHASES[*]} " == *" $RECOVERY_INTERRUPT_AFTER_PHASE "* ]] || return 1
    [[ $(type -t recovery_drill_assert_isolated) == function ]] || return 1
    recovery_drill_assert_isolated "$bundle" || return 1
  fi
}

recovery_checkpoint_store() {
  local name=${1:?evidence name is required} value=${2:?evidence value is required}
  [[ $name =~ ^[A-Za-z0-9_.-]{1,80}$ ]] || return 1
  recovery_checkpoint_assert_directory "$RECOVERY_CHECKPOINT_ROOT" || return 1
  if [[ -e $RECOVERY_CHECKPOINT_ROOT/$name || -L $RECOVERY_CHECKPOINT_ROOT/$name ]]; then
    recovery_checkpoint_assert_file "$RECOVERY_CHECKPOINT_ROOT/$name" || return 1
    [[ $(<"$RECOVERY_CHECKPOINT_ROOT/$name") == "$value" ]] || return 1
    return 0
  fi
  local temporary
  temporary=$(mktemp "$RECOVERY_CHECKPOINT_ROOT/.checkpoint.XXXXXXXX") || return 1
  if ! printf '%s\n' "$value" > "$temporary" ||
     ! chown root:root "$temporary" || ! chmod 400 "$temporary"; then
    rm -f -- "$temporary"
    return 1
  fi
  mv -T -- "$temporary" "$RECOVERY_CHECKPOINT_ROOT/$name" || { rm -f -- "$temporary"; return 1; }
}

recovery_checkpoint_completed() {
  local phase=${1:?phase is required}
  [[ " ${RECOVERY_CHECKPOINT_PHASES[*]} " == *" $phase "* ]] || return 1
  [[ -e $RECOVERY_CHECKPOINT_ROOT/$phase.done || -L $RECOVERY_CHECKPOINT_ROOT/$phase.done ]] || return 1
  recovery_checkpoint_assert_file "$RECOVERY_CHECKPOINT_ROOT/$phase.done" || return 1
  [[ $(<"$RECOVERY_CHECKPOINT_ROOT/$phase.done") == "$RECOVERY_CHECKPOINT_IDENTITY"$'\n'"phase=$phase" ]]
}

recovery_checkpoint_run() {
  local phase=${1:?phase is required} verify=${2:?live verifier is required} apply=${3:?apply callback is required}
  [[ " ${RECOVERY_CHECKPOINT_PHASES[*]} " == *" $phase "* ]] || return 1
  [[ $(type -t "$verify") == function && $(type -t "$apply") == function ]] || return 1
  local previous
  for previous in "${RECOVERY_CHECKPOINT_PHASES[@]}"; do
    [[ $previous != "$phase" ]] || break
    recovery_checkpoint_completed "$previous" || return 1
  done
  if [[ -e $RECOVERY_CHECKPOINT_ROOT/$phase.done || -L $RECOVERY_CHECKPOINT_ROOT/$phase.done ]]; then
    recovery_checkpoint_completed "$phase" || return 1
    "$verify" || return 1
    printf 'recovery_checkpoint=%s status=reverified\n' "$phase" || return 1
  else
    # An interrupted destructive phase may be adopted only if its verifier
    # proves the complete postcondition. Never replay the data write blindly.
    if [[ -e $RECOVERY_CHECKPOINT_ROOT/$phase.started || -L $RECOVERY_CHECKPOINT_ROOT/$phase.started ]]; then
      recovery_checkpoint_assert_file "$RECOVERY_CHECKPOINT_ROOT/$phase.started" || return 1
      [[ $(<"$RECOVERY_CHECKPOINT_ROOT/$phase.started") == "$RECOVERY_CHECKPOINT_IDENTITY"$'\n'"phase=$phase" ]] || return 1
    else
      recovery_checkpoint_store "$phase.started" "$RECOVERY_CHECKPOINT_IDENTITY"$'\n'"phase=$phase" || return 1
      "$apply"
      local apply_status=$?
      ((apply_status == 0)) || return "$apply_status"
    fi
    "$verify" || return 1
    recovery_checkpoint_store "$phase.done" "$RECOVERY_CHECKPOINT_IDENTITY"$'\n'"phase=$phase" || return 1
    printf 'recovery_checkpoint=%s status=completed\n' "$phase" || return 1
  fi
  if [[ ${RECOVERY_INTERRUPT_AFTER_PHASE:-} == "$phase" ]]; then
    printf 'recovery_drill_interrupt=%s\n' "$phase" || return 1
    exit 79
  fi
}
