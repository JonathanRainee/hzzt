// Package marketdata owns market instruments (searchable coin/stock lists),
// the latest provider prices and the background jobs that keep them fresh.
package marketdata

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/robfig/cron/v3"
	"github.com/rs/zerolog"

	"github.com/JonathanRainee/hzzt/internal/db"
	"github.com/JonathanRainee/hzzt/internal/db/sqlc"
	"github.com/JonathanRainee/hzzt/internal/investments"
	"github.com/JonathanRainee/hzzt/internal/marketdata/coinmarketcap"
	"github.com/JonathanRainee/hzzt/internal/marketdata/jobs"
	"github.com/JonathanRainee/hzzt/internal/platform/apperror"
)

// Providers and kinds stored in market_instruments.
const (
	ProviderCMC = "CMC"
	KindCrypto  = "CRYPTO"
)

// Job names recorded in job_runs.
const (
	JobCoinMap     = "cmc.coin_map"
	JobCryptoPrice = "cmc.crypto_prices"
)

// Error codes owned by this package (see plan Appendix B).
const (
	CodeInstrumentNotFound = "INSTRUMENT_NOT_FOUND"
)

// DefaultRefreshMinutes is how often crypto prices are refreshed (CMC_REFRESH_MINUTES).
const DefaultRefreshMinutes = 10

// Crypto is the CoinMarketCap capability the service needs; *coinmarketcap.Client satisfies it.
type Crypto interface {
	Map(ctx context.Context, limit int) ([]coinmarketcap.Coin, error)
	Prices(ctx context.Context, ids []int64) (map[int64]coinmarketcap.Quote, int, error)
}

// DB is what the service needs; *pgxpool.Pool and pgx.Tx both satisfy it.
type DB interface {
	sqlc.DBTX
	db.TxStarter
}

// Options configures the service.
type Options struct {
	// Crypto may be nil when no CMC key is configured: the jobs then do nothing.
	Crypto         Crypto
	Log            zerolog.Logger
	RefreshMinutes int
	Now            func() time.Time
}

// Service holds market data logic.
type Service struct {
	db     DB
	q      *sqlc.Queries
	runner *jobs.Runner
	opts   Options
	now    func() time.Time
}

// NewService builds the service.
func NewService(database DB, o Options) *Service {
	now := o.Now
	if now == nil {
		now = time.Now
	}
	if o.RefreshMinutes <= 0 {
		o.RefreshMinutes = DefaultRefreshMinutes
	}
	return &Service{db: database, q: sqlc.New(database), runner: jobs.New(database, o.Log, now), opts: o, now: now}
}

// Enabled reports whether a provider is configured.
func (s *Service) Enabled() bool { return s.opts.Crypto != nil }

var errDisabled = apperror.Upstream("Market data is not configured on this server")

// --- coin map ---------------------------------------------------------------------

// SyncCoinMap refreshes the local coin list (used for search, costs no credits).
func (s *Service) SyncCoinMap(ctx context.Context) (jobs.Result, error) {
	if s.opts.Crypto == nil {
		return jobs.Result{}, errDisabled
	}
	return s.runner.Run(ctx, JobCoinMap, func(ctx context.Context, tx pgx.Tx) (int, error) {
		coins, err := s.opts.Crypto.Map(ctx, coinmarketcap.MapLimit)
		if err != nil {
			return 0, err
		}
		if len(coins) == 0 {
			return 0, errors.New("coin map came back empty")
		}
		p := sqlc.UpsertInstrumentsParams{Provider: ProviderCMC, Kind: KindCrypto, Currency: "USD"}
		for _, c := range coins {
			p.ExternalIds = append(p.ExternalIds, strconv.FormatInt(c.ID, 10))
			p.Symbols = append(p.Symbols, c.Symbol)
			p.Names = append(p.Names, c.Name)
			p.Ranks = append(p.Ranks, int32(c.Rank))
			p.Actives = append(p.Actives, c.Active)
		}
		if err := sqlc.New(tx).UpsertInstruments(ctx, p); err != nil {
			return 0, err
		}
		return len(coins), nil
	})
}

// EnsureCoinMap syncs the coin map only when there is none yet.
func (s *Service) EnsureCoinMap(ctx context.Context) error {
	n, err := s.q.CountInstruments(ctx, sqlc.CountInstrumentsParams{Provider: ProviderCMC, Kind: KindCrypto})
	if err != nil || n > 0 {
		return err
	}
	res, err := s.SyncCoinMap(ctx)
	if err != nil {
		return err
	}
	return res.ErrOrNil()
}

