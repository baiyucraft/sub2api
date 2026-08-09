#!/usr/bin/env bash
set -Eeuo pipefail

deploy_dir=${DEPLOY_DIR:-/opt/sub2api}
release_dir=${RELEASE_DIR:?RELEASE_DIR is required}
minimum_free_bytes=${MINIMUM_FREE_BYTES:-10737418240}
canary_key_file=${CANARY_KEY_FILE:-/root/.config/sub2api-release/canary-api-key}
source /opt/sub2api/releases/.active-release/assets/context.sh
[[ ! -e $release_dir/.consumed ]]
[[ -f $canary_key_file && ! -L $canary_key_file && $(stat -c '%a' "$canary_key_file") == 600 ]]
[[ $(docker image inspect -f '{{.Id}}' "$candidate_image_id") == "$candidate_image_id" ]]
cd "$deploy_dir"
[[ -f docker-compose.yml && -f .env ]]
[[ $(docker inspect -f '{{.State.Status}}' sub2api) == running ]]
[[ $(docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' sub2api) == healthy ]]
[[ $(docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' sub2api-postgres) == healthy ]]
[[ $(docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' sub2api-redis) == healthy ]]
[[ $(systemctl is-active nginx) == active ]]
[[ $(systemctl is-active sub2api-backup.service 2>/dev/null || true) != active ]]
[[ $(systemctl is-enabled sub2api-backup.timer 2>/dev/null || true) == enabled ]]
backup_exec=$(systemctl show sub2api-backup.service -p ExecStart --value)
backup_path=$(sed -n 's/.*path=\([^ ;}]*\).*/\1/p' <<<"$backup_exec" | head -n1)
[[ -f $backup_path && ! -L $backup_path ]]
grep -Fq '/run/lock/sub2api-backup-global.lock' "$backup_path"
migration_status=verified
migration_195_status=verified
migration_196_status=not_applicable
migration_197_status=not_applicable
migration_198_status=not_applicable
migration_199_status=not_applicable
migration_200_status=not_applicable
migration_201_status=not_applicable
migration_202_status=not_applicable
migration_203_status=not_applicable
migration_204_status=not_applicable
migration_205_status=not_applicable
migration_206_status=not_applicable
migration_208_status=not_applicable
migration_209_status=not_applicable
migration_211_status=not_applicable
migration_212_status=not_applicable
migration_214_status=not_applicable
migration_215_status=not_applicable
migration_216_status=not_applicable
migration_217_status=not_applicable
migration_218_status=not_applicable
migration_219_status=not_applicable
migration_220_status=not_applicable
migration_221_status=not_applicable
migration_222_status=not_applicable
migration_223_status=not_applicable
migration_224_status=not_applicable
migration_225_status=not_applicable
migration_226_status=not_applicable
migration_227_status=not_applicable
migration_228_status=not_applicable
migration_229_status=not_applicable
migration_230_status=not_applicable
migration_231_status=not_applicable
migration_232_status=not_applicable
while IFS=$'\t' read -r migration migration_checksum; do
  case "$migration" in
    196_ops_ingress_reject_aggregates.sql) migration_196_status=verified ;;
    197_auth_cache_invalidation_outbox.sql) migration_197_status=verified ;;
    198_normalize_managed_monitor_key_names.sql) migration_198_status=verified ;;
    199_group_reasoning_effort_policy.sql) migration_199_status=verified ;;
    200_alipay_mobile_precreate_deep_link.sql) migration_200_status=verified ;;
    201_group_auth_cache_image_generation.sql) migration_201_status=verified ;;
    202_composite_model_routes.sql) migration_202_status=verified ;;
    203_add_usage_log_session_id.sql) migration_203_status=verified ;;
    204_allow_live_usage_request_type.sql) migration_204_status=verified ;;
    205_add_group_allow_live.sql) migration_205_status=verified ;;
    206_add_users_email_alias_dedup_index_notx.sql) migration_206_status=verified ;;
    208_passkey_credentials.sql) migration_208_status=verified ;;
    209_user_usage_aggregation.sql) migration_209_status=verified ;;
    211_group_profit_control.sql) migration_211_status=verified ;;
    212_group_profit_control_auth_cache_invalidation.sql) migration_212_status=verified ;;
    214_add_usage_log_upstream_response_model.sql) migration_214_status=verified ;;
    215_add_usage_log_upstream_model_mismatch_index_notx.sql) migration_215_status=verified ;;
    216_channel_monitor_v2.sql) migration_216_status=verified ;;
    217_channel_monitor_mode.sql) migration_217_status=verified ;;
    218_channel_monitor_v2_ignored_error_categories.sql) migration_218_status=verified ;;
    219_channel_monitor_v2_seed_popular_models.sql) migration_219_status=verified ;;
    220_channel_monitor_v2_health_thresholds.sql) migration_220_status=verified ;;
    221_channel_monitor_v2_fixed_rollups.sql) migration_221_status=verified ;;
    222_channel_monitor_v2_rollup_permissions.sql) migration_222_status=verified ;;
    223_channel_monitor_v2_refresh_5m.sql) migration_223_status=verified ;;
    224_channel_monitor_v2_full_table_permissions.sql) migration_224_status=verified ;;
    225_channel_monitor_v2_default_ignore_and_cache.sql) migration_225_status=verified ;;
    226_channel_monitor_hide_throughput.sql) migration_226_status=verified ;;
    227_channel_monitor_v2_reset_factory_cache_thresholds.sql) migration_227_status=verified ;;
    228_channel_monitor_v2_privacy_defaults.sql) migration_228_status=verified ;;
    229_group_video_model_prices.sql) migration_229_status=verified ;;
    230_group_audio_voice_pricing.sql) migration_230_status=verified ;;
    231_group_search_price_per_1k.sql) migration_231_status=verified ;;
    232_clear_non_grok_video_generation_config.sql) migration_232_status=verified ;;
  esac
  migration_state=$(docker exec sub2api-postgres psql -X -A -t -F '|' -U sub2api -d sub2api -c "SELECT filename,checksum FROM schema_migrations WHERE filename='$migration'")
  if [[ -z $migration_state ]]; then
    migration_status=absent
    case "$migration" in
      195_upstream_scheduling_monitor_rates.sql) migration_195_status=absent ;;
      196_ops_ingress_reject_aggregates.sql) migration_196_status=absent ;;
      197_auth_cache_invalidation_outbox.sql) migration_197_status=absent ;;
      198_normalize_managed_monitor_key_names.sql) migration_198_status=absent ;;
      199_group_reasoning_effort_policy.sql) migration_199_status=absent ;;
      200_alipay_mobile_precreate_deep_link.sql) migration_200_status=absent ;;
      201_group_auth_cache_image_generation.sql) migration_201_status=absent ;;
      202_composite_model_routes.sql) migration_202_status=absent ;;
      203_add_usage_log_session_id.sql) migration_203_status=absent ;;
      204_allow_live_usage_request_type.sql) migration_204_status=absent ;;
      205_add_group_allow_live.sql) migration_205_status=absent ;;
      206_add_users_email_alias_dedup_index_notx.sql) migration_206_status=absent ;;
      208_passkey_credentials.sql) migration_208_status=absent ;;
      209_user_usage_aggregation.sql) migration_209_status=absent ;;
      211_group_profit_control.sql) migration_211_status=absent ;;
      212_group_profit_control_auth_cache_invalidation.sql) migration_212_status=absent ;;
      214_add_usage_log_upstream_response_model.sql) migration_214_status=absent ;;
      215_add_usage_log_upstream_model_mismatch_index_notx.sql) migration_215_status=absent ;;
      216_channel_monitor_v2.sql) migration_216_status=absent ;;
      217_channel_monitor_mode.sql) migration_217_status=absent ;;
      218_channel_monitor_v2_ignored_error_categories.sql) migration_218_status=absent ;;
      219_channel_monitor_v2_seed_popular_models.sql) migration_219_status=absent ;;
      220_channel_monitor_v2_health_thresholds.sql) migration_220_status=absent ;;
      221_channel_monitor_v2_fixed_rollups.sql) migration_221_status=absent ;;
      222_channel_monitor_v2_rollup_permissions.sql) migration_222_status=absent ;;
      223_channel_monitor_v2_refresh_5m.sql) migration_223_status=absent ;;
      224_channel_monitor_v2_full_table_permissions.sql) migration_224_status=absent ;;
      225_channel_monitor_v2_default_ignore_and_cache.sql) migration_225_status=absent ;;
      226_channel_monitor_hide_throughput.sql) migration_226_status=absent ;;
      227_channel_monitor_v2_reset_factory_cache_thresholds.sql) migration_227_status=absent ;;
      228_channel_monitor_v2_privacy_defaults.sql) migration_228_status=absent ;;
      229_group_video_model_prices.sql) migration_229_status=absent ;;
      230_group_audio_voice_pricing.sql) migration_230_status=absent ;;
      231_group_search_price_per_1k.sql) migration_231_status=absent ;;
      232_clear_non_grok_video_generation_config.sql) migration_232_status=absent ;;
    esac
  else
    [[ $migration_state == "$migration|$migration_checksum" ]]
  fi
