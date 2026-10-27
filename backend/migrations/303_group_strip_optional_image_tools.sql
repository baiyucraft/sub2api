-- 禁图分组自动清理可选生图工具；本开关允许生图分组独立启用相同清理。
ALTER TABLE groups
    ADD COLUMN IF NOT EXISTS strip_optional_image_tools BOOLEAN NOT NULL DEFAULT FALSE;

COMMENT ON COLUMN groups.strip_optional_image_tools IS '允许生图分组是否主动清理可选生图工具；禁图分组自动清理';

-- 新字段属于认证快照中的请求策略，直写分组时也应使缓存失效。
CREATE OR REPLACE FUNCTION enqueue_group_auth_cache_invalidation()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
DECLARE
    target_group_id BIGINT;
BEGIN
    target_group_id := OLD.id;
    IF TG_OP = 'UPDATE'
       AND OLD.status IS NOT DISTINCT FROM NEW.status
       AND OLD.is_exclusive IS NOT DISTINCT FROM NEW.is_exclusive
       AND OLD.allow_image_generation IS NOT DISTINCT FROM NEW.allow_image_generation
       AND OLD.strip_optional_image_tools IS NOT DISTINCT FROM NEW.strip_optional_image_tools
       AND OLD.platform IS NOT DISTINCT FROM NEW.platform
       AND OLD.subscription_type IS NOT DISTINCT FROM NEW.subscription_type
       AND OLD.rate_multiplier IS NOT DISTINCT FROM NEW.rate_multiplier
       AND OLD.peak_rate_enabled IS NOT DISTINCT FROM NEW.peak_rate_enabled
       AND OLD.peak_start IS NOT DISTINCT FROM NEW.peak_start
       AND OLD.peak_end IS NOT DISTINCT FROM NEW.peak_end
       AND OLD.peak_rate_multiplier IS NOT DISTINCT FROM NEW.peak_rate_multiplier
       AND OLD.profit_control_enabled IS NOT DISTINCT FROM NEW.profit_control_enabled
       AND OLD.profit_min_margin IS NOT DISTINCT FROM NEW.profit_min_margin
       AND OLD.profit_safety_buffer IS NOT DISTINCT FROM NEW.profit_safety_buffer
       AND OLD.video_price_480p IS NOT DISTINCT FROM NEW.video_price_480p
       AND OLD.video_price_720p IS NOT DISTINCT FROM NEW.video_price_720p
       AND OLD.video_price_1080p IS NOT DISTINCT FROM NEW.video_price_1080p
       AND OLD.video_model_prices IS NOT DISTINCT FROM NEW.video_model_prices
       AND OLD.web_search_price_per_call IS NOT DISTINCT FROM NEW.web_search_price_per_call
       AND OLD.search_price_per_1k IS NOT DISTINCT FROM NEW.search_price_per_1k
       AND OLD.audio_realtime_price_per_min IS NOT DISTINCT FROM NEW.audio_realtime_price_per_min
       AND OLD.audio_tts_price_per_million_chars IS NOT DISTINCT FROM NEW.audio_tts_price_per_million_chars
       AND OLD.audio_stt_price_per_hour IS NOT DISTINCT FROM NEW.audio_stt_price_per_hour
       AND OLD.image_cost_routing_enabled IS NOT DISTINCT FROM NEW.image_cost_routing_enabled
       AND OLD.image_cost_routing_mode IS NOT DISTINCT FROM NEW.image_cost_routing_mode
       AND OLD.image_cost_tolerance_percent IS NOT DISTINCT FROM NEW.image_cost_tolerance_percent
       AND OLD.image_cost_stale_after_seconds IS NOT DISTINCT FROM NEW.image_cost_stale_after_seconds
       AND OLD.deleted_at IS NOT DISTINCT FROM NEW.deleted_at THEN
        RETURN NEW;
    END IF;

    INSERT INTO auth_cache_invalidation_outbox (cache_key)
    SELECT encode(sha256(convert_to(k.key, 'UTF8')), 'hex')
    FROM api_keys AS k
    WHERE k.group_id = target_group_id
      AND k.deleted_at IS NULL
      AND k.key <> '';
    IF TG_OP = 'DELETE' THEN
        RETURN OLD;
    END IF;
    RETURN NEW;
END;
$$;
