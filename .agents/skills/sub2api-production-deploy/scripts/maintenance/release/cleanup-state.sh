#!/usr/bin/env bash
set -Eeuo pipefail

release_dir=${RELEASE_DIR:?RELEASE_DIR is required}
ACTIVE_CLAIM=${ACTIVE_CLAIM:-/opt/sub2api/releases/.active-release}
source "$ACTIVE_CLAIM/assets/context.sh"
verify_migrations_restored() {
  [[ -f $state_dir/pre-migrations.tsv && ! -L $state_dir/pre-migrations.tsv ]]
  diff -u "$state_dir/pre-migrations.tsv" <(docker exec sub2api-postgres psql -X -A -t -F '|' -U sub2api -d sub2api -c "SELECT filename,checksum FROM schema_migrations ORDER BY filename") >/dev/null
}
active_container_from_slot() {
  [[ -f /opt/sub2api/active-app && ! -L /opt/sub2api/active-app ]]
  sed -n 's/^container=//p' /opt/sub2api/active-app
}
assert_ingress_transaction_cleanup_state() {
  local txn=${1:?transaction directory is required}
  [[ -d $txn && ! -L $txn ]]
  local marker
  for marker in applied rollback-complete rollback-failure; do
    if [[ -e $txn/$marker || -L $txn/$marker ]]; then
      [[ -f $txn/$marker && ! -L $txn/$marker ]]
    fi
  done
  [[ ! -e $txn/rollback-failure && ! -L $txn/rollback-failure ]]
  if [[ $active_claim == "$release_dir/.consumed" ]]; then
    [[ -f $txn/applied && ! -L $txn/applied ]]
    [[ ! -e $txn/rollback-complete && ! -L $txn/rollback-complete ]]
    [[ ! -e $state_dir/nginx-recovery-restored && ! -L $state_dir/nginx-recovery-restored ]]
  else
    if [[ -f $txn/rollback-complete ]]; then
      [[ ! -e $txn/applied && ! -L $txn/applied ]]
      [[ ! -e $state_dir/nginx-recovery-restored && ! -L $state_dir/nginx-recovery-restored ]]
    else
      [[ -f $state_dir/nginx-recovery-restored && ! -L $state_dir/nginx-recovery-restored ]]
      [[ ! -e $txn/applied && ! -L $txn/applied ]]
    fi
  fi
}
assert_cleanup_state() {
  local txn=${1:?transaction directory is required}
  local recovery_marker="$state_dir/nginx-recovery-restored"
  local has_recovery=false
  local has_txn=false
  local has_applied=false
  local has_rollback=false

  if [[ -e $recovery_marker || -L $recovery_marker ]]; then
    [[ -f $recovery_marker && ! -L $recovery_marker ]]
    has_recovery=true
  fi
  if [[ -e $txn || -L $txn ]]; then
    assert_ingress_transaction_cleanup_state "$txn"
    has_txn=true
    [[ -f $txn/applied ]] && has_applied=true
    [[ -f $txn/rollback-complete ]] && has_rollback=true
  fi

  if [[ $active_claim == "$release_dir/.consumed" ]]; then
    [[ $has_txn == true && $has_applied == true && $has_rollback == false && $has_recovery == false ]]
  elif [[ $has_rollback == true ]]; then
    [[ $has_txn == true && $has_applied == false && $has_recovery == false ]]
  else
    [[ $has_recovery == true && $has_applied == false && $has_rollback == false ]]
  fi
}
state_cleanup=removed
if [[ -d $state_dir && ! -L $state_dir ]]; then
  ingress_txn="$state_dir/nginx-ingress-transaction"
  assert_cleanup_state "$ingress_txn"
  if [[ -e $state_dir/recovery-point.age || -L $state_dir/recovery-point.age ||
        -e $state_dir/recovery-point.age.sha256 || -L $state_dir/recovery-point.age.sha256 ]]; then
    [[ -f $state_dir/recovery-point.age && ! -L $state_dir/recovery-point.age ]]
    [[ -f $state_dir/recovery-point.age.sha256 && ! -L $state_dir/recovery-point.age.sha256 ]]
    find "$state_dir" -mindepth 1 -maxdepth 1 \
      ! -name recovery-point.age ! -name recovery-point.age.sha256 ! -name pre-image-id \
      ! -name backup-result ! -name backup-result.sha256 ! -name backup-failure \
      -exec rm -rf -- {} +
    state_cleanup=recovery_point_preserved
  elif [[ -f $state_dir/pre-image-id && -f $state_dir/SHA256SUMS ]]; then
    [[ ! -e $state_dir/recovery-point.age && ! -L $state_dir/recovery-point.age ]]
    [[ ! -e $state_dir/recovery-point.age.sha256 && ! -L $state_dir/recovery-point.age.sha256 ]]
    (cd "$state_dir" && sha256sum -c SHA256SUMS >/dev/null)
    verify_migrations_restored
    active_container=$(active_container_from_slot); [[ $(docker inspect -f '{{.State.Health.Status}}' "$active_container") == healthy ]]
    [[ $(systemctl is-active nginx) == active ]]
    [[ $(systemctl is-enabled sub2api-backup.timer 2>/dev/null || true) == enabled ]]
    rm -rf -- "$state_dir"
  else
    [[ ! -e $state_dir/pre-image-id && ! -L $state_dir/pre-image-id ]]
    [[ ! -e $state_dir/SHA256SUMS && ! -L $state_dir/SHA256SUMS ]]
    [[ -f $state_dir/restored.committed && ! -L $state_dir/restored.committed ]]
    verify_migrations_restored
    active_container=$(active_container_from_slot); [[ $(docker inspect -f '{{.State.Health.Status}}' "$active_container") == healthy ]]
    [[ $(systemctl is-active nginx) == active ]]
    [[ $(systemctl is-enabled sub2api-backup.timer 2>/dev/null || true) == enabled ]]
    rm -rf -- "$state_dir"
  fi
else
  [[ ! -e $state_dir && ! -L $state_dir ]]
  active_container=$(active_container_from_slot); [[ $(docker inspect -f '{{.State.Health.Status}}' "$active_container") == healthy ]]
  [[ $(systemctl is-active nginx) == active ]]
  [[ $(systemctl is-active sub2api-backup.service 2>/dev/null || true) != active ]]
  [[ $(systemctl is-enabled sub2api-backup.timer 2>/dev/null || true) == enabled ]]
fi
printf 'cleaned_at=%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" > "$active_claim/plaintext-cleaned"
chmod 400 "$active_claim/plaintext-cleaned"
printf 'plaintext_state_removed=true\n'
printf 'state_cleanup=%s\n' "$state_cleanup"
