-- +goose Up
CREATE TABLE fx_rates (
    base       char(3)        NOT NULL DEFAULT 'USD' CHECK (base = 'USD'),
    quote      char(3)        NOT NULL,
    rate       numeric(28,12) NOT NULL CHECK (rate > 0),
    as_of      date           NOT NULL,
    source     text           NOT NULL,
    fetched_at timestamptz    NOT NULL DEFAULT now(),
    PRIMARY KEY (base, quote, as_of)
);

CREATE TABLE manual_fx_rates (
    space_id      uuid           NOT NULL REFERENCES spaces (id) ON DELETE CASCADE,
    base          char(3)        NOT NULL,
    quote         char(3)        NOT NULL,
    rate          numeric(28,12) NOT NULL CHECK (rate > 0),
    updated_by_id uuid           NOT NULL REFERENCES users (id),
    updated_at    timestamptz    NOT NULL DEFAULT now(),
    PRIMARY KEY (space_id, base, quote),
    CHECK (base <> quote)
);

-- +goose Down
DROP TABLE manual_fx_rates;
DROP TABLE fx_rates;
