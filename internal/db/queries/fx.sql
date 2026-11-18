-- name: UpsertFxRate :exec
INSERT INTO fx_rates (base, quote, rate, as_of, source)
VALUES ('USD', $1, $2, $3, $4)
ON CONFLICT (base, quote, as_of)
DO UPDATE SET rate = EXCLUDED.rate, source = EXCLUDED.source, fetched_at = now();

-- name: LatestFxRates :many
-- Newest rate per quote with as_of <= the given date.
SELECT DISTINCT ON (quote) quote, rate, as_of, source
FROM fx_rates
WHERE base = 'USD' AND as_of <= $1
ORDER BY quote, as_of DESC;

-- name: CountFxRates :one
SELECT count(*)::int FROM fx_rates;

-- name: TryFxRefreshLock :one
-- Transaction-scoped advisory lock so only one instance refreshes at a time.
SELECT pg_try_advisory_xact_lock(7305001);

-- name: GetSpaceByID :one
SELECT * FROM spaces WHERE id = $1 AND deleted_at IS NULL;

-- name: ListManualFxRates :many
SELECT * FROM manual_fx_rates WHERE space_id = $1 ORDER BY base, quote;

-- name: UpsertManualFxRate :one
INSERT INTO manual_fx_rates (space_id, base, quote, rate, updated_by_id)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (space_id, base, quote)
DO UPDATE SET rate = EXCLUDED.rate, updated_by_id = EXCLUDED.updated_by_id, updated_at = now()
RETURNING *;

-- name: DeleteManualFxRate :execrows
DELETE FROM manual_fx_rates WHERE space_id = $1 AND base = $2 AND quote = $3;
