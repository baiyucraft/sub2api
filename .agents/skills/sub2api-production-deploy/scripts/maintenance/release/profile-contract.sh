#!/usr/bin/env bash

is_configured_release_profile() {
  [[ $1 =~ ^[1-9][0-9]{2,8}$ ]] && (( $1 > 286 ))
}

validate_release_profile_contract_file() {
  local contract=$1 size
  [[ -f $contract && ! -L $contract ]] || return 1
  size=$(wc -c < "$contract") || return 1
  (( size <= 131072 )) || return 1
  jq -es '
    length == 1 and
    (.[0] | type == "object" and keys == ["profiles", "schema"] and
      .schema == 1 and (.profiles | type == "array") and
      (.profiles | to_entries | all(
        .key as $i | .value |
        type == "object" and keys == ["new_migrations", "parent", "profile", "version"] and
        .profile == (($i + 287) | tostring) and .parent == (($i + 286) | tostring) and
        (.version | type == "string" and test("\\A(0|[1-9][0-9]*)\\.(0|[1-9][0-9]*)\\.(0|[1-9][0-9]*)-baiyu\\z")) and
        (.new_migrations | type == "array" and all(type == "string" and test("\\A[0-9]{3}_[a-z0-9_]+\\.sql\\z")))
      )) and
      ([313, (.profiles[] .new_migrations[] | .[0:3] | tonumber)] |
        . as $numbers | all(range(1; length); $numbers[.] > $numbers[. - 1])))
  ' "$contract" >/dev/null
}

# The caller authenticates the manifest and the helper before executing this
# check. Match its signed profile tuple against the exact committed contract.
validate_release_profile_contract() {
  local document=$1 contract=$2 profile_id=$3 scope=${4:-production} digest
  is_configured_release_profile "$profile_id" || return 1
  [[ -f $document && ! -L $document ]] || return 1
  validate_release_profile_contract_file "$contract" || return 1
  digest=$(sha256sum "$contract") || return 1
  digest=${digest%% *}
  jq -es --arg profile "$profile_id" --arg digest "$digest" --arg scope "$scope" --slurpfile contract "$contract" '
    if length != 1 or ($contract | length) != 1 then false
    else
      (.[0] | if has("manifest") then .manifest else . end) as $m |
      $contract[0] as $c |
      $m.schema == 2 and
      (if $scope == "production" then $m.deployment_mode == "downtime"
       elif $scope == "vm-only" then $m.scope == "vm-only" and $m.vm_only_schema == 1 and ($m | has("deployment_mode") | not)
       else false end) and
      $m.profile == $profile and $m.profile_contract_sha256 == $digest and
      $c.schema == 1 and ($c.profiles | type) == "array" and
      $c.profiles[-1] == {
        profile: $m.profile,
        parent: $m.parent_profile,
        version: $m.version,
        new_migrations: $m.new_migrations
      }
    end
  ' "$document" >/dev/null
}

validate_migration_hook_profile() {
  local claim=${active_claim:-/opt/sub2api/releases/.active-release}
  if [[ ${release_profile_contract_verified:-} == 1 ]]; then
    validate_release_profile_contract "$claim/gate.json" "$claim/profile-contract.json" "$1"
    return
  fi
  local document=${ASSERT_MANIFEST_FILE:-$claim/gate.json} scope=production
  if [[ -n ${ASSERT_MANIFEST_FILE:-} ]] && jq -e '.scope == "vm-only"' "$document" >/dev/null; then
    scope=vm-only
  fi
  validate_release_profile_contract "$document" "${ASSERT_PROFILE_CONTRACT_FILE:-$claim/profile-contract.json}" "$1" "$scope"
}
