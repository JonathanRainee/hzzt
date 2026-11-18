// Package goals implements savings goals funded by earmarking ("reserving")
// part of wallet balances. Reserving moves no money.
package goals

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/JonathanRainee/hzzt/internal/currency"
	"github.com/JonathanRainee/hzzt/internal/db"
	"github.com/JonathanRainee/hzzt/internal/db/sqlc"
	"github.com/JonathanRainee/hzzt/internal/platform/apperror"
	"github.com/JonathanRainee/hzzt/internal/platform/money"
)

// Error codes owned by this package (see plan Appendix B).
const (
	CodeGoalNotFound          = "GOAL_NOT_FOUND"
	CodeInsufficientAvailable = "INSUFFICIENT_AVAILABLE_BALANCE"
	codeWalletNotFound        = "WALLET_NOT_FOUND"
)

var (
	errGoalNF   = apperror.New(404, CodeGoalNotFound, "Goal not found")
	errWalletNF = apperror.New(404, codeWalletNotFound, "Wallet not found")
)

// DB is what the service needs; *pgxpool.Pool and pgx.Tx both satisfy it.
type DB interface {
	sqlc.DBTX
	db.TxStarter
}

// Service holds goal logic.
type Service struct {
	db       DB
	q        *sqlc.Queries
	currency *currency.Service
	now      func() time.Time
}

// NewService builds the service; now may be nil.
func NewService(database DB, cur *currency.Service, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{db: database, q: sqlc.New(database), currency: cur, now: now}
}

// Allocation is the part of one wallet earmarked for a goal.
type Allocation struct {
	WalletID       uuid.UUID
	WalletName     string
	WalletIcon     string
	WalletCurrency string
	Amount         decimal.Decimal // in the wallet's currency
	Converted      *decimal.Decimal
}

// Goal is a goal with its progress, computed on read.
type Goal struct {
	sqlc.Goal
	Allocations []Allocation
	// Progress is the sum of the allocations converted to the goal's currency.
	Progress decimal.Decimal
	// ProgressPct is progress / target × 100 (not capped: over 100 means exceeded).
	ProgressPct decimal.Decimal
	Remaining   decimal.Decimal
	Achieved    bool
	// RequiredMonthly is remaining / months until the target date; nil when
	// there is no date, it has passed or the goal is achieved.
	RequiredMonthly *decimal.Decimal
	Warnings        []currency.Warning
}

// MonthsUntil counts the months from today until target, rounded up and at
// least 1 while the target is still ahead; 0 when it is today or past.
func MonthsUntil(today, target time.Time) int {
	t := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, time.UTC)
	d := time.Date(target.Year(), target.Month(), target.Day(), 0, 0, 0, 0, time.UTC)
	if !d.After(t) {
		return 0
	}
	months := (d.Year()-t.Year())*12 + int(d.Month()) - int(t.Month())
	if d.Day() > t.Day() {
		months++
	}
	return max(months, 1)
}

// compute works out progress, achievement and the required monthly amount.
func (s *Service) compute(g sqlc.Goal, allocs []sqlc.ListAllocationsForGoalsRow, cc *currency.ConversionContext, today time.Time) Goal {
	out := Goal{Goal: g, Allocations: make([]Allocation, 0, len(allocs)), Warnings: []currency.Warning{}}
	cur := strings.TrimSpace(g.Currency)
	items := make([]currency.MoneyItem, 0, len(allocs))
	for _, a := range allocs {
		al := Allocation{
			WalletID: a.WalletID, WalletName: a.WalletName, WalletIcon: a.WalletIcon,
			WalletCurrency: strings.TrimSpace(a.WalletCurrency), Amount: a.Amount,
		}
		if c, err := currency.Convert(a.Amount, al.WalletCurrency, cur, cc); err == nil {
			al.Converted = &c
		}
		out.Allocations = append(out.Allocations, al)
		items = append(items, currency.MoneyItem{Label: a.WalletName, Amount: a.Amount, Currency: al.WalletCurrency})
	}
	// progress converts to the goal currency (not the space base): reuse the aggregator with a context whose base is the goal currency
	gc := *cc
	gc.BaseCurrency = cur
	agg := currency.Aggregate(items, &gc)
	out.Progress, out.Warnings = agg.Total, agg.Warnings

	target := g.TargetAmount
	out.ProgressPct = out.Progress.Div(target).Mul(decimal.NewFromInt(100)).Round(2)
	out.Remaining = decimal.Max(target.Sub(out.Progress), decimal.Zero)
	out.Achieved = out.Progress.GreaterThanOrEqual(target)
	if !out.Achieved && g.TargetDate != nil {
		if m := MonthsUntil(today, *g.TargetDate); m > 0 {
			v := money.RoundToMinor(out.Remaining.DivRound(decimal.NewFromInt(int64(m)), 12), cur)
			out.RequiredMonthly = &v
		}
	}
	return out
}

