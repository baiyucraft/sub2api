from __future__ import annotations


PROFILES = {
    "182": {
        "name": "182",
        "origin": "https://github.com/baiyucraft/sub2api.git",
        "version": "0.1.153-baiyu",
        "migrations": ["182_upstream_actual_rate_multiplier.sql"],
        "gate_ttl_seconds": 86400,
        "vm_identity": "sub2api-dev",
        "vm_source": "/opt/sub2api-src",
        "vm_deploy": "/opt/sub2api-deploy",
        "vm_data": "/opt/sub2api-deploy/data-dev",
        "rack_source": "/opt/sub2api-src",
        "rack_deploy": "/opt/sub2api",
        "public_domain": "sub.baiyuapi.xyz",
        "rack_public_ip": "173.254.217.135",
        "dmit_public_ip": "179.255.148.240",
        "production_health_port": 18080,
        "minimum_rack_free_bytes": 10737418240,
        "minimum_backup_free_bytes": 5368709120,
        "minimum_free_after_bytes": 2147483648,
        "canary_api_key_id": 2,
    },
    "187": {
        "name": "187",
        "origin": "https://github.com/baiyucraft/sub2api.git",
        "version": "0.1.156-baiyu",
        "migrations": [
            "182_upstream_actual_rate_multiplier.sql",
            "183_add_usage_log_long_context_billing.sql",
            "184_add_ops_system_logs_host.sql",
            "185_default_openai_long_context_billing.sql",
            "185a_add_ops_system_logs_host_index_notx.sql",
            "186_channel_monitor_grok_provider.sql",
            "187_upstream_actual_balance_and_cost_groups.sql",
        ],
        "gate_ttl_seconds": 86400,
        "vm_identity": "sub2api-dev",
        "vm_source": "/opt/sub2api-src",
        "vm_deploy": "/opt/sub2api-deploy",
        "vm_data": "/opt/sub2api-deploy/data-dev",
        "rack_source": "/opt/sub2api-src",
        "rack_deploy": "/opt/sub2api",
        "public_domain": "sub.baiyuapi.xyz",
        "rack_public_ip": "173.254.217.135",
        "dmit_public_ip": "179.255.148.240",
        "production_health_port": 18080,
        "minimum_rack_free_bytes": 10737418240,
        "minimum_backup_free_bytes": 5368709120,
        "minimum_free_after_bytes": 2147483648,
        "canary_api_key_id": 2,
    },
    "191": {
        "name": "191",
        "origin": "https://github.com/baiyucraft/sub2api.git",
        "version": "0.1.157-baiyu",
        "migrations": [
            "182_upstream_actual_rate_multiplier.sql",
            "183_add_usage_log_long_context_billing.sql",
            "184_add_ops_system_logs_host.sql",
            "185_default_openai_long_context_billing.sql",
            "185a_add_ops_system_logs_host_index_notx.sql",
            "186_channel_monitor_grok_provider.sql",
            "187_upstream_actual_balance_and_cost_groups.sql",
            "188_add_subscription_plan_currency.sql",
            "189_channel_image_input_price.sql",
            "190_usage_log_image_input_tokens.sql",
            "191_audit_logs.sql",
        ],
        "gate_ttl_seconds": 86400,
        "vm_identity": "sub2api-dev",
        "vm_source": "/opt/sub2api-src",
        "vm_deploy": "/opt/sub2api-deploy",
        "vm_data": "/opt/sub2api-deploy/data-dev",
        "rack_source": "/opt/sub2api-src",
        "rack_deploy": "/opt/sub2api",
        "public_domain": "sub.baiyuapi.xyz",
        "rack_public_ip": "173.254.217.135",
        "dmit_public_ip": "179.255.148.240",
        "production_health_port": 18080,
        "minimum_rack_free_bytes": 10737418240,
        "minimum_backup_free_bytes": 5368709120,
        "minimum_free_after_bytes": 2147483648,
        "canary_api_key_id": 2,
    },
    "192": {
        "name": "192",
        "origin": "https://github.com/baiyucraft/sub2api.git",
        "version": "0.1.158-baiyu",
        "migrations": [
            "182_upstream_actual_rate_multiplier.sql",
            "183_add_usage_log_long_context_billing.sql",
            "184_add_ops_system_logs_host.sql",
            "185_default_openai_long_context_billing.sql",
            "185a_add_ops_system_logs_host_index_notx.sql",
            "186_channel_monitor_grok_provider.sql",
            "187_upstream_actual_balance_and_cost_groups.sql",
            "188_add_subscription_plan_currency.sql",
            "189_channel_image_input_price.sql",
            "190_usage_log_image_input_tokens.sql",
            "191_audit_logs.sql",
            "192_group_duplicate_operation_id.sql",
        ],
        "gate_ttl_seconds": 86400,
        "vm_identity": "sub2api-dev",
        "vm_source": "/opt/sub2api-src",
        "vm_deploy": "/opt/sub2api-deploy",
        "vm_data": "/opt/sub2api-deploy/data-dev",
        "rack_source": "/opt/sub2api-src",
        "rack_deploy": "/opt/sub2api",
        "public_domain": "sub.baiyuapi.xyz",
        "rack_public_ip": "173.254.217.135",
        "dmit_public_ip": "179.255.148.240",
        "production_health_port": 18080,
        "minimum_rack_free_bytes": 10737418240,
        "minimum_backup_free_bytes": 5368709120,
        "minimum_free_after_bytes": 2147483648,
        "canary_api_key_id": 2,
    },
    "194": {
        "name": "194",
        "origin": "https://github.com/baiyucraft/sub2api.git",
        "version": "0.1.160-baiyu",
        "migrations": [
            "182_upstream_actual_rate_multiplier.sql",
            "183_add_usage_log_long_context_billing.sql",
            "184_add_ops_system_logs_host.sql",
            "185_default_openai_long_context_billing.sql",
            "185a_add_ops_system_logs_host_index_notx.sql",
            "186_channel_monitor_grok_provider.sql",
            "187_upstream_actual_balance_and_cost_groups.sql",
            "188_add_subscription_plan_currency.sql",
            "189_channel_image_input_price.sql",
            "190_usage_log_image_input_tokens.sql",
            "191_audit_logs.sql",
            "192_group_duplicate_operation_id.sql",
            "193_prompt_audit.sql",
            "194_prompt_audit_full_prompt.sql",
        ],
        "gate_ttl_seconds": 86400,
        "vm_identity": "sub2api-dev",
        "vm_source": "/opt/sub2api-src",
        "vm_deploy": "/opt/sub2api-deploy",
        "vm_data": "/opt/sub2api-deploy/data-dev",
        "rack_source": "/opt/sub2api-src",
        "rack_deploy": "/opt/sub2api",
        "public_domain": "sub.baiyuapi.xyz",
        "rack_public_ip": "173.254.217.135",
        "dmit_public_ip": "179.255.148.240",
        "production_health_port": 18080,
        "minimum_rack_free_bytes": 10737418240,
        "minimum_backup_free_bytes": 5368709120,
        "minimum_free_after_bytes": 2147483648,
        "canary_api_key_id": 2,
    },
    "195": {
        "name": "195",
        "origin": "https://github.com/baiyucraft/sub2api.git",
        "version": "0.1.161-baiyu",
        "migrations": [
            "182_upstream_actual_rate_multiplier.sql",
            "183_add_usage_log_long_context_billing.sql",
            "184_add_ops_system_logs_host.sql",
            "185_default_openai_long_context_billing.sql",
            "185a_add_ops_system_logs_host_index_notx.sql",
            "186_channel_monitor_grok_provider.sql",
            "187_upstream_actual_balance_and_cost_groups.sql",
            "188_add_subscription_plan_currency.sql",
            "189_channel_image_input_price.sql",
            "190_usage_log_image_input_tokens.sql",
            "191_audit_logs.sql",
            "192_group_duplicate_operation_id.sql",
            "193_prompt_audit.sql",
            "194_prompt_audit_full_prompt.sql",
            "195_upstream_scheduling_monitor_rates.sql",
        ],
        "gate_ttl_seconds": 86400,
        "vm_identity": "sub2api-dev",
        "vm_source": "/opt/sub2api-src",
        "vm_deploy": "/opt/sub2api-deploy",
        "vm_data": "/opt/sub2api-deploy/data-dev",
        "rack_source": "/opt/sub2api-src",
        "rack_deploy": "/opt/sub2api",
        "public_domain": "sub.baiyuapi.xyz",
        "rack_public_ip": "173.254.217.135",
        "dmit_public_ip": "179.255.148.240",
        "production_health_port": 18080,
        "minimum_rack_free_bytes": 10737418240,
        "minimum_backup_free_bytes": 5368709120,
        "minimum_free_after_bytes": 2147483648,
        "canary_api_key_id": 2,
    },
    "197": {
        "name": "197",
        "origin": "https://github.com/baiyucraft/sub2api.git",
        "version": "0.1.162-baiyu",
        "migrations": [
            "182_upstream_actual_rate_multiplier.sql",
            "183_add_usage_log_long_context_billing.sql",
            "184_add_ops_system_logs_host.sql",
            "185_default_openai_long_context_billing.sql",
            "185a_add_ops_system_logs_host_index_notx.sql",
            "186_channel_monitor_grok_provider.sql",
            "187_upstream_actual_balance_and_cost_groups.sql",
            "188_add_subscription_plan_currency.sql",
            "189_channel_image_input_price.sql",
            "190_usage_log_image_input_tokens.sql",
            "191_audit_logs.sql",
            "192_group_duplicate_operation_id.sql",
            "193_prompt_audit.sql",
            "194_prompt_audit_full_prompt.sql",
            "195_upstream_scheduling_monitor_rates.sql",
            "196_ops_ingress_reject_aggregates.sql",
            "197_auth_cache_invalidation_outbox.sql",
        ],
        "gate_ttl_seconds": 86400,
        "vm_identity": "sub2api-dev",
        "vm_source": "/opt/sub2api-src",
        "vm_deploy": "/opt/sub2api-deploy",
        "vm_data": "/opt/sub2api-deploy/data-dev",
        "rack_source": "/opt/sub2api-src",
        "rack_deploy": "/opt/sub2api",
        "public_domain": "sub.baiyuapi.xyz",
        "rack_public_ip": "173.254.217.135",
        "dmit_public_ip": "179.255.148.240",
        "production_health_port": 18080,
        "minimum_rack_free_bytes": 10737418240,
        "minimum_backup_free_bytes": 5368709120,
        "minimum_free_after_bytes": 2147483648,
        "canary_api_key_id": 2,
    },
    "198": {
        "name": "198",
        "origin": "https://github.com/baiyucraft/sub2api.git",
        "version": "0.1.162-baiyu",
        "migrations": [
            "182_upstream_actual_rate_multiplier.sql",
            "183_add_usage_log_long_context_billing.sql",
            "184_add_ops_system_logs_host.sql",
            "185_default_openai_long_context_billing.sql",
            "185a_add_ops_system_logs_host_index_notx.sql",
            "186_channel_monitor_grok_provider.sql",
            "187_upstream_actual_balance_and_cost_groups.sql",
            "188_add_subscription_plan_currency.sql",
            "189_channel_image_input_price.sql",
            "190_usage_log_image_input_tokens.sql",
            "191_audit_logs.sql",
            "192_group_duplicate_operation_id.sql",
            "193_prompt_audit.sql",
            "194_prompt_audit_full_prompt.sql",
            "195_upstream_scheduling_monitor_rates.sql",
            "196_ops_ingress_reject_aggregates.sql",
            "197_auth_cache_invalidation_outbox.sql",
            "198_normalize_managed_monitor_key_names.sql",
        ],
        "gate_ttl_seconds": 86400,
        "vm_identity": "sub2api-dev",
        "vm_source": "/opt/sub2api-src",
        "vm_deploy": "/opt/sub2api-deploy",
        "vm_data": "/opt/sub2api-deploy/data-dev",
        "rack_source": "/opt/sub2api-src",
        "rack_deploy": "/opt/sub2api",
        "public_domain": "sub.baiyuapi.xyz",
        "rack_public_ip": "173.254.217.135",
        "dmit_public_ip": "179.255.148.240",
        "production_health_port": 18080,
        "minimum_rack_free_bytes": 10737418240,
        "minimum_backup_free_bytes": 5368709120,
        "minimum_free_after_bytes": 2147483648,
        "canary_api_key_id": 2,
    }
}

