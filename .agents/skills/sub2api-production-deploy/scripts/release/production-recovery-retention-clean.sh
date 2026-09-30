#!/usr/bin/env bash
set -Eeuo pipefail
umask 077

mode=${1:?cleanup mode is required}
expected_plan_sha256=${2:--}
requested_cutoff_epoch=${3:--}
[[ $mode == dry-run || $mode == apply ]]
if [[ $mode == apply ]]; then
  [[ $expected_plan_sha256 =~ ^[0-9a-f]{64}$ ]]
  [[ $requested_cutoff_epoch =~ ^[0-9]+$ ]]
else
  [[ $expected_plan_sha256 == - ]]
  [[ $requested_cutoff_epoch == - ]]
fi

required_commands=(awk date df find flock grep mktemp ps rmdir rm sha256sum sort stat tr wc)
for command_name in "${required_commands[@]}"; do
  command -v "$command_name" >/dev/null 2>&1 || exit 127
done

exec 9>/run/lock/sub2api-production-release.lock
flock -n 9
exec 8>/run/lock/sub2api-backup-global.lock
flock -n 8

state_root=/opt/sub2api/backups/release-state
release_root=/opt/sub2api/releases
active_claim=$release_root/.active-release
retention_days=3
minimum_keep=3
if [[ $mode == apply ]]; then
  cutoff_epoch=$requested_cutoff_epoch
else
  cutoff_epoch=$(( $(date +%s) - retention_days * 86400 ))
fi
work_dir=$(mktemp -d /tmp/sub2api-recovery-retention.XXXXXXXX)
cleanup() { rm -rf -- "$work_dir"; }
trap cleanup EXIT

release_id_pattern='^(182|187|191|192|194|195|197|198|199|202|206|207|208|209|210|212|213|215|232|233|234|235|236|237|238|239|240|241|242|243|244|245|246|247|248|249|250|251|252|253|254|255|256|257|258|259)-[0-9a-f]{12}-[0-9]+-[0-9a-f]{8}$'
records=$work_dir/records
valid_records=$work_dir/valid-records
protected_records=$work_dir/protected-records
candidates=$work_dir/candidates
plan=$work_dir/plan
: > "$records"
: > "$valid_records"
: > "$protected_records"
: > "$candidates"

assert_idle() {
  [[ ! -e $active_claim && ! -L $active_claim ]]
  [[ $(ps -eo args= | awk '/docker (build|buildx)|buildctl|release\.py (deploy|deploy-start)|release\.supervisor/ && ! /awk/ {count++} END {print count+0}') == 0 ]]
}

read_terminal_state() {
  local release_id=$1 release_dir="$release_root/$1"
  if [[ -e $release_dir/.reconciliation || -L $release_dir/.reconciliation ]]; then
    printf 'protected_reconciliation\n'
  elif [[ -d $release_dir/.consumed && ! -L $release_dir/.consumed && -f $release_dir/.consumed/marker && ! -L $release_dir/.consumed/marker ]]; then
    grep -Fxq "release_id=$release_id" "$release_dir/.consumed/marker" || return 1
    printf 'consumed\n'
  elif [[ -d $release_dir/.recovered && ! -L $release_dir/.recovered && -f $release_dir/.recovered/release_id && ! -L $release_dir/.recovered/release_id ]]; then
    grep -Fxq "release_id=$release_id" "$release_dir/.recovered/release_id" || return 1
    printf 'recovered\n'
  else
    return 1
  fi
}