// syncAchieved sets achieved_at when progress first reaches the target and clears
// it when it drops below. It runs after every change and lazily on reads (progress
// also moves with FX rates).
func (s *Service) syncAchieved(ctx context.Context, q *sqlc.Queries, g *Goal) error {
	switch {
	case g.Achieved && g.AchievedAt == nil:
		now := s.now().UTC().Truncate(time.Microsecond) // what Postgres stores
		g.AchievedAt = &now
	case !g.Achieved && g.AchievedAt != nil:
		g.AchievedAt = nil
	default:
		return nil
	}
	return q.SetGoalAchieved(ctx, sqlc.SetGoalAchievedParams{ID: g.ID, AchievedAt: g.AchievedAt})
}

func (s *Service) load(ctx context.Context, q *sqlc.Queries, spaceID uuid.UUID, rows []sqlc.Goal, tz *time.Location) ([]Goal, error) {
	if len(rows) == 0 {
		return []Goal{}, nil
	}
	ids := make([]uuid.UUID, len(rows))
	for i, g := range rows {
		ids[i] = g.ID
	}
	allocs, err := q.ListAllocationsForGoals(ctx, ids)
	if err != nil {
		return nil, err
	}
	by := map[uuid.UUID][]sqlc.ListAllocationsForGoalsRow{}
	for _, a := range allocs {
		by[a.GoalID] = append(by[a.GoalID], a)
	}
	cc, err := s.currency.BuildContext(ctx, spaceID)
	if err != nil {
		return nil, err
	}
	n := s.now().In(tz)
	today := time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, time.UTC)
	out := make([]Goal, 0, len(rows))
	for _, g := range rows {
		goal := s.compute(g, by[g.ID], cc, today)
		if err := s.syncAchieved(ctx, q, &goal); err != nil {
			return nil, err
		}
		out = append(out, goal)
	}
	return out, nil
}

// List returns the space's goals with progress.
func (s *Service) List(ctx context.Context, spaceID uuid.UUID, tz *time.Location) ([]Goal, error) {
	rows, err := s.q.ListGoals(ctx, spaceID)
	if err != nil {
		return nil, err
	}
	return s.load(ctx, s.q, spaceID, rows, tz)
}

// Get returns one goal with progress.
func (s *Service) Get(ctx context.Context, spaceID, id uuid.UUID, tz *time.Location) (Goal, error) {
	g, err := s.q.GetGoal(ctx, sqlc.GetGoalParams{ID: id, SpaceID: spaceID})
	if errors.Is(err, pgx.ErrNoRows) {
		return Goal{}, errGoalNF
	}
	if err != nil {
		return Goal{}, err
	}
	out, err := s.load(ctx, s.q, spaceID, []sqlc.Goal{g}, tz)
	if err != nil {
		return Goal{}, err
	}
	return out[0], nil
}

// CreateInput describes a new goal.
type CreateInput struct {
	Name, Icon, Color string
	TargetAmount      decimal.Decimal
	Currency          string // "" → the space's base currency
	TargetDate        *time.Time
	// InitialReserve optionally reserves part of a wallet right away.
	InitialReserve *Reserve
}

// Reserve is a wallet allocation request.
type Reserve struct {
	WalletID uuid.UUID
	Amount   decimal.Decimal
}

// Create stores a goal and its optional initial reservation in one database transaction.
func (s *Service) Create(ctx context.Context, space sqlc.Space, userID uuid.UUID, in CreateInput, tz *time.Location) (Goal, error) {
	cur := strings.ToUpper(strings.TrimSpace(in.Currency))
	if cur == "" {
		cur = strings.TrimSpace(space.BaseCurrency)
	}
	var id uuid.UUID
	err := db.WithTx(ctx, s.db, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		g, err := q.CreateGoal(ctx, sqlc.CreateGoalParams{
			SpaceID: space.ID, Name: in.Name, Icon: in.Icon, Color: in.Color, TargetAmount: in.TargetAmount,
			Currency: cur, TargetDate: in.TargetDate, CreatedByID: userID,
		})
		if err != nil {
			return err
		}
		id = g.ID
		if in.InitialReserve != nil {
			return s.setAllocation(ctx, q, space.ID, g.ID, in.InitialReserve.WalletID, in.InitialReserve.Amount)
		}
		return nil
	})
	if err != nil {
		return Goal{}, err
	}
	return s.Get(ctx, space.ID, id, tz)
}