PROFILES["199"] = {
    **PROFILES["198"],
    "name": "199",
    "version": "0.1.163-baiyu",
    "migrations": [
        *PROFILES["198"]["migrations"],
        "199_group_reasoning_effort_policy.sql",
    ],
}

PROFILES["202"] = {
    **PROFILES["199"],
    "name": "202",
    "version": "0.1.164-baiyu",
    "migrations": [
        *PROFILES["199"]["migrations"],
        "200_alipay_mobile_precreate_deep_link.sql",
        "201_group_auth_cache_image_generation.sql",
        "202_composite_model_routes.sql",
    ],
}

PROFILES["206"] = {
    **PROFILES["202"],
    "name": "206",
    "version": "0.1.165-baiyu",
    "migrations": [
        *PROFILES["202"]["migrations"],
        "203_add_usage_log_session_id.sql",
        "204_allow_live_usage_request_type.sql",
        "205_add_group_allow_live.sql",
        "206_add_users_email_alias_dedup_index_notx.sql",
    ],
}

PROFILES["207"] = {
    **PROFILES["206"],
    "name": "207",
    "version": "0.1.166-baiyu",
    "migrations": [*PROFILES["206"]["migrations"]],
}

PROFILES["208"] = {
    **PROFILES["207"],
    "name": "208",
    "version": "0.1.168-baiyu",
    "migrations": [
        *PROFILES["207"]["migrations"],
        "208_passkey_credentials.sql",
    ],
}

