#!/usr/bin/env bash
set -Eeuo pipefail

release_dir=${RELEASE_DIR:?RELEASE_DIR is required}
[[ $release_dir =~ ^/opt/sub2api/releases/(182-[0-9a-f]{12}-[0-9]+-[0-9a-f]{8})$ ]]
release_id=${BASH_REMATCH[1]}
[[ -d $release_dir && ! -L $release_dir ]]
[[ -f $release_dir/gate.json && ! -L $release_dir/gate.json ]]
[[ -f $release_dir/.prepared && ! -L $release_dir/.prepared ]]
[[ -d $release_dir/.claimed && ! -L $release_dir/.claimed ]]
active_claim=/opt/sub2api/releases/.active-release
[[ -d $active_claim && ! -L $active_claim ]]
[[ $(<"$active_claim/release_id") == "$release_id" ]]
candidate_image_id=$(jq -er '.evidence.candidate_image_id' "$release_dir/gate.json")
candidate_archive_sha=$(jq -er '.evidence.candidate_archive_sha256' "$release_dir/gate.json")
commit=$(jq -er '.manifest.commit_sha' "$release_dir/gate.json")
version=0.1.153-baiyu
candidate_tag="sub2api:baiyu-$version-$commit"
migration=182_upstream_actual_rate_multiplier.sql
migration_checksum=$(jq -er '.manifest.migration_sha256["182_upstream_actual_rate_multiplier.sql"]' "$release_dir/gate.json")
[[ $candidate_image_id =~ ^sha256:[0-9a-f]{64}$ ]]
[[ $candidate_archive_sha =~ ^[0-9a-f]{64}$ ]]
[[ $commit =~ ^[0-9a-f]{40}$ ]]
[[ $migration_checksum =~ ^[0-9a-f]{64}$ ]]
grep -Fxq "release_id=$release_id" "$release_dir/.prepared"
grep -Fxq "candidate_image_id=$candidate_image_id" "$release_dir/.prepared"
[[ $(docker image inspect -f '{{.Id}}' "$candidate_image_id") == "$candidate_image_id" ]]
state_dir="/opt/sub2api/backups/release-state/$release_id"
