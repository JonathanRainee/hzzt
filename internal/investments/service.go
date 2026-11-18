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
	"github.com/JonathanRainee/hzzt/internal/ledger"
	"github.com/JonathanRainee/hzzt/internal/platform/apperror"
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

// Market is the market-data capability: latest prices, linking crypto
// investments to coins, and on-demand price refresh. It is installed by
// SetMarket; without it AUTO investments use their manual price.
type Market interface {
	PriceSource
	// ResolveInstrument returns the instrument that prices an investment of the
	// asset type: the explicit one (picked from search) when given, otherwise the
	// symbol resolved to the highest-ranked coin / exact ticker.
	ResolveInstrument(ctx context.Context, assetType string, explicit *uuid.UUID, symbol string) (uuid.UUID, error)
	// EnsurePrice fetches the first price of a freshly linked instrument (best effort).
	EnsurePrice(ctx context.Context, instrumentID uuid.UUID)
	// RefreshPrice fetches one instrument's price now.
	RefreshPrice(ctx context.Context, instrumentID uuid.UUID) error
}

// Service holds investment logic.
type Service struct {
	db       DB
	q        *sqlc.Queries
	currency *currency.Service
	market   Market
	now      func() time.Time
}

// NewService builds the service; now may be nil.
func NewService(database DB, cur *currency.Service, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{db: database, q: sqlc.New(database), currency: cur, now: now}
}

// SetMarket installs market data (prices and instrument linking).
func (s *Service) SetMarket(m Market) { s.market = m }

// Item is an investment with its valuation.
type Item struct {
	Investment sqlc.Investment
	Valuation  Valuation
	Details    any
	Realized   RealizedPL
}

// ListResult is the portfolio: items with valuations and the base-currency summary.
type ListResult struct {
	Items   []Item
	Summary Summary
}

