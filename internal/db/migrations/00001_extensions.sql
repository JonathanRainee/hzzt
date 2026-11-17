-- +goose Up
CREATE EXTENSION IF NOT EXISTS citext;
CREATE EXTENSION IF NOT EXISTS pgcrypto;

-- +goose Down
-- Extensions are intentionally kept: other objects may depend on them.
SELECT 1;