PROFILES["209"] = {
    **PROFILES["208"],
    "name": "209",
    "version": "0.1.168-baiyu",
    "migrations": [
        *PROFILES["208"]["migrations"],
        "209_user_usage_aggregation.sql",
    ],
}

PROFILES["210"] = {
    **PROFILES["209"],
    "name": "210",
    "version": "0.1.169-baiyu",
    "migrations": [*PROFILES["209"]["migrations"]],
}

PROFILES["212"] = {
    **PROFILES["210"],
    "name": "212",
    "version": "0.1.170-baiyu",
    "migrations": [
        *PROFILES["210"]["migrations"],
        "211_group_profit_control.sql",
        "212_group_profit_control_auth_cache_invalidation.sql",
    ],
}

PROFILES["213"] = {
    **PROFILES["212"],
    "name": "213",
    "version": "0.1.171-baiyu",
    "migrations": [*PROFILES["212"]["migrations"]],
}

PROFILES["215"] = {
    **PROFILES["213"],
    "name": "215",
    "version": "0.1.172-baiyu",
    "migrations": [
        *PROFILES["213"]["migrations"],
        "214_add_usage_log_upstream_response_model.sql",
        "215_add_usage_log_upstream_model_mismatch_index_notx.sql",
    ],
}

PROFILES["232"] = {
    **PROFILES["215"],
    "name": "232",
    "version": "0.1.173-baiyu",
    "compatibility_version": "0.1.172-baiyu",
    "compatibility_commit": "74e47e67205084750ccd994c331ead328e4ce35b",
    "compatibility_image_id": "sha256:cd3dff0ce18762d7faa9d4a4492eb770b616f9b01b66256ce6280c2f4855abd6",
    "migrations": [
        *PROFILES["215"]["migrations"],
        "216_channel_monitor_v2.sql",
        "217_channel_monitor_mode.sql",
        "218_channel_monitor_v2_ignored_error_categories.sql",
        "219_channel_monitor_v2_seed_popular_models.sql",
        "220_channel_monitor_v2_health_thresholds.sql",
        "221_channel_monitor_v2_fixed_rollups.sql",
        "222_channel_monitor_v2_rollup_permissions.sql",
        "223_channel_monitor_v2_refresh_5m.sql",
        "224_channel_monitor_v2_full_table_permissions.sql",
        "225_channel_monitor_v2_default_ignore_and_cache.sql",
        "226_channel_monitor_hide_throughput.sql",
        "227_channel_monitor_v2_reset_factory_cache_thresholds.sql",
        "228_channel_monitor_v2_privacy_defaults.sql",
        "229_group_video_model_prices.sql",
        "230_group_audio_voice_pricing.sql",
        "231_group_search_price_per_1k.sql",
        "232_clear_non_grok_video_generation_config.sql",
    ],
}