func (s *Service) marketPrices(ctx context.Context, invs []sqlc.Investment) (map[uuid.UUID]MarketPrice, error) {
	if s.market == nil {
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
	return s.market.MarketPrices(ctx, ids)
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
	realized, err := s.realized(ctx, spaceID, cc, rows)
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
		it.Realized = realized[r.ID]
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
	it, err := s.value(inv, prices, cc, s.now())
	if err != nil {
		return Item{}, err
	}
	realized, err := s.realized(ctx, inv.SpaceID, cc, []sqlc.Investment{inv})
	if err != nil {
		return Item{}, err
	}
	it.Realized = realized[inv.ID]
	return it, nil
}

func toNullDecimal(d *decimal.Decimal) decimal.NullDecimal {
	if d == nil {
		return decimal.NullDecimal{}
	}
	return decimal.NullDecimal{Decimal: *d, Valid: true}
}

// Create stores a validated investment spec. With spec.Funding the initial
// position is paid from the wallet in the same database transaction.
func (s *Service) Create(ctx context.Context, spaceID, userID uuid.UUID, spec Spec) (Item, error) {
	details, err := DetailsJSON(spec.Details)
	if err != nil {
		return Item{}, err
	}
	instrument, err := s.linkInstrument(ctx, spec, nil, spec.InstrumentID != nil)
	if err != nil {
		return Item{}, err
	}
	insert := func(q *sqlc.Queries) (sqlc.Investment, error) {
		return q.CreateInvestment(ctx, sqlc.CreateInvestmentParams{
			SpaceID: spaceID, AssetType: spec.AssetType, PriceMode: spec.PriceMode, Name: spec.Name, Symbol: spec.Symbol,
			Icon: spec.Icon, QuoteCurrency: spec.QuoteCurrency, Quantity: spec.Quantity, AvgBuyPrice: spec.AvgBuyPrice,
			ManualPrice: spec.ManualPrice, Fees: spec.Fees, ManualCurrentValue: toNullDecimal(spec.ManualCurrentValue),
			PurchaseDate: spec.PurchaseDate, EstimatedAnnualChangePct: spec.EstimatedAnnualChangePct,
			ManualFxRate: toNullDecimal(spec.ManualFxRate), Details: details, CreatedByID: userID,
			InstrumentID: instrument,
		})
	}

	var inv sqlc.Investment
	if spec.Funding == nil {
		if inv, err = insert(s.q); err != nil {
			return Item{}, err
		}
	} else {
		cc, err := s.currency.BuildContext(ctx, spaceID)
		if err != nil {
			return Item{}, err
		}
		err = db.WithTx(ctx, s.db, func(tx pgx.Tx) error {
			q := s.q.WithTx(tx)
			var err error
			if inv, err = insert(q); err != nil {
				return err
			}
			return s.fundInitialPosition(ctx, tx, q, spaceID, userID, inv, *spec.Funding, cc)
		})
		if err != nil {
			return Item{}, err
		}
	}
	s.firstPrice(ctx, inv)
	return s.item(ctx, inv)
}

// fundInitialPosition records the BUY that paid for a new investment's initial
// position: the position goes from nothing to what was just stored.
func (s *Service) fundInitialPosition(ctx context.Context, tx pgx.Tx, q *sqlc.Queries, spaceID, userID uuid.UUID, inv sqlc.Investment, f Funding, cc *currency.ConversionContext) error {
	w, err := loadWallet(ctx, q, spaceID, f.WalletID)
	if err != nil {
		return err
	}
	l := Leg{Quantity: inv.Quantity, UnitPrice: inv.AvgBuyPrice, Fee: inv.Fees}
	if !l.Gross().IsPositive() {
		return validationErr("funding", "needs a quantity and an average buy price to pay for")
	}
	total := l.Gross().Add(l.Fee)
	amount, rate, err := walletSide(w, strings.TrimSpace(inv.QuoteCurrency), total, f.Amount, nil, cc)
	if err != nil {
		return err
	}
	title := "Initial position " + inv.Name
	if inv.Symbol != nil {
		title = "Initial position " + *inv.Symbol
	}
	r := tradeResult{amount: amount, total: total, fxRate: rate, before: Position{}, after: PositionOf(inv)}
	p, err := tradeParams(spaceID, userID, inv, w, ledger.ActionBuy, l, r, s.now(), nil, &title)
	if err != nil {
		return err
	}
	t, err := q.CreateTransaction(ctx, p)
	if err != nil {
		return err
	}
	return ledger.Apply(ctx, tx, t)
}

// RealizedPL is the realized profit/loss of an investment's SELLs.
type RealizedPL struct {
	Amount decimal.Decimal  // in the investment's quote currency
	Base   *decimal.Decimal // converted to the space base currency; nil without an FX rate
	Sells  int
}

func (s *Service) realized(ctx context.Context, spaceID uuid.UUID, cc *currency.ConversionContext, invs []sqlc.Investment) (map[uuid.UUID]RealizedPL, error) {
	rows, err := s.q.RealizedPLByInvestment(ctx, spaceID)
	if err != nil {
		return nil, err
	}
	quote := map[uuid.UUID]string{}
	for _, i := range invs {
		quote[i.ID] = strings.TrimSpace(i.QuoteCurrency)
	}
	out := make(map[uuid.UUID]RealizedPL, len(rows))
	for _, r := range rows {
		if !r.InvestmentID.Valid {
			continue
		}
		rp := RealizedPL{Amount: r.RealizedPl, Sells: int(r.Sells)}
		if q, ok := quote[r.InvestmentID.UUID]; ok {
			if base, err := currency.Convert(r.RealizedPl, q, cc.BaseCurrency, cc); err == nil {
				rp.Base = &base
			}
		}
		out[r.InvestmentID.UUID] = rp
	}
	return out, nil
}

// linkInstrument decides which market instrument an auto-priced investment points at.
// An explicit instrument (from search) is validated and used; an AUTO investment
// without one resolves its symbol (highest-ranked coin / exact ticker). cur is the stored
// row on update (nil on create); relink forces a lookup even if cur is linked.
func (s *Service) linkInstrument(ctx context.Context, spec Spec, cur *sqlc.Investment, relink bool) (uuid.NullUUID, error) {
	var current uuid.NullUUID
	if cur != nil {
		current = cur.InstrumentID
	}
	if meta, _ := MetaFor(spec.AssetType); !meta.SupportsAutoPrice || s.market == nil {
		return current, nil
	}
	if spec.InstrumentID != nil { // explicit choice
		id, err := s.market.ResolveInstrument(ctx, spec.AssetType, spec.InstrumentID, "")
		return uuid.NullUUID{UUID: id, Valid: err == nil}, err
	}
	symbolChanged := cur != nil && cur.Symbol != nil && spec.Symbol != nil && !strings.EqualFold(*cur.Symbol, *spec.Symbol)
	switchedToAuto := cur != nil && cur.PriceMode != PriceModeAuto && spec.PriceMode == PriceModeAuto
	if spec.PriceMode == PriceModeAuto && (!current.Valid || relink || symbolChanged || switchedToAuto) {
		sym := ""
		if spec.Symbol != nil {
			sym = *spec.Symbol
		}
		id, err := s.market.ResolveInstrument(ctx, spec.AssetType, nil, sym)
		return uuid.NullUUID{UUID: id, Valid: err == nil}, err
	}
	return current, nil
}

// firstPrice gives a newly linked AUTO instrument its first price right away (best effort).
func (s *Service) firstPrice(ctx context.Context, inv sqlc.Investment) {
	if s.market != nil && inv.PriceMode == PriceModeAuto && inv.InstrumentID.Valid {
		s.market.EnsurePrice(ctx, inv.InstrumentID.UUID)
	}
}

// RefreshPrice refreshes the market price of an AUTO investment on demand.
func (s *Service) RefreshPrice(ctx context.Context, spaceID, id uuid.UUID) (Item, error) {
	inv, err := s.q.GetInvestment(ctx, sqlc.GetInvestmentParams{ID: id, SpaceID: spaceID})
	if errors.Is(err, pgx.ErrNoRows) {
		return Item{}, errInvestmentNF
	}
	if err != nil {
		return Item{}, err
	}
	meta, _ := MetaFor(inv.AssetType)
	switch {
	case !meta.SupportsAutoPrice:
		return Item{}, errAutoNotSupported
	case inv.PriceMode != PriceModeAuto:
		return Item{}, apperror.Unprocessable(CodePriceModeNotAuto, "Switch the investment to automatic pricing first")
	case !inv.InstrumentID.Valid || s.market == nil:
		return Item{}, apperror.Unprocessable(CodeInstrumentNotLinked, "This investment is not linked to a market instrument yet")
	}
	if err := s.market.RefreshPrice(ctx, inv.InstrumentID.UUID); err != nil {
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
		instrument, err := s.linkInstrument(ctx, spec, &cur, p.InstrumentID != nil)
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
		if err != nil {
			return err
		}
		if instrument != cur.InstrumentID {
			if err := q.SetInvestmentInstrument(ctx, sqlc.SetInvestmentInstrumentParams{ID: id, SpaceID: space.ID, InstrumentID: instrument}); err != nil {
				return err
			}
			out.InstrumentID = instrument
		}
		return nil
	})
	if err != nil {
		return Item{}, err
	}
	s.firstPrice(ctx, out)
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
