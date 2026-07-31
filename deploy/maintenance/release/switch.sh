#!/usr/bin/env bash
set -Eeuo pipefail

deploy_dir=${DEPLOY_DIR:-/opt/sub2api}
release_dir=${RELEASE_DIR:?RELEASE_DIR is required}
source /opt/sub2api/releases/.active-release/assets/context.sh
cd "$deploy_dir"
[[ $(docker inspect -f '{{.State.Status}}' sub2api) != running ]]
[[ $(systemctl is-active nginx 2>/dev/null || true) != active ]]
[[ $(docker image inspect -f '{{.Id}}' "$candidate_image_id") == "$candidate_image_id" ]]
[[ -d $state_dir && ! -L $state_dir ]]
if [[ $profile == 195 || $profile == 197 || $profile == 198 || $profile == 199 || $profile == 202 || $profile == 206 || $profile == 207 || $profile == 208 || $profile == 209 || $profile == 210 ]]; then
  [[ -f $state_dir/migration-195-plan.sha256 && ! -L $state_dir/migration-195-plan.sha256 ]]
  migration_status=$(<"$state_dir/migration-195-status")
  export MIGRATION_STATUS="$migration_status"
fi
active_override="$deploy_dir/docker-compose.release-active.yml"
override_tmp="$active_override.tmp.$$"
cat > "$override_tmp" <<EOF
services:
  sub2api:
    image: $candidate_image_id
    environment:
      UPSTREAM_SYNC_AUTO_ENABLED: \${UPSTREAM_SYNC_AUTO_ENABLED:-false}
EOF
chmod 600 "$override_tmp"
mv -T -- "$override_tmp" "$active_override"
env_tmp="$deploy_dir/.env.release.$$"
awk '!/^(COMPOSE_FILE|SUB2API_RELEASE_IMAGE|UPSTREAM_SYNC_AUTO_ENABLED)=/' "$deploy_dir/.env" > "$env_tmp"
printf 'COMPOSE_FILE=docker-compose.yml:docker-compose.release-active.yml\n' >> "$env_tmp"
printf 'SUB2API_RELEASE_IMAGE=%s\n' "$candidate_image_id" >> "$env_tmp"
printf 'UPSTREAM_SYNC_AUTO_ENABLED=false\n' >> "$env_tmp"
chmod --reference="$deploy_dir/.env" "$env_tmp"
mv -T -- "$env_tmp" "$deploy_dir/.env"
export BIND_HOST=127.0.0.1
compose_image=$(docker compose config --format json | jq -r '.services.sub2api.image // empty')
[[ $(docker image inspect -f '{{.Id}}' "$compose_image") == "$candidate_image_id" ]]
[[ $(docker compose config --format json | jq -r '.services.sub2api.environment.UPSTREAM_SYNC_AUTO_ENABLED') == false ]]
mapfile -t migrations < <(jq -er '.manifest.migrations[]' "$active_claim/gate.json")
migration_container="sub2api-migrate-$release_id"
[[ -z $(docker ps -aq -f "name=^${migration_container}$") ]]
docker compose run --name "$migration_container" --no-deps sub2api /app/sub2api --migrate-only >/dev/null 2>&1
while IFS=$'\t' read -r migration migration_checksum; do
  recorded=$(docker exec sub2api-postgres psql -X -A -t -U sub2api -d sub2api -c "SELECT checksum FROM schema_migrations WHERE filename='$migration'")
  [[ $recorded == "$migration_checksum" ]]
done < <(jq -r '.manifest.migration_sha256 | to_entries[] | [.key,.value] | @tsv' "$active_claim/gate.json")
if [[ $profile == 198 || $profile == 199 || $profile == 202 || $profile == 206 || $profile == 207 || $profile == 208 || $profile == 209 || $profile == 210 ]]; then
  managed_monitor_key_name_state=$(docker exec sub2api-postgres psql -X -A -t -F '|' -U sub2api -d sub2api -c "SELECT character_maximum_length, (SELECT COUNT(*) FROM api_keys k JOIN channel_monitors m ON m.id=k.managed_monitor_id AND m.managed_api_key_id=k.id WHERE k.purpose='managed_monitor' AND k.deleted_at IS NULL AND k.name IS DISTINCT FROM '监控-' || BTRIM(m.name)) FROM information_schema.columns WHERE table_schema='public' AND table_name='api_keys' AND column_name='name'")
  [[ $managed_monitor_key_name_state == '103|0' ]]