write_records() {
  local state_dir release_id terminal recovery_file recovery_sha pre_image bytes mtime links
  [[ -d $state_root && ! -L $state_root ]]
  while IFS= read -r -d '' state_dir; do
    release_id=${state_dir##*/}
    [[ $release_id =~ $release_id_pattern ]] || continue
    [[ -d $state_dir && ! -L $state_dir ]] || continue
    terminal=$(read_terminal_state "$release_id" || true)
    recovery_file=$state_dir/recovery-point.age
    recovery_sha=$state_dir/recovery-point.age.sha256
    pre_image=$state_dir/pre-image-id
    if [[ $terminal != consumed && $terminal != recovered ]]; then
      printf '%s\tunknown\n' "$release_id" >> "$records"
      continue
    fi
    if [[ ! -f $recovery_file || -L $recovery_file || ! -f $recovery_sha || -L $recovery_sha || ! -f $pre_image || -L $pre_image ]]; then
      printf '%s\tunknown\n' "$release_id" >> "$records"
      continue
    fi
    if [[ $(find "$state_dir" -mindepth 1 -maxdepth 1 ! -type f -o -type l | wc -l | tr -d ' ') != 0 ||
          $(find "$state_dir" -mindepth 1 -maxdepth 1 -type f ! -name 'backup-result' ! -name 'backup-result.sha256' ! -name 'pre-image-id' ! -name 'recovery-point.age' ! -name 'recovery-point.age.sha256' | wc -l | tr -d ' ') != 0 ||
          ! -f "$state_dir/backup-result" || -L "$state_dir/backup-result" ||
          ! -f "$state_dir/backup-result.sha256" || -L "$state_dir/backup-result.sha256" ]]; then
      printf '%s\tunknown\n' "$release_id" >> "$records"
      continue
    fi
    (cd "$state_dir" && sha256sum -c recovery-point.age.sha256 >/dev/null) || {
      printf '%s\tunknown\n' "$release_id" >> "$records"
      continue
    }
    bytes=$(stat -c '%s' "$recovery_file")
    mtime=$(stat -c '%Y' "$recovery_file")
    links=$(stat -c '%h' "$recovery_file")
    if [[ ! $bytes =~ ^[0-9]+$ || ! $mtime =~ ^[0-9]+$ || $links != 1 ]]; then
      printf '%s\tunknown\n' "$release_id" >> "$records"
      continue
    fi
    printf '%s\t%s\t%s\t%s\t%s\n' "$release_id" "$terminal" "$mtime" "$bytes" "$links" >> "$valid_records"
    printf '%s\tvalid\n' "$release_id" >> "$records"
  done < <(find "$state_root" -mindepth 1 -maxdepth 1 -type d ! -name '.*' -print0 | LC_ALL=C sort -z)
}

build_protected_set() {
  local release_id marker consumed_at
  # Keep the latest two successfully consumed release points as current/previous
  # protection even when the active-release claim is absent after a recovery.
  while IFS=$'\t' read -r consumed_at release_id; do
    [[ -n $release_id ]] || continue
    printf '%s\tcurrent_or_previous\n' "$release_id" >> "$protected_records"
  done < <(
    for marker in "$release_root"/*/.consumed/marker; do
      [[ -f $marker && ! -L $marker ]] || continue
      release_id=${marker#"$release_root/"}; release_id=${release_id%%/*}
      consumed_at=$(sed -n 's/^consumed_at=//p' "$marker")
      [[ $consumed_at =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$ ]] || continue
      printf '%s\t%s\n' "$consumed_at" "$release_id"
    done | LC_ALL=C sort -r | head -n 2
  )
}

build_candidates() {
  local release_id terminal mtime bytes links rank=0
  LC_ALL=C sort -t $'\t' -k3,3nr -k1,1r "$valid_records" > "$work_dir/sorted-records"
  while IFS=$'\t' read -r release_id terminal mtime bytes links; do
    [[ -n $release_id ]] || continue
    rank=$((rank + 1))
    if (( mtime >= cutoff_epoch )) || (( rank <= minimum_keep )) || grep -Fq "$release_id" "$protected_records"; then
      continue
    fi
    printf '%s\t%s\t%s\t%s\t%s\n' "$release_id" "$terminal" "$mtime" "$bytes" "$links" >> "$candidates"
  done < "$work_dir/sorted-records"
}

assert_candidate_unchanged() {
  local release_id=$1 terminal=$2 mtime=$3 bytes=$4 links=$5 state_dir recovery_file
  state_dir=$state_root/$release_id
  recovery_file=$state_dir/recovery-point.age
  [[ -d $state_dir && ! -L $state_dir && -f $recovery_file && ! -L $recovery_file ]]
  [[ $(stat -c '%Y:%s:%h' "$recovery_file") == "$mtime:$bytes:$links" ]]
  [[ $(read_terminal_state "$release_id") == "$terminal" ]]
  [[ ! -e $state_dir/.reconciliation && ! -L $state_dir/.reconciliation ]]
  (cd "$state_dir" && sha256sum -c recovery-point.age.sha256 >/dev/null)
}

write_records
build_protected_set
build_candidates
{
  printf 'retention_days=%s\nminimum_keep=%s\ncutoff_epoch=%s\n' "$retention_days" "$minimum_keep" "$cutoff_epoch"
  printf 'valid_records\n'; cat "$valid_records"
  printf 'protected_records\n'; LC_ALL=C sort -u "$protected_records"
  printf 'candidates\n'; cat "$candidates"
} > "$plan"
plan_sha256=$(sha256sum "$plan" | awk '{print $1}')
[[ $plan_sha256 =~ ^[0-9a-f]{64}$ ]]

root_free_before_bytes=$(df -PB1 / | awk 'NR==2 {print $4}')
candidate_count=$(awk 'NF {count++} END {print count+0}' "$candidates")
candidate_bytes=$(awk -F '\t' '{total += $4} END {printf "%.0f\n", total+0}' "$candidates")
valid_count=$(awk 'NF {count++} END {print count+0}' "$valid_records")
protected_count=$(LC_ALL=C sort -u "$protected_records" | awk 'NF {count++} END {print count+0}')
deleted_count=0
deleted_bytes=0

if [[ $mode == apply ]]; then
  [[ $plan_sha256 == "$expected_plan_sha256" ]]
  while IFS=$'\t' read -r release_id terminal mtime bytes links; do
    [[ -n $release_id ]] || continue
    assert_candidate_unchanged "$release_id" "$terminal" "$mtime" "$bytes" "$links"
    state_dir=$state_root/$release_id
    rm -f -- "$state_dir/backup-result" "$state_dir/backup-result.sha256" "$state_dir/pre-image-id" "$state_dir/recovery-point.age" "$state_dir/recovery-point.age.sha256"
    rmdir -- "$state_dir"
    deleted_count=$((deleted_count + 1))
    deleted_bytes=$((deleted_bytes + bytes))
  done < "$candidates"
  [[ $deleted_count == $candidate_count && $deleted_bytes == $candidate_bytes ]]
fi

root_free_after_bytes=$(df -PB1 / | awk 'NR==2 {print $4}')
[[ $root_free_before_bytes =~ ^[0-9]+$ && $root_free_after_bytes =~ ^[0-9]+$ ]]
root_free_delta_bytes=$((root_free_after_bytes - root_free_before_bytes))
printf 'cleanup_mode=%s\n' "$mode"
printf 'cleanup_status=%s\n' "$(if [[ $mode == dry-run ]]; then printf ready; else printf completed; fi)"
printf 'plan_sha256=%s\n' "$plan_sha256"
printf 'retention_days=%s\n' "$retention_days"
printf 'minimum_keep=%s\n' "$minimum_keep"
printf 'cutoff_epoch=%s\n' "$cutoff_epoch"
printf 'valid_count=%s\n' "$valid_count"
printf 'protected_count=%s\n' "$protected_count"
printf 'candidate_count=%s\n' "$candidate_count"
printf 'candidate_bytes=%s\n' "$candidate_bytes"
printf 'candidate_ids=%s\n' "$(cut -f1 "$candidates" | paste -sd, -)"
printf 'deleted_count=%s\n' "$deleted_count"
printf 'deleted_bytes=%s\n' "$deleted_bytes"
printf 'candidate_count_after=%s\n' "$(if [[ $mode == dry-run ]]; then printf '%s' "$candidate_count"; else printf '0'; fi)"
printf 'root_free_before_bytes=%s\n' "$root_free_before_bytes"
printf 'root_free_after_bytes=%s\n' "$root_free_after_bytes"
printf 'root_free_delta_bytes=%s\n' "$root_free_delta_bytes"
