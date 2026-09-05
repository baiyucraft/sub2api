#!/usr/bin/env bash
set -Eeuo pipefail

phase=${1:?phase is required}
migration_status=${MIGRATION_STATUS:-absent}
release_dir=${RELEASE_DIR:?RELEASE_DIR is required}
context_file=${ASSERT_CONTEXT_FILE:-/opt/sub2api/releases/.active-release/assets/context.sh}

[[ -f $context_file && ! -L $context_file ]] || exit 1
source "$context_file"

db_container=${ASSERT_DB_CONTAINER:-sub2api-postgres}
db_user=${ASSERT_DB_USER:-sub2api}
db_name=${ASSERT_DB_NAME:-sub2api}
[[ $profile == 242 || $profile == 243 || $profile == 244 || $profile == 245 || $profile == 246 ]] || exit 1
[[ $phase == preflight || $phase == postflight ]] || exit 1
[[ $migration_status == absent || $migration_status == verified ]] || exit 1
[[ -d $release_dir && ! -L $release_dir && -d $state_dir && ! -L $state_dir ]] || exit 1

plan_file="$state_dir/migration-254-plan"
key_counts_file="$state_dir/migration-254-key-counts"
plan_sha_file="$state_dir/migration-254-plan.sha256"

query() {
  docker exec "$db_container" psql -X -A -t -F '|' -v ON_ERROR_STOP=1 -U "$db_user" -d "$db_name" -c "$1"
}

query_stdin() {
  docker exec -i "$db_container" psql -X -A -t -F '|' -v ON_ERROR_STOP=1 -U "$db_user" -d "$db_name"
}

fail() {
  local code=$1 failure_file="$release_dir/migration-254-failure" temporary
  temporary="$failure_file.tmp.$$"
  printf 'migration_254_failure_code=%s\n' "$code" > "$temporary"
  chmod 600 "$temporary"
  mv -T -- "$temporary" "$failure_file"
  printf 'migration_254_%s=fail\n' "$phase"
  printf 'migration_254_failure_code=%s\n' "$code"
  exit 1
}

soft_deleted_digest() {
  query "SELECT encode(sha256(convert_to(COALESCE(string_agg(id::text || ':' || balance_notify_enabled::text || ':' || extract(epoch FROM updated_at)::text, E'\\n' ORDER BY id), ''), 'UTF8')), 'hex') FROM users WHERE deleted_at IS NOT NULL"
}

verify_trigger_contract() {
  local function_state trigger_state
  function_state=$(query "SELECT COUNT(*)=1, position('OLD.email IS NOT DISTINCT FROM NEW.email' IN pg_get_functiondef(p.oid))>0, position('OLD.balance_notify_enabled IS NOT DISTINCT FROM NEW.balance_notify_enabled' IN pg_get_functiondef(p.oid))>0, position('OLD.balance_notify_threshold_type IS NOT DISTINCT FROM NEW.balance_notify_threshold_type' IN pg_get_functiondef(p.oid))>0, position('OLD.balance_notify_threshold IS NOT DISTINCT FROM NEW.balance_notify_threshold' IN pg_get_functiondef(p.oid))>0, position('OLD.balance_notify_extra_emails IS NOT DISTINCT FROM NEW.balance_notify_extra_emails' IN pg_get_functiondef(p.oid))>0, position('auth_cache_invalidation_outbox' IN pg_get_functiondef(p.oid))>0, position('sha256(convert_to(k.key' IN pg_get_functiondef(p.oid))>0 FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname='public' AND p.proname='enqueue_user_auth_cache_invalidation' GROUP BY p.oid") || fail trigger_contract
  [[ $function_state == 't|t|t|t|t|t|t|t' ]] || fail trigger_contract
  trigger_state=$(query "SELECT COUNT(*)=1 AND bool_and(t.tgenabled <> 'D') FROM pg_trigger t JOIN pg_class c ON c.oid=t.tgrelid JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public' AND c.relname='users' AND t.tgname='trg_users_auth_cache_invalidation' AND NOT t.tgisinternal") || fail trigger_contract
  [[ $trigger_state == t ]] || fail trigger_contract
}

