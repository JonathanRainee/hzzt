-- +goose Up
CREATE TABLE investments (
    id                           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    space_id                     uuid           NOT NULL REFERENCES spaces (id) ON DELETE CASCADE,
    asset_type                   text           NOT NULL CHECK (asset_type IN ('CRYPTO', 'ID_STOCK', 'US_STOCK', 'MUTUAL_FUND', 'GLOBAL_ETF')),
    price_mode                   text           NOT NULL DEFAULT 'MANUAL' CHECK (price_mode IN ('AUTO', 'MANUAL')),
    name                         text           NOT NULL CHECK (char_length(name) BETWEEN 1 AND 60),
    -- Ticker or coin symbol; null for mutual funds.
    symbol                       text           CHECK (symbol IS NULL OR char_length(symbol) BETWEEN 1 AND 20),
    icon                         text           NOT NULL,
    quote_currency               char(3)        NOT NULL,
    quantity                     numeric(38,18) NOT NULL DEFAULT 0 CHECK (quantity >= 0),
    avg_buy_price                numeric(28,8)  NOT NULL DEFAULT 0 CHECK (avg_buy_price >= 0),
    -- "Current price" / "Current NAV".
    manual_price                 numeric(28,8)  NOT NULL DEFAULT 0 CHECK (manual_price >= 0),
    fees                         numeric(28,8)  NOT NULL DEFAULT 0 CHECK (fees >= 0),
    manual_current_value         numeric(28,8)  CHECK (manual_current_value IS NULL OR manual_current_value >= 0),
    purchase_date                date,
    estimated_annual_change_pct  numeric(8,4)   NOT NULL DEFAULT 5.5 CHECK (estimated_annual_change_pct BETWEEN -100 AND 1000),
    -- Units of the space base currency per 1 quote currency (the "USD/IDR rate" field).
    manual_fx_rate               numeric(28,12) CHECK (manual_fx_rate IS NULL OR manual_fx_rate > 0),
    details                      jsonb          NOT NULL DEFAULT '{}',
    -- Linked to market_instruments in P11.
    instrument_id                uuid,
    is_shared                    boolean        NOT NULL DEFAULT false,
    created_by_id                uuid           NOT NULL REFERENCES users (id),
    created_at                   timestamptz    NOT NULL DEFAULT now(),
    updated_at                   timestamptz    NOT NULL DEFAULT now(),
    deleted_at                   timestamptz
);

CREATE INDEX investments_space_type_idx ON investments (space_id, asset_type) WHERE deleted_at IS NULL;

-- P7 left investment_id without a foreign key until this table existed.
ALTER TABLE transactions
    ADD CONSTRAINT transactions_investment_fk FOREIGN KEY (investment_id) REFERENCES investments (id);

-- +goose Down
ALTER TABLE transactions DROP CONSTRAINT transactions_investment_fk;
DROP TABLE investments;
