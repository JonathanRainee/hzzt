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

-- name: GetTransactionDetail :one
SELECT t.*,
       w.name  AS wallet_name,  w.icon  AS wallet_icon,
       tw.name AS to_wallet_name, tw.icon AS to_wallet_icon,
       c.name  AS category_name, c.icon AS category_icon, c.color AS category_color
FROM transactions t
LEFT JOIN wallets w     ON w.id = t.wallet_id
LEFT JOIN wallets tw    ON tw.id = t.to_wallet_id
LEFT JOIN categories c  ON c.id = t.category_id
WHERE t.id = $1 AND t.space_id = $2;

-- name: GetTransactionForUpdate :one
SELECT * FROM transactions WHERE id = $1 AND space_id = $2 FOR UPDATE;

-- name: UpdateIncomeExpense :one
UPDATE transactions SET
    type = $3, wallet_id = $4, category_id = sqlc.narg('category_id'), amount = $5, currency = $6,
    title = sqlc.narg('title'), note = sqlc.narg('note'), occurred_at = $7, updated_at = now()
WHERE id = $1 AND space_id = $2
RETURNING *;

-- name: UpdateTransactionNote :one
UPDATE transactions SET note = sqlc.narg('note'), updated_at = now()
WHERE id = $1 AND space_id = $2
RETURNING *;

-- name: DeleteTransaction :execrows
DELETE FROM transactions WHERE id = $1 AND space_id = $2;

-- name: ListTransactions :many
SELECT t.*,
       w.name  AS wallet_name,  w.icon  AS wallet_icon,
       tw.name AS to_wallet_name, tw.icon AS to_wallet_icon,
       c.name  AS category_name, c.icon AS category_icon, c.color AS category_color
FROM transactions t
LEFT JOIN wallets w     ON w.id = t.wallet_id
LEFT JOIN wallets tw    ON tw.id = t.to_wallet_id
LEFT JOIN categories c  ON c.id = t.category_id
WHERE t.space_id = $1
  AND (sqlc.narg('from_ts')::timestamptz IS NULL OR t.occurred_at >= sqlc.narg('from_ts'))
  AND (sqlc.narg('to_ts')::timestamptz IS NULL OR t.occurred_at < sqlc.narg('to_ts'))
  AND (sqlc.narg('types')::text[] IS NULL OR t.type = ANY(sqlc.narg('types')::text[]))
  AND (sqlc.narg('category_ids')::uuid[] IS NULL OR t.category_id = ANY(sqlc.narg('category_ids')::uuid[]))
  AND (sqlc.narg('wallet_ids')::uuid[] IS NULL
       OR t.wallet_id = ANY(sqlc.narg('wallet_ids')::uuid[])
       OR t.to_wallet_id = ANY(sqlc.narg('wallet_ids')::uuid[]))
  AND (sqlc.narg('q')::text IS NULL
       OR t.title ILIKE sqlc.narg('q') OR t.note ILIKE sqlc.narg('q')
       OR c.name ILIKE sqlc.narg('q') OR w.name ILIKE sqlc.narg('q') OR tw.name ILIKE sqlc.narg('q'))
  AND (sqlc.narg('cursor_at')::timestamptz IS NULL
       OR (t.occurred_at, t.id) < (sqlc.narg('cursor_at')::timestamptz, sqlc.narg('cursor_id')::uuid))
ORDER BY t.occurred_at DESC, t.id DESC
LIMIT sqlc.arg('row_limit')::int;

-- name: DailySummaryRows :many
SELECT (t.occurred_at AT TIME ZONE sqlc.arg('tz')::text)::date AS day,
       t.type, t.currency,
       sum(t.amount)::numeric(38,8) AS total,
       count(*)::int AS cnt
FROM transactions t
LEFT JOIN wallets w     ON w.id = t.wallet_id
LEFT JOIN wallets tw    ON tw.id = t.to_wallet_id
LEFT JOIN categories c  ON c.id = t.category_id
WHERE t.space_id = $1
  AND (sqlc.narg('from_ts')::timestamptz IS NULL OR t.occurred_at >= sqlc.narg('from_ts'))
  AND (sqlc.narg('to_ts')::timestamptz IS NULL OR t.occurred_at < sqlc.narg('to_ts'))
  AND (sqlc.narg('types')::text[] IS NULL OR t.type = ANY(sqlc.narg('types')::text[]))
  AND (sqlc.narg('category_ids')::uuid[] IS NULL OR t.category_id = ANY(sqlc.narg('category_ids')::uuid[]))
  AND (sqlc.narg('wallet_ids')::uuid[] IS NULL
       OR t.wallet_id = ANY(sqlc.narg('wallet_ids')::uuid[])
       OR t.to_wallet_id = ANY(sqlc.narg('wallet_ids')::uuid[]))
  AND (sqlc.narg('q')::text IS NULL
       OR t.title ILIKE sqlc.narg('q') OR t.note ILIKE sqlc.narg('q')
       OR c.name ILIKE sqlc.narg('q') OR w.name ILIKE sqlc.narg('q') OR tw.name ILIKE sqlc.narg('q'))
GROUP BY 1, 2, 3
ORDER BY 1 DESC;

-- name: CategoryInUse :one
SELECT EXISTS (SELECT 1 FROM transactions WHERE space_id = $1 AND category_id = $2);

-- name: CategoryStats :many
SELECT t.category_id, t.type, t.currency, sum(t.amount)::numeric(38,8) AS total
FROM transactions t
WHERE t.space_id = $1 AND t.category_id IS NOT NULL AND t.type IN ('INCOME', 'EXPENSE')
  AND (sqlc.narg('from_ts')::timestamptz IS NULL OR t.occurred_at >= sqlc.narg('from_ts'))
  AND (sqlc.narg('to_ts')::timestamptz IS NULL OR t.occurred_at < sqlc.narg('to_ts'))
GROUP BY t.category_id, t.type, t.currency;

-- name: UpdateTransfer :one
UPDATE transactions SET
    wallet_id = $3, to_wallet_id = $4, amount = $5, currency = $6,
    to_amount = $7, to_currency = $8, fx_rate = $9, occurred_at = $10,
    note = sqlc.narg('note'), updated_at = now()
WHERE id = $1 AND space_id = $2 AND type = 'TRANSFER'
RETURNING *;