fi
if [[ $profile == 199 || $profile == 202 || $profile == 206 || $profile == 207 || $profile == 208 || $profile == 209 || $profile == 210 ]]; then
  reasoning_effort_policy_state=$(docker exec sub2api-postgres psql -X -A -t -F '|' -U sub2api -d sub2api -c "SELECT COALESCE(MAX(CASE WHEN column_name='max_reasoning_effort' THEN data_type || ':' || is_nullable || ':' || column_default END),''), COALESCE(MAX(CASE WHEN column_name='reasoning_effort_mappings' THEN data_type || ':' || is_nullable || ':' || column_default END),'') FROM information_schema.columns WHERE table_schema='public' AND table_name='groups' AND column_name IN ('max_reasoning_effort','reasoning_effort_mappings')")
  [[ $reasoning_effort_policy_state == *'character varying:NO:'*"''::character varying"*'|'*'jsonb:NO:'*"'[]'::jsonb"* ]]
fi
if [[ $profile == 202 || $profile == 206 || $profile == 207 || $profile == 208 || $profile == 209 || $profile == 210 ]]; then
  alipay_mobile_precreate_deep_link_state=$(docker exec sub2api-postgres psql -X -A -t -F '|' -U sub2api -d sub2api -c "SELECT COUNT(*), COUNT(*) FILTER (WHERE value IN ('true','false')) FROM settings WHERE key='ALIPAY_MOBILE_PRECREATE_DEEP_LINK'")
  [[ $alipay_mobile_precreate_deep_link_state == '1|1' ]]
  group_auth_cache_image_generation_state=$(docker exec sub2api-postgres psql -X -A -t -F '|' -U sub2api -d sub2api -c "SELECT COUNT(*) FILTER (WHERE pg_get_functiondef(p.oid) LIKE '%OLD.allow_image_generation IS NOT DISTINCT FROM NEW.allow_image_generation%'), (SELECT COUNT(*) FROM pg_trigger t WHERE t.tgrelid='groups'::regclass AND t.tgname='trg_groups_auth_cache_invalidation' AND NOT t.tgisinternal AND t.tgenabled <> 'D') FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname='public' AND p.proname='enqueue_group_auth_cache_invalidation' AND pg_get_function_identity_arguments(p.oid)=''")
  [[ $group_auth_cache_image_generation_state == '1|1' ]]
  composite_model_routes_state=$(docker exec sub2api-postgres psql -X -A -t -F '|' -U sub2api -d sub2api -c "SELECT (SELECT COUNT(*) FROM information_schema.columns WHERE table_schema='public' AND table_name='composite_model_routes'), (SELECT COUNT(*) FROM pg_constraint WHERE conrelid='composite_model_routes'::regclass AND contype='c' AND conname IN ('composite_model_routes_match_type_check','composite_model_routes_endpoint_check','composite_model_routes_target_platform_check')), (SELECT COUNT(*) FROM pg_constraint WHERE conrelid='composite_model_routes'::regclass AND contype='f' AND confrelid='groups'::regclass AND confdeltype='c'), (SELECT COUNT(*) FROM pg_indexes WHERE schemaname='public' AND tablename='composite_model_routes' AND indexname IN ('idx_composite_model_routes_unique_active','idx_composite_model_routes_group_enabled','idx_composite_model_routes_group_priority'))")
  [[ $composite_model_routes_state == '13|3|1|3' ]]
