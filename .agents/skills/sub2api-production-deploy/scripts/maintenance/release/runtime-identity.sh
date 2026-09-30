#!/usr/bin/env bash

resolve_production_commit() {
  local image=$1 image_ref=$2 ref_commit= commits
  production_current_commit_sha=
  if [[ $image_ref =~ ^sub2api:baiyu-[0-9][0-9A-Za-z.-]*-([0-9a-f]{40})$ ]]; then
    ref_commit=${BASH_REMATCH[1]}
    if [[ $(docker image inspect -f '{{.Id}}' "$image_ref" 2>/dev/null || true) != "$image" ]]; then ref_commit=; fi
  fi
  commits=$(docker image inspect -f '{{json .RepoTags}}' "$image" | jq -c --arg ref "$ref_commit" '[.[]? | select(test("^sub2api:baiyu-[0-9][0-9A-Za-z.-]*-[0-9a-f]{40}$")) | capture("-(?<sha>[0-9a-f]{40})$").sha] + (if $ref == "" then [] else [$ref] end) | unique')
  if [[ $(jq -r 'length' <<<"$commits") == 1 ]]; then
    production_current_commit_sha=$(jq -r '.[0]' <<<"$commits")
  fi
}
