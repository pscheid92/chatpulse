CREATE TABLE users (
    id UUID PRIMARY KEY DEFAULT uuidv7(),
    overlay_uuid UUID NOT NULL UNIQUE DEFAULT uuidv7(),
    twitch_user_id TEXT UNIQUE NOT NULL,
    twitch_username TEXT NOT NULL,
    access_token TEXT NOT NULL,
    refresh_token TEXT NOT NULL,
    token_expiry TIMESTAMP NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMP NOT NULL DEFAULT NOW()
);

CREATE TABLE configs (
    user_id UUID PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    for_trigger TEXT NOT NULL DEFAULT 'yes',
    against_trigger TEXT NOT NULL DEFAULT 'no',
    left_label TEXT NOT NULL DEFAULT 'Against',
    right_label TEXT NOT NULL DEFAULT 'For',
    decay_speed FLOAT NOT NULL DEFAULT 0.5,
    version INTEGER NOT NULL DEFAULT 1,
    created_at TIMESTAMP NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMP NOT NULL DEFAULT NOW()
);

CREATE TABLE eventsub_subscriptions (
    user_id UUID PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    broadcaster_user_id TEXT NOT NULL,
    subscription_id TEXT NOT NULL,
    conduit_id TEXT NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT NOW()
);

---- create above / drop below ----

DROP TABLE IF EXISTS eventsub_subscriptions;
DROP TABLE IF EXISTS configs;
DROP TABLE IF EXISTS users;
