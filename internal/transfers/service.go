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
	"github.com/JonathanRainee/hzzt/internal/investments"
	"github.com/JonathanRainee/hzzt/internal/ledger"
	"github.com/JonathanRainee/hzzt/internal/manualassets"
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

// Service holds transfer logic: wallet-to-wallet transfers, and BUY/SELL of
// investments through a wallet (delegated to package investments).
type Service struct {
	db  DB
	q   *sqlc.Queries
	txn *transactions.Service
	inv *investments.Service
	ma  *manualassets.Service
	now func() time.Time
}

// NewService builds the service; now may be nil.
func NewService(database DB, txn *transactions.Service, inv *investments.Service, ma *manualassets.Service, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{db: database, q: sqlc.New(database), txn: txn, inv: inv, ma: ma, now: now}
}

// Trade records a BUY or SELL and returns the transaction.
func (s *Service) Trade(ctx context.Context, spaceID, userID uuid.UUID, in investments.TradeInput) (transactions.Detail, error) {
	t, err := s.inv.Trade(ctx, spaceID, userID, in)
	if err != nil {
		return transactions.Detail{}, err
	}
	return s.txn.Get(ctx, spaceID, t.ID)
}

// isSettlement reports whether the transaction is the settlement of a manual asset.
func (s *Service) isSettlement(ctx context.Context, spaceID, id uuid.UUID) bool {
	d, err := s.txn.Get(ctx, spaceID, id)
	return err == nil && d.ManualAssetID.Valid
}

// isTrade reports whether the transaction is an investment BUY/SELL.
func (s *Service) isTrade(ctx context.Context, spaceID, id uuid.UUID) bool {
	d, err := s.txn.Get(ctx, spaceID, id)
	return err == nil && d.InvestmentID.Valid && d.InvestmentAction != nil
}

func mapTradeErr(err error) error {
	if errors.Is(err, investments.ErrNotATrade) {
		return errTransferNF
	}
	return err
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

// InvestmentPatch is the investment side of an edit of a BUY/SELL.
type InvestmentPatch struct {
	InvestmentID             *uuid.UUID
	Quantity, UnitPrice, Fee *decimal.Decimal
}

// Patch is a partial update of a transfer or of a BUY/SELL.
type Patch struct {
	FromWalletID, ToWalletID *uuid.UUID
	Amount, ToAmount         *decimal.Decimal
	OccurredAt               *time.Time
	Note                     Optional[string]
	// Investment carries the investment side of a BUY/SELL edit.
	Investment *InvestmentPatch
}

// Update reverses the old transfer and applies the new one in the same database transaction.
func (s *Service) Update(ctx context.Context, spaceID, userID, id uuid.UUID, p Patch) (transactions.Detail, error) {
	if s.isTrade(ctx, spaceID, id) {
		return s.updateTrade(ctx, spaceID, userID, id, p)
	}
	if s.isSettlement(ctx, spaceID, id) {
		return transactions.Detail{}, apperror.Unprocessable(CodeNotSupportedYet, "A settlement cannot be edited; delete it and record it again")
	}
	if p.Investment != nil {
		return transactions.Detail{}, apperror.Unprocessable(CodeNotSupportedYet, "Investments are not part of a wallet transfer")
	}
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

// updateTrade edits a BUY/SELL.
func (s *Service) updateTrade(ctx context.Context, spaceID, userID, id uuid.UUID, p Patch) (transactions.Detail, error) {
	d, err := s.txn.Get(ctx, spaceID, id)
	if err != nil {
		return transactions.Detail{}, errTransferNF
	}
	tp := investments.TradePatch{Amount: p.Amount, OccurredAt: p.OccurredAt, NoteSet: p.Note.Set, Note: p.Note.Value}
	if p.ToAmount != nil {
		return transactions.Detail{}, apperror.Validation("Validation failed", map[string]any{"toAmount": "is not used for investment trades; use amount for the wallet side"})
	}
	tp.WalletID = p.FromWalletID
	if tp.WalletID == nil {
		tp.WalletID = p.ToWalletID
	}
	if iv := p.Investment; iv != nil {
		if iv.InvestmentID != nil && *iv.InvestmentID != d.InvestmentID.UUID {
			return transactions.Detail{}, apperror.Validation("Validation failed", map[string]any{"investmentId": "cannot be changed"})
		}
		tp.Quantity, tp.UnitPrice, tp.Fee = iv.Quantity, iv.UnitPrice, iv.Fee
	}
	if _, err := s.inv.UpdateTrade(ctx, spaceID, userID, id, tp); err != nil {
		return transactions.Detail{}, mapTradeErr(err)
	}
	return s.txn.Get(ctx, spaceID, id)
}

// Delete reverses the transfer (or BUY/SELL) and removes it.
func (s *Service) Delete(ctx context.Context, spaceID, userID, id uuid.UUID) error {
	if s.isTrade(ctx, spaceID, id) {
		return mapTradeErr(s.inv.DeleteTrade(ctx, spaceID, userID, id))
	}
	if s.isSettlement(ctx, spaceID, id) {
		err := s.ma.DeleteSettlement(ctx, spaceID, uuid.Nil, id)
		if errors.Is(err, manualassets.ErrNotASettlement) {
			return errTransferNF
		}
		return err
	}
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
