CREATE TABLE streamers
(
    id              UUID PRIMARY KEY            DEFAULT uuidv7(),
    created_at      TIMESTAMP   NOT NULL        DEFAULT NOW(),
    updated_at      TIMESTAMP   NOT NULL        DEFAULT NOW(),

    overlay_uuid    UUID        NOT NULL UNIQUE DEFAULT uuidv7(),

    twitch_user_id  TEXT UNIQUE NOT NULL,
    twitch_username TEXT        NOT NULL,

    access_token    TEXT        NOT NULL,
    refresh_token   TEXT        NOT NULL,
    token_expiry    TIMESTAMP   NOT NULL
);

CREATE TABLE configs
(
    streamer_id     UUID PRIMARY KEY REFERENCES streamers (id) ON DELETE CASCADE,

    version         INTEGER          NOT NULL DEFAULT 1,
    created_at      TIMESTAMP        NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMP        NOT NULL DEFAULT NOW(),

    for_trigger     TEXT             NOT NULL DEFAULT 'yes',
    for_label       TEXT             NOT NULL DEFAULT 'For',
    against_trigger TEXT             NOT NULL DEFAULT 'no',
    against_label   TEXT             NOT NULL DEFAULT 'Against',

    memory_seconds  INTEGER          NOT NULL DEFAULT 30,

    display_mode    TEXT             NOT NULL DEFAULT 'combined'
);

CREATE TABLE eventsub_subscriptions
(
    streamer_id     UUID PRIMARY KEY REFERENCES streamers (id) ON DELETE CASCADE,
    subscription_id TEXT      NOT NULL,
    conduit_id      TEXT      NOT NULL,
    created_at      TIMESTAMP NOT NULL DEFAULT NOW()
);

---- create above / drop below ----

DROP TABLE IF EXISTS eventsub_subscriptions;
DROP TABLE IF EXISTS configs;
DROP TABLE IF EXISTS streamers;
