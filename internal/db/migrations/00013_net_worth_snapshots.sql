-- +goose Up
CREATE TABLE net_worth_snapshots (
    space_id     uuid           NOT NULL REFERENCES spaces (id) ON DELETE CASCADE,
    -- The UTC date the snapshot stands for (TD-04); one row per space and day.
    date         date           NOT NULL,
    currency     char(3)        NOT NULL,
    cash         numeric(28,8)  NOT NULL,
    investments  numeric(28,8)  NOT NULL DEFAULT 0,
    fixed_assets numeric(28,8)  NOT NULL DEFAULT 0,
    receivables  numeric(28,8)  NOT NULL DEFAULT 0,
    liabilities  numeric(28,8)  NOT NULL DEFAULT 0,
    net_worth    numeric(28,8)  NOT NULL,
    created_at   timestamptz    NOT NULL DEFAULT now(),
    PRIMARY KEY (space_id, date)
);

-- +goose Down
DROP TABLE net_worth_snapshots;