// Optional is a PATCH field that distinguishes "not sent" from an explicit null.
type Optional[T any] struct {
	Set   bool
	Value *T
}

// UpdateInput is a partial update.
type UpdateInput struct {
	Name, Icon, Color, Currency *string
	TargetAmount                *decimal.Decimal
	TargetDate                  Optional[time.Time]
}

// Update applies a partial update.
func (s *Service) Update(ctx context.Context, spaceID, id uuid.UUID, in UpdateInput, tz *time.Location) (Goal, error) {
	err := db.WithTx(ctx, s.db, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		cur, err := q.GetGoalForUpdate(ctx, sqlc.GetGoalForUpdateParams{ID: id, SpaceID: spaceID})
		if errors.Is(err, pgx.ErrNoRows) {
			return errGoalNF
		}
		if err != nil {
			return err
		}
		p := sqlc.UpdateGoalParams{
			ID: id, SpaceID: spaceID, Name: cur.Name, Icon: cur.Icon, Color: cur.Color, TargetAmount: cur.TargetAmount,
			Currency: cur.Currency, TargetDate: cur.TargetDate,
		}
		if in.Name != nil {
			p.Name = *in.Name
		}
		if in.Icon != nil {
			p.Icon = *in.Icon
		}
		if in.Color != nil {
			p.Color = *in.Color
		}
		if in.TargetAmount != nil {
			p.TargetAmount = *in.TargetAmount
		}
		if in.Currency != nil {
			p.Currency = *in.Currency
		}
		if in.TargetDate.Set {
			p.TargetDate = in.TargetDate.Value
		}
		_, err = q.UpdateGoal(ctx, p)
		return err
	})
	if err != nil {
		return Goal{}, err
	}
	return s.Get(ctx, spaceID, id, tz) // re-evaluates achievement against the new target
}

// Delete soft-deletes a goal and releases all its allocations.
func (s *Service) Delete(ctx context.Context, spaceID, id uuid.UUID) error {
	return db.WithTx(ctx, s.db, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		n, err := q.SoftDeleteGoal(ctx, sqlc.SoftDeleteGoalParams{ID: id, SpaceID: spaceID})
		if err != nil {
			return err
		}
		if n == 0 {
			return errGoalNF
		}
		return q.DeleteAllocationsForGoal(ctx, id)
	})
}

// setAllocation sets (or, with 0, removes) a goal's reservation on a wallet. The
// wallet row is locked so concurrent changes cannot over-reserve it. The amount
// must not exceed available + this goal's current reservation, i.e. balance minus
// what other goals reserve (INSUFFICIENT_AVAILABLE_BALANCE).
func (s *Service) setAllocation(ctx context.Context, q *sqlc.Queries, spaceID, goalID, walletID uuid.UUID, amount decimal.Decimal) error {
	w, err := q.GetWalletForUpdate(ctx, sqlc.GetWalletForUpdateParams{ID: walletID, SpaceID: spaceID})
	if errors.Is(err, pgx.ErrNoRows) {
		return errWalletNF
	}
	if err != nil {
		return err
	}
	if amount.IsZero() {
		return q.DeleteAllocation(ctx, sqlc.DeleteAllocationParams{GoalID: goalID, WalletID: walletID})
	}
	others, err := q.ReservedForWalletExcludingGoal(ctx, sqlc.ReservedForWalletExcludingGoalParams{WalletID: walletID, GoalID: goalID})
	if err != nil {
		return err
	}
	available := w.Balance.Sub(others)
	if amount.GreaterThan(available) {
		return apperror.Unprocessable(CodeInsufficientAvailable, "The wallet does not have enough available balance to reserve that much").
			WithDetails(map[string]any{"available": decimal.Max(available, decimal.Zero).String(), "requested": amount.String()})
	}
	return q.UpsertAllocation(ctx, sqlc.UpsertAllocationParams{GoalID: goalID, WalletID: walletID, Amount: amount})
}

// SetAllocation reserves `amount` of a wallet for a goal (0 removes the reservation).
func (s *Service) SetAllocation(ctx context.Context, spaceID, goalID, walletID uuid.UUID, amount decimal.Decimal, tz *time.Location) (Goal, error) {
	err := db.WithTx(ctx, s.db, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		if _, err := q.GetGoalForUpdate(ctx, sqlc.GetGoalForUpdateParams{ID: goalID, SpaceID: spaceID}); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return errGoalNF
			}
			return err
		}
		return s.setAllocation(ctx, q, spaceID, goalID, walletID, amount)
	})
	if err != nil {
		return Goal{}, err
	}
	return s.Get(ctx, spaceID, goalID, tz)
}
