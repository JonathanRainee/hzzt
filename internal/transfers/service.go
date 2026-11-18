// Package transfers moves money between wallets, including cross-currency
// transfers. Balance changes go through package ledger inside one database
// transaction.
package transfers

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/JonathanRainee/hzzt/internal/db"
	"github.com/JonathanRainee/hzzt/internal/db/sqlc"
	"github.com/JonathanRainee/hzzt/internal/ledger"
	"github.com/JonathanRainee/hzzt/internal/platform/apperror"
	"github.com/JonathanRainee/hzzt/internal/transactions"
)

// Error codes owned by this package (see plan Appendix B).
const (
	CodeTransferSameWallet     = "TRANSFER_SAME_WALLET"
	CodeTransferAmountRequired = "TRANSFER_AMOUNT_REQUIRED"
	CodeNotSupportedYet        = "NOT_SUPPORTED_YET"
	CodeTransferNotFound       = "TRANSFER_NOT_FOUND"
)

var (
	errSameWallet     = apperror.Unprocessable(CodeTransferSameWallet, "The source and destination wallets must differ")
	errAmountRequired = apperror.Unprocessable(CodeTransferAmountRequired, "toAmount is required when the wallets use different currencies")
	errTransferNF     = apperror.New(404, CodeTransferNotFound, "Transfer not found")
	errWalletNF       = apperror.New(404, "WALLET_NOT_FOUND", "Wallet not found")
)

// DB is what the service needs; *pgxpool.Pool and pgx.Tx both satisfy it.
type DB interface {
	sqlc.DBTX
	db.TxStarter
}

// Service holds transfer logic.
type Service struct {
	db  DB
	q   *sqlc.Queries
	txn *transactions.Service
	now func() time.Time
}

// NewService builds the service; now may be nil.
func NewService(database DB, txn *transactions.Service, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{db: database, q: sqlc.New(database), txn: txn, now: now}
}

func nu(id uuid.UUID) uuid.NullUUID { return uuid.NullUUID{UUID: id, Valid: true} }

func loadWallet(ctx context.Context, q *sqlc.Queries, spaceID, id uuid.UUID) (sqlc.Wallet, error) {
	w, err := q.GetWallet(ctx, sqlc.GetWalletParams{ID: id, SpaceID: spaceID})
	if errors.Is(err, pgx.ErrNoRows) {
		return w, errWalletNF
	}
	return w, err
}

// finalize validates the wallet pair and works out toAmount and the stored rate.
// toAmount may be nil: that is fine for same-currency transfers (it defaults to
// amount) and an error for cross-currency ones, unless fallback is set (an
// unchanged cross-currency edit keeps its previous to_amount).
func finalize(from, to sqlc.Wallet, amount decimal.Decimal, toAmount *decimal.Decimal, fallback *decimal.Decimal) (decimal.Decimal, decimal.Decimal, error) {
	if from.ID == to.ID {
		return decimal.Zero, decimal.Zero, errSameWallet
	}
	if from.Currency == to.Currency {
		if toAmount != nil && !toAmount.Equal(amount) {
			return decimal.Zero, decimal.Zero, apperror.Validation("Validation failed", map[string]any{
				"toAmount": "must equal amount when both wallets use the same currency (or be omitted)",
			})
		}
		return amount, decimal.NewFromInt(1), nil
	}
	ta := toAmount
	if ta == nil {
		ta = fallback
	}
	if ta == nil {
		return decimal.Zero, decimal.Zero, errAmountRequired
	}
	rate := ta.DivRound(amount, 12)
	if rate.GreaterThanOrEqual(decimal.New(1, 16)) {
		return decimal.Zero, decimal.Zero, apperror.Validation("Validation failed", map[string]any{
			"toAmount": "implies an exchange rate that is out of range",
		})
	}
	return *ta, rate, nil
}

// Input describes a new transfer between two wallets.
type Input struct {
	FromWalletID, ToWalletID uuid.UUID
	Amount                   decimal.Decimal
	ToAmount                 *decimal.Decimal
	OccurredAt               *time.Time
	Note                     *string
}

