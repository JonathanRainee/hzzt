-- name: AddToWalletBalance :one
-- The only statement that changes wallets.balance (called from package ledger).
UPDATE wallets SET balance = balance + $3, updated_at = now()
WHERE id = $1 AND space_id = $2
RETURNING balance;

-- name: LedgerSumForWallet :one
-- Recomputes a wallet's balance from its transactions; must mirror ledger.Deltas.
SELECT (
    COALESCE((SELECT sum(CASE
        WHEN t.type = 'INCOME' THEN t.amount
        WHEN t.type = 'EXPENSE' THEN -t.amount
        WHEN t.type IN ('ADJUSTMENT', 'OPENING_BALANCE') AND t.direction = 'IN' THEN t.amount
        WHEN t.type IN ('ADJUSTMENT', 'OPENING_BALANCE') AND t.direction = 'OUT' THEN -t.amount
        WHEN t.type = 'TRANSFER' THEN -t.amount
        ELSE 0 END)
      FROM transactions t WHERE t.wallet_id = $1), 0)
    +
    COALESCE((SELECT sum(t.to_amount) FROM transactions t
      WHERE t.to_wallet_id = $1 AND t.type = 'TRANSFER'), 0)
)::numeric(28,8) AS balance;

-- name: ListLiveWalletIDsAll :many
SELECT id FROM wallets WHERE deleted_at IS NULL;
