-- +goose Up
CREATE TABLE user_preferences (
    user_id                   uuid PRIMARY KEY REFERENCES users (id) ON DELETE CASCADE,
    hide_amounts              boolean     NOT NULL DEFAULT false,
    keyboard_type             text        NOT NULL DEFAULT 'MINI_CALCULATOR' CHECK (keyboard_type IN ('MINI_CALCULATOR', 'STANDARD_NUMERIC')),
    app_lock_enabled          boolean     NOT NULL DEFAULT false,
    app_lock_pin_hash         text,
    app_lock_failed_attempts  integer     NOT NULL DEFAULT 0,
    app_lock_locked_until     timestamptz,
    updated_at                timestamptz NOT NULL DEFAULT now(),
    CHECK (NOT app_lock_enabled OR app_lock_pin_hash IS NOT NULL)
);

-- Accounts created before this migration get default preferences.
INSERT INTO user_preferences (user_id) SELECT id FROM users;

-- +goose Down
DROP TABLE user_preferences;
