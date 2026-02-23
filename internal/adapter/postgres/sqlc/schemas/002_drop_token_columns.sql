ALTER TABLE streamers
    DROP COLUMN access_token,
    DROP COLUMN refresh_token,
    DROP COLUMN token_expiry;

---- create above / drop below ----

ALTER TABLE streamers
    ADD COLUMN access_token  TEXT      NOT NULL DEFAULT '',
    ADD COLUMN refresh_token TEXT      NOT NULL DEFAULT '',
    ADD COLUMN token_expiry  TIMESTAMP NOT NULL DEFAULT NOW();
