-- 图片成本优先路由配置；默认关闭，不改变现有生图调度。
ALTER TABLE groups
    ADD COLUMN IF NOT EXISTS image_cost_routing_enabled BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN IF NOT EXISTS image_cost_routing_mode VARCHAR(20) NOT NULL DEFAULT 'prefer_lowest',
    ADD COLUMN IF NOT EXISTS image_cost_tolerance_percent DECIMAL(5,2) NOT NULL DEFAULT 5,
    ADD COLUMN IF NOT EXISTS image_cost_stale_after_seconds INTEGER NOT NULL DEFAULT 86400;

UPDATE groups
SET image_cost_routing_mode = 'prefer_lowest'
WHERE image_cost_routing_mode IS NULL OR image_cost_routing_mode = '';

UPDATE groups
SET image_cost_tolerance_percent = 5
WHERE image_cost_tolerance_percent IS NULL OR image_cost_tolerance_percent < 0 OR image_cost_tolerance_percent > 100;

UPDATE groups
SET image_cost_stale_after_seconds = 86400
WHERE image_cost_stale_after_seconds IS NULL OR image_cost_stale_after_seconds < 300 OR image_cost_stale_after_seconds > 604800;

COMMENT ON COLUMN groups.image_cost_routing_enabled IS '是否按上游图片成本优先路由';
COMMENT ON COLUMN groups.image_cost_routing_mode IS '图片成本路由模式：prefer_lowest/strict_lowest';
COMMENT ON COLUMN groups.image_cost_tolerance_percent IS '图片成本同层容差百分比';
COMMENT ON COLUMN groups.image_cost_stale_after_seconds IS '图片成本快照过期阈值（秒）';

-- 图片成本路由字段属于 API Key auth snapshot 的调度输入；保留现有
-- outbox 触发器语义，并把这些列纳入变更检测，避免直写 groups 后缓存过期。
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
