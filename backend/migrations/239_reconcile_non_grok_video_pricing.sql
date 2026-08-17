-- Migration 232 cleared unsupported video pricing once, but later writes could
-- reintroduce it because the platform contract was not enforced by the DB.
-- Preserve the replayed rows before clearing them and prevent future drift.

CREATE TABLE IF NOT EXISTS groups_video_price_backup_239 (
    group_id BIGINT PRIMARY KEY,
    platform TEXT,
    video_price_480p DECIMAL(20,8),
    video_price_720p DECIMAL(20,8),
    video_price_1080p DECIMAL(20,8),
    video_model_prices JSONB,
    backed_up_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

INSERT INTO groups_video_price_backup_239 (
    group_id,
    platform,
    video_price_480p,
    video_price_720p,
    video_price_1080p,
    video_model_prices
)
SELECT
    id,
    platform,
    video_price_480p,
    video_price_720p,
    video_price_1080p,
    video_model_prices
FROM groups
WHERE platform IS DISTINCT FROM 'grok'
  AND platform IS DISTINCT FROM 'composite'
  AND (
      video_price_480p IS NOT NULL
      OR video_price_720p IS NOT NULL
      OR video_price_1080p IS NOT NULL
      OR video_model_prices IS NOT NULL
  )
ON CONFLICT (group_id) DO NOTHING;

UPDATE groups
SET video_price_480p = NULL,
    video_price_720p = NULL,
    video_price_1080p = NULL,
    video_model_prices = NULL
WHERE platform IS DISTINCT FROM 'grok'
  AND platform IS DISTINCT FROM 'composite'
  AND (
      video_price_480p IS NOT NULL
      OR video_price_720p IS NOT NULL
      OR video_price_1080p IS NOT NULL
      OR video_model_prices IS NOT NULL
  );

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conrelid = 'public.groups'::regclass
          AND conname = 'groups_video_pricing_platform_check'
    ) THEN
        ALTER TABLE groups
            ADD CONSTRAINT groups_video_pricing_platform_check
            CHECK (
                platform IS NOT DISTINCT FROM 'grok'
                OR platform IS NOT DISTINCT FROM 'composite'
                OR (
                    video_price_480p IS NULL
                    AND video_price_720p IS NULL
                    AND video_price_1080p IS NULL
                    AND video_model_prices IS NULL
                )
            ) NOT VALID;
    END IF;
END
$$;

ALTER TABLE groups VALIDATE CONSTRAINT groups_video_pricing_platform_check;

COMMENT ON TABLE groups_video_price_backup_239 IS
    'Migration 239 backup of unsupported non-Grok/non-composite video pricing before reconciliation.';
