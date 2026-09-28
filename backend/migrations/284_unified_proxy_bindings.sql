-- Unify account proxy bindings into one positive, non-reusable namespace.
-- The coordinated migration runner freezes application writes during this step.
LOCK TABLE proxies, proxy_ip_groups, accounts IN ACCESS EXCLUSIVE MODE;
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM proxies GROUP BY id HAVING COUNT(*) > 1) THEN
        RAISE EXCEPTION 'duplicate real proxy IDs';
    END IF;
    IF NOT EXISTS (SELECT 1 FROM information_schema.tables
                   WHERE table_schema = current_schema() AND table_name = 'proxy_bindings')
       AND EXISTS (SELECT 1 FROM accounts a LEFT JOIN proxies p ON p.id = a.proxy_id
                   WHERE a.proxy_id IS NOT NULL AND p.id IS NULL) THEN
        RAISE EXCEPTION 'orphaned account proxy ID';
    END IF;
    IF EXISTS (SELECT 1 FROM information_schema.columns
               WHERE table_schema = current_schema() AND table_name = 'accounts' AND column_name = 'proxy_ip_group_id') THEN
        IF EXISTS (SELECT 1 FROM accounts a LEFT JOIN proxy_ip_groups g ON g.id = a.proxy_ip_group_id
                   WHERE a.proxy_ip_group_id IS NOT NULL AND (g.id IS NULL OR a.proxy_id IS NOT NULL)) THEN
            RAISE EXCEPTION 'ambiguous or orphaned account proxy group';
        END IF;
    END IF;
END $$;
CREATE TABLE IF NOT EXISTS proxy_bindings (
    id BIGINT PRIMARY KEY,
    binding_type VARCHAR(20) NOT NULL,
    proxy_id BIGINT,
    proxy_ip_group_id BIGINT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT proxy_bindings_positive_ids_check CHECK (
        id > 0 AND (proxy_id IS NULL OR proxy_id > 0)
        AND (proxy_ip_group_id IS NULL OR proxy_ip_group_id > 0)
    ),
    CONSTRAINT proxy_bindings_type_check CHECK (binding_type IN ('proxy', 'proxy_ip_group')),
    CONSTRAINT proxy_bindings_shape_check CHECK (
        (binding_type = 'proxy' AND proxy_id IS NOT NULL AND proxy_ip_group_id IS NULL)
        OR (binding_type = 'proxy_ip_group' AND proxy_id IS NULL AND proxy_ip_group_id IS NOT NULL)
    )
);
-- Tombstones retain their target IDs after source deletion; foreign keys here
-- would prevent the existing proxy/group hard-delete paths from working.
ALTER TABLE proxy_bindings DROP CONSTRAINT IF EXISTS proxy_bindings_proxy_fk;
ALTER TABLE proxy_bindings DROP CONSTRAINT IF EXISTS proxy_bindings_group_fk;
CREATE UNIQUE INDEX IF NOT EXISTS idx_proxy_bindings_proxy_id ON proxy_bindings(proxy_id) WHERE proxy_id IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS idx_proxy_bindings_group_id ON proxy_bindings(proxy_ip_group_id) WHERE proxy_ip_group_id IS NOT NULL;

INSERT INTO proxy_bindings (id, binding_type, proxy_id)
SELECT p.id, 'proxy', p.id FROM proxies p
WHERE NOT EXISTS (SELECT 1 FROM proxy_bindings b WHERE b.id = p.id);
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM proxies p LEFT JOIN proxy_bindings b ON b.id = p.id
               WHERE b.id IS NULL OR b.binding_type <> 'proxy' OR b.proxy_id IS DISTINCT FROM p.id) THEN
        RAISE EXCEPTION 'real proxy binding backfill incomplete or conflicting';
    END IF;
END $$;

ALTER TABLE proxy_ip_groups ADD COLUMN IF NOT EXISTS binding_id BIGINT;

DO $$
DECLARE
    group_row RECORD;
    new_binding_id BIGINT;
    max_id BIGINT;
BEGIN
    SELECT GREATEST(COALESCE((SELECT MAX(id) FROM proxies), 0),
                    COALESCE((SELECT MAX(id) FROM proxy_bindings), 0),
                    (SELECT last_value FROM proxies_id_seq)) INTO max_id;
    PERFORM setval('proxies_id_seq', GREATEST(max_id, 1), max_id > 0);
    FOR group_row IN SELECT id FROM proxy_ip_groups WHERE binding_id IS NULL ORDER BY id LOOP
        new_binding_id := nextval('proxies_id_seq');
        INSERT INTO proxy_bindings (id, binding_type, proxy_ip_group_id)
        VALUES (new_binding_id, 'proxy_ip_group', group_row.id);
        UPDATE proxy_ip_groups SET binding_id = new_binding_id WHERE id = group_row.id;
    END LOOP;
