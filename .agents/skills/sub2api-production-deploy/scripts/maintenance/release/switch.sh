#!/usr/bin/env bash
set -Eeuo pipefail

deploy_dir=${DEPLOY_DIR:-/opt/sub2api}
release_dir=${RELEASE_DIR:?RELEASE_DIR is required}
source /opt/sub2api/releases/.active-release/assets/context.sh
cd "$deploy_dir"
load_release_compose_files "$deploy_dir"
[[ $(docker inspect -f '{{.State.Health.Status}}' "$active_container") == healthy ]]
[[ $(systemctl is-active nginx) == active ]]
[[ $(docker image inspect -f '{{.Id}}' "$candidate_image_id") == "$candidate_image_id" ]]
[[ -d $state_dir && ! -L $state_dir ]]
switch_stage_file="$state_dir/switch-stage"
mark_switch_stage() {
  local value=${1:?switch stage is required}
  [[ $value =~ ^(initialized|downtime_stopped|migration_started|migration_completed|schema_verified|migration_committed|downtime_compose_prepared|candidate_started|candidate_healthy|candidate_network_verified|candidate_port_verified|candidate_probe_started|candidate_http_verified|candidate_headers_verified|background_activated|active_health_verified|prompt_audit_verified|runtime_verified)$ ]]
  printf '%s\n' "$value" > "$switch_stage_file.tmp.$$"
  chmod 600 "$switch_stage_file.tmp.$$"
  mv -T -- "$switch_stage_file.tmp.$$" "$switch_stage_file"
}
mark_switch_stage initialized
if [[ $profile == 195 || $profile == 197 || $profile == 198 || $profile == 199 || $profile == 202 || $profile == 206 || $profile == 207 || $profile == 208 || $profile == 209 || $profile == 210 || $profile == 212 || $profile == 213 || $profile == 215 || $profile == 232 || $profile == 233 || $profile == 234 || $profile == 235 ]]; then
  [[ -f $state_dir/migration-195-plan.sha256 && ! -L $state_dir/migration-195-plan.sha256 ]]
  migration_status=$(<"$state_dir/migration-195-status")
  export MIGRATION_STATUS="$migration_status"
fi
if [[ $profile == 232 || $profile == 233 || $profile == 234 || $profile == 235 ]]; then
  [[ -f $state_dir/migration-232-status && ! -L $state_dir/migration-232-status ]]
  migration_232_status=$(<"$state_dir/migration-232-status")
  [[ $migration_232_status == absent || $migration_232_status == verified ]]
  if [[ $profile == 233 || $profile == 234 || $profile == 235 ]]; then
    [[ -f $state_dir/migration-233-status && ! -L $state_dir/migration-233-status ]]
    migration_233_status=$(<"$state_dir/migration-233-status")
    [[ $migration_233_status == absent || $migration_233_status == verified ]]
  fi
  if [[ $profile == 235 ]]; then
    [[ -f $state_dir/migration-234-status && ! -L $state_dir/migration-234-status ]]
    migration_234_status=$(<"$state_dir/migration-234-status")
    [[ $migration_234_status == absent || $migration_234_status == verified ]]
  fi
fi
active_compose_json=$(docker compose "${release_compose_args[@]}" config --format json)
candidate_network_mode=$(sub2api_compose_network_mode "$active_compose_json" "$active_port")
candidate_health_url=$(sub2api_healthcheck_url "$candidate_network_mode" "$candidate_port")
candidate_override="$state_dir/docker-compose.release-candidate.yml"
override_tmp="$candidate_override.tmp.$$"
{
  printf 'services:\n  sub2api:\n    image: %s\n    environment:\n' "$candidate_image_id"
  if [[ $candidate_network_mode == host ]]; then
    printf '      SERVER_HOST: 127.0.0.1\n      SERVER_PORT: "%s"\n' "$candidate_port"
  else
    printf '      SERVER_HOST: 0.0.0.0\n      SERVER_PORT: "8080"\n'
  fi
  printf '      UPSTREAM_SYNC_AUTO_ENABLED: ${UPSTREAM_SYNC_AUTO_ENABLED:-true}\n'
  printf '    healthcheck:\n'
  printf '      test: ["CMD", "wget", "-q", "-T", "5", "-O", "/dev/null", "%s"]\n' "$candidate_health_url"
} > "$override_tmp"
chmod 600 "$override_tmp"
mv -T -- "$override_tmp" "$candidate_override"
export BIND_HOST=127.0.0.1
export SERVER_PORT="$candidate_port"
candidate_compose_args=("${release_compose_args[@]}" -f "$candidate_override")
compose_image=$(docker compose "${candidate_compose_args[@]}" config --format json | jq -r '.services.sub2api.image // empty')
[[ $(docker image inspect -f '{{.Id}}' "$compose_image") == "$candidate_image_id" ]]
candidate_compose_json=$(docker compose "${candidate_compose_args[@]}" config --format json)
[[ $(sub2api_compose_network_mode "$candidate_compose_json" "$candidate_port") == "$candidate_network_mode" ]]
assert_sub2api_healthcheck_contract "$candidate_compose_json" "$candidate_network_mode" "$candidate_port"
jq -e '.services.sub2api.environment.UPSTREAM_SYNC_AUTO_ENABLED == "true"' <<<"$candidate_compose_json" >/dev/null
mapfile -t migrations < <(jq -er '.manifest.migrations[]' "$active_claim/gate.json")
migration_container="sub2api-migrate-$release_id"
[[ -z $(docker ps -aq -f "name=^${migration_container}$") ]]
if [[ $deployment_mode == downtime ]]; then
  docker stop -t 60 "$active_container" >/dev/null
  systemctl stop nginx
  [[ $(docker inspect -f '{{.State.Status}}' "$active_container") != running ]]
  [[ $(systemctl is-active nginx 2>/dev/null || true) != active ]]
  mark_switch_stage downtime_stopped
fi
mark_switch_stage migration_started
docker compose "${candidate_compose_args[@]}" run --name "$migration_container" --no-deps sub2api /app/sub2api --migrate-only >/dev/null 2>&1
mark_switch_stage migration_completed
while IFS=$'\t' read -r migration migration_checksum; do
  recorded=$(docker exec sub2api-postgres psql -X -A -t -U sub2api -d sub2api -c "SELECT checksum FROM schema_migrations WHERE filename='$migration'")
  [[ $recorded == "$migration_checksum" ]]
