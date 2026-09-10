#!/usr/bin/env bash
set -Eeuo pipefail

release_dir=${RELEASE_DIR:?RELEASE_DIR is required}
source /opt/sub2api/releases/.active-release/assets/context.sh
source "$assets_dir/nginx-ingress-contract.sh"
if [[ ${RELEASE_LOCK_HELD:-false} != true ]]; then
  exec 8>/run/lock/sub2api-production-release.lock
  flock -n 8
fi
[[ -d $state_dir && ! -L $state_dir ]]
(cd "$state_dir" && sha256sum -c SHA256SUMS >/dev/null)
[[ $(systemctl is-active nginx) == active ]]
[[ $(systemctl is-active sub2api-backup.service 2>/dev/null || true) != active ]]
[[ -f $state_dir/backup-result && ! -L $state_dir/backup-result ]]
[[ -f $state_dir/backup-result.sha256 && ! -L $state_dir/backup-result.sha256 ]]
(cd "$state_dir" && sha256sum -c backup-result.sha256 >/dev/null)

txn="$state_dir/nginx-ingress-transaction"
if [[ -d $txn && ! -L $txn && -f $txn/applied ]]; then
  (cd "$txn" && sha256sum -c SHA256SUMS >/dev/null && sha256sum -c SHA256SUMS.files >/dev/null)
  assert_nginx_ingress_policy
  printf 'nginx_ingress_applied=already_applied\n'
  printf 'nginx_request_buffering=on\nnginx_response_buffering=off\nnginx_upstream_logging=ready\n'
  exit 0
fi
[[ ! -e $txn && ! -L $txn ]]
install -d -m 700 "$txn/files" "$txn/stale"
site=$(find_managed_nginx_site)
: > "$txn/targets.tsv"

snapshot_target() {
  local label=${1:?label is required}
  local target=${2:?target is required}
  if [[ -e $target || -L $target ]]; then
    [[ -f $target && ! -L $target ]]
    cp -p -- "$target" "$txn/files/$label"
    printf '%s\t%s\tpresent\n' "$label" "$target" >> "$txn/targets.tsv"
  else
    printf '%s\t%s\tabsent\n' "$label" "$target" >> "$txn/targets.tsv"
  fi
}

snapshot_target snippet "$NGINX_INGRESS_SNIPPET"
snapshot_target observability "$NGINX_OBSERVABILITY_CONF"
snapshot_target logrotate "$NGINX_UPSTREAM_LOGROTATE"
cp -p -- "$site" "$txn/files/site"
printf 'site\t%s\tpresent\n' "$site" >> "$txn/targets.tsv"

if [[ -e $NGINX_SITE_BACKUP_DIR || -L $NGINX_SITE_BACKUP_DIR ]]; then
  [[ -d $NGINX_SITE_BACKUP_DIR && ! -L $NGINX_SITE_BACKUP_DIR ]]
  [[ $(stat -c '%U:%G:%a' "$NGINX_SITE_BACKUP_DIR") == root:root:700 ]]
else
  install -d -o root -g root -m 700 "$NGINX_SITE_BACKUP_DIR"
fi
: > "$txn/stale.tsv"
stale_count=0
while IFS= read -r -d '' stale; do
  [[ $stale =~ ^/etc/nginx/sites-enabled/[A-Za-z0-9._-]{1,160}\.sub2api-release-backup$ ]]
  name=$(basename -- "$stale")
  archive="$NGINX_SITE_BACKUP_DIR/$release_id-$name"
  [[ ! -e $archive && ! -L $archive ]]
  cp -p -- "$stale" "$txn/stale/$name"
  printf '%s\t%s\t%s\n' "$stale" "$archive" "$name" >> "$txn/stale.tsv"
  stale_count=$((stale_count + 1))
