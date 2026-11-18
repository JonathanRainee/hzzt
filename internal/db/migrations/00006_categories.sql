-- +goose Up
CREATE TABLE categories (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    space_id      uuid           NOT NULL REFERENCES spaces (id) ON DELETE CASCADE,
    type          text           NOT NULL CHECK (type IN ('EXPENSE', 'INCOME')),
    name          text           NOT NULL CHECK (char_length(name) BETWEEN 1 AND 30),
    icon          text           NOT NULL,
    color         text           NOT NULL CHECK (color ~ '^#[0-9A-Fa-f]{6}$'),
    -- Monthly budget in the space's base currency; expense categories only.
    budget_amount numeric(28,8)  CHECK (budget_amount IS NULL OR budget_amount > 0),
    sort_order    integer        NOT NULL DEFAULT 0,
    created_by_id uuid           NOT NULL REFERENCES users (id),
    created_at    timestamptz    NOT NULL DEFAULT now(),
    updated_at    timestamptz    NOT NULL DEFAULT now(),
    deleted_at    timestamptz,
    CHECK (budget_amount IS NULL OR type = 'EXPENSE')
);

-- Names are unique per type, case-insensitively, among live categories.
CREATE UNIQUE INDEX categories_space_type_name_live_key
    ON categories (space_id, type, lower(name)) WHERE deleted_at IS NULL;
CREATE INDEX categories_space_type_order_idx ON categories (space_id, type, sort_order) WHERE deleted_at IS NULL;

-- +goose Down
DROP TABLE categories;
