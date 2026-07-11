ALTER TABLE upstream_configs
    RENAME COLUMN base_url TO site_url;

ALTER TABLE upstream_configs
    ADD COLUMN api_url VARCHAR(512) NULL;