fi
if [[ $profile == 206 || $profile == 207 || $profile == 208 || $profile == 209 || $profile == 210 ]]; then
  session_id_columns_state=$(docker exec sub2api-postgres psql -X -A -t -F '|' -U sub2api -d sub2api -c "SELECT COUNT(*) FILTER (WHERE table_name='usage_logs' AND data_type='character varying' AND character_maximum_length=255 AND is_nullable='YES'), COUNT(*) FILTER (WHERE table_name='batch_image_jobs' AND data_type='character varying' AND character_maximum_length=255 AND is_nullable='YES') FROM information_schema.columns WHERE table_schema='public' AND column_name='session_id'")
  [[ $session_id_columns_state == '1|1' ]]
  live_request_type_state=$(docker exec sub2api-postgres psql -X -A -t -U sub2api -d sub2api -c "SELECT COUNT(*)=1 AND BOOL_AND(pg_get_constraintdef(oid) LIKE '%request_type <= 5%') FROM pg_constraint WHERE conrelid='usage_logs'::regclass AND conname='usage_logs_request_type_check'")
  [[ $live_request_type_state == t ]]
  group_allow_live_state=$(docker exec sub2api-postgres psql -X -A -t -F '|' -U sub2api -d sub2api -c "SELECT data_type,is_nullable,column_default FROM information_schema.columns WHERE table_schema='public' AND table_name='groups' AND column_name='allow_live'")
  [[ $group_allow_live_state == 'boolean|NO|false' ]]
  email_alias_index_state=$(docker exec sub2api-postgres psql -X -A -t -F '|' -U sub2api -d sub2api -c "SELECT i.indisvalid,i.indisready,pg_get_expr(i.indexprs,i.indrelid)='replace(lower(TRIM(BOTH FROM email)), ''.''::text, ''''::text)',pg_get_expr(i.indpred,i.indrelid)='(deleted_at IS NULL)',o.opcname='text_pattern_ops' FROM pg_index i JOIN pg_class c ON c.oid=i.indexrelid JOIN pg_opclass o ON o.oid=i.indclass[0] WHERE c.relname='idx_users_email_dot_stripped'")
  [[ $email_alias_index_state == 't|t|t|t|t' ]]
fi
if [[ $profile == 208 || $profile == 209 || $profile == 210 ]]; then
  passkey_schema_state=$(docker exec sub2api-postgres psql -X -A -t -F '|' -U sub2api -d sub2api -c "SELECT to_regclass('public.passkey_user_handles') IS NOT NULL, to_regclass('public.passkey_credentials') IS NOT NULL, (SELECT COUNT(*) FROM information_schema.columns WHERE table_schema='public' AND table_name='passkey_user_handles' AND ((column_name='user_id' AND data_type='bigint' AND is_nullable='NO') OR (column_name='user_handle' AND data_type='bytea' AND is_nullable='NO') OR (column_name='created_at' AND data_type='timestamp with time zone' AND is_nullable='NO' AND column_default LIKE 'now()%'))), (SELECT COUNT(*) FROM information_schema.columns WHERE table_schema='public' AND table_name='passkey_credentials' AND ((column_name='id' AND data_type='bigint' AND is_nullable='NO' AND column_default LIKE 'nextval(%') OR (column_name='user_id' AND data_type='bigint' AND is_nullable='NO') OR (column_name='credential_id' AND data_type='bytea' AND is_nullable='NO') OR (column_name='name' AND data_type='character varying' AND character_maximum_length=100 AND is_nullable='NO' AND column_default LIKE '''Passkey''%') OR (column_name='credential_data' AND data_type='jsonb' AND is_nullable='NO') OR (column_name='last_used_at' AND data_type='timestamp with time zone' AND is_nullable='YES') OR (column_name IN ('created_at','updated_at') AND data_type='timestamp with time zone' AND is_nullable='NO' AND column_default LIKE 'now()%'))), (SELECT COUNT(*) FROM pg_constraint WHERE conrelid IN ('passkey_user_handles'::regclass,'passkey_credentials'::regclass) AND contype='p'), (SELECT COUNT(*) FROM pg_constraint WHERE conrelid IN ('passkey_user_handles'::regclass,'passkey_credentials'::regclass) AND contype='u'), (SELECT COUNT(*) FROM pg_constraint WHERE conrelid IN ('passkey_user_handles'::regclass,'passkey_credentials'::regclass) AND contype='f' AND confrelid='users'::regclass AND confdeltype='c'), (SELECT COUNT(*) FROM pg_indexes WHERE schemaname='public' AND tablename='passkey_credentials' AND indexname IN ('passkey_credentials_user_id_idx','passkey_credentials_last_used_at_idx'))")
  [[ $passkey_schema_state == 't|t|3|8|2|2|2|2' ]]