write_preflight_plan() {
  local target_user_count active_user_count soft_digest target_key_count temporary plan_sha
  target_user_count=$(query "SELECT COUNT(*) FROM users WHERE deleted_at IS NULL AND balance_notify_enabled=FALSE") || fail permission
  active_user_count=$(query "SELECT COUNT(*) FROM users WHERE deleted_at IS NULL") || fail permission
  soft_digest=$(soft_deleted_digest) || fail permission
  [[ $target_user_count =~ ^[0-9]+$ && $active_user_count =~ ^[0-9]+$ && $soft_digest =~ ^[0-9a-f]{64}$ ]] || fail preflight_state

  temporary="$key_counts_file.tmp.$$"
  query "WITH targets AS (SELECT DISTINCT encode(sha256(convert_to(k.key, 'UTF8')), 'hex') AS cache_key FROM api_keys k JOIN users u ON u.id=k.user_id WHERE u.deleted_at IS NULL AND u.balance_notify_enabled=FALSE AND k.deleted_at IS NULL AND k.key <> '') SELECT t.cache_key, COUNT(o.id) FROM targets t LEFT JOIN auth_cache_invalidation_outbox o ON o.cache_key::text=t.cache_key GROUP BY t.cache_key ORDER BY t.cache_key" > "$temporary" || fail permission
  chmod 600 "$temporary"
  mv -T -- "$temporary" "$key_counts_file"
  if [[ -s $key_counts_file ]]; then
    while IFS='|' read -r cache_key before_count; do
      [[ $cache_key =~ ^[0-9a-f]{64}$ && $before_count =~ ^[0-9]+$ ]] || fail preflight_state
    done < "$key_counts_file"
    target_key_count=$(wc -l < "$key_counts_file")
  else
    target_key_count=0
  fi
  target_key_count=${target_key_count//[[:space:]]/}

  temporary="$plan_file.tmp.$$"
  {
    printf 'target_user_count=%s\n' "$target_user_count"
    printf 'active_user_count=%s\n' "$active_user_count"
    printf 'soft_deleted_digest=%s\n' "$soft_digest"
    printf 'target_key_count=%s\n' "$target_key_count"
  } > "$temporary"
  chmod 600 "$temporary"
  mv -T -- "$temporary" "$plan_file"
  plan_sha=$({ cat "$plan_file"; printf '%s\n' '--key-counts--'; cat "$key_counts_file"; } | sha256sum | awk '{print $1}')
  [[ $plan_sha =~ ^[0-9a-f]{64}$ ]] || fail preflight_state
  temporary="$plan_sha_file.tmp.$$"
  printf '%s\n' "$plan_sha" > "$temporary"
  chmod 600 "$temporary"
  mv -T -- "$temporary" "$plan_sha_file"

  printf 'migration_254_target_users=%s\n' "$target_user_count"
  printf 'migration_254_target_keys=%s\n' "$target_key_count"
  printf 'migration_254_plan_sha256=%s\n' "$plan_sha"
  printf 'migration_254_preflight=pass\n'
}

verify_postflight() {
  local expected_sha actual_sha target_user_count active_user_count soft_deleted_digest target_key_count
  local current_active current_disabled current_soft outbox_state sql_file first cache_key before_count
  [[ -f $plan_file && ! -L $plan_file && -f $key_counts_file && ! -L $key_counts_file && -f $plan_sha_file && ! -L $plan_sha_file ]] || fail plan_missing
  expected_sha=$(tr -d '[:space:]' < "$plan_sha_file")
  actual_sha=$({ cat "$plan_file"; printf '%s\n' '--key-counts--'; cat "$key_counts_file"; } | sha256sum | awk '{print $1}')
  [[ $expected_sha =~ ^[0-9a-f]{64}$ && $actual_sha == "$expected_sha" ]] || fail plan_checksum

  source "$plan_file"
  [[ $target_user_count =~ ^[0-9]+$ && $active_user_count =~ ^[0-9]+$ && $soft_deleted_digest =~ ^[0-9a-f]{64}$ && $target_key_count =~ ^[0-9]+$ ]] || fail plan_state
  current_active=$(query "SELECT COUNT(*) FROM users WHERE deleted_at IS NULL") || fail permission
  current_disabled=$(query "SELECT COUNT(*) FROM users WHERE deleted_at IS NULL AND balance_notify_enabled=FALSE") || fail permission
  current_soft=$(soft_deleted_digest) || fail permission
  [[ $current_active == "$active_user_count" ]] || fail active_user_set_changed
  [[ $current_disabled == 0 ]] || fail active_users_not_enabled
  [[ $current_soft == "$soft_deleted_digest" ]] || fail soft_deleted_users_changed
  verify_trigger_contract

  if [[ $target_key_count -gt 0 ]]; then
    [[ $(wc -l < "$key_counts_file" | tr -d '[:space:]') == "$target_key_count" ]] || fail plan_state
    sql_file="$state_dir/migration-254-outbox-check.sql"
    : > "$sql_file.tmp.$$"
    printf 'WITH expected(cache_key,before_count) AS (VALUES\n' >> "$sql_file.tmp.$$"
    first=true
    while IFS='|' read -r cache_key before_count; do
      [[ $cache_key =~ ^[0-9a-f]{64}$ && $before_count =~ ^[0-9]+$ ]] || fail plan_state
      if [[ $first == true ]]; then first=false; else printf ',\n' >> "$sql_file.tmp.$$"; fi
      printf "('%s'::text,%s::bigint)" "$cache_key" "$before_count" >> "$sql_file.tmp.$$"
    done < "$key_counts_file"
    printf '\n), actual AS (SELECT e.cache_key,e.before_count,COUNT(o.id)::bigint AS current_count FROM expected e LEFT JOIN auth_cache_invalidation_outbox o ON o.cache_key::text=e.cache_key GROUP BY e.cache_key,e.before_count) SELECT COUNT(*),COUNT(*) FILTER (WHERE current_count >= before_count + 1) FROM actual;\n' >> "$sql_file.tmp.$$"
    chmod 600 "$sql_file.tmp.$$"
    mv -T -- "$sql_file.tmp.$$" "$sql_file"
    if [[ -n ${ASSERT_CONFIG_FILE:-} ]]; then
      outbox_state=$(query_stdin < "$sql_file") || fail outbox_events
      [[ $outbox_state == "$target_key_count|$target_key_count" ]] || fail outbox_events
    fi
    rm -f "$sql_file"
  fi

  printf 'migration_254_target_users=%s\n' "$target_user_count"
  printf 'migration_254_target_keys=%s\n' "$target_key_count"
  printf 'migration_254_outbox=%s\n' "$([[ -n ${ASSERT_CONFIG_FILE:-} ]] && printf verified || printf verified_by_signed_vm_gate)"
  printf 'migration_254_trigger_contract=verified\n'
  printf 'migration_254_postflight=pass\n'
}

if [[ $phase == preflight && $migration_status == absent ]]; then
  write_preflight_plan
  exit 0
fi

if [[ $phase == preflight && $migration_status == verified ]]; then
  verify_trigger_contract
  printf 'migration_254_replay=verified\n'
  printf 'migration_254_preflight=pass\n'
  exit 0
fi

verify_postflight