END $$;

ALTER TABLE proxy_ip_groups DROP CONSTRAINT IF EXISTS proxy_ip_groups_binding_id_fkey;
ALTER TABLE proxy_ip_groups ADD CONSTRAINT proxy_ip_groups_binding_id_fkey
    FOREIGN KEY (binding_id) REFERENCES proxy_bindings(id) ON DELETE RESTRICT;
CREATE UNIQUE INDEX IF NOT EXISTS idx_proxy_ip_groups_binding_id ON proxy_ip_groups(binding_id) WHERE binding_id IS NOT NULL;

-- Remove the legacy proxy FK and exclusivity check before moving group accounts.
-- Both are replaced or retired within this same migration transaction.
ALTER TABLE accounts DROP CONSTRAINT IF EXISTS accounts_proxy_id_fkey;
ALTER TABLE accounts DROP CONSTRAINT IF EXISTS accounts_proxy_source_exclusive;

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM information_schema.columns
               WHERE table_schema = current_schema() AND table_name = 'accounts' AND column_name = 'proxy_ip_group_id') THEN
        EXECUTE 'UPDATE accounts a SET proxy_id = b.id FROM proxy_bindings b '
             || 'WHERE a.proxy_ip_group_id IS NOT NULL AND b.proxy_ip_group_id = a.proxy_ip_group_id';
    END IF;
END $$;
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM accounts a LEFT JOIN proxy_bindings b ON b.id = a.proxy_id
               WHERE a.proxy_id IS NOT NULL AND b.id IS NULL) THEN
        RAISE EXCEPTION 'account proxy binding backfill incomplete';
    END IF;
    IF EXISTS (SELECT 1 FROM proxy_ip_groups g LEFT JOIN proxy_bindings b ON b.id = g.binding_id
               WHERE b.id IS NULL OR b.proxy_ip_group_id IS DISTINCT FROM g.id) THEN
        RAISE EXCEPTION 'proxy group binding backfill incomplete';
    END IF;
END $$;

ALTER TABLE accounts ADD CONSTRAINT accounts_proxy_id_fkey
    FOREIGN KEY (proxy_id) REFERENCES proxy_bindings(id) ON DELETE SET NULL;
ALTER TABLE accounts DROP CONSTRAINT IF EXISTS accounts_proxy_ip_group_id_fkey;
DROP INDEX IF EXISTS idx_accounts_proxy_ip_group_id;
ALTER TABLE accounts DROP COLUMN IF EXISTS proxy_ip_group_id;
ALTER TABLE proxy_bindings ALTER COLUMN id SET DEFAULT nextval('proxies_id_seq');

CREATE OR REPLACE FUNCTION register_real_proxy_binding() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO proxy_bindings(id, binding_type, proxy_id) VALUES(NEW.id, 'proxy', NEW.id);
    RETURN NEW;
END $$;
DROP TRIGGER IF EXISTS trg_register_real_proxy_binding ON proxies;
CREATE TRIGGER trg_register_real_proxy_binding AFTER INSERT ON proxies
    FOR EACH ROW EXECUTE FUNCTION register_real_proxy_binding();

CREATE OR REPLACE FUNCTION register_proxy_group_binding() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE new_binding_id BIGINT;
BEGIN
    new_binding_id := nextval('proxies_id_seq');
    INSERT INTO proxy_bindings(id, binding_type, proxy_ip_group_id)
        VALUES(new_binding_id, 'proxy_ip_group', NEW.id);
    UPDATE proxy_ip_groups SET binding_id = new_binding_id WHERE id = NEW.id;
    RETURN NEW;
END $$;
DROP TRIGGER IF EXISTS trg_register_proxy_group_binding ON proxy_ip_groups;
CREATE TRIGGER trg_register_proxy_group_binding AFTER INSERT ON proxy_ip_groups
    FOR EACH ROW EXECUTE FUNCTION register_proxy_group_binding();

SELECT setval('proxies_id_seq', GREATEST(COALESCE((SELECT MAX(id) FROM proxy_bindings), 0),
    (SELECT last_value FROM proxies_id_seq), 1), true);
