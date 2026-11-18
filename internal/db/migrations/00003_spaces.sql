-- +goose Up
CREATE TABLE spaces (
    id                      uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name                    text        NOT NULL CHECK (char_length(name) BETWEEN 1 AND 40),
    avatar_emoji            text,
    avatar_color            text        NOT NULL DEFAULT '#4F46E5' CHECK (avatar_color ~ '^#[0-9A-Fa-f]{6}$'),
    base_currency           char(3)     NOT NULL DEFAULT 'USD',
    auto_fx_conversion      boolean     NOT NULL DEFAULT true,
    tracking_mode           text        NOT NULL DEFAULT 'FULL_NET_WORTH' CHECK (tracking_mode IN ('FULL_NET_WORTH', 'CASH_ONLY')),
    budget_period_start_day smallint    NOT NULL DEFAULT 1 CHECK (budget_period_start_day BETWEEN 1 AND 31),
    created_by_id           uuid        NOT NULL REFERENCES users (id),
    created_at              timestamptz NOT NULL DEFAULT now(),
    updated_at              timestamptz NOT NULL DEFAULT now(),
    deleted_at              timestamptz
);

CREATE TABLE space_members (
    space_id  uuid        NOT NULL REFERENCES spaces (id) ON DELETE CASCADE,
    user_id   uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    role      text        NOT NULL CHECK (role IN ('OWNER', 'MEMBER')),
    joined_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (space_id, user_id)
);

CREATE INDEX space_members_user_id_idx ON space_members (user_id);

CREATE TABLE space_invitations (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    space_id      uuid        NOT NULL REFERENCES spaces (id) ON DELETE CASCADE,
    code          text        NOT NULL UNIQUE CHECK (char_length(code) = 8),
    created_by_id uuid        NOT NULL REFERENCES users (id),
    expires_at    timestamptz NOT NULL,
    revoked_at    timestamptz,
    created_at    timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX space_invitations_space_id_idx ON space_invitations (space_id);

ALTER TABLE users
    ADD CONSTRAINT users_last_active_space_fk
    FOREIGN KEY (last_active_space_id) REFERENCES spaces (id) ON DELETE SET NULL;

-- +goose Down
ALTER TABLE users DROP CONSTRAINT users_last_active_space_fk;
DROP TABLE space_invitations;
DROP TABLE space_members;
DROP TABLE spaces;
