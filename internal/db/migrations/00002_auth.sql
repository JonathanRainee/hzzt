-- +goose Up
CREATE TABLE users (
    id                    uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    email                 citext      NOT NULL,
    password_hash         text        NOT NULL,
    name                  text        NOT NULL CHECK (char_length(name) BETWEEN 1 AND 60),
    timezone              text        NOT NULL DEFAULT 'UTC',
    -- FK to spaces is added in P3.
    last_active_space_id  uuid,
    created_at            timestamptz NOT NULL DEFAULT now(),
    updated_at            timestamptz NOT NULL DEFAULT now(),
    deleted_at            timestamptz
);

-- Unique among live accounts only, so a soft-deleted user's email can be reused.
CREATE UNIQUE INDEX users_email_live_key ON users (email) WHERE deleted_at IS NULL;

CREATE TABLE refresh_tokens (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id        uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    family_id      uuid        NOT NULL,
    token_hash     text        NOT NULL UNIQUE,
    expires_at     timestamptz NOT NULL,
    revoked_at     timestamptz,
    replaced_by_id uuid REFERENCES refresh_tokens (id),
    user_agent     text,
    ip             text,
    created_at     timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX refresh_tokens_user_id_idx ON refresh_tokens (user_id);
CREATE INDEX refresh_tokens_family_id_idx ON refresh_tokens (family_id);

CREATE TABLE password_reset_tokens (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id     uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    token_hash  text        NOT NULL UNIQUE,
    expires_at  timestamptz NOT NULL,
    used_at     timestamptz,
    created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX password_reset_tokens_user_id_idx ON password_reset_tokens (user_id);

-- +goose Down
DROP TABLE password_reset_tokens;
DROP TABLE refresh_tokens;
DROP TABLE users;
