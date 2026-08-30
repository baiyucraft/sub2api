-- Existing users should receive low-balance alerts without first configuring a
-- separate notification address. Users may still opt out again after rollout.
WITH enabled_users AS (
    UPDATE users
    SET balance_notify_enabled = TRUE,
        updated_at = NOW()
    WHERE deleted_at IS NULL
      AND balance_notify_enabled = FALSE
    RETURNING id
)
INSERT INTO auth_cache_invalidation_outbox (cache_key)
SELECT DISTINCT encode(sha256(convert_to(k.key, 'UTF8')), 'hex')
FROM api_keys AS k
JOIN enabled_users AS u ON u.id = k.user_id
WHERE k.deleted_at IS NULL
  AND k.key <> '';

-- Keep future registration-email and balance-notification changes durable even
-- when a write path does not have an in-process cache invalidator available.
CREATE OR REPLACE FUNCTION enqueue_user_auth_cache_invalidation()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
DECLARE
    target_user_id BIGINT;
BEGIN
    target_user_id := OLD.id;
    IF TG_OP = 'UPDATE'
       AND OLD.status IS NOT DISTINCT FROM NEW.status
       AND OLD.role IS NOT DISTINCT FROM NEW.role
       AND OLD.email IS NOT DISTINCT FROM NEW.email
       AND OLD.balance_notify_enabled IS NOT DISTINCT FROM NEW.balance_notify_enabled
       AND OLD.balance_notify_threshold_type IS NOT DISTINCT FROM NEW.balance_notify_threshold_type
       AND OLD.balance_notify_threshold IS NOT DISTINCT FROM NEW.balance_notify_threshold
       AND OLD.balance_notify_extra_emails IS NOT DISTINCT FROM NEW.balance_notify_extra_emails
       AND OLD.deleted_at IS NOT DISTINCT FROM NEW.deleted_at THEN
        RETURN NEW;
    END IF;

    INSERT INTO auth_cache_invalidation_outbox (cache_key)
    SELECT encode(sha256(convert_to(k.key, 'UTF8')), 'hex')
    FROM api_keys AS k
    WHERE k.user_id = target_user_id
      AND k.deleted_at IS NULL
      AND k.key <> '';
    IF TG_OP = 'DELETE' THEN
        RETURN OLD;
    END IF;
    RETURN NEW;
END;
$$;
