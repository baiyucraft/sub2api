ALTER TABLE batch_image_jobs
    ADD COLUMN IF NOT EXISTS upstream_config_id BIGINT,
    ADD COLUMN IF NOT EXISTS upstream_key_id BIGINT,
    ADD COLUMN IF NOT EXISTS upstream_cost_currency VARCHAR(8),
    ADD COLUMN IF NOT EXISTS upstream_cost_to_cny_rate DECIMAL(20,10);

COMMENT ON COLUMN batch_image_jobs.upstream_config_id IS '任务创建时快照的上游配置 ID；NULL 表示 legacy 或未完整绑定';
COMMENT ON COLUMN batch_image_jobs.upstream_key_id IS '任务创建时快照的上游密钥 ID；NULL 表示 legacy 或未完整绑定';
COMMENT ON COLUMN batch_image_jobs.upstream_cost_currency IS '任务创建时快照的上游成本币种；完整上游绑定固定为 CNY';
COMMENT ON COLUMN batch_image_jobs.upstream_cost_to_cny_rate IS '任务创建时快照的上游成本兑 CNY 汇率；完整上游绑定固定为 1';