done < <(jq -r '.manifest.migration_sha256 | to_entries[] | [.key,.value] | @tsv' "$active_claim/gate.json")
if [[ $profile == 198 || $profile == 199 || $profile == 202 || $profile == 206 || $profile == 207 || $profile == 208 || $profile == 209 || $profile == 210 || $profile == 212 || $profile == 213 || $profile == 215 || $profile == 232 || $profile == 233 || $profile == 234 || $profile == 235 ]]; then
  managed_monitor_key_name_state=$(docker exec sub2api-postgres psql -X -A -t -F '|' -U sub2api -d sub2api -c "SELECT character_maximum_length, (SELECT COUNT(*) FROM api_keys k JOIN channel_monitors m ON m.id=k.managed_monitor_id AND m.managed_api_key_id=k.id WHERE k.purpose='managed_monitor' AND k.deleted_at IS NULL AND k.name IS DISTINCT FROM '监控-' || BTRIM(m.name)) FROM information_schema.columns WHERE table_schema='public' AND table_name='api_keys' AND column_name='name'")
  [[ $managed_monitor_key_name_state == '103|0' ]]
fi
if [[ $profile == 199 || $profile == 202 || $profile == 206 || $profile == 207 || $profile == 208 || $profile == 209 || $profile == 210 || $profile == 212 || $profile == 213 || $profile == 215 || $profile == 232 || $profile == 233 || $profile == 234 || $profile == 235 ]]; then
  reasoning_effort_policy_state=$(docker exec sub2api-postgres psql -X -A -t -F '|' -U sub2api -d sub2api -c "SELECT COALESCE(MAX(CASE WHEN column_name='max_reasoning_effort' THEN data_type || ':' || is_nullable || ':' || column_default END),''), COALESCE(MAX(CASE WHEN column_name='reasoning_effort_mappings' THEN data_type || ':' || is_nullable || ':' || column_default END),'') FROM information_schema.columns WHERE table_schema='public' AND table_name='groups' AND column_name IN ('max_reasoning_effort','reasoning_effort_mappings')")
  [[ $reasoning_effort_policy_state == *'character varying:NO:'*"''::character varying"*'|'*'jsonb:NO:'*"'[]'::jsonb"* ]]
fi
if [[ $profile == 202 || $profile == 206 || $profile == 207 || $profile == 208 || $profile == 209 || $profile == 210 || $profile == 212 || $profile == 213 || $profile == 215 || $profile == 232 || $profile == 233 || $profile == 234 || $profile == 235 ]]; then
  alipay_mobile_precreate_deep_link_state=$(docker exec sub2api-postgres psql -X -A -t -F '|' -U sub2api -d sub2api -c "SELECT COUNT(*), COUNT(*) FILTER (WHERE value IN ('true','false')) FROM settings WHERE key='ALIPAY_MOBILE_PRECREATE_DEEP_LINK'")
  [[ $alipay_mobile_precreate_deep_link_state == '1|1' ]]
  group_auth_cache_image_generation_state=$(docker exec sub2api-postgres psql -X -A -t -F '|' -U sub2api -d sub2api -c "SELECT COUNT(*) FILTER (WHERE pg_get_functiondef(p.oid) LIKE '%OLD.allow_image_generation IS NOT DISTINCT FROM NEW.allow_image_generation%'), (SELECT COUNT(*) FROM pg_trigger t WHERE t.tgrelid='groups'::regclass AND t.tgname='trg_groups_auth_cache_invalidation' AND NOT t.tgisinternal AND t.tgenabled <> 'D') FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname='public' AND p.proname='enqueue_group_auth_cache_invalidation' AND pg_get_function_identity_arguments(p.oid)=''")
  [[ $group_auth_cache_image_generation_state == '1|1' ]]
  composite_model_routes_state=$(docker exec sub2api-postgres psql -X -A -t -F '|' -U sub2api -d sub2api -c "SELECT (SELECT COUNT(*) FROM information_schema.columns WHERE table_schema='public' AND table_name='composite_model_routes'), (SELECT COUNT(*) FROM pg_constraint WHERE conrelid='composite_model_routes'::regclass AND contype='c' AND conname IN ('composite_model_routes_match_type_check','composite_model_routes_endpoint_check','composite_model_routes_target_platform_check')), (SELECT COUNT(*) FROM pg_constraint WHERE conrelid='composite_model_routes'::regclass AND contype='f' AND confrelid='groups'::regclass AND confdeltype='c'), (SELECT COUNT(*) FROM pg_indexes WHERE schemaname='public' AND tablename='composite_model_routes' AND indexname IN ('idx_composite_model_routes_unique_active','idx_composite_model_routes_group_enabled','idx_composite_model_routes_group_priority'))")
  [[ $composite_model_routes_state == '13|3|1|3' ]]
fi
if [[ $profile == 206 || $profile == 207 || $profile == 208 || $profile == 209 || $profile == 210 || $profile == 212 || $profile == 213 || $profile == 215 || $profile == 232 || $profile == 233 || $profile == 234 || $profile == 235 ]]; then
  session_id_columns_state=$(docker exec sub2api-postgres psql -X -A -t -F '|' -U sub2api -d sub2api -c "SELECT COUNT(*) FILTER (WHERE table_name='usage_logs' AND data_type='character varying' AND character_maximum_length=255 AND is_nullable='YES'), COUNT(*) FILTER (WHERE table_name='batch_image_jobs' AND data_type='character varying' AND character_maximum_length=255 AND is_nullable='YES') FROM information_schema.columns WHERE table_schema='public' AND column_name='session_id'")
  [[ $session_id_columns_state == '1|1' ]]
  live_request_type_state=$(docker exec sub2api-postgres psql -X -A -t -U sub2api -d sub2api -c "SELECT COUNT(*)=1 AND BOOL_AND(pg_get_constraintdef(oid) LIKE '%request_type <= 5%') FROM pg_constraint WHERE conrelid='usage_logs'::regclass AND conname='usage_logs_request_type_check'")
  [[ $live_request_type_state == t ]]
  group_allow_live_state=$(docker exec sub2api-postgres psql -X -A -t -F '|' -U sub2api -d sub2api -c "SELECT data_type,is_nullable,column_default FROM information_schema.columns WHERE table_schema='public' AND table_name='groups' AND column_name='allow_live'")
  [[ $group_allow_live_state == 'boolean|NO|false' ]]
  email_alias_index_state=$(docker exec sub2api-postgres psql -X -A -t -F '|' -U sub2api -d sub2api -c "SELECT i.indisvalid,i.indisready,pg_get_expr(i.indexprs,i.indrelid)='replace(lower(TRIM(BOTH FROM email)), ''.''::text, ''''::text)',pg_get_expr(i.indpred,i.indrelid)='(deleted_at IS NULL)',o.opcname='text_pattern_ops' FROM pg_index i JOIN pg_class c ON c.oid=i.indexrelid JOIN pg_opclass o ON o.oid=i.indclass[0] WHERE c.relname='idx_users_email_dot_stripped'")
  [[ $email_alias_index_state == 't|t|t|t|t' ]]
