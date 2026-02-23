ALTER TABLE configs ADD COLUMN theme TEXT NOT NULL DEFAULT 'dark';

---- create above / drop below ----

ALTER TABLE configs DROP COLUMN theme;
