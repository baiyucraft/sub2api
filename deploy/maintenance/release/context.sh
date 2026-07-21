#!/usr/bin/env bash
set -Eeuo pipefail

release_dir=${RELEASE_DIR:?RELEASE_DIR is required}
[[ $release_dir =~ ^/opt/sub2api/releases/((182|187|191|192|194|195|197|198)-[0-9a-f]{12}-[0-9]+-[0-9a-f]{8})$ ]]
release_id=${BASH_REMATCH[1]}
[[ -d $release_dir && ! -L $release_dir ]]
[[ -f $release_dir/.prepared && ! -L $release_dir/.prepared ]]
active_claim=/opt/sub2api/releases/.active-release
[[ -d $active_claim && ! -L $active_claim ]]
grep -Fxq "release_id=$release_id" "$active_claim/release_id"
[[ -f $active_claim/gate.json && ! -L $active_claim/gate.json ]]
(cd "$active_claim" && sha256sum -c CLAIM_SHA256SUMS >/dev/null)
assets_dir="$active_claim/assets"
candidate_image_id=$(jq -er '.evidence.candidate_image_id' "$active_claim/gate.json")
candidate_archive_sha=$(jq -er '.evidence.candidate_archive_sha256' "$active_claim/gate.json")
commit=$(jq -er '.manifest.commit_sha' "$active_claim/gate.json")
profile=$(jq -er '.manifest.profile' "$active_claim/gate.json")
version=$(jq -er '.manifest.version' "$active_claim/gate.json")
candidate_tag="sub2api:baiyu-$version-$commit"
mapfile -t migrations < <(jq -er '.manifest.migrations[]' "$active_claim/gate.json")
[[ $candidate_image_id =~ ^sha256:[0-9a-f]{64}$ ]]
[[ $candidate_archive_sha =~ ^[0-9a-f]{64}$ ]]
[[ $commit =~ ^[0-9a-f]{40}$ ]]
[[ ${#migrations[@]} -gt 0 ]]
grep -Fxq "release_id=$release_id" "$release_dir/.prepared"
grep -Fxq "candidate_image_id=$candidate_image_id" "$release_dir/.prepared"
[[ $(docker image inspect -f '{{.Id}}' "$candidate_image_id") == "$candidate_image_id" ]]
state_dir="/opt/sub2api/backups/release-state/$release_id"

assert_prompt_audit_disabled() {
  if [[ $profile != 194 && $profile != 195 && $profile != 197 && $profile != 198 ]]; then
    printf 'prompt_audit_disabled=not_applicable\n'
    printf 'prompt_audit_jobs=not_applicable\n'
    printf 'prompt_audit_events=not_applicable\n'
    return 0
  fi
  local row
  row=$(docker exec sub2api-postgres psql -X -A -t -F '|' -U sub2api -d sub2api -c "
WITH config AS (
  SELECT COALESCE(NULLIF((SELECT value FROM settings WHERE key='prompt_audit_config'), ''), '{}')::jsonb AS value
)
SELECT
  NOT COALESCE((value->>'enabled')::boolean, false)
    AND NOT COALESCE((value->>'blocking_enabled')::boolean, false)
    AND NOT COALESCE((value->>'store_pass_events')::boolean, false)
    AND jsonb_typeof(COALESCE(value->'endpoints', '[]'::jsonb)) = 'array'
    AND jsonb_array_length(COALESCE(value->'endpoints', '[]'::jsonb)) = 0,
  (SELECT COUNT(*) FROM prompt_audit_jobs),
  (SELECT COUNT(*) FROM prompt_audit_events)
FROM config")
  [[ $row == 't|0|0' ]]
  printf 'prompt_audit_disabled=true\n'
  printf 'prompt_audit_jobs=0\n'
  printf 'prompt_audit_events=0\n'
}