fi
if [[ $profile == 208 || $profile == 209 || $profile == 210 || $profile == 212 || $profile == 213 || $profile == 215 || $profile == 232 || $profile == 233 || $profile == 234 || $profile == 235 ]]; then
  passkey_schema_state=$(docker exec sub2api-postgres psql -X -A -t -F '|' -U sub2api -d sub2api -c "SELECT to_regclass('public.passkey_user_handles') IS NOT NULL, to_regclass('public.passkey_credentials') IS NOT NULL, (SELECT COUNT(*) FROM information_schema.columns WHERE table_schema='public' AND table_name='passkey_user_handles' AND ((column_name='user_id' AND data_type='bigint' AND is_nullable='NO') OR (column_name='user_handle' AND data_type='bytea' AND is_nullable='NO') OR (column_name='created_at' AND data_type='timestamp with time zone' AND is_nullable='NO' AND column_default LIKE 'now()%'))), (SELECT COUNT(*) FROM information_schema.columns WHERE table_schema='public' AND table_name='passkey_credentials' AND ((column_name='id' AND data_type='bigint' AND is_nullable='NO' AND column_default LIKE 'nextval(%') OR (column_name='user_id' AND data_type='bigint' AND is_nullable='NO') OR (column_name='credential_id' AND data_type='bytea' AND is_nullable='NO') OR (column_name='name' AND data_type='character varying' AND character_maximum_length=100 AND is_nullable='NO' AND column_default LIKE '''Passkey''%') OR (column_name='credential_data' AND data_type='jsonb' AND is_nullable='NO') OR (column_name='last_used_at' AND data_type='timestamp with time zone' AND is_nullable='YES') OR (column_name IN ('created_at','updated_at') AND data_type='timestamp with time zone' AND is_nullable='NO' AND column_default LIKE 'now()%'))), (SELECT COUNT(*) FROM pg_constraint WHERE conrelid IN ('passkey_user_handles'::regclass,'passkey_credentials'::regclass) AND contype='p'), (SELECT COUNT(*) FROM pg_constraint WHERE conrelid IN ('passkey_user_handles'::regclass,'passkey_credentials'::regclass) AND contype='u'), (SELECT COUNT(*) FROM pg_constraint WHERE conrelid IN ('passkey_user_handles'::regclass,'passkey_credentials'::regclass) AND contype='f' AND confrelid='users'::regclass AND confdeltype='c'), (SELECT COUNT(*) FROM pg_indexes WHERE schemaname='public' AND tablename='passkey_credentials' AND indexname IN ('passkey_credentials_user_id_idx','passkey_credentials_last_used_at_idx'))")
  [[ $passkey_schema_state == 't|t|3|8|2|2|2|2' ]]
fi
if [[ $profile == 209 || $profile == 210 || $profile == 212 || $profile == 213 || $profile == 215 || $profile == 232 || $profile == 233 || $profile == 234 || $profile == 235 ]]; then
  user_usage_aggregation_schema_state=$(docker exec sub2api-postgres psql -X -A -t -F '|' -U sub2api -d sub2api -c "SELECT to_regclass('public.usage_dashboard_user_hourly') IS NOT NULL, to_regclass('public.usage_dashboard_user_daily') IS NOT NULL, to_regclass('public.usage_dashboard_user_backfill_state') IS NOT NULL, (SELECT COUNT(*) FROM information_schema.columns WHERE table_schema='public' AND table_name='usage_dashboard_user_hourly' AND ((column_name='bucket_start' AND data_type='timestamp with time zone' AND is_nullable='NO') OR (column_name='user_id' AND data_type='bigint' AND is_nullable='NO') OR (column_name IN ('input_tokens','output_tokens','cache_creation_tokens','cache_read_tokens') AND data_type='bigint' AND is_nullable='NO' AND column_default LIKE '0%') OR (column_name IN ('user_spend','account_cost') AND data_type='numeric' AND numeric_precision=20 AND numeric_scale=10 AND is_nullable='NO' AND column_default LIKE '0%') OR (column_name='computed_at' AND data_type='timestamp with time zone' AND is_nullable='NO' AND column_default LIKE 'now()%'))), (SELECT COUNT(*) FROM information_schema.columns WHERE table_schema='public' AND table_name='usage_dashboard_user_daily' AND ((column_name='bucket_date' AND data_type='date' AND is_nullable='NO') OR (column_name='user_id' AND data_type='bigint' AND is_nullable='NO') OR (column_name IN ('input_tokens','output_tokens','cache_creation_tokens','cache_read_tokens') AND data_type='bigint' AND is_nullable='NO' AND column_default LIKE '0%') OR (column_name IN ('user_spend','account_cost') AND data_type='numeric' AND numeric_precision=20 AND numeric_scale=10 AND is_nullable='NO' AND column_default LIKE '0%') OR (column_name='computed_at' AND data_type='timestamp with time zone' AND is_nullable='NO' AND column_default LIKE 'now()%'))), (SELECT COUNT(*) FROM information_schema.columns WHERE table_schema='public' AND table_name='usage_dashboard_user_backfill_state' AND ((column_name='id' AND data_type='smallint' AND is_nullable='NO') OR (column_name IN ('earliest_covered_date','last_completed_date') AND data_type='date' AND is_nullable='YES') OR (column_name='status' AND data_type='character varying' AND character_maximum_length=20 AND is_nullable='NO' AND column_default LIKE '''unavailable''%') OR (column_name IN ('coverage_start','coverage_end','target_end','completed_at') AND data_type='timestamp with time zone' AND is_nullable='YES') OR (column_name='attempt_count' AND data_type='bigint' AND is_nullable='NO' AND column_default LIKE '0%') OR (column_name='last_error' AND data_type='text' AND is_nullable='YES') OR (column_name='updated_at' AND data_type='timestamp with time zone' AND is_nullable='NO' AND column_default LIKE 'now()%'))), (SELECT COUNT(*) FROM pg_constraint WHERE conrelid IN ('usage_dashboard_user_hourly'::regclass,'usage_dashboard_user_daily'::regclass,'usage_dashboard_user_backfill_state'::regclass) AND contype='p'), (SELECT COUNT(*) FROM pg_constraint WHERE conrelid IN ('usage_dashboard_user_hourly'::regclass,'usage_dashboard_user_daily'::regclass) AND contype='f' AND confrelid='users'::regclass AND confdeltype='c'), (SELECT COUNT(*) FROM pg_indexes WHERE schemaname='public' AND indexname IN ('idx_usage_dashboard_user_hourly_user_bucket','idx_usage_dashboard_user_daily_user_bucket')), (SELECT COUNT(*) FROM pg_constraint WHERE conrelid='usage_dashboard_user_backfill_state'::regclass AND contype='c'), (SELECT COUNT(*)=1 AND BOOL_AND(id=1 AND status IN ('available','building','partial','unavailable')) FROM usage_dashboard_user_backfill_state)")
  [[ $user_usage_aggregation_schema_state == 't|t|t|9|9|11|3|2|2|3|t' ]]
