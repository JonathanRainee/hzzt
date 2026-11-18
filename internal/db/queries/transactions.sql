-- name: CreateTransaction :one
INSERT INTO transactions (
    space_id, type, direction, title, note, amount, currency, wallet_id, category_id,
    occurred_at, created_by_id,
    to_wallet_id, to_amount, to_currency, fx_rate
) VALUES (
    $1, $2, sqlc.narg('direction'), sqlc.narg('title'), sqlc.narg('note'), $3, $4,
    sqlc.narg('wallet_id'), sqlc.narg('category_id'),
    $5, $6,
    sqlc.narg('to_wallet_id'), sqlc.narg('to_amount'), sqlc.narg('to_currency'), sqlc.narg('fx_rate')
)
RETURNING *;