// Create records a transfer and applies both sides atomically.
func (s *Service) Create(ctx context.Context, spaceID, userID uuid.UUID, in Input) (transactions.Detail, error) {
	var id uuid.UUID
	err := db.WithTx(ctx, s.db, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		from, err := loadWallet(ctx, q, spaceID, in.FromWalletID)
		if err != nil {
			return err
		}
		to, err := loadWallet(ctx, q, spaceID, in.ToWalletID)
		if err != nil {
			return err
		}
		toAmount, rate, err := finalize(from, to, in.Amount, in.ToAmount, nil)
		if err != nil {
			return err
		}
		occurred := s.now().UTC()
		if in.OccurredAt != nil {
			occurred = in.OccurredAt.UTC()
		}
		toCur := to.Currency
		t, err := q.CreateTransaction(ctx, sqlc.CreateTransactionParams{
			SpaceID: spaceID, Type: ledger.TypeTransfer, Note: cleanNote(in.Note),
			Amount: in.Amount, Currency: from.Currency, WalletID: nu(from.ID),
			OccurredAt: occurred, CreatedByID: userID,
			ToWalletID: nu(to.ID), ToAmount: decimal.NullDecimal{Decimal: toAmount, Valid: true},
			ToCurrency: &toCur, FxRate: decimal.NullDecimal{Decimal: rate, Valid: true},
		})
		if err != nil {
			return err
		}
		id = t.ID
		return ledger.Apply(ctx, tx, t)
	})
	if err != nil {
		return transactions.Detail{}, err
	}
	return s.txn.Get(ctx, spaceID, id)
}

// Optional is a PATCH field that distinguishes "not sent" from an explicit null.
type Optional[T any] struct {
	Set   bool
	Value *T
}

// Patch is a partial update of a transfer.
type Patch struct {
	FromWalletID, ToWalletID *uuid.UUID
	Amount, ToAmount         *decimal.Decimal
	OccurredAt               *time.Time
	Note                     Optional[string]
}

// Update reverses the old transfer and applies the new one in the same database transaction.
func (s *Service) Update(ctx context.Context, spaceID, id uuid.UUID, p Patch) (transactions.Detail, error) {
	err := db.WithTx(ctx, s.db, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		old, err := q.GetTransactionForUpdate(ctx, sqlc.GetTransactionForUpdateParams{ID: id, SpaceID: spaceID})
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && old.Type != ledger.TypeTransfer) {
			return errTransferNF
		}
		if err != nil {
			return err
		}

		fromID, toID := old.WalletID.UUID, old.ToWalletID.UUID
		if p.FromWalletID != nil {
			fromID = *p.FromWalletID
		}
		if p.ToWalletID != nil {
			toID = *p.ToWalletID
		}
		from, err := loadWallet(ctx, q, spaceID, fromID)
		if err != nil {
			return err
		}
		to, err := loadWallet(ctx, q, spaceID, toID)
		if err != nil {
			return err
		}
		amount := old.Amount
		if p.Amount != nil {
			amount = *p.Amount
		}

		// An unchanged cross-currency pair and amount keeps its agreed toAmount.
		var fallback *decimal.Decimal
		if old.ToAmount.Valid && fromID == old.WalletID.UUID && toID == old.ToWalletID.UUID && amount.Equal(old.Amount) {
			fallback = &old.ToAmount.Decimal
		}
		toAmount, rate, err := finalize(from, to, amount, p.ToAmount, fallback)
		if err != nil {
			return err
		}

		occurred := old.OccurredAt
		if p.OccurredAt != nil {
			occurred = p.OccurredAt.UTC()
		}
		note := old.Note
		if p.Note.Set {
			note = cleanNote(p.Note.Value)
		}

		if err := ledger.Reverse(ctx, tx, old); err != nil {
			return err
		}
		updated, err := q.UpdateTransfer(ctx, sqlc.UpdateTransferParams{
			ID: id, SpaceID: spaceID, WalletID: nu(from.ID), ToWalletID: nu(to.ID), Amount: amount,
			Currency: from.Currency, ToAmount: decimal.NullDecimal{Decimal: toAmount, Valid: true},
			ToCurrency: &to.Currency, FxRate: decimal.NullDecimal{Decimal: rate, Valid: true},
			OccurredAt: occurred, Note: note,
		})
		if err != nil {
			return err
		}
		return ledger.Apply(ctx, tx, updated)
	})
	if err != nil {
		return transactions.Detail{}, err
	}
	return s.txn.Get(ctx, spaceID, id)
}

// Delete reverses the transfer and removes it.
func (s *Service) Delete(ctx context.Context, spaceID, id uuid.UUID) error {
	return db.WithTx(ctx, s.db, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		old, err := q.GetTransactionForUpdate(ctx, sqlc.GetTransactionForUpdateParams{ID: id, SpaceID: spaceID})
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && old.Type != ledger.TypeTransfer) {
			return errTransferNF
		}
		if err != nil {
			return err
		}
		if err := ledger.Reverse(ctx, tx, old); err != nil {
			return err
		}
		_, err = q.DeleteTransaction(ctx, sqlc.DeleteTransactionParams{ID: id, SpaceID: spaceID})
		return err
	})
}

func trim(s string) string { return strings.TrimSpace(s) }

func cleanNote(n *string) *string {
	if n == nil {
		return nil
	}
	t := trim(*n)
	if t == "" {
		return nil
	}
	return &t
}