fi
if [[ $profile == 212 || $profile == 213 || $profile == 215 || $profile == 232 || $profile == 233 || $profile == 234 || $profile == 235 ]]; then
  group_profit_control_schema_state=$(docker exec sub2api-postgres psql -X -A -t -F '|' -U sub2api -d sub2api -c "SELECT COUNT(*) FILTER (WHERE column_name='profit_control_enabled' AND data_type='boolean' AND is_nullable='NO' AND column_default='false'), COUNT(*) FILTER (WHERE column_name IN ('profit_min_margin','profit_safety_buffer') AND data_type='numeric' AND numeric_precision=10 AND numeric_scale=4 AND is_nullable='NO' AND column_default='0') FROM information_schema.columns WHERE table_schema='public' AND table_name='groups'")
  [[ $group_profit_control_schema_state == '1|2' ]]
  group_profit_auth_cache_trigger_definition=$(docker exec sub2api-postgres psql -X -A -t -U sub2api -d sub2api -c "SELECT pg_get_functiondef('enqueue_group_auth_cache_invalidation()'::regprocedure)")
  for trigger_field in status is_exclusive allow_image_generation platform subscription_type rate_multiplier peak_rate_enabled peak_start peak_end peak_rate_multiplier profit_control_enabled profit_min_margin profit_safety_buffer deleted_at; do
    grep -Fq "OLD.$trigger_field IS NOT DISTINCT FROM NEW.$trigger_field" <<<"$group_profit_auth_cache_trigger_definition"
  done
  group_profit_auth_cache_trigger_state=$(docker exec sub2api-postgres psql -X -A -t -U sub2api -d sub2api -c "SELECT COUNT(*) FROM pg_trigger t WHERE t.tgrelid='groups'::regclass AND t.tgname='trg_groups_auth_cache_invalidation' AND NOT t.tgisinternal AND t.tgenabled <> 'D'")
  [[ $group_profit_auth_cache_trigger_state == 1 ]]
fi
if [[ $profile == 215 || $profile == 232 || $profile == 233 || $profile == 234 || $profile == 235 ]]; then
  usage_log_response_model_schema_state=$(docker exec sub2api-postgres psql -X -A -t -F '|' -U sub2api -d sub2api -c "SELECT COUNT(*) FILTER (WHERE column_name='upstream_response_model' AND data_type='character varying' AND character_maximum_length=200 AND is_nullable='YES'), COUNT(*) FILTER (WHERE column_name='upstream_model_mismatch' AND data_type='boolean' AND is_nullable='YES') FROM information_schema.columns WHERE table_schema='public' AND table_name='usage_logs'")
  [[ $usage_log_response_model_schema_state == '1|1' ]]
  usage_log_upstream_model_columns_verified=true
  usage_log_model_mismatch_index_state=$(docker exec sub2api-postgres psql -X -A -t -F '|' -U sub2api -d sub2api -c "SELECT i.indisvalid,i.indisready,pg_get_expr(i.indpred,i.indrelid)='(upstream_model_mismatch IS TRUE)',pg_get_indexdef(i.indexrelid) LIKE '%(created_at DESC, id DESC)%' FROM pg_index i JOIN pg_class c ON c.oid=i.indexrelid JOIN pg_class t ON t.oid=i.indrelid JOIN pg_namespace n ON n.oid=t.relnamespace WHERE n.nspname='public' AND t.relname='usage_logs' AND c.relname='idx_usage_logs_upstream_model_mismatch_created_at'")
  [[ $usage_log_model_mismatch_index_state == 't|t|t|t' ]]
  usage_log_upstream_model_mismatch_index_verified=true
