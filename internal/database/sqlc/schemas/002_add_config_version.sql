-- Add version field to track config changes
ALTER TABLE configs ADD COLUMN version INTEGER NOT NULL DEFAULT 1;

-- Trigger to auto-increment version on updates
CREATE OR REPLACE FUNCTION increment_config_version()
RETURNS TRIGGER AS $$
BEGIN
    NEW.version = OLD.version + 1;
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER configs_version_trigger
    BEFORE UPDATE ON configs
    FOR EACH ROW
    EXECUTE FUNCTION increment_config_version();

---- create above / drop below ----

DROP TRIGGER IF EXISTS configs_version_trigger ON configs;
DROP FUNCTION IF EXISTS increment_config_version();
ALTER TABLE configs DROP COLUMN IF EXISTS version;