// --- search ------------------------------------------------------------------------

// Instrument is a searchable market instrument.
type Instrument struct {
	ID     uuid.UUID
	Symbol string
	Name   string
	Rank   *int
}

func toInstrument(m sqlc.MarketInstrument) Instrument {
	i := Instrument{ID: m.ID, Symbol: m.Symbol, Name: m.Name}
	if m.Rank != nil {
		r := int(*m.Rank)
		i.Rank = &r
	}
	return i
}

func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// SearchCrypto finds coins by symbol or name: exact symbol first, then symbol
// prefix, then name matches, each tier ordered by market-cap rank.
func (s *Service) SearchCrypto(ctx context.Context, query string, limit int) ([]Instrument, error) {
	q := strings.TrimSpace(query)
	if q == "" {
		return []Instrument{}, nil
	}
	rows, err := s.q.SearchInstruments(ctx, sqlc.SearchInstrumentsParams{
		Provider: ProviderCMC, Kind: KindCrypto, Q: q, Prefix: escapeLike(q), QEscaped: escapeLike(q), RowLimit: int32(limit),
	})
	if err != nil {
		return nil, err
	}
	out := make([]Instrument, 0, len(rows))
	for _, r := range rows {
		out = append(out, toInstrument(r))
	}
	return out, nil
}

// ResolveCrypto picks the coin for a crypto investment: the explicit instrument
// (from search) when given, otherwise the highest-ranked coin with that symbol.
func (s *Service) ResolveCrypto(ctx context.Context, explicit *uuid.UUID, symbol string) (uuid.UUID, error) {
	if explicit != nil {
		m, err := s.q.GetInstrument(ctx, *explicit)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && (m.Provider != ProviderCMC || m.Kind != KindCrypto || !m.Active)) {
			return uuid.Nil, apperror.Unprocessable(CodeInstrumentNotFound, "That coin is not available; pick one from the coin search")
		}
		if err != nil {
			return uuid.Nil, err
		}
		return m.ID, nil
	}
	m, err := s.q.BestInstrumentBySymbol(ctx, sqlc.BestInstrumentBySymbolParams{Provider: ProviderCMC, Kind: KindCrypto, Symbol: symbol})
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, apperror.Unprocessable(CodeInstrumentNotFound,
			fmt.Sprintf("No coin matches the symbol %s; pick one from the coin search and send its instrumentId", symbol))
	}
	if err != nil {
		return uuid.Nil, err
	}
	return m.ID, nil
}

// --- prices ------------------------------------------------------------------------

type target struct {
	id         uuid.UUID
	externalID int64
}

func (s *Service) targets(ctx context.Context, q *sqlc.Queries) ([]target, error) {
	rows, err := q.AutoPricedInstruments(ctx, sqlc.AutoPricedInstrumentsParams{Provider: ProviderCMC, Kind: KindCrypto})
	if err != nil {
		return nil, err
	}
	out := make([]target, 0, len(rows))
	for _, r := range rows {
		n, err := strconv.ParseInt(r.ExternalID, 10, 64)
		if err != nil {
			continue
		}
		out = append(out, target{id: r.ID, externalID: n})
	}
	return out, nil
}

// fetchAndStore gets prices for the targets and upserts them; it returns how many were stored.
func (s *Service) fetchAndStore(ctx context.Context, q *sqlc.Queries, ts []target) (int, error) {
	if len(ts) == 0 {
		return 0, nil
	}
	ids := make([]int64, len(ts))
	for i, t := range ts {
		ids[i] = t.externalID
	}
	quotes, _, err := s.opts.Crypto.Prices(ctx, ids)
	if err != nil {
		return 0, err
	}
	stored := 0
	for _, t := range ts {
		qt, ok := quotes[t.externalID]
		if !ok {
			continue
		}
		if err := q.UpsertMarketPrice(ctx, sqlc.UpsertMarketPriceParams{
			InstrumentID: t.id, Price: qt.Price, Currency: "USD", AsOf: qt.AsOf,
		}); err != nil {
			return stored, err
		}
		stored++
	}
	return stored, nil
}

