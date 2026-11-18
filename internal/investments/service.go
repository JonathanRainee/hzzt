package investments

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
)

// DB is what the service needs; *pgxpool.Pool and pgx.Tx both satisfy it.
type DB interface {
	sqlc.DBTX
	db.TxStarter
}

// PriceSource supplies latest market prices (P11 CoinMarketCap, P12 Massive).
// Until a source is installed every AUTO investment falls back to its manual price.
type PriceSource interface {
	MarketPrices(ctx context.Context, instrumentIDs []uuid.UUID) (map[uuid.UUID]MarketPrice, error)
}

// Service holds investment logic.
type Service struct {
	db       DB
	q        *sqlc.Queries
	currency *currency.Service
	prices   PriceSource
	now      func() time.Time
}

// NewService builds the service; now may be nil.
func NewService(database DB, cur *currency.Service, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{db: database, q: sqlc.New(database), currency: cur, now: now}
}

// SetPriceSource installs the market price source.
func (s *Service) SetPriceSource(p PriceSource) { s.prices = p }

// Item is an investment with its valuation.
type Item struct {
	Investment sqlc.Investment
	Valuation  Valuation
	Details    any
}

// ListResult is the portfolio: items with valuations and the base-currency summary.
type ListResult struct {
	Items   []Item
	Summary Summary
}