fi
if [[ $profile == 232 || $profile == 233 || $profile == 234 || $profile == 235 ]]; then
  channel_monitor_v2_schema_state=$(docker exec sub2api-postgres psql -X -A -t -F '|' -U sub2api -d sub2api -c "WITH expected_tables(name) AS (VALUES
    ('channel_monitor_v2_config'),('channel_monitor_v2_metrics_1m'),('channel_monitor_v2_user_metrics_1m'),('channel_monitor_v2_error_metrics_1m'),('channel_monitor_v2_latency_histograms_1m'),('channel_monitor_v2_watermarks'),('channel_monitor_v2_metrics_rollup'),('channel_monitor_v2_user_metrics_rollup'),('channel_monitor_v2_error_metrics_rollup'),('channel_monitor_v2_latency_histograms_rollup')
  ), expected_indexes(name) AS (VALUES
    ('idx_channel_monitor_v2_metrics_platform_time'),('idx_channel_monitor_v2_metrics_group_time'),('idx_channel_monitor_v2_metrics_model_time'),('idx_channel_monitor_v2_user_metrics_user_time'),('idx_channel_monitor_v2_user_metrics_time'),('idx_channel_monitor_v2_errors_time'),('idx_channel_monitor_v2_errors_category_time'),('idx_channel_monitor_v2_histograms_time'),('idx_channel_monitor_v2_metrics_rollup_platform_time'),('idx_channel_monitor_v2_metrics_rollup_group_time'),('idx_channel_monitor_v2_metrics_rollup_model_time'),('idx_channel_monitor_v2_user_rollup_user_time'),('idx_channel_monitor_v2_user_rollup_time'),('idx_channel_monitor_v2_errors_rollup_time'),('idx_channel_monitor_v2_errors_rollup_category_time'),('idx_channel_monitor_v2_histograms_rollup_time')
  ) SELECT
    (SELECT COUNT(*) FROM expected_tables WHERE to_regclass('public.' || name) IS NOT NULL),
    (SELECT COUNT(*) FROM pg_constraint WHERE contype='p' AND conrelid IN (SELECT to_regclass('public.' || name) FROM expected_tables)),
    (SELECT COUNT(*) FROM information_schema.columns WHERE table_schema='public' AND table_name='channel_monitor_v2_config' AND ((column_name='id' AND data_type='smallint' AND is_nullable='NO') OR (column_name='version' AND data_type='integer' AND is_nullable='NO') OR (column_name='enabled' AND data_type='boolean' AND is_nullable='NO') OR (column_name='refresh_interval_seconds' AND data_type='integer' AND is_nullable='NO') OR (column_name='platforms' AND data_type='jsonb' AND is_nullable='NO') OR (column_name='group_ids' AND data_type='ARRAY' AND is_nullable='NO') OR (column_name='updated_by' AND data_type='bigint' AND is_nullable='YES') OR (column_name='updated_at' AND data_type='timestamp with time zone' AND is_nullable='NO') OR (column_name='ignored_error_categories' AND data_type='ARRAY' AND is_nullable='NO') OR (column_name='health_thresholds' AND data_type='jsonb' AND is_nullable='NO'))),
    (SELECT COUNT(*) FROM pg_constraint WHERE contype='c' AND conrelid IN (SELECT to_regclass('public.' || name) FROM expected_tables)),
    (SELECT COUNT(*) FROM expected_indexes e JOIN pg_class c ON c.relname=e.name JOIN pg_namespace n ON n.oid=c.relnamespace AND n.nspname='public' JOIN pg_index i ON i.indexrelid=c.oid WHERE i.indisvalid AND i.indisready),
    (SELECT COUNT(*) FROM expected_tables WHERE has_table_privilege(current_user, 'public.' || name, 'SELECT,INSERT,UPDATE,DELETE'))")
  [[ $channel_monitor_v2_schema_state == '10|10|10|9|16|10' ]]
  channel_monitor_v2_defaults_state=$(docker exec sub2api-postgres psql -X -A -t -F '|' -U sub2api -d sub2api -c "SELECT
    (SELECT value FROM settings WHERE key='channel_monitor_mode'),
    (SELECT value FROM settings WHERE key='channel_monitor_hide_throughput'),
    (SELECT refresh_interval_seconds FROM channel_monitor_v2_config WHERE id=1),
    (SELECT enabled FROM channel_monitor_v2_config WHERE id=1),
    (SELECT cardinality(ignored_error_categories) FROM channel_monitor_v2_config WHERE id=1),
    (SELECT (health_thresholds->>'minimum_sample')::integer FROM channel_monitor_v2_config WHERE id=1),
    (SELECT (health_thresholds->>'warning_cache_rate')::numeric FROM channel_monitor_v2_config WHERE id=1),
    (SELECT (health_thresholds->>'critical_cache_rate')::numeric FROM channel_monitor_v2_config WHERE id=1),
    (SELECT platforms::text LIKE '%gpt-5.6-luna%' FROM channel_monitor_v2_config WHERE id=1),
    (SELECT platforms::text LIKE '%lcodex%' FROM channel_monitor_v2_config WHERE id=1)")
  [[ $channel_monitor_v2_defaults_state == 'v1|true|300|t|8|50|0|0|t|f' ]]
  group_media_pricing_schema_state=$(docker exec sub2api-postgres psql -X -A -t -F '|' -U sub2api -d sub2api -c "SELECT
    COUNT(*) FILTER (WHERE column_name='video_model_prices' AND data_type='jsonb' AND is_nullable='YES'),
    COUNT(*) FILTER (WHERE column_name IN ('audio_realtime_price_per_min','audio_tts_price_per_million_chars','audio_stt_price_per_hour','search_price_per_1k') AND data_type='numeric' AND numeric_precision=20 AND numeric_scale=8 AND is_nullable='YES')
  FROM information_schema.columns WHERE table_schema='public' AND table_name='groups'")
  [[ $group_media_pricing_schema_state == '1|4' ]]
  MIGRATION_STATUS="$migration_232_status" "$assets_dir/migration-232-assert.sh" postflight
  if [[ $profile == 233 || $profile == 234 || $profile == 235 ]]; then
    MIGRATION_STATUS="$migration_233_status" "$assets_dir/migration-233-assert.sh" postflight
  fi
  if [[ $profile == 235 ]]; then
    group_model_pricing_schema_state=$(docker exec sub2api-postgres psql -X -A -t -F '|' -U sub2api -d sub2api -c "SELECT COUNT(*) FILTER (WHERE column_name='long_context_pricing_enabled' AND data_type='boolean' AND is_nullable='NO' AND column_default='true'), COUNT(*) FILTER (WHERE column_name='model_pricing' AND data_type='jsonb' AND is_nullable='YES') FROM information_schema.columns WHERE table_schema='public' AND table_name='groups'")
    [[ $group_model_pricing_schema_state == '1|1' ]]
    MIGRATION_STATUS="$migration_234_status" "$assets_dir/migration-234-assert.sh" postflight
  fi
  channel_monitor_v2_schema_verified=true
  channel_monitor_v2_defaults_verified=true
  group_media_pricing_schema_verified=true
  group_media_auth_cache_trigger_verified=true
fi
mark_switch_stage schema_verified
[[ $(docker inspect -f '{{.Image}}' "$migration_container") == "$candidate_image_id" ]]
[[ $(docker inspect -f '{{.State.ExitCode}}' "$migration_container") == 0 ]]
if [[ $profile == 195 || $profile == 197 || $profile == 198 || $profile == 199 || $profile == 202 || $profile == 206 || $profile == 207 || $profile == 208 || $profile == 209 || $profile == 210 || $profile == 212 || $profile == 213 || $profile == 215 || $profile == 232 || $profile == 233 || $profile == 234 || $profile == 235 ]]; then
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
mark_switch_stage migration_committed
docker rm "$migration_container" >/dev/null 2>&1
if [[ $deployment_mode == downtime ]]; then
  [[ $active_container == sub2api ]]
  candidate_container=sub2api
  candidate_port=$active_port
  candidate_instance_id=$final_instance_id
  active_override_tmp="$deploy_dir/docker-compose.release-active.yml.tmp.$$"
  write_release_active_override "$active_override_tmp" "$candidate_image_id" "$candidate_instance_id" "$candidate_port" "$candidate_network_mode"
  chmod 600 "$active_override_tmp"
  mv -T -- "$active_override_tmp" "$deploy_dir/docker-compose.release-active.yml"
  env_tmp="$deploy_dir/.env.active.$$"
  awk '!/^(COMPOSE_FILE|SUB2API_RELEASE_IMAGE|BIND_HOST|SERVER_PORT)=/' "$deploy_dir/.env" > "$env_tmp"
  printf 'COMPOSE_FILE=%s\n' "$(release_compose_value_with_active_override)" >> "$env_tmp"
  printf 'SUB2API_RELEASE_IMAGE=%s\nBIND_HOST=127.0.0.1\nSERVER_PORT=%s\n' "$candidate_image_id" "$candidate_port" >> "$env_tmp"
  chmod --reference="$deploy_dir/.env" "$env_tmp"
  mv -T -- "$env_tmp" "$deploy_dir/.env"
  load_release_compose_files "$deploy_dir"
  candidate_compose_args=("${release_compose_args[@]}")
  candidate_compose_json=$(docker compose "${candidate_compose_args[@]}" config --format json)
  [[ $(assert_sub2api_compose_closure "$deploy_dir" "$candidate_port" "$candidate_image_id" "$candidate_instance_id") == "$candidate_network_mode" ]]
  mark_switch_stage downtime_compose_prepared
  docker compose "${candidate_compose_args[@]}" up -d --no-deps --force-recreate sub2api >/dev/null 2>&1
else
  [[ -z $(docker ps -aq -f "name=^${candidate_container}$") ]]
  candidate_run_args=(run -d --name "$candidate_container" --no-deps)
  [[ $candidate_network_mode == host ]] || candidate_run_args+=(--service-ports)
  docker compose "${candidate_compose_args[@]}" "${candidate_run_args[@]}" \
    -e "SUB2API_INSTANCE_ID=$candidate_instance_id" \
    -e SUB2API_BACKGROUND_ACTIVATION_FILE=/app/data/.sub2api-active-instance sub2api >/dev/null 2>&1
fi
mark_switch_stage candidate_started
candidate_failure_file="$state_dir/candidate-failure"
capture_candidate_failure() {
  local failure_line=${1:?candidate failure line is required}
  local forced_failure_kind=${2:-}
  local failure_tmp
  local candidate_state=missing
  local candidate_health=missing
  local candidate_exit_code=unknown
  local candidate_oom_killed=unknown
  local candidate_restart_count=unknown
  local candidate_health_log_entries=unknown
  local candidate_failure_kind=container_missing
  local candidate_log_capture=not_configured
  if docker inspect "$candidate_container" >/dev/null 2>&1; then
    candidate_state=$(docker inspect -f '{{.State.Status}}' "$candidate_container" 2>/dev/null || true)
    candidate_health=$(docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' "$candidate_container" 2>/dev/null || true)
    candidate_exit_code=$(docker inspect -f '{{.State.ExitCode}}' "$candidate_container" 2>/dev/null || true)
    candidate_oom_killed=$(docker inspect -f '{{.State.OOMKilled}}' "$candidate_container" 2>/dev/null || true)
    candidate_restart_count=$(docker inspect -f '{{.RestartCount}}' "$candidate_container" 2>/dev/null || true)
    candidate_health_log_entries=$(docker inspect -f '{{if .State.Health}}{{len .State.Health.Log}}{{else}}0{{end}}' "$candidate_container" 2>/dev/null || true)
    case "$candidate_state" in
      created|running|paused|restarting|removing|exited|dead) ;;
      *) candidate_state=unknown ;;
    esac
    case "$candidate_health" in
      starting|healthy|unhealthy|none) ;;
      *) candidate_health=unknown ;;
    esac
    [[ $candidate_exit_code =~ ^[0-9]+$ ]] || candidate_exit_code=unknown
    [[ $candidate_oom_killed == true || $candidate_oom_killed == false ]] || candidate_oom_killed=unknown
    [[ $candidate_restart_count =~ ^[0-9]+$ ]] || candidate_restart_count=unknown
    [[ $candidate_health_log_entries =~ ^[0-9]+$ ]] || candidate_health_log_entries=unknown
    case "$candidate_state" in
      exited|dead) candidate_failure_kind=container_exited ;;
      unknown) candidate_failure_kind=inspect_failed ;;
      *)
        if [[ $candidate_health == unhealthy ]]; then
          candidate_failure_kind=health_unhealthy
        else
          candidate_failure_kind=health_timeout
        fi
        ;;
    esac
  fi
  if [[ -n $forced_failure_kind ]]; then
    [[ $forced_failure_kind == runtime_contract_mismatch ]]
    candidate_failure_kind=$forced_failure_kind
  fi
  if [[ -n ${SUB2API_RELEASE_RAW_LOG:-} ]]; then
    candidate_log_capture=unavailable
    if [[ $SUB2API_RELEASE_RAW_LOG == "$release_dir/logs/production.raw.log" && -f $SUB2API_RELEASE_RAW_LOG && ! -L $SUB2API_RELEASE_RAW_LOG ]] &&
       [[ $(stat -c '%U:%G:%a:%h' "$SUB2API_RELEASE_RAW_LOG") == root:root:600:1 ]]; then
      candidate_log_capture=saved
      {
        printf '\n[%s] stage=candidate_failure stream=container\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
        printf 'candidate_state=%s candidate_health=%s candidate_exit_code=%s candidate_oom_killed=%s candidate_restart_count=%s candidate_health_log_entries=%s candidate_failure_kind=%s failure_line=%s\n' \
          "$candidate_state" "$candidate_health" "$candidate_exit_code" "$candidate_oom_killed" "$candidate_restart_count" "$candidate_health_log_entries" "$candidate_failure_kind" "$failure_line"
        printf '[%s] stage=candidate_failure stream=healthcheck\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
        docker inspect -f '{{range .State.Health.Log}}{{printf "start=%s end=%s exit_code=%d output=%q\n" .Start .End .ExitCode .Output}}{{end}}' "$candidate_container" 2>&1 || true
        docker logs --since 15m "$candidate_container" 2>&1 || true
      } >> "$SUB2API_RELEASE_RAW_LOG"
    fi
  fi
  [[ $failure_line =~ ^[0-9]+$ ]]
  failure_tmp="$candidate_failure_file.tmp.$$"
  printf 'candidate_failure_kind=%s\ncandidate_state=%s\ncandidate_health=%s\ncandidate_exit_code=%s\ncandidate_oom_killed=%s\ncandidate_restart_count=%s\ncandidate_health_log_entries=%s\ncandidate_log_capture=%s\ncandidate_failure_line=%s\n' \
    "$candidate_failure_kind" "$candidate_state" "$candidate_health" "$candidate_exit_code" "$candidate_oom_killed" "$candidate_restart_count" "$candidate_health_log_entries" "$candidate_log_capture" "$failure_line" > "$failure_tmp"
  chmod 600 "$failure_tmp"
  [[ ! -L $candidate_failure_file ]]
  mv -T -- "$failure_tmp" "$candidate_failure_file"
}
if ! assert_sub2api_runtime_contract "$candidate_container" "$candidate_image_id" "$candidate_network_mode" "$candidate_port"; then
  capture_candidate_failure "$LINENO" runtime_contract_mismatch
  exit 1
