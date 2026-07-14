#!/usr/bin/env bash
set -Eeuo pipefail

release_dir=${RELEASE_DIR:?RELEASE_DIR is required}
source /opt/sub2api/releases/.active-release/assets/context.sh
domain=${PUBLIC_DOMAIN:?PUBLIC_DOMAIN is required}
direct_ip=${DIRECT_IP:?DIRECT_IP is required}
[[ $(docker inspect -f '{{.Image}}' sub2api) == "$candidate_image_id" ]]
[[ $(docker inspect -f '{{.State.Health.Status}}' sub2api) == healthy ]]
[[ $(systemctl is-active nginx) == active ]]
[[ $(curl -sS --resolve "$domain:443:$direct_ip" --max-time 15 -o /dev/null -w '%{http_code}' "https://$domain/health") == 200 ]]
tmp=$(mktemp -d /tmp/sub2api-release-verify.XXXXXX)
chmod 700 "$tmp"
cleanup() { rm -rf "$tmp"; }
trap cleanup EXIT
request_marker="release-$(date +%s)-$RANDOM-$RANDOM"
printf '{"email":"' > "$tmp/body.bin"
dd if=/dev/zero bs=65536 count=33 status=none | tr '\0' 'a' >> "$tmp/body.bin"
printf '%s"BROKEN' "$request_marker" >> "$tmp/body.bin"
[[ $(stat -c %s "$tmp/body.bin") -gt 2097152 ]]
internal_large_code=$(curl -sS --max-time 30 -D "$tmp/large.internal.headers" -o "$tmp/large.internal.body" -w '%{http_code}' -H 'Content-Type: application/json' -H "x_release_probe: $request_marker" --data-binary @"$tmp/body.bin" "http://127.0.0.1:18080/api/v1/auth/login" || true)
[[ $internal_large_code != 000 && $internal_large_code != 413 ]]
grep -Eiq '^x-request-id:' "$tmp/large.internal.headers"
internal_body_sha=$(sha256sum "$tmp/large.internal.body" | awk '{print $1}')
large_code=$(curl -sS --resolve "$domain:443:$direct_ip" --max-time 30 -D "$tmp/large.headers" -o "$tmp/large.body" -w '%{http_code}' -H 'Content-Type: application/json' -H "x_release_probe: $request_marker" --data-binary @"$tmp/body.bin" "https://$domain/api/v1/auth/login" || true)
[[ $large_code == "$internal_large_code" ]]
grep -Eiq '^x-request-id:' "$tmp/large.headers"
[[ $(sha256sum "$tmp/large.body" | awk '{print $1}') == "$internal_body_sha" ]]
nginx -T 2>&1 | grep -Eq '^[[:space:]]*underscores_in_headers[[:space:]]+on;'
critical=$(docker logs --since 15m sub2api 2>&1 | grep -Eic 'panic|fatal|migration.*(failed|error)|database.*(failed|error)|redis.*(failed|error)' || true)
[[ $critical == 0 ]]
printf 'direct_health=pass\n'
printf 'underscore_header_path=pass\n'
printf 'two_mib_reached_app=pass\n'
printf 'startup_logs=pass\n'
