#!/usr/bin/env bash
set -Eeuo pipefail

release_dir=${RELEASE_DIR:?RELEASE_DIR is required}
source "$release_dir/assets/context.sh"
domain=${PUBLIC_DOMAIN:?PUBLIC_DOMAIN is required}
direct_ip=${DIRECT_IP:?DIRECT_IP is required}
dmit_ip=${DMIT_IP:?DMIT_IP is required}
canary_key_file=${CANARY_KEY_FILE:-/root/.config/sub2api-release/canary-api-key}
[[ -f $canary_key_file && ! -L $canary_key_file ]]
[[ $(stat -c '%a' "$canary_key_file") == 600 ]]
[[ $(docker inspect -f '{{.Image}}' sub2api) == "$candidate_image_id" ]]
[[ $(docker inspect -f '{{.State.Health.Status}}' sub2api) == healthy ]]
[[ $(systemctl is-active nginx) == active ]]
[[ $(curl -sS --resolve "$domain:443:$direct_ip" --max-time 15 -o /dev/null -w '%{http_code}' "https://$domain/health") == 200 ]]
tmp=$(mktemp -d /tmp/sub2api-release-verify.XXXXXX)
chmod 700 "$tmp"
cleanup() { rm -rf "$tmp"; }
trap cleanup EXIT
request_marker="release-$(date +%s)-$RANDOM-$RANDOM"
cat > "$tmp/request.json" <<EOF
{"model":"gpt-5.4-mini","input":"Reply with OK only. Marker: $request_marker","stream":true,"max_output_tokens":8}
EOF
{
  printf 'silent\nshow-error\nmax-time = 120\nno-buffer\n'
  printf 'header = "Authorization: Bearer '
  tr -d '\r\n' < "$canary_key_file"
  printf '"\nheader = "Content-Type: application/json"\n'
} > "$tmp/curl.conf"
chmod 600 "$tmp/curl.conf"
for route in direct dmit; do
  if [[ $route == direct ]]; then route_ip=$direct_ip; else route_ip=$dmit_ip; fi
  user_agent="sub2api-release-$request_marker-$route"
  stream_code=$(curl -K "$tmp/curl.conf" --resolve "$domain:443:$route_ip" -A "$user_agent" -H "x_release_probe: $request_marker-$route" -o "$tmp/stream-$route.txt" -w '%{http_code}' --data-binary @"$tmp/request.json" "https://$domain/v1/responses")
  [[ $stream_code == 200 ]]
  grep -Eq '^data:|response\.(created|completed)' "$tmp/stream-$route.txt"
done
printf '{"email":"' > "$tmp/body.bin"
dd if=/dev/zero bs=65536 count=33 status=none | tr '\0' 'a' >> "$tmp/body.bin"
printf '%s"BROKEN' "$request_marker" >> "$tmp/body.bin"
[[ $(stat -c %s "$tmp/body.bin") -gt 2097152 ]]
large_code=$(curl -sS --resolve "$domain:443:$direct_ip" --max-time 30 -D "$tmp/large.headers" -o "$tmp/large.body" -w '%{http_code}' -H 'Content-Type: application/json' -H "x_release_probe: $request_marker" --data-binary @"$tmp/body.bin" "https://$domain/api/v1/auth/login" || true)
[[ $large_code != 000 && $large_code != 413 ]]
grep -Eiq '^x-request-id:' "$tmp/large.headers"
grep -Eiq 'invalid|json|request' "$tmp/large.body"
nginx -T 2>&1 | grep -Eq '^[[:space:]]*underscores_in_headers[[:space:]]+on;'
critical=$(docker logs --since 15m sub2api 2>&1 | grep -Eic 'panic|fatal|migration.*(failed|error)|database.*(failed|error)|redis.*(failed|error)' || true)
[[ $critical == 0 ]]
for _ in $(seq 1 30); do
  mapfile -t canary_rows < <(docker exec sub2api-postgres psql -X -A -t -F '|' -U sub2api -d sub2api -c "SELECT user_agent, COALESCE(ip_address,'') FROM usage_logs WHERE created_at > NOW() - INTERVAL '15 minutes' AND user_agent IN ('sub2api-release-$request_marker-direct','sub2api-release-$request_marker-dmit') ORDER BY user_agent")
  [[ ${#canary_rows[@]} == 2 ]] && break
  sleep 1
done
[[ ${#canary_rows[@]} == 2 ]]
[[ ${canary_rows[0]%%|*} == "sub2api-release-$request_marker-direct" ]]
[[ ${canary_rows[1]%%|*} == "sub2api-release-$request_marker-dmit" ]]
direct_client_ip=${canary_rows[0]#*|}
dmit_client_ip=${canary_rows[1]#*|}
[[ -n $direct_client_ip && $direct_client_ip == "$dmit_client_ip" ]]
printf 'direct_health=pass\n'
printf 'dmit_health=pass\n'
printf 'streaming=pass\n'
printf 'real_client_ip=pass\n'
printf 'underscore_header_path=pass\n'
printf 'two_mib_reached_app=pass\n'
printf 'startup_logs=pass\n'
printf 'canary_usage_recorded=true\n'
