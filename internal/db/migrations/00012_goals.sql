-- +goose Up
CREATE TABLE goals (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    space_id      uuid           NOT NULL REFERENCES spaces (id) ON DELETE CASCADE,
    name          text           NOT NULL CHECK (char_length(name) BETWEEN 1 AND 40),
    icon          text           NOT NULL,
    color         text           NOT NULL CHECK (color ~ '^#[0-9A-Fa-f]{6}$'),
    target_amount numeric(28,8)  NOT NULL CHECK (target_amount > 0),
    currency      char(3)        NOT NULL,
    target_date   date,
    achieved_at   timestamptz,
    created_by_id uuid           NOT NULL REFERENCES users (id),
    created_at    timestamptz    NOT NULL DEFAULT now(),
    updated_at    timestamptz    NOT NULL DEFAULT now(),
    deleted_at    timestamptz
);

CREATE INDEX goals_space_idx ON goals (space_id) WHERE deleted_at IS NULL;

-- Earmarked ("reserved") parts of wallet balances; no money moves.
CREATE TABLE goal_allocations (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    goal_id    uuid           NOT NULL REFERENCES goals (id) ON DELETE CASCADE,
    wallet_id  uuid           NOT NULL REFERENCES wallets (id),
    -- In the wallet's currency.
    amount     numeric(28,8)  NOT NULL CHECK (amount > 0),
    updated_at timestamptz    NOT NULL DEFAULT now(),
    UNIQUE (goal_id, wallet_id)
);

CREATE INDEX goal_allocations_wallet_idx ON goal_allocations (wallet_id);

-- +goose Down
DROP TABLE goal_allocations;
DROP TABLE goals;