// RefreshCryptoPrices is the scheduled job: the distinct coins of every live
// AUTO crypto investment, fetched in batches of 100 (one call each) and stored.
// On any provider error the cycle fails as a whole, the previous prices stay
// (they turn stale) and a FAILED job_runs row records why.
func (s *Service) RefreshCryptoPrices(ctx context.Context) (jobs.Result, error) {
	if s.opts.Crypto == nil {
		return jobs.Result{}, errDisabled
	}
	return s.runner.Run(ctx, JobCryptoPrice, func(ctx context.Context, tx pgx.Tx) (int, error) {
		q := sqlc.New(tx)
		ts, err := s.targets(ctx, q)
		if err != nil {
			return 0, err
		}
		return s.fetchAndStore(ctx, q, ts)
	})
}

// RefreshPrice fetches one instrument's price now (on-demand refresh).
func (s *Service) RefreshPrice(ctx context.Context, instrumentID uuid.UUID) error {
	if s.opts.Crypto == nil {
		return errDisabled
	}
	m, err := s.q.GetInstrument(ctx, instrumentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return apperror.Unprocessable(CodeInstrumentNotFound, "Instrument not found")
	}
	if err != nil {
		return err
	}
	ext, err := strconv.ParseInt(m.ExternalID, 10, 64)
	if err != nil {
		return fmt.Errorf("instrument %s has a non-numeric external id", m.ID)
	}
	n, err := s.fetchAndStore(ctx, s.q, []target{{id: m.ID, externalID: ext}})
	if err != nil {
		s.opts.Log.Warn().Err(err).Str("instrument", m.Symbol).Msg("on-demand price refresh failed")
		return apperror.Upstream("The price provider could not be reached; the last known price is kept")
	}
	if n == 0 {
		return apperror.Upstream("The price provider returned no price for this instrument")
	}
	return nil
}

// EnsurePrice fetches the price of a freshly linked instrument if it has none
// yet, so a new AUTO investment shows a market price right away. It is best
// effort: failures are logged and the next scheduled cycle retries.
func (s *Service) EnsurePrice(ctx context.Context, instrumentID uuid.UUID) {
	if s.opts.Crypto == nil {
		return
	}
	has, err := s.q.HasMarketPrice(ctx, instrumentID)
	if err != nil || has {
		return
	}
	if err := s.RefreshPrice(ctx, instrumentID); err != nil {
		s.opts.Log.Warn().Err(err).Msg("could not fetch the first price of a new instrument")
	}
}

// MarketPrices implements investments.PriceSource.
func (s *Service) MarketPrices(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]investments.MarketPrice, error) {
	rows, err := s.q.GetMarketPrices(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make(map[uuid.UUID]investments.MarketPrice, len(rows))
	for _, r := range rows {
		out[r.InstrumentID] = investments.MarketPrice{Price: r.Price, AsOf: r.AsOf}
	}
	return out, nil
}

// --- scheduling ---------------------------------------------------------------------

// StartJobs schedules the daily coin map sync and the price refresh (every
// CMC_REFRESH_MINUTES), and on boot syncs the coin map if it is empty and
// refreshes prices once. The returned function stops the scheduler.
func (s *Service) StartJobs(ctx context.Context, log zerolog.Logger) (stop func(), err error) {
	if s.opts.Crypto == nil {
		log.Info().Msg("market data disabled: CMC_API_KEY is not set")
		return func() {}, nil
	}
	run := func(name string, fn func(context.Context) error) {
		jctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
		defer cancel()
		if err := fn(jctx); err != nil {
			log.Error().Err(err).Str("job", name).Msg("market data job failed")
		}
	}
	c := cron.New(cron.WithChain(cron.SkipIfStillRunning(cron.DiscardLogger)))
	if _, err := c.AddFunc("@daily", func() {
		run(JobCoinMap, func(ctx context.Context) error { _, err := s.SyncCoinMap(ctx); return err })
	}); err != nil {
		return nil, err
	}
	if _, err := c.AddFunc(fmt.Sprintf("@every %dm", s.opts.RefreshMinutes), func() {
		run(JobCryptoPrice, func(ctx context.Context) error { _, err := s.RefreshCryptoPrices(ctx); return err })
	}); err != nil {
		return nil, err
	}
	c.Start()
	go func() { // never block startup on the provider
		run("cmc.boot", func(ctx context.Context) error {
			if err := s.EnsureCoinMap(ctx); err != nil {
				return err
			}
			_, err := s.RefreshCryptoPrices(ctx)
			return err
		})
	}()
	return func() { <-c.Stop().Done() }, nil
}