# Profile 233 consolidates the previously-unreleased upstream-management
# migrations while retaining profile 232's production compatibility identity.
PROFILES["233"] = {
    **PROFILES["232"],
    "name": "233",
    "version": "0.1.173-baiyu",
    "migrations": [
        *PROFILES["232"]["migrations"],
        "233_upstream_management.sql",
    ],
}

# Profile 234 is a version-only fork sync profile. It deliberately reuses the
# exact migration contract and production compatibility identity from profile
# 233; no database migration is added for the upstream 0.1.175 merge.
PROFILES["234"] = {
    **PROFILES["233"],
    "name": "234",
    "version": "0.1.175-baiyu",
    "migrations": [*PROFILES["233"]["migrations"]],
}

# Profile 235 combines the official group model-pricing migration with the
# already released upstream-management contract. The historical 233/234
# profiles remain immutable.
PROFILES["235"] = {
    **PROFILES["234"],
    "name": "235",
    "version": "0.1.176-baiyu",
    "migrations": [*PROFILES["234"]["migrations"], "234_group_model_pricing.sql"],
}

# Profile 236 is a version-only UI release. It inherits profile 235's complete
# 51-migration contract and compatibility identity without adding schema work.
PROFILES["236"] = {
    **PROFILES["235"],
    "name": "236",
    "version": "0.176-baiyu",
    "migrations": [*PROFILES["235"]["migrations"]],
}

