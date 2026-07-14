#!/usr/bin/env bash
set -Eeuo pipefail

release_dir=${RELEASE_DIR:?RELEASE_DIR is required}
source /opt/sub2api/releases/.active-release/assets/context.sh
[[ ! -e $release_dir/.consumed ]]
[[ -f $release_dir/.claimed/plaintext-cleaned && ! -L $release_dir/.claimed/plaintext-cleaned ]]
[[ $(docker inspect -f '{{.Image}}' sub2api) == "$candidate_image_id" ]]
[[ $(docker inspect -f '{{.State.Health.Status}}' sub2api) == healthy ]]
printf 'release_id=%s\ncandidate_image_id=%s\nconsumed_at=%s\n' "$release_id" "$candidate_image_id" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" > "$release_dir/.claimed/marker"
chmod 400 "$release_dir/.claimed/marker"
mv -T -- "$release_dir/.claimed" "$release_dir/.consumed"
rm -rf "$active_claim"
[[ ! -e $active_claim && ! -L $active_claim ]]
printf 'gate_consumed=true\n'