done < <(jq -r '.manifest.migration_sha256 | to_entries[] | [.key,.value] | @tsv' "$active_claim/gate.json")
free_bytes=$(df -PB1 /var/lib/docker 2>/dev/null | awk 'NR==2{print $4}' || df -PB1 / | awk 'NR==2{print $4}')
(( free_bytes >= minimum_free_bytes ))
compose_json=$(docker compose config --format json)
rendered_image=$(jq -r '.services.sub2api.image // empty' <<<"$compose_json")
[[ -n $rendered_image ]]
pre_image_id=$(docker inspect -f '{{.Image}}' sub2api)
[[ $(docker image inspect -f '{{.Id}}' "$rendered_image") == "$pre_image_id" ]]
jq -e '.services.sub2api.volumes | any(.target == "/app/data" and (.type == "bind" or .type == "volume"))' <<<"$compose_json" >/dev/null
jq -e '(.services.sub2api.network_mode == "host" and .services.sub2api.environment.SERVER_HOST == "127.0.0.1" and (.services.sub2api.environment.SERVER_PORT | tostring) == "18080") or ((.services.sub2api.ports // []) | any(.target == 8080 and (.published | tostring) == "18080" and .host_ip == "127.0.0.1"))' <<<"$compose_json" >/dev/null
printf 'preflight=pass\n'
printf 'pre_switch_image_id=%s\n' "$pre_image_id"
printf 'free_bytes=%s\n' "$free_bytes"
printf 'migration_status=%s\n' "$migration_status"
printf 'migration_195_status=%s\n' "$migration_195_status"
printf 'migration_196_status=%s\n' "$migration_196_status"
printf 'migration_197_status=%s\n' "$migration_197_status"
printf 'migration_198_status=%s\n' "$migration_198_status"
printf 'migration_199_status=%s\n' "$migration_199_status"
printf 'migration_200_status=%s\n' "$migration_200_status"
printf 'migration_201_status=%s\n' "$migration_201_status"
printf 'migration_202_status=%s\n' "$migration_202_status"
printf 'migration_203_status=%s\n' "$migration_203_status"
printf 'migration_204_status=%s\n' "$migration_204_status"
printf 'migration_205_status=%s\n' "$migration_205_status"
printf 'migration_206_status=%s\n' "$migration_206_status"
printf 'migration_208_status=%s\n' "$migration_208_status"
printf 'migration_209_status=%s\n' "$migration_209_status"
printf 'migration_211_status=%s\n' "$migration_211_status"
printf 'migration_212_status=%s\n' "$migration_212_status"
printf 'migration_214_status=%s\n' "$migration_214_status"
printf 'migration_215_status=%s\n' "$migration_215_status"
printf 'migration_216_status=%s\n' "$migration_216_status"
printf 'migration_217_status=%s\n' "$migration_217_status"
printf 'migration_218_status=%s\n' "$migration_218_status"
printf 'migration_219_status=%s\n' "$migration_219_status"
printf 'migration_220_status=%s\n' "$migration_220_status"
printf 'migration_221_status=%s\n' "$migration_221_status"
printf 'migration_222_status=%s\n' "$migration_222_status"
printf 'migration_223_status=%s\n' "$migration_223_status"
printf 'migration_224_status=%s\n' "$migration_224_status"
printf 'migration_225_status=%s\n' "$migration_225_status"
printf 'migration_226_status=%s\n' "$migration_226_status"
printf 'migration_227_status=%s\n' "$migration_227_status"
printf 'migration_228_status=%s\n' "$migration_228_status"
printf 'migration_229_status=%s\n' "$migration_229_status"
printf 'migration_230_status=%s\n' "$migration_230_status"
printf 'migration_231_status=%s\n' "$migration_231_status"
printf 'migration_232_status=%s\n' "$migration_232_status"