fi
if [[ $profile == 209 || $profile == 210 ]]; then
  user_usage_aggregation_schema_state=$(docker exec sub2api-postgres psql -X -A -t -F '|' -U sub2api -d sub2api -c "SELECT to_regclass('public.usage_dashboard_user_hourly') IS NOT NULL, to_regclass('public.usage_dashboard_user_daily') IS NOT NULL, to_regclass('public.usage_dashboard_user_backfill_state') IS NOT NULL, (SELECT COUNT(*) FROM information_schema.columns WHERE table_schema='public' AND table_name='usage_dashboard_user_hourly' AND ((column_name='bucket_start' AND data_type='timestamp with time zone' AND is_nullable='NO') OR (column_name='user_id' AND data_type='bigint' AND is_nullable='NO') OR (column_name IN ('input_tokens','output_tokens','cache_creation_tokens','cache_read_tokens') AND data_type='bigint' AND is_nullable='NO' AND column_default LIKE '0%') OR (column_name IN ('user_spend','account_cost') AND data_type='numeric' AND numeric_precision=20 AND numeric_scale=10 AND is_nullable='NO' AND column_default LIKE '0%') OR (column_name='computed_at' AND data_type='timestamp with time zone' AND is_nullable='NO' AND column_default LIKE 'now()%'))), (SELECT COUNT(*) FROM information_schema.columns WHERE table_schema='public' AND table_name='usage_dashboard_user_daily' AND ((column_name='bucket_date' AND data_type='date' AND is_nullable='NO') OR (column_name='user_id' AND data_type='bigint' AND is_nullable='NO') OR (column_name IN ('input_tokens','output_tokens','cache_creation_tokens','cache_read_tokens') AND data_type='bigint' AND is_nullable='NO' AND column_default LIKE '0%') OR (column_name IN ('user_spend','account_cost') AND data_type='numeric' AND numeric_precision=20 AND numeric_scale=10 AND is_nullable='NO' AND column_default LIKE '0%') OR (column_name='computed_at' AND data_type='timestamp with time zone' AND is_nullable='NO' AND column_default LIKE 'now()%'))), (SELECT COUNT(*) FROM information_schema.columns WHERE table_schema='public' AND table_name='usage_dashboard_user_backfill_state' AND ((column_name='id' AND data_type='smallint' AND is_nullable='NO') OR (column_name IN ('earliest_covered_date','last_completed_date') AND data_type='date' AND is_nullable='YES') OR (column_name='status' AND data_type='character varying' AND character_maximum_length=20 AND is_nullable='NO' AND column_default LIKE '''unavailable''%') OR (column_name IN ('coverage_start','coverage_end','target_end','completed_at') AND data_type='timestamp with time zone' AND is_nullable='YES') OR (column_name='attempt_count' AND data_type='bigint' AND is_nullable='NO' AND column_default LIKE '0%') OR (column_name='last_error' AND data_type='text' AND is_nullable='YES') OR (column_name='updated_at' AND data_type='timestamp with time zone' AND is_nullable='NO' AND column_default LIKE 'now()%'))), (SELECT COUNT(*) FROM pg_constraint WHERE conrelid IN ('usage_dashboard_user_hourly'::regclass,'usage_dashboard_user_daily'::regclass,'usage_dashboard_user_backfill_state'::regclass) AND contype='p'), (SELECT COUNT(*) FROM pg_constraint WHERE conrelid IN ('usage_dashboard_user_hourly'::regclass,'usage_dashboard_user_daily'::regclass) AND contype='f' AND confrelid='users'::regclass AND confdeltype='c'), (SELECT COUNT(*) FROM pg_indexes WHERE schemaname='public' AND indexname IN ('idx_usage_dashboard_user_hourly_user_bucket','idx_usage_dashboard_user_daily_user_bucket')), (SELECT COUNT(*) FROM pg_constraint WHERE conrelid='usage_dashboard_user_backfill_state'::regclass AND contype='c'), (SELECT COUNT(*)=1 AND BOOL_AND(id=1 AND status IN ('available','building','partial','unavailable')) FROM usage_dashboard_user_backfill_state)")
  [[ $user_usage_aggregation_schema_state == 't|t|t|9|9|11|3|2|2|3|t' ]]
