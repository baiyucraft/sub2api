#!/usr/bin/env bash
set -Eeuo pipefail

domain=${PUBLIC_DOMAIN:?PUBLIC_DOMAIN is required}
curl_status=0
code=$(curl -sS --connect-timeout 5 --max-time 10 -o /dev/null -w '%{http_code}' "https://$domain/health" 2>/dev/null) || curl_status=$?
[[ $code =~ ^[0-9]{3}$ && $code != 200 ]]
case "$curl_status:$code" in
  0:[1-5][0-9][0-9]) ;;
  7:000|28:000|52:000|56:000) ;;
  *) exit 1 ;;
esac
printf 'public_health_blocked=true\n'
