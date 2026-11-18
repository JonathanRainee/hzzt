// Package ledger is the only code that changes wallets.balance. Every
// transaction's effect on wallets is defined once, in Deltas; Apply and Reverse
// run it inside the caller's open pgx.Tx with atomic UPDATEs, and
// RecomputeBalance rebuilds a balance from the transaction history so tests
// and maintenance can verify the cached value.
package ledger

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/JonathanRainee/hzzt/internal/db/sqlc"
)

// Transaction types and directions.
const (
	TypeIncome         = "INCOME"
	TypeExpense        = "EXPENSE"
	TypeTransfer       = "TRANSFER"
	TypeAdjustment     = "ADJUSTMENT"
	TypeOpeningBalance = "OPENING_BALANCE"

	DirectionIn  = "IN"
	DirectionOut = "OUT"
)

// Delta is a signed change to one wallet's balance.
type Delta struct {
	WalletID uuid.UUID
	Amount   decimal.Decimal
}

// Deltas returns the balance effects of t (see the plan's balance-effects table).
// Keep sqlc's LedgerSumForWallet in step with this function.
func Deltas(t sqlc.Transaction) ([]Delta, error) {
	var out []Delta
	need := func() (uuid.UUID, error) {
		if !t.WalletID.Valid {
			return uuid.Nil, fmt.Errorf("ledger: %s transaction %s has no wallet", t.Type, t.ID)
		}
		return t.WalletID.UUID, nil
	}
	switch t.Type {
	case TypeIncome:
		w, err := need()
		if err != nil {
			return nil, err
		}
		out = append(out, Delta{w, t.Amount})
	case TypeExpense:
		w, err := need()
		if err != nil {
			return nil, err
		}
		out = append(out, Delta{w, t.Amount.Neg()})
	case TypeAdjustment, TypeOpeningBalance:
		w, err := need()
		if err != nil {
			return nil, err
		}
		switch {
		case t.Direction != nil && *t.Direction == DirectionIn:
			out = append(out, Delta{w, t.Amount})
		case t.Direction != nil && *t.Direction == DirectionOut:
			out = append(out, Delta{w, t.Amount.Neg()})
		default:
			return nil, fmt.Errorf("ledger: %s transaction %s needs a direction", t.Type, t.ID)
		}
	case TypeTransfer:
		w, err := need()
		if err != nil {
			return nil, err
		}
		if !t.ToWalletID.Valid || !t.ToAmount.Valid {
			return nil, fmt.Errorf("ledger: transfer %s needs to_wallet_id and to_amount", t.ID)
		}
		out = append(out, Delta{w, t.Amount.Neg()}, Delta{t.ToWalletID.UUID, t.ToAmount.Decimal})
	default:
		return nil, fmt.Errorf("ledger: unknown transaction type %q", t.Type)
	}
	return out, nil
}

// Apply adds the effect of t to its wallet(s). tx must be the caller's open
// transaction so the balance change commits or rolls back with the row itself.
func Apply(ctx context.Context, tx pgx.Tx, t sqlc.Transaction) error {
	ds, err := Deltas(t)
	if err != nil {
		return err
	}
	return run(ctx, tx, t.SpaceID, ds, false)
}

// Reverse undoes the effect of t.
func Reverse(ctx context.Context, tx pgx.Tx, t sqlc.Transaction) error {
	ds, err := Deltas(t)
	if err != nil {
		return err
	}
	return run(ctx, tx, t.SpaceID, ds, true)
}

func run(ctx context.Context, tx pgx.Tx, spaceID uuid.UUID, ds []Delta, negate bool) error {
	// A fixed lock order (by wallet id) prevents deadlocks between concurrent transfers.
	slices.SortFunc(ds, func(a, b Delta) int { return slices.Compare(a.WalletID[:], b.WalletID[:]) })
	q := sqlc.New(tx)
	for _, d := range ds {
		amt := d.Amount
		if negate {
			amt = amt.Neg()
		}
		if _, err := q.AddToWalletBalance(ctx, sqlc.AddToWalletBalanceParams{ID: d.WalletID, SpaceID: spaceID, Balance: amt}); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf("ledger: wallet %s not found in space %s", d.WalletID, spaceID)
			}
			return fmt.Errorf("ledger: update wallet %s: %w", d.WalletID, err)
		}
	}
	return nil
}

// RecomputeBalance rebuilds a wallet's balance from its transactions.
func RecomputeBalance(ctx context.Context, db sqlc.DBTX, walletID uuid.UUID) (decimal.Decimal, error) {
	return sqlc.New(db).LedgerSumForWallet(ctx, uuid.NullUUID{UUID: walletID, Valid: true})
}
