-- +goose Up
CREATE TABLE market_instruments (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    provider    text        NOT NULL CHECK (provider IN ('CMC', 'MASSIVE')),
    -- CMC numeric id or a ticker.
    external_id text        NOT NULL,
    symbol      text        NOT NULL,
    name        text        NOT NULL,
    kind        text        NOT NULL CHECK (kind IN ('CRYPTO', 'STOCK', 'ETF')),
    currency    char(3)     NOT NULL,
    rank        integer,
    active      boolean     NOT NULL DEFAULT true,
    updated_at  timestamptz NOT NULL DEFAULT now(),
    UNIQUE (provider, external_id)
);

CREATE INDEX market_instruments_symbol_idx ON market_instruments (kind, upper(symbol));

CREATE TABLE market_prices (
    instrument_id  uuid PRIMARY KEY REFERENCES market_instruments (id) ON DELETE CASCADE,
    price          numeric(38,18) NOT NULL CHECK (price >= 0),
    currency       char(3)        NOT NULL,
    change_24h_pct numeric(12,4),
    as_of          timestamptz    NOT NULL,
    fetched_at     timestamptz    NOT NULL DEFAULT now()
);

CREATE TABLE job_runs (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name            text        NOT NULL,
    started_at      timestamptz NOT NULL,
    finished_at     timestamptz NOT NULL,
    status          text        NOT NULL CHECK (status IN ('SUCCESS', 'FAILED', 'SKIPPED')),
    items_processed integer     NOT NULL DEFAULT 0,
    error           text
);

CREATE INDEX job_runs_name_started_idx ON job_runs (name, started_at DESC);

ALTER TABLE investments
    ADD CONSTRAINT investments_instrument_fk FOREIGN KEY (instrument_id) REFERENCES market_instruments (id);

-- +goose Down
ALTER TABLE investments DROP CONSTRAINT investments_instrument_fk;
DROP TABLE job_runs;
DROP TABLE market_prices;
DROP TABLE market_instruments;
