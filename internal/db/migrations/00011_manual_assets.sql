-- +goose Up
CREATE TABLE manual_assets (
    id                          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    space_id                    uuid           NOT NULL REFERENCES spaces (id) ON DELETE CASCADE,
    kind                        text           NOT NULL CHECK (kind IN ('FIXED_ASSET', 'RECEIVABLE', 'LIABILITY')),
    name                        text           NOT NULL CHECK (char_length(name) BETWEEN 1 AND 60),
    icon                        text           NOT NULL,
    currency                    char(3)        NOT NULL,
    -- For a liability this is the outstanding amount.
    current_value               numeric(28,8)  NOT NULL DEFAULT 0 CHECK (current_value >= 0),
    counterparty_name           text           CHECK (counterparty_name IS NULL OR char_length(counterparty_name) <= 60),
    due_date                    date,
    estimated_annual_change_pct numeric(8,4)   CHECK (estimated_annual_change_pct IS NULL OR estimated_annual_change_pct BETWEEN -100 AND 1000),
    note                        text           CHECK (note IS NULL OR char_length(note) <= 1000),
    created_by_id               uuid           NOT NULL REFERENCES users (id),
    created_at                  timestamptz    NOT NULL DEFAULT now(),
    updated_at                  timestamptz    NOT NULL DEFAULT now(),
    deleted_at                  timestamptz
);

CREATE INDEX manual_assets_space_kind_idx ON manual_assets (space_id, kind) WHERE deleted_at IS NULL;

CREATE TABLE manual_asset_value_changes (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    asset_id       uuid           NOT NULL REFERENCES manual_assets (id) ON DELETE CASCADE,
    previous_value numeric(28,8)  NOT NULL,
    new_value      numeric(28,8)  NOT NULL,
    reason         text           NOT NULL CHECK (reason IN ('MANUAL', 'SETTLEMENT')),
    -- Set to NULL when the settlement transaction is deleted.
    transaction_id uuid           REFERENCES transactions (id) ON DELETE SET NULL,
    -- clock_timestamp(), not now(): changes made in one transaction still sort in order.
    changed_at     timestamptz    NOT NULL DEFAULT clock_timestamp()
);

CREATE INDEX manual_asset_value_changes_asset_idx ON manual_asset_value_changes (asset_id, changed_at DESC);

-- A settlement is a TRANSFER row tied to a manual asset: IN = money received back
-- (receivable, wallet +amount), OUT = a debt payment (liability, wallet −amount).
ALTER TABLE transactions
    ADD COLUMN settlement_direction text CHECK (settlement_direction IN ('IN', 'OUT')),
    ADD CONSTRAINT transactions_manual_asset_fk FOREIGN KEY (manual_asset_id) REFERENCES manual_assets (id),
    ADD CONSTRAINT transactions_settlement_chk CHECK ((manual_asset_id IS NOT NULL) = (settlement_direction IS NOT NULL));

-- +goose Down
ALTER TABLE transactions
    DROP CONSTRAINT transactions_settlement_chk,
    DROP CONSTRAINT transactions_manual_asset_fk,
    DROP COLUMN settlement_direction;
DROP TABLE manual_asset_value_changes;
DROP TABLE manual_assets;
