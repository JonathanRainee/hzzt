-- +goose Up
CREATE TABLE wallets (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    space_id      uuid           NOT NULL REFERENCES spaces (id) ON DELETE CASCADE,
    type          text           NOT NULL CHECK (type IN ('BANK', 'E_WALLET', 'CASH', 'OTHER')),
    name          text           NOT NULL CHECK (char_length(name) BETWEEN 1 AND 30),
    icon          text           NOT NULL,
    currency      char(3)        NOT NULL,
    -- Cached; the ledger package is the only writer (plan 5.8).
    balance       numeric(28,8)  NOT NULL DEFAULT 0,
    is_favorite   boolean        NOT NULL DEFAULT false,
    sort_order    integer        NOT NULL DEFAULT 0,
    card_style    jsonb          NOT NULL,
    created_by_id uuid           NOT NULL REFERENCES users (id),
    created_at    timestamptz    NOT NULL DEFAULT now(),
    updated_at    timestamptz    NOT NULL DEFAULT now(),
    deleted_at    timestamptz
);

CREATE INDEX wallets_space_order_idx ON wallets (space_id, is_favorite DESC, sort_order) WHERE deleted_at IS NULL;

-- Shared by every later money phase; columns used only by P9/P13/P15 are included now to avoid churn.
CREATE TABLE transactions (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    space_id          uuid           NOT NULL REFERENCES spaces (id) ON DELETE CASCADE,
    type              text           NOT NULL CHECK (type IN ('INCOME', 'EXPENSE', 'TRANSFER', 'ADJUSTMENT', 'OPENING_BALANCE')),
    -- Only for ADJUSTMENT and OPENING_BALANCE.
    direction         text           CHECK (direction IN ('IN', 'OUT')),
    title             text,
    note              text,
    amount            numeric(28,8)  NOT NULL CHECK (amount > 0),
    currency          char(3)        NOT NULL,
    wallet_id         uuid           REFERENCES wallets (id),
    category_id       uuid           REFERENCES categories (id),
    occurred_at       timestamptz    NOT NULL,
    created_by_id     uuid           NOT NULL REFERENCES users (id),
    created_at        timestamptz    NOT NULL DEFAULT now(),
    updated_at        timestamptz    NOT NULL DEFAULT now(),

    -- Transfers (P9)
    to_wallet_id      uuid           REFERENCES wallets (id),
    to_amount         numeric(28,8)  CHECK (to_amount IS NULL OR to_amount > 0),
    to_currency       char(3),
    fx_rate           numeric(28,12),

    -- Investments (P13); investment_id gets its foreign key when the table exists (P10).
    investment_id     uuid,
    investment_action text           CHECK (investment_action IN ('BUY', 'SELL')),
    quantity          numeric(38,18),
    unit_price        numeric(28,8),
    fee               numeric(28,8),
    realized_pl       numeric(28,8),
    position_before   jsonb,
    position_after    jsonb,

    -- Manual assets (P15)
    manual_asset_id   uuid,

    CHECK ((type IN ('ADJUSTMENT', 'OPENING_BALANCE')) = (direction IS NOT NULL))
);

CREATE INDEX transactions_space_occurred_idx ON transactions (space_id, occurred_at DESC, id DESC);
CREATE INDEX transactions_wallet_occurred_idx ON transactions (wallet_id, occurred_at);
CREATE INDEX transactions_to_wallet_idx ON transactions (to_wallet_id) WHERE to_wallet_id IS NOT NULL;
CREATE INDEX transactions_category_idx ON transactions (category_id) WHERE category_id IS NOT NULL;
CREATE INDEX transactions_investment_idx ON transactions (investment_id) WHERE investment_id IS NOT NULL;

-- +goose Down
DROP TABLE transactions;
DROP TABLE wallets;