fi
candidate_ready=false
for _ in $(seq 1 90); do
  candidate_state_now=$(docker inspect -f '{{.State.Status}}' "$candidate_container" 2>/dev/null || true)
  candidate_health_now=$(docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' "$candidate_container" 2>/dev/null || true)
  if [[ $candidate_health_now == healthy ]]; then
    candidate_ready=true
    break
  fi
  [[ -n $candidate_state_now ]] || break
  [[ $candidate_state_now != exited && $candidate_state_now != dead ]] || break
  sleep 2
done
if [[ $candidate_ready != true ]]; then
  capture_candidate_failure "$LINENO"
  exit 1
fi
candidate_runtime_image=$(docker inspect -f '{{.Image}}' "$candidate_container" 2>/dev/null || true)
candidate_runtime_health=$(docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' "$candidate_container" 2>/dev/null || true)
if [[ $candidate_runtime_image != "$candidate_image_id" || $candidate_runtime_health != healthy ]]; then
  capture_candidate_failure "$LINENO"
  exit 1
fi
mark_switch_stage candidate_healthy
assert_sub2api_runtime_contract "$candidate_container" "$candidate_image_id" "$candidate_network_mode" "$candidate_port"
mark_switch_stage candidate_network_verified
mark_switch_stage candidate_port_verified
printf 'container=%s\nport=%s\nimage_id=%s\ninstance_id=%s\n' "$candidate_container" "$candidate_port" "$candidate_image_id" "$candidate_instance_id" > "$state_dir/candidate-app"
chmod 600 "$state_dir/candidate-app"
candidate_headers=$(mktemp /tmp/sub2api-candidate-health.XXXXXX)
trap 'rm -f "$candidate_headers"' EXIT
mark_switch_stage candidate_probe_started
candidate_http_code=000
candidate_curl_exit=0
for _ in $(seq 1 30); do
  if candidate_http_code=$(curl -sS -D "$candidate_headers" -o /dev/null -w '%{http_code}' "http://127.0.0.1:${candidate_port}/health"); then
    candidate_curl_exit=0
  else
    candidate_curl_exit=$?
  fi
  printf '%s\n' "$candidate_http_code" > "$state_dir/candidate-http.code"
  printf '%s\n' "$candidate_curl_exit" > "$state_dir/candidate-curl.exit"
  chmod 600 "$state_dir/candidate-http.code" "$state_dir/candidate-curl.exit"
  [[ $candidate_curl_exit == 0 && $candidate_http_code == 200 ]] && break
  sleep 1
done
[[ $candidate_curl_exit == 0 && $candidate_http_code == 200 ]]
mark_switch_stage candidate_http_verified
assert_http_header_equals "$candidate_headers" X-Sub2API-Instance "$candidate_instance_id"
assert_http_header_equals "$candidate_headers" X-Sub2API-Background-Ready false
mark_switch_stage candidate_headers_verified
if [[ $deployment_mode == blue-green ]]; then
  [[ $(docker inspect -f '{{.State.Health.Status}}' "$active_container") == healthy ]]
else
  activation_host_dir=$(docker inspect -f '{{range .Mounts}}{{if eq .Destination "/app/data"}}{{.Source}}{{end}}{{end}}' "$candidate_container")
  [[ -n $activation_host_dir && -d $activation_host_dir && ! -L $activation_host_dir ]]
  printf '%s\n' "$candidate_instance_id" > "$activation_host_dir/.sub2api-active-instance.tmp"
  chmod 600 "$activation_host_dir/.sub2api-active-instance.tmp"
  mv -T -- "$activation_host_dir/.sub2api-active-instance.tmp" "$activation_host_dir/.sub2api-active-instance"
  background_ready=false
  for _ in $(seq 1 120); do
    : > "$candidate_headers"
    if [[ $(curl -sS -D "$candidate_headers" -o /dev/null -w '%{http_code}' "http://127.0.0.1:${candidate_port}/health" 2>/dev/null || true) == 200 ]] &&
       assert_http_header_equals "$candidate_headers" X-Sub2API-Instance "$candidate_instance_id" &&
       assert_http_header_equals "$candidate_headers" X-Sub2API-Background-Ready true; then
      background_ready=true
      break
    fi
    sleep 1
  done
  [[ $background_ready == true ]]
  slot_tmp="$active_slot_file.tmp.$$"
  printf 'container=sub2api\nport=%s\nimage_id=%s\nrelease_id=%s\ninstance_id=%s\n' \
    "$candidate_port" "$candidate_image_id" "$release_id" "$candidate_instance_id" > "$slot_tmp"
  chmod 600 "$slot_tmp"
  mv -T -- "$slot_tmp" "$active_slot_file"
  mark_switch_stage background_activated
fi
mark_switch_stage active_health_verified
assert_prompt_audit_disabled
mark_switch_stage prompt_audit_verified
if [[ $profile == 195 || $profile == 197 || $profile == 198 || $profile == 199 || $profile == 202 || $profile == 206 || $profile == 207 || $profile == 208 || $profile == 209 || $profile == 210 || $profile == 212 || $profile == 213 || $profile == 215 || $profile == 232 || $profile == 233 || $profile == 234 || $profile == 235 ]]; then
  "$assets_dir/migration-195-assert.sh" postflight_runtime
fi
mark_switch_stage runtime_verified
printf 'migration_verified=true\n'
printf 'running_image_id=%s\n' "$candidate_image_id"
printf 'internal_health=pass\n'
printf 'public_traffic_enabled=false\n'
printf 'candidate_container=%s\n' "$candidate_container"
printf 'candidate_port=%s\n' "$candidate_port"
printf 'active_container=%s\n' "$active_container"
printf 'active_port=%s\n' "$active_port"
printf 'background_activation=%s\n' "$([[ $deployment_mode == downtime ]] && printf pass || printf pending)"
if [[ $profile == 198 || $profile == 199 || $profile == 202 || $profile == 206 || $profile == 207 || $profile == 208 || $profile == 209 || $profile == 210 || $profile == 212 || $profile == 213 || $profile == 215 || $profile == 232 || $profile == 233 || $profile == 234 || $profile == 235 ]]; then
  printf 'managed_monitor_key_names_verified=true\n'
fi
if [[ $profile == 199 || $profile == 202 || $profile == 206 || $profile == 207 || $profile == 208 || $profile == 209 || $profile == 210 || $profile == 212 || $profile == 213 || $profile == 215 || $profile == 232 || $profile == 233 || $profile == 234 || $profile == 235 ]]; then
  printf 'reasoning_effort_policy_verified=true\n'
fi
if [[ $profile == 202 || $profile == 206 || $profile == 207 || $profile == 208 || $profile == 209 || $profile == 210 || $profile == 212 || $profile == 213 || $profile == 215 || $profile == 232 || $profile == 233 || $profile == 234 || $profile == 235 ]]; then
  printf 'alipay_mobile_precreate_migration_verified=true\n'
  printf 'group_auth_cache_image_generation_verified=true\n'
  printf 'composite_model_routes_verified=true\n'
fi
if [[ $profile == 206 || $profile == 207 || $profile == 208 || $profile == 209 || $profile == 210 || $profile == 212 || $profile == 213 || $profile == 215 || $profile == 232 || $profile == 233 || $profile == 234 || $profile == 235 ]]; then
  printf 'session_id_columns_verified=true\n'
  printf 'live_request_type_verified=true\n'
  printf 'group_allow_live_verified=true\n'
  printf 'email_alias_index_verified=true\n'
fi
if [[ $profile == 208 || $profile == 209 || $profile == 210 || $profile == 212 || $profile == 213 || $profile == 215 || $profile == 232 || $profile == 233 || $profile == 234 || $profile == 235 ]]; then
  printf 'passkey_schema_verified=true\n'
fi
if [[ $profile == 209 || $profile == 210 || $profile == 212 || $profile == 213 || $profile == 215 || $profile == 232 || $profile == 233 || $profile == 234 || $profile == 235 ]]; then
  printf 'user_usage_aggregation_schema_verified=true\n'
fi
if [[ $profile == 212 || $profile == 213 || $profile == 215 || $profile == 232 || $profile == 233 || $profile == 234 || $profile == 235 ]]; then
  printf 'group_profit_control_schema_verified=true\n'
  printf 'group_profit_auth_cache_trigger_verified=true\n'
fi
if [[ $profile == 215 || $profile == 232 || $profile == 233 || $profile == 234 || $profile == 235 ]]; then
  printf 'usage_log_upstream_model_columns_verified=true\n'
  printf 'usage_log_upstream_model_mismatch_index_verified=true\n'
fi
if [[ $profile == 232 || $profile == 233 || $profile == 234 || $profile == 235 ]]; then
  printf 'channel_monitor_v2_schema_verified=true\n'
  printf 'channel_monitor_v2_defaults_verified=true\n'
  printf 'group_media_pricing_schema_verified=true\n'
  printf 'group_media_auth_cache_trigger_verified=true\n'
fi
if [[ $profile == 233 || $profile == 234 || $profile == 235 ]]; then
  printf 'migration_233_duplicate_keys=0\n'
  printf 'migration_233_index_verified=true\n'
  printf 'migration_233_table_state=verified\n'
  printf 'migration_233_columns_verified=true\n'
  printf 'migration_233_health_index_verified=true\n'
  printf 'migration_233_privileges_verified=true\n'
  printf 'migration_233_trigger_verified=true\n'
  printf 'migration_233_postflight=pass\n'
fi
if [[ $profile == 235 ]]; then
  printf 'migration_234_schema_state=verified\n'
  printf 'migration_234_schema_verified=true\n'
  printf 'migration_234_postflight=pass\n'
fi
