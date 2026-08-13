#!/usr/bin/env bash
set -Eeuo pipefail

release_dir=${RELEASE_DIR:?RELEASE_DIR is required}
source /opt/sub2api/releases/.active-release/assets/context.sh
[[ ! -e $release_dir/.consumed ]]
[[ -f $active_claim/plaintext-cleaned && ! -L $active_claim/plaintext-cleaned ]]
[[ -f /opt/sub2api/active-app && ! -L /opt/sub2api/active-app ]]
active_container=$(sed -n 's/^container=//p' /opt/sub2api/active-app)
[[ $(docker inspect -f '{{.Image}}' "$active_container") == "$candidate_image_id" ]]
[[ $(docker inspect -f '{{.State.Health.Status}}' "$active_container") == healthy ]]
printf 'release_id=%s\ncandidate_image_id=%s\nconsumed_at=%s\n' "$release_id" "$candidate_image_id" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" > "$active_claim/marker"
chmod 400 "$active_claim/marker"
mv -T -- "$active_claim" "$release_dir/.consumed"
[[ -d $release_dir/.consumed && ! -L $release_dir/.consumed && ! -e $active_claim ]]
printf 'gate_consumed=true\n'
