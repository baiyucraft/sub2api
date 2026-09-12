-- Migration: 271_channel_monitor_zhipu_native_api_mode
-- 为智谱监控和请求模板恢复显式的智谱原生协议选项。

ALTER TABLE channel_monitors
    DROP CONSTRAINT IF EXISTS channel_monitors_api_mode_check;

-- Existing Zhipu chat_completions rows used the native endpoint before this mode
-- became explicit. Backfill them to preserve the historical probe behavior.
UPDATE channel_monitors
SET api_mode = 'zhipu_native'
WHERE provider = 'zhipu'
  AND api_mode = 'chat_completions';

ALTER TABLE channel_monitors
    ADD CONSTRAINT channel_monitors_api_mode_check
    CHECK (api_mode IN ('chat_completions', 'responses', 'zhipu_native'));

ALTER TABLE channel_monitor_request_templates
    DROP CONSTRAINT IF EXISTS channel_monitor_request_templates_api_mode_check;

UPDATE channel_monitor_request_templates
SET api_mode = 'zhipu_native'
WHERE provider = 'zhipu'
  AND api_mode = 'chat_completions';

ALTER TABLE channel_monitor_request_templates
    ADD CONSTRAINT channel_monitor_request_templates_api_mode_check
    CHECK (api_mode IN ('chat_completions', 'responses', 'zhipu_native'));
