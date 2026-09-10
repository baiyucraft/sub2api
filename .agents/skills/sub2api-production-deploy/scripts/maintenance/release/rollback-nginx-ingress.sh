#!/usr/bin/env bash
set -Eeuo pipefail

release_dir=${RELEASE_DIR:?RELEASE_DIR is required}
source /opt/sub2api/releases/.active-release/assets/context.sh
source "$assets_dir/nginx-ingress-contract.sh"
if [[ ${RELEASE_LOCK_HELD:-false} != true ]]; then
  exec 8>/run/lock/sub2api-production-release.lock
  flock -n 8
fi
txn="$state_dir/nginx-ingress-transaction"
if [[ ! -e $txn && ! -L $txn ]]; then
  printf 'nginx_ingress_rollback=not_applicable\n'
  exit 0
fi
[[ -d $txn && ! -L $txn ]]
assert_ingress_transaction_layout "$txn"
assert_ingress_transaction_files "$txn"
(cd "$txn" && sha256sum --strict -c SHA256SUMS >/dev/null && sha256sum --strict -c SHA256SUMS.files >/dev/null)
grep -Fxq "release_id=$release_id" "$txn/identity"

validate_target() {
  local label=${1:?label is required}
  local target=${2:?target is required}
  case "$label" in
    site) [[ $target =~ ^/etc/nginx/sites-enabled/[A-Za-z0-9._-]{1,160}$ ]] ;;
    snippet) [[ $target == /etc/nginx/snippets/sub2api-release-ingress.conf ]] ;;
    observability) [[ $target == /etc/nginx/conf.d/sub2api-release-observability.conf ]] ;;
    logrotate) [[ $target == /etc/logrotate.d/sub2api-upstream-access ]] ;;
    *) return 1 ;;
  esac
}

restore_target() {
  local label=${1:?label is required}
  local target=${2:?target is required}
  local state=${3:?state is required}
  local tmp
  validate_target "$label" "$target"
  case "$state" in
    present)
      [[ -f $txn/files/$label && ! -L $txn/files/$label ]]
      tmp="$target.restore.$$"
      cp -p -- "$txn/files/$label" "$tmp"
      mv -T -- "$tmp" "$target"
      ;;
    absent) rm -f -- "$target" ;;
    *) return 1 ;;
  esac
}

verify_restored_target() {
  local label=${1:?label is required}
  local target=${2:?target is required}
  local state=${3:?state is required}
  validate_target "$label" "$target"
  case "$state" in
    present)
      [[ -f $txn/files/$label && ! -L $txn/files/$label ]]
      [[ -f $target && ! -L $target ]]
      [[ $(sha256sum "$txn/files/$label" | awk '{print $1}') == "$(sha256sum "$target" | awk '{print $1}')" ]]
      [[ $(stat -c '%U:%G:%a:%h' "$txn/files/$label") == "$(stat -c '%U:%G:%a:%h' "$target")" ]]
      ;;
    absent) [[ ! -e $target && ! -L $target ]] ;;
    *) return 1 ;;
  esac
}

rollback_failed=false
rollback_code=0
set +e
while IFS=$'\t' read -r label target state; do
  if ! restore_target "$label" "$target" "$state" || ! verify_restored_target "$label" "$target" "$state"; then
    rollback_failed=true
    rollback_code=1
  fi
done < "$txn/targets.tsv"
while IFS=$'\t' read -r stale archive name; do
  if [[ ! $stale =~ ^/etc/nginx/sites-enabled/[A-Za-z0-9._-]{1,160}\.sub2api-release-backup$ ]] ||
     [[ ! $archive =~ ^/etc/nginx/sub2api-release-backups/[A-Za-z0-9._-]{1,240}$ ]] ||
     [[ $name != $(basename -- "$stale") ]] ||
     [[ ! -f $txn/stale/$name || -L $txn/stale/$name ]]; then
    rollback_failed=true
    rollback_code=1
    continue
  fi
  tmp="$stale.restore.$$"
  if ! cp -p -- "$txn/stale/$name" "$tmp" || ! mv -T -- "$tmp" "$stale" || ! rm -f -- "$archive" ||
     ! cmp -s "$txn/stale/$name" "$stale" ||
     [[ $(stat -c '%U:%G:%a:%h' "$txn/stale/$name") != "$(stat -c '%U:%G:%a:%h' "$stale")" ]] ||
     [[ -e $archive || -L $archive ]]; then
    rollback_failed=true
    rollback_code=1
  fi
done < "$txn/stale.tsv"
if ! nginx -t >/dev/null 2>&1 || ! systemctl reload nginx >/dev/null 2>&1; then
  rollback_failed=true
  rollback_code=1
fi
set -e
if [[ $rollback_failed == true ]]; then
  printf 'rollback_failed=true\nexit_code=%s\n' "$rollback_code" > "$txn/rollback-failure.tmp"
  chmod 600 "$txn/rollback-failure.tmp"
  mv -T -- "$txn/rollback-failure.tmp" "$txn/rollback-failure"
  exit 125
fi
rm -f -- "$txn/applied" "$txn/rollback-failure"
printf 'restored_at=%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" > "$txn/rollback-complete.tmp"
chmod 400 "$txn/rollback-complete.tmp"
mv -T -- "$txn/rollback-complete.tmp" "$txn/rollback-complete"
printf 'nginx_ingress_rollback=restored\n'
