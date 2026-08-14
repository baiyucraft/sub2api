#!/usr/bin/env bash
set -Eeuo pipefail

domain=${PUBLIC_DOMAIN:?PUBLIC_DOMAIN is required}
route_ip=${ROUTE_IP:?ROUTE_IP is required}
route_name=${ROUTE_NAME:?ROUTE_NAME is required}
marker=${MARKER:?MARKER is required}
[[ $route_name == direct || $route_name == dmit ]]
IFS= read -r api_key
[[ $api_key == sk-* && ${#api_key} -ge 16 ]]
tmp=$(mktemp -d /tmp/sub2api-route-canary.XXXXXX)
chmod 700 "$tmp"
cleanup() { rm -rf "$tmp"; }
trap cleanup EXIT
printf '%s' '{"model":"gpt-5.4-mini","input":"Reply with OK only. Marker: ' > "$tmp/request.json"
printf '%s' "$marker" >> "$tmp/request.json"
printf '%s' '","stream":true,"max_output_tokens":8}' >> "$tmp/request.json"
{
  printf 'silent\nshow-error\nmax-time = 120\nno-buffer\n'
  printf 'header = "Authorization: Bearer %s"\n' "$api_key"
  printf 'header = "Content-Type: application/json"\n'
} > "$tmp/curl.conf"
chmod 600 "$tmp/curl.conf"
user_agent="sub2api-release-$marker-$route_name"

classify_failure() {
  local curl_exit=$1
  local http_code=$2
  if [[ $curl_exit == 28 || $http_code == 502 || $http_code == 503 || $http_code == 504 ]]; then
    printf 'canary_status=retryable\n'
  else
    printf 'canary_status=failed\n'
  fi
}

set +e
health_code=$(curl -sS --max-time 15 --resolve "$domain:443:$route_ip" -o /dev/null -w '%{http_code}' "https://$domain/health" 2>/dev/null)
health_exit=$?
set -e
if [[ $health_exit != 0 || $health_code != 200 ]]; then
  printf 'route_health=fail\nstreaming=fail\ncurl_exit=%s\nhttp_code=%s\n' "$health_exit" "${health_code:-000}"
  classify_failure "$health_exit" "${health_code:-000}"
  exit 0
fi

set +e
code=$(curl -K "$tmp/curl.conf" --resolve "$domain:443:$route_ip" -A "$user_agent" -H "x_release_probe: $marker-$route_name" -o "$tmp/stream.txt" -w '%{http_code}' --data-binary @"$tmp/request.json" "https://$domain/v1/responses" 2>/dev/null)
curl_exit=$?
set -e
if [[ $curl_exit == 0 && $code == 200 ]] && grep -Eq '^data:|response\.(created|completed)' "$tmp/stream.txt"; then
  printf 'route_health=pass\nstreaming=pass\ncurl_exit=0\nhttp_code=200\ncanary_status=pass\n'
  exit 0
fi

printf 'route_health=pass\nstreaming=fail\ncurl_exit=%s\nhttp_code=%s\n' "$curl_exit" "${code:-000}"
classify_failure "$curl_exit" "${code:-000}"
