#!/usr/bin/env bash
set -Eeuo pipefail

domain=${PUBLIC_DOMAIN:?PUBLIC_DOMAIN is required}
code=$(curl -sS --connect-timeout 5 --max-time 10 -o /dev/null -w '%{http_code}' "https://$domain/health" 2>/dev/null || true)
[[ $code != 200 ]]
printf 'public_health_blocked=true\n'