func (s *Service) marketPrices(ctx context.Context, invs []sqlc.Investment) (map[uuid.UUID]MarketPrice, error) {
	if s.prices == nil {
		return nil, nil
	}
	var ids []uuid.UUID
	for _, i := range invs {
		if i.PriceMode == PriceModeAuto && i.InstrumentID.Valid {
			ids = append(ids, i.InstrumentID.UUID)
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}
	return s.prices.MarketPrices(ctx, ids)
}

func (s *Service) value(inv sqlc.Investment, prices map[uuid.UUID]MarketPrice, cc *currency.ConversionContext, now time.Time) (Item, error) {
	var mp *MarketPrice
	if inv.InstrumentID.Valid {
		if p, ok := prices[inv.InstrumentID.UUID]; ok {
			mp = &p
		}
	}
	details, err := parseStoredDetails(inv.AssetType, inv.Details)
	if err != nil {
		return Item{}, err
	}
	return Item{Investment: inv, Valuation: Value(inv, mp, cc, now), Details: details}, nil
}

// List returns the space's investments (optionally one asset type) with valuations and a summary.
func (s *Service) List(ctx context.Context, spaceID uuid.UUID, assetType string) (ListResult, error) {
	var t *string
	if assetType != "" {
		t = &assetType
	}
	rows, err := s.q.ListInvestments(ctx, sqlc.ListInvestmentsParams{SpaceID: spaceID, AssetType: t})
	if err != nil {
		return ListResult{}, err
	}
	cc, err := s.currency.BuildContext(ctx, spaceID)
	if err != nil {
		return ListResult{}, err
	}
	prices, err := s.marketPrices(ctx, rows)
	if err != nil {
		return ListResult{}, err
	}
	now := s.now()
	res := ListResult{Items: make([]Item, 0, len(rows))}
	vals := make([]Valuation, 0, len(rows))
	for _, r := range rows {
		it, err := s.value(r, prices, cc, now)
		if err != nil {
			return ListResult{}, err
		}
		res.Items = append(res.Items, it)
		vals = append(vals, it.Valuation)
	}
	res.Summary = Summarize(cc.BaseCurrency, vals)
	return res, nil
}

// Get returns one investment with its valuation.
func (s *Service) Get(ctx context.Context, spaceID, id uuid.UUID) (Item, error) {
	inv, err := s.q.GetInvestment(ctx, sqlc.GetInvestmentParams{ID: id, SpaceID: spaceID})
	if errors.Is(err, pgx.ErrNoRows) {
		return Item{}, errInvestmentNF
	}
	if err != nil {
		return Item{}, err
	}
	return s.item(ctx, inv)
}

func (s *Service) item(ctx context.Context, inv sqlc.Investment) (Item, error) {
	cc, err := s.currency.BuildContext(ctx, inv.SpaceID)
	if err != nil {
		return Item{}, err
	}
	prices, err := s.marketPrices(ctx, []sqlc.Investment{inv})
	if err != nil {
		return Item{}, err
	}
	return s.value(inv, prices, cc, s.now())
}

func toNullDecimal(d *decimal.Decimal) decimal.NullDecimal {
	if d == nil {
		return decimal.NullDecimal{}
	}
	return decimal.NullDecimal{Decimal: *d, Valid: true}
}

// Create stores a validated investment spec.
func (s *Service) Create(ctx context.Context, spaceID, userID uuid.UUID, spec Spec) (Item, error) {
	details, err := DetailsJSON(spec.Details)
	if err != nil {
		return Item{}, err
	}
	inv, err := s.q.CreateInvestment(ctx, sqlc.CreateInvestmentParams{
		SpaceID: spaceID, AssetType: spec.AssetType, PriceMode: spec.PriceMode, Name: spec.Name, Symbol: spec.Symbol,
		Icon: spec.Icon, QuoteCurrency: spec.QuoteCurrency, Quantity: spec.Quantity, AvgBuyPrice: spec.AvgBuyPrice,
		ManualPrice: spec.ManualPrice, Fees: spec.Fees, ManualCurrentValue: toNullDecimal(spec.ManualCurrentValue),
		PurchaseDate: spec.PurchaseDate, EstimatedAnnualChangePct: spec.EstimatedAnnualChangePct,
		ManualFxRate: toNullDecimal(spec.ManualFxRate), Details: details, CreatedByID: userID,
	})
	if err != nil {
		return Item{}, err
	}
	return s.item(ctx, inv)
}

// Update applies a patch to an investment; assetType never changes.
func (s *Service) Update(ctx context.Context, space sqlc.Space, id uuid.UUID, p PatchInput) (Item, error) {
	var out sqlc.Investment
	err := db.WithTx(ctx, s.db, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		cur, err := q.GetInvestmentForUpdate(ctx, sqlc.GetInvestmentForUpdateParams{ID: id, SpaceID: space.ID})
		if errors.Is(err, pgx.ErrNoRows) {
			return errInvestmentNF
		}
		if err != nil {
			return err
		}
		spec, err := p.apply(cur, strings.TrimSpace(space.BaseCurrency))
		if err != nil {
			return err
		}
		details, err := DetailsJSON(spec.Details)
		if err != nil {
			return err
		}
		out, err = q.UpdateInvestment(ctx, sqlc.UpdateInvestmentParams{
			ID: id, SpaceID: space.ID, PriceMode: spec.PriceMode, Name: spec.Name, Symbol: spec.Symbol, Icon: spec.Icon,
			QuoteCurrency: spec.QuoteCurrency, Quantity: spec.Quantity, AvgBuyPrice: spec.AvgBuyPrice,
			ManualPrice: spec.ManualPrice, Fees: spec.Fees, ManualCurrentValue: toNullDecimal(spec.ManualCurrentValue),
			PurchaseDate: spec.PurchaseDate, EstimatedAnnualChangePct: spec.EstimatedAnnualChangePct,
			ManualFxRate: toNullDecimal(spec.ManualFxRate), Details: details,
		})
		return err
	})
	if err != nil {
		return Item{}, err
	}
	return s.item(ctx, out)
}

// Delete soft-deletes an investment.
func (s *Service) Delete(ctx context.Context, spaceID, id uuid.UUID) error {
	n, err := s.q.SoftDeleteInvestment(ctx, sqlc.SoftDeleteInvestmentParams{ID: id, SpaceID: spaceID})
	if err != nil {
		return err
	}
	if n == 0 {
		return errInvestmentNF
	}
	return nil
}