done < <(find /etc/nginx/sites-enabled -maxdepth 1 -type f -name '*.sub2api-release-backup' -print0)
[[ $stale_count -le 4 ]]
printf 'release_id=%s\n' "$release_id" > "$txn/identity"
(cd "$txn" && find files stale -type f -print0 | LC_ALL=C sort -z | xargs -0 sha256sum > SHA256SUMS.files)
(cd "$txn" && sha256sum identity targets.tsv stale.tsv SHA256SUMS.files > SHA256SUMS)
chmod 400 "$txn/identity" "$txn/targets.tsv" "$txn/stale.tsv" "$txn/SHA256SUMS" "$txn/SHA256SUMS.files"
(cd "$txn" && sha256sum -c SHA256SUMS >/dev/null && sha256sum -c SHA256SUMS.files >/dev/null)

mutation_started=true
rollback_on_error() {
  local code=$?
  trap - ERR INT TERM
  if [[ $mutation_started == true ]]; then
    if ! RELEASE_LOCK_HELD=true RELEASE_DIR="$release_dir" "$assets_dir/rollback-nginx-ingress.sh" >/dev/null; then
      exit 125
    fi
  fi
  exit "$code"
}
trap rollback_on_error ERR INT TERM

rewrite="$site.sub2api-ingress.$$"
rewrite_managed_nginx_site "$site" "$rewrite"
chmod --reference="$site" "$rewrite"
mv -T -- "$rewrite" "$site"

snippet_tmp="$NGINX_INGRESS_SNIPPET.tmp.$$"
printf 'proxy_request_buffering on;\nproxy_buffering off;\naccess_log /var/log/nginx/sub2api-upstream-access.log sub2api_upstream;\n' > "$snippet_tmp"
chmod 600 "$snippet_tmp"
mv -T -- "$snippet_tmp" "$NGINX_INGRESS_SNIPPET"

observability_tmp="$NGINX_OBSERVABILITY_CONF.tmp.$$"
cat > "$observability_tmp" <<'NGINX'
log_format sub2api_upstream escape=json
    'time="$time_iso8601" request_id="$request_id" remote_addr="$remote_addr" '
    'method="$request_method" uri="$uri" protocol="$server_protocol" status=$status '
    'request_length=$request_length bytes_sent=$bytes_sent request_time=$request_time '
    'upstream_addr="$upstream_addr" upstream_status="$upstream_status" '
    'upstream_connect_time="$upstream_connect_time" upstream_header_time="$upstream_header_time" '
    'upstream_response_time="$upstream_response_time"';
NGINX
chmod 600 "$observability_tmp"
mv -T -- "$observability_tmp" "$NGINX_OBSERVABILITY_CONF"

logrotate_tmp="$NGINX_UPSTREAM_LOGROTATE.tmp.$$"
cat > "$logrotate_tmp" <<'LOGROTATE'
/var/log/nginx/sub2api-upstream-access.log {
    daily
    rotate 14
    compress
    delaycompress
    missingok
    notifempty
    create 0640 root adm
    sharedscripts
    postrotate
        systemctl reload nginx >/dev/null 2>&1 || true
    endscript
}
LOGROTATE
chmod 644 "$logrotate_tmp"
mv -T -- "$logrotate_tmp" "$NGINX_UPSTREAM_LOGROTATE"

while IFS=$'\t' read -r stale archive name; do
  [[ -f $stale && ! -L $stale ]]
  install -m 600 "$stale" "$archive"
  rm -f -- "$stale"
done < "$txn/stale.tsv"

nginx_check=$(nginx -t 2>&1)
! grep -Eqi 'conflicting server name' <<<"$nginx_check"
systemctl reload nginx
assert_nginx_ingress_policy
printf 'applied_at=%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" > "$txn/applied.tmp"
chmod 400 "$txn/applied.tmp"
mv -T -- "$txn/applied.tmp" "$txn/applied"
mutation_started=false
trap - ERR INT TERM
printf 'nginx_ingress_applied=true\n'
printf 'nginx_request_buffering=on\nnginx_response_buffering=off\nnginx_upstream_logging=ready\n'