# Profile 237 is the official 0.1.177 fork merge.  It keeps the historical
# profile 236 contract immutable and appends the two official group-usage
# migrations under the next locally available numbers.
PROFILES["237"] = {
    **PROFILES["236"],
    "name": "237",
    "version": "0.1.177-baiyu",
    "migrations": [
        *PROFILES["236"]["migrations"],
        "235_group_usage_daily_rollups.sql",
        "236_group_usage_rollup_timezone.sql",
    ],
}

# Profile 238 adds fork-only image-cost routing configuration while preserving
# every historical profile and compatibility identity unchanged.
PROFILES["238"] = {
    **PROFILES["237"],
    "name": "238",
    "version": "0.1.177-baiyu",
    "migrations": [*PROFILES["237"]["migrations"], "237_image_cost_routing.sql"],
}

# Profile 239 adds the fork-only upstream derived-account lifecycle contract.
# Historical profiles, checksums, and compatibility identity remain unchanged.
PROFILES["239"] = {
    **PROFILES["238"],
    "name": "239",
    "version": "0.1.177-baiyu",
    "migrations": [*PROFILES["238"]["migrations"], "238_upstream_account_lifecycle.sql"],
}


def get_profile(name: str) -> dict:
    try:
        return dict(PROFILES[name])
    except KeyError as error:
        raise ValueError(f"unknown release profile: {name}") from error