fi
[[ $(docker inspect -f '{{.Image}}' "$migration_container") == "$candidate_image_id" ]]
[[ $(docker inspect -f '{{.State.ExitCode}}' "$migration_container") == 0 ]]
if [[ $profile == 195 || $profile == 197 || $profile == 198 || $profile == 199 || $profile == 202 || $profile == 206 || $profile == 207 || $profile == 208 || $profile == 209 || $profile == 210 ]]; then
  migration_checksum=$(jq -er '.manifest.migration_sha256["195_upstream_scheduling_monitor_rates.sql"]' "$active_claim/gate.json")
  migration_manifest_sha256=$(jq -cS '.manifest.migration_sha256' "$active_claim/gate.json" | sha256sum | awk '{print $1}')
  marker_tmp="$state_dir/.migration-committed.tmp.$$"
  printf 'plan_sha256=%s\nmigration_manifest_sha256=%s\n' "$(<"$state_dir/migration-195-plan.sha256")" "$migration_manifest_sha256" > "$marker_tmp"
  while IFS=$'\t' read -r migration migration_checksum; do
    printf 'migration=%s checksum=%s\n' "$migration" "$migration_checksum" >> "$marker_tmp"
  done < <(jq -r '.manifest.migration_sha256 | to_entries[] | [.key,.value] | @tsv' "$active_claim/gate.json")
  chmod 600 "$marker_tmp"
  [[ ! -e $state_dir/migration-committed && ! -L $state_dir/migration-committed ]]
  mv -T -- "$marker_tmp" "$state_dir/migration-committed"
  "$assets_dir/migration-195-assert.sh" postflight_db
fi
docker rm "$migration_container" >/dev/null
docker compose up -d --no-deps --force-recreate sub2api >/dev/null 2>&1
for _ in $(seq 1 90); do
  [[ $(docker inspect -f '{{.State.Health.Status}}' sub2api) == healthy ]] && break
  sleep 2
done
[[ $(docker inspect -f '{{.Image}}' sub2api) == "$candidate_image_id" ]]
[[ $(docker inspect -f '{{.State.Health.Status}}' sub2api) == healthy ]]
[[ $(curl -sS -o /dev/null -w '%{http_code}' http://127.0.0.1:18080/health) == 200 ]]
[[ $(docker inspect -f '{{.Image}}' sub2api) == "$candidate_image_id" ]]
[[ $(docker inspect -f '{{.State.Health.Status}}' sub2api) == healthy ]]
[[ $(docker compose config --format json | jq -r '.services.sub2api.environment.UPSTREAM_SYNC_AUTO_ENABLED') == false ]]
assert_prompt_audit_disabled
if [[ $profile == 195 || $profile == 197 || $profile == 198 || $profile == 199 || $profile == 202 || $profile == 206 || $profile == 207 || $profile == 208 || $profile == 209 || $profile == 210 ]]; then
  "$assets_dir/migration-195-assert.sh" postflight_runtime
fi
printf 'migration_verified=true\n'
printf 'running_image_id=%s\n' "$candidate_image_id"
printf 'internal_health=pass\n'
printf 'public_traffic_enabled=false\n'
if [[ $profile == 198 || $profile == 199 || $profile == 202 || $profile == 206 || $profile == 207 || $profile == 208 || $profile == 209 || $profile == 210 ]]; then
  printf 'managed_monitor_key_names_verified=true\n'
fi
if [[ $profile == 199 || $profile == 202 || $profile == 206 || $profile == 207 || $profile == 208 || $profile == 209 || $profile == 210 ]]; then
  printf 'reasoning_effort_policy_verified=true\n'
fi
if [[ $profile == 202 || $profile == 206 || $profile == 207 || $profile == 208 || $profile == 209 || $profile == 210 ]]; then
  printf 'alipay_mobile_precreate_migration_verified=true\n'
  printf 'group_auth_cache_image_generation_verified=true\n'
  printf 'composite_model_routes_verified=true\n'
fi
if [[ $profile == 206 || $profile == 207 || $profile == 208 || $profile == 209 || $profile == 210 ]]; then
  printf 'session_id_columns_verified=true\n'
  printf 'live_request_type_verified=true\n'
  printf 'group_allow_live_verified=true\n'
  printf 'email_alias_index_verified=true\n'
fi
if [[ $profile == 208 || $profile == 209 || $profile == 210 ]]; then
  printf 'passkey_schema_verified=true\n'
fi
if [[ $profile == 209 || $profile == 210 ]]; then
  printf 'user_usage_aggregation_schema_verified=true\n'
fi
