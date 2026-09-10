#!/usr/bin/env bash
set -Eeuo pipefail

NGINX_INGRESS_SNIPPET=${NGINX_INGRESS_SNIPPET:-/etc/nginx/snippets/sub2api-release-ingress.conf}
NGINX_OBSERVABILITY_CONF=${NGINX_OBSERVABILITY_CONF:-/etc/nginx/conf.d/sub2api-release-observability.conf}
NGINX_UPSTREAM_LOGROTATE=${NGINX_UPSTREAM_LOGROTATE:-/etc/logrotate.d/sub2api-upstream-access}
NGINX_SITE_BACKUP_DIR=${NGINX_SITE_BACKUP_DIR:-/etc/nginx/sub2api-release-backups}
NGINX_MANAGED_PROXY_PATTERN='^[[:space:]]*proxy_pass[[:space:]]+http://sub2api_release_backend;[[:space:]]*$'
NGINX_MANAGED_INCLUDE_PATTERN='^[[:space:]]*include[[:space:]]+/etc/nginx/snippets/sub2api-release-ingress\.conf;[[:space:]]*$'

find_managed_nginx_site() {
  local candidate
  local -a sites=()
  while IFS= read -r -d '' candidate; do
    if grep -Eq "$NGINX_MANAGED_PROXY_PATTERN" "$candidate"; then
      sites+=("$candidate")
    fi
  done < <(find /etc/nginx/sites-enabled -maxdepth 1 -type f ! -name '*.sub2api-release-backup' -print0)
  [[ ${#sites[@]} == 1 ]] || return 1
  [[ ${sites[0]} =~ ^/etc/nginx/sites-enabled/[A-Za-z0-9._-]{1,160}$ ]] || return 1
  [[ -f ${sites[0]} && ! -L ${sites[0]} ]] || return 1
  printf '%s\n' "${sites[0]}"
}

assert_managed_proxy_includes() {
  local site=${1:?site is required}
  awk '
    function normalized(value) {
      sub(/^[[:space:]]+/, "", value)
      sub(/[[:space:]]+$/, "", value)
      return value
    }
    /^[[:space:]]*proxy_pass[[:space:]]+http:\/\/sub2api_release_backend;[[:space:]]*$/ {
      if (previous != "include /etc/nginx/snippets/sub2api-release-ingress.conf;") exit 1
      count++
    }
    {
      current = normalized($0)
      if (current != "" && current !~ /^#/) previous = current
    }
    END { if (count < 1) exit 1 }
  ' "$site"
}

assert_ingress_transaction_layout() {
  local txn=${1:?transaction directory is required}
  local markers actual expected
  [[ -d $txn && ! -L $txn ]]
  [[ -d $txn/files && ! -L $txn/files ]]
  [[ -d $txn/stale && ! -L $txn/stale ]]
  for file in identity targets.tsv stale.tsv SHA256SUMS SHA256SUMS.files; do
    [[ -f $txn/$file && ! -L $txn/$file ]]
  done
  for marker in applied rollback-complete rollback-failure; do
    if [[ -e $txn/$marker || -L $txn/$marker ]]; then
      [[ -f $txn/$marker && ! -L $txn/$marker ]]
    fi
  done
  markers=$(find "$txn" -mindepth 1 -maxdepth 1 \
    \( -name applied -o -name rollback-complete -o -name rollback-failure \) \
    -printf '%f\n' | LC_ALL=C sort)
  case "$markers" in
    ''|applied|rollback-complete|rollback-failure|$'applied\nrollback-failure') ;;
    *) return 1 ;;
  esac
  actual=$(find "$txn" -mindepth 1 -maxdepth 1 -printf '%f\n' | LC_ALL=C sort)
  expected=$(printf '%s\n' SHA256SUMS SHA256SUMS.files files identity stale stale.tsv targets.tsv "$markers" | sed '/^$/d' | LC_ALL=C sort)
  [[ $actual == "$expected" ]]
  [[ -z $(find "$txn/files" "$txn/stale" -mindepth 1 ! -type f -print -quit) ]]
}

assert_ingress_transaction_files() {
  local txn=${1:?transaction directory is required}
  local expected actual
  expected=$(cd "$txn" && find files stale -type f -print | LC_ALL=C sort)
  actual=$(awk 'NF == 2 && length($1) == 64 { print $2 }' "$txn/SHA256SUMS.files" | LC_ALL=C sort)
  [[ $actual == "$expected" ]]
}

rewrite_managed_nginx_site() {
  local site=${1:?site is required}
  local output=${2:?output is required}
  [[ -f $site && ! -L $site ]]
  awk '
    {
      lines[NR] = $0
      clean = $0
      sub(/#.*/, "", clean)
      before[NR] = depth
      opens = gsub(/\{/, "{", clean)
      closes = gsub(/\}/, "}", clean)
      depth += opens - closes
      after[NR] = depth
      if ($0 ~ /^[[:space:]]*proxy_pass[[:space:]]+http:\/\/sub2api_release_backend;[[:space:]]*$/) proxies[++proxy_count] = NR
    }
    END {
      if (proxy_count < 1 || depth != 0) exit 1
      for (p = 1; p <= proxy_count; p++) {
        line = proxies[p]
        d = before[line]
        start = 0
        finish = 0
        for (i = line - 1; i >= 1; i--) if (before[i] == d - 1 && after[i] >= d && lines[i] ~ /\{/) { start = i; break }
        for (i = line + 1; i <= NR; i++) if (before[i] == d && after[i] == d - 1 && lines[i] ~ /\}/) { finish = i; break }
        if (start == 0 || finish == 0) exit 1
        for (i = start + 1; i < finish; i++) managed[i] = 1
      }
      for (i = 1; i <= NR; i++) {
        if (managed[i] && lines[i] ~ /^[[:space:]]*proxy_request_buffering[[:space:]]+(on|off);[[:space:]]*$/) continue
        if (managed[i] && lines[i] ~ /^[[:space:]]*proxy_buffering[[:space:]]+(on|off);[[:space:]]*$/) continue
        if (managed[i] && lines[i] ~ /^[[:space:]]*access_log[[:space:]]+\/var\/log\/nginx\/sub2api-upstream-access\.log[[:space:]]+sub2api_upstream;[[:space:]]*$/) continue
        if (managed[i] && lines[i] ~ /^[[:space:]]*include[[:space:]]+\/etc\/nginx\/snippets\/sub2api-release-ingress\.conf;[[:space:]]*$/) continue
        if (lines[i] ~ /^[[:space:]]*proxy_pass[[:space:]]+http:\/\/sub2api_release_backend;[[:space:]]*$/) {
          match(lines[i], /^[[:space:]]*/)
          print substr(lines[i], RSTART, RLENGTH) "include /etc/nginx/snippets/sub2api-release-ingress.conf;"
        }
        print lines[i]
      }
    }
  ' "$site" > "$output"
}

assert_nginx_ingress_policy() {
  local site dump
  site=$(find_managed_nginx_site) || return 1
  assert_managed_proxy_includes "$site" || return 1
  [[ -z $(find /etc/nginx/sites-enabled -maxdepth 1 -name '*.sub2api-release-backup' -print -quit) ]] || return 1
  [[ -f $NGINX_INGRESS_SNIPPET && ! -L $NGINX_INGRESS_SNIPPET ]] || return 1
  [[ $(stat -c '%U:%G:%a:%h' "$NGINX_INGRESS_SNIPPET") == root:root:600:1 ]] || return 1
  [[ $(grep -Fxc 'proxy_request_buffering on;' "$NGINX_INGRESS_SNIPPET") == 1 ]] || return 1
  [[ $(grep -Fxc 'proxy_buffering off;' "$NGINX_INGRESS_SNIPPET") == 1 ]] || return 1
  [[ $(grep -Fxc 'access_log /var/log/nginx/sub2api-upstream-access.log sub2api_upstream;' "$NGINX_INGRESS_SNIPPET") == 1 ]] || return 1
  [[ -f $NGINX_OBSERVABILITY_CONF && ! -L $NGINX_OBSERVABILITY_CONF ]] || return 1
  [[ $(stat -c '%U:%G:%a:%h' "$NGINX_OBSERVABILITY_CONF") == root:root:600:1 ]] || return 1
  grep -Eq '^[[:space:]]*log_format[[:space:]]+sub2api_upstream([[:space:]]|$)' "$NGINX_OBSERVABILITY_CONF" || return 1
  local field
  for field in request_time upstream_status upstream_connect_time upstream_header_time upstream_response_time; do
    grep -Fq "\$$field" "$NGINX_OBSERVABILITY_CONF" || return 1
  done
  [[ -f $NGINX_UPSTREAM_LOGROTATE && ! -L $NGINX_UPSTREAM_LOGROTATE ]] || return 1
  [[ $(stat -c '%U:%G:%a:%h' "$NGINX_UPSTREAM_LOGROTATE") == root:root:644:1 ]] || return 1
  grep -Fxq '/var/log/nginx/sub2api-upstream-access.log {' "$NGINX_UPSTREAM_LOGROTATE" || return 1
  [[ -d $NGINX_SITE_BACKUP_DIR && ! -L $NGINX_SITE_BACKUP_DIR ]] || return 1
  [[ $(stat -c '%U:%G:%a' "$NGINX_SITE_BACKUP_DIR") == root:root:700 ]] || return 1
  dump=$(nginx -T 2>&1) || return 1
  ! grep -Eqi 'conflicting server name' <<<"$dump" || return 1
  grep -Fq "# configuration file $site:" <<<"$dump" || return 1
  grep -Fq "# configuration file $NGINX_INGRESS_SNIPPET:" <<<"$dump" || return 1
  grep -Fq "# configuration file $NGINX_OBSERVABILITY_CONF:" <<<"$dump" || return 1
  grep -Fq 'proxy_request_buffering on;' <<<"$dump" || return 1
  grep -Fq 'proxy_buffering off;' <<<"$dump" || return 1
  grep -Fq 'sub2api-upstream-access.log sub2api_upstream;' <<<"$dump" || return 1
}
