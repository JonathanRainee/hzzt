//go:build e2e

package e2e

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/JonathanRainee/hzzt/internal/marketdata"
	"github.com/JonathanRainee/hzzt/internal/marketdata/massive"
)

func (s *TestServer) searchStocks(u session, query string) []coinBody {
	s.t.Helper()
	res := s.Get("/api/v1/market/stocks/search"+query, bearer(u.AccessToken))
	require.Equal(s.t, http.StatusOK, res.Status, "%s: %s", query, res.Body)
	var out []coinBody
	res.JSON(s.t, &out)
	return out
}

func stockInv(sym string, extra ...any) map[string]any {
	return clone(aapl, append([]any{"name", sym, "symbol", sym, "priceMode", "AUTO"}, extra...)...)
}

func etfInv(sym string, extra ...any) map[string]any {
	return clone(map[string]any{"assetType": "GLOBAL_ETF", "name": sym, "symbol": sym, "quantity": "3", "avgBuyPrice": "600",
		"manualPrice": "650", "priceMode": "AUTO", "details": map[string]any{"exchange": "NYSE"}}, extra...)
}

func (s *TestServer) localInstruments(provider string) int {
	s.t.Helper()
	var n int
	require.NoError(s.t, s.Tx.QueryRow(context.Background(), `SELECT count(*) FROM market_instruments WHERE provider = $1`, provider).Scan(&n))
	return n
}

func TestStockSearch(t *testing.T) {
	s := NewTestServer(t)
	u := s.register("jane@example.com")

	t.Run("unauthenticated and validation", func(t *testing.T) {
		assert.Equal(t, http.StatusUnauthorized, s.Get("/api/v1/market/stocks/search?q=apple").Status)
		for _, q := range []string{"", "?q=", "?q=%20", "?q=" + strings.Repeat("x", 61), "?q=a&kind=BOND", "?q=a&limit=0", "?q=a&limit=51", "?q=a&limit=x"} {
			assert.Equal(t, http.StatusBadRequest, s.Get("/api/v1/market/stocks/search"+q, bearer(u.AccessToken)).Status, q)
		}
	})

	t.Run("an empty local list asks Massive and caches the results as instruments", func(t *testing.T) {
		assert.Zero(t, s.localInstruments("MASSIVE"))
		got := s.searchStocks(u, "?q=apple")
		assert.Equal(t, []string{"AAPL", "APLE"}, sorted(coinSymbols(got)), "stocks only; delisted and ETFs excluded")
		assert.Equal(t, 1, s.Massive.SearchCalls)
		assert.Equal(t, 2, s.localInstruments("MASSIVE"), "cached as market_instruments")
		for _, c := range got {
			assert.Nil(t, c.Rank, "stocks are not ranked")
			assert.NotEmpty(t, c.InstrumentID)
		}
	})

	t.Run("the same query is not repeated within ten minutes", func(t *testing.T) {
		s.searchStocks(u, "?q=apple")
		s.searchStocks(u, "?q=APPLE")
		assert.Equal(t, 1, s.Massive.SearchCalls)
		s.Clock.Advance(11 * time.Minute)
		s.searchStocks(u, "?q=apple")
		assert.Equal(t, 2, s.Massive.SearchCalls, "asked again after the TTL")
	})

	t.Run("kind separates stocks from ETFs", func(t *testing.T) {
		etfs := s.searchStocks(u, "?q=s%26p&kind=etf")
		assert.ElementsMatch(t, []string{"VOO", "SPY"}, coinSymbols(etfs))
		assert.Empty(t, s.searchStocks(u, "?q=s%26p&kind=STOCK"))
		assert.Contains(t, s.Massive.SearchQueries, "ETF|s&p")
	})

}

func TestStockSearchRanking(t *testing.T) {
	s2 := NewTestServer(t)
	u2 := s2.register("jane@example.com")
	got := s2.searchStocks(u2, "?q=aapl")
	require.NotEmpty(t, got)
	assert.Equal(t, "AAPL", got[0].Symbol)
}

func TestStockSearchPrefersLocalHits(t *testing.T) {
	s2 := NewTestServer(t)
	u2 := s2.register("jane@example.com")
	_, err := s2.Tx.Exec(context.Background(), `
			INSERT INTO market_instruments (provider, external_id, symbol, name, kind, currency)
			SELECT 'MASSIVE', 'ZT' || g, 'ZT' || g, 'Zeta Corp ' || g, 'STOCK', 'USD' FROM generate_series(1, 6) g`)
	require.NoError(t, err)
	got := s2.searchStocks(u2, "?q=zeta")
	assert.Len(t, got, 6)
	assert.Zero(t, s2.Massive.SearchCalls, "5 or more local hits are served locally")
	assert.Len(t, s2.searchStocks(u2, "?q=zeta&limit=3"), 3)
}

func TestStockSearchSurvivesAThrottledProvider(t *testing.T) {
	s2 := NewTestServer(t)
	u2 := s2.register("jane@example.com")
	_, err := s2.Tx.Exec(context.Background(), `INSERT INTO market_instruments (provider, external_id, symbol, name, kind, currency) VALUES ('MASSIVE', 'MSFT', 'MSFT', 'Microsoft Corporation', 'STOCK', 'USD')`)
	require.NoError(t, err)

	s2.Massive.SearchErr = massive.ErrThrottled
	got := s2.searchStocks(u2, "?q=micro")
	assert.Equal(t, []string{"MSFT"}, coinSymbols(got), "throttled: local only, no error")
	s2.Massive.SearchErr = nil
	s2.searchStocks(u2, "?q=micro")
	assert.Equal(t, 2, s2.Massive.SearchCalls, "a throttled attempt is not remembered, so the next search tries again")

	s2.Massive.SearchErr = massive.ErrRateLimited
	got = s2.searchStocks(u2, "?q=tesla")
	assert.Empty(t, got)
	calls := s2.Massive.SearchCalls
	s2.Massive.SearchErr = nil
	s2.searchStocks(u2, "?q=tesla")
	assert.Equal(t, calls, s2.Massive.SearchCalls, "a failing provider is not hammered: the attempt is remembered")
}

func TestStockSearchIsRateLimitedPerUser(t *testing.T) {
	s2 := NewTestServerWithMarketLimits(t, marketdata.DefaultLimits())
	u2 := s2.register("jane@example.com")
	for i := 0; i < 20; i++ {
		require.Equal(t, http.StatusOK, s2.Get("/api/v1/market/stocks/search?q=apple", bearer(u2.AccessToken)).Status, "request %d", i+1)
	}
	res := s2.Get("/api/v1/market/stocks/search?q=apple", bearer(u2.AccessToken))
	require.Equal(t, http.StatusTooManyRequests, res.Status)
	assert.Equal(t, "RATE_LIMITED", errCode(t, res))
	other := s2.register("other@example.com")
	assert.Equal(t, http.StatusOK, s2.Get("/api/v1/market/stocks/search?q=apple", bearer(other.AccessToken)).Status, "per user")
}

func TestStockSearchWithoutAMassiveKey(t *testing.T) {
	s2 := NewTestServer(t)
	u2 := s2.register("jane@example.com")
	off := marketdata.NewService(s2.Tx, marketdata.Options{})
	got, err := off.SearchStocks(context.Background(), "apple", "STOCK", 10)
	require.NoError(t, err)
	assert.Empty(t, got)
	_ = u2
}

func sorted(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

func TestStockInvestmentsLinkAndGetTheLastClose(t *testing.T) {
	ctx := context.Background()
	s := NewTestServer(t)
	e := newInvEnv(s)
	e.setSpace(map[string]any{"baseCurrency": "USD"})
	// Wednesday 09:00 New York: today's bar does not exist yet, yesterday's does
	s.Clock.Set(time.Date(2026, 11, 18, 14, 0, 0, 0, time.UTC))
	s.Massive.Strict = true
	s.Massive.NotReleasedFrom = "2026-11-18" // the free plan answers 403 for the current day
	s.Massive.DayCloses = map[string]map[string]string{"2026-11-17": {"AAPL": "351.91", "VOO": "673.19", "MSFT": "500"}}

	t.Run("acceptance: AAPL and VOO in AUTO mode show the last close", func(t *testing.T) {
		aaplInv := e.must(stockInv("AAPL"))
		require.NotNil(t, aaplInv.InstrumentID)
		v := aaplInv.Valuation
		assert.Equal(t, "MARKET", v.PriceSource)
		assert.True(t, dec("351.91").Equal(dec(v.Price)), v.Price)
		assert.True(t, dec("3519.1").Equal(dec(v.MarketValue)), "10 × 351.91")
		require.NotNil(t, v.PriceAsOf)
		assert.Equal(t, "2026-11-17T21:00:00Z", *v.PriceAsOf, "as of the previous session's close")
		assert.False(t, v.IsStale)
		assert.Equal(t, []string{"2026-11-18", "2026-11-17"}, s.Massive.GroupedDates, "today had no bar yet; the previous close was used")

		voo := e.must(etfInv("VOO"))
		assert.Equal(t, "MARKET", voo.Valuation.PriceSource)
		assert.True(t, dec("673.19").Equal(dec(voo.Valuation.Price)))
		assert.True(t, dec("2019.57").Equal(dec(voo.Valuation.MarketValue)), "3 × 673.19")
		assert.NotNil(t, voo.InstrumentID)
	})

	t.Run("a second holding of the same ticker shares the instrument and its price", func(t *testing.T) {
		calls := s.Massive.GroupedCalls
		again := e.must(stockInv("aapl", "name", "Apple (second broker)"))
		assert.Equal(t, "MARKET", again.Valuation.PriceSource)
		assert.Equal(t, calls, s.Massive.GroupedCalls, "no new fetch: the instrument already has a price")
		var n int
		require.NoError(t, s.Tx.QueryRow(ctx, `SELECT count(DISTINCT instrument_id) FROM investments WHERE symbol = 'AAPL'`).Scan(&n))
		assert.Equal(t, 1, n)
	})

	t.Run("one daily job fetches every referenced ticker in a single call", func(t *testing.T) {
		e.must(stockInv("MSFT"))
		s.Massive.GroupedCalls, s.Massive.GroupedDates = 0, nil
		s.Massive.NotReleasedFrom = "" // the day's bars are out by the time the evening job runs
		s.Massive.DayCloses["2026-11-18"] = map[string]string{"AAPL": "355", "VOO": "675", "MSFT": "505"}
		s.Clock.Set(time.Date(2026, 11, 18, 22, 30, 0, 0, time.UTC)) // 17:30 New York, after the close
		res, err := s.Market.RefreshStockPrices(ctx)
		require.NoError(t, err)
		require.Equal(t, "SUCCESS", res.Status, "%v", res.Err)
		assert.Equal(t, 3, res.Items, "AAPL, VOO and MSFT")
		assert.Equal(t, 1, s.Massive.GroupedCalls, "EOD mode: one call returns the whole market")
		assert.Equal(t, []string{"2026-11-18"}, s.Massive.GroupedDates)

		l := e.list("?assetType=US_STOCK")
		for _, it := range l.Items {
			if it.Symbol != nil && *it.Symbol == "AAPL" {
				assert.True(t, dec("355").Equal(dec(it.Valuation.Price)))
			}
		}
		runs := s.jobRuns(marketdata.JobStockEOD)
		require.NotEmpty(t, runs)
		last := runs[len(runs)-1]
		assert.Equal(t, jobRow{Name: marketdata.JobStockEOD, Status: "SUCCESS", Items: 3}, last)
	})

	t.Run("acceptance: a weekend run uses Friday's close", func(t *testing.T) {
		s.Massive.DayCloses["2026-11-20"] = map[string]string{"AAPL": "360", "VOO": "680", "MSFT": "510"} // Friday
		s.Clock.Set(time.Date(2026, 11, 22, 22, 30, 0, 0, time.UTC))                                      // Sunday evening
		s.Massive.GroupedCalls, s.Massive.GroupedDates = 0, nil
		res, err := s.Market.RefreshStockPrices(ctx)
		require.NoError(t, err)
		require.Equal(t, "SUCCESS", res.Status, "%v", res.Err)
		assert.Equal(t, []string{"2026-11-20"}, s.Massive.GroupedDates, "Sunday and Saturday were skipped without spending calls")
		var found bool
		for _, it := range e.list("?assetType=US_STOCK").Items {
			if it.Symbol != nil && *it.Symbol == "AAPL" {
				found = true
				assert.True(t, dec("360").Equal(dec(it.Valuation.Price)), "Friday's close")
				assert.Equal(t, "2026-11-20T21:00:00Z", *it.Valuation.PriceAsOf)
				assert.False(t, it.Valuation.IsStale, "a weekend-old close is not stale")
			}
		}
		assert.True(t, found)
	})

	t.Run("staleness: stale only after 4 days; outages keep the last price", func(t *testing.T) {
		s.Massive.Err = massive.ErrRateLimited
		s.Clock.Set(time.Date(2026, 11, 24, 22, 30, 0, 0, time.UTC)) // Tuesday: Friday's close is 4 days + 1h old
		res, err := s.Market.RefreshStockPrices(ctx)
		require.NoError(t, err)
		assert.Equal(t, "FAILED", res.Status)
		runs := s.jobRuns(marketdata.JobStockEOD)
		assert.Equal(t, "FAILED", runs[len(runs)-1].Status)
		require.NotNil(t, runs[len(runs)-1].Error)
		assert.Contains(t, *runs[len(runs)-1].Error, "rate limited")

		for _, it := range e.list("?assetType=US_STOCK").Items {
			if it.Symbol != nil && *it.Symbol == "AAPL" {
				assert.True(t, dec("360").Equal(dec(it.Valuation.Price)), "the last known price is kept")
				assert.True(t, it.Valuation.IsStale, "older than 4 days")
			}
		}
		s.Clock.Set(time.Date(2026, 11, 24, 20, 0, 0, 0, time.UTC)) // 3d 23h after the close
		for _, it := range e.list("?assetType=US_STOCK").Items {
			if it.Symbol != nil && *it.Symbol == "AAPL" {
				assert.False(t, it.Valuation.IsStale, "just under 4 days")
			}
		}
		s.Massive.Err = nil
	})

	t.Run("resolution rules", func(t *testing.T) {
		// unknown ticker; an ETF ticker as a stock; a stock ticker as an ETF
		for name, body := range map[string]map[string]any{
			"unknown ticker":  stockInv("NOPE"),
			"ETF as a stock":  stockInv("VOO"),
			"stock as an ETF": etfInv("AAPL"),
			"delisted ticker": stockInv("OLDC"),
		} {
			res := e.post(body)
			require.Equal(t, http.StatusUnprocessableEntity, res.Status, "%s: %s", name, res.Body)
			assert.Equal(t, "INSTRUMENT_NOT_FOUND", errCode(t, res), name)
		}
		u := e.u
		var aaplID, vooID string
		for _, c := range s.searchStocks(u, "?q=aapl") {
			if c.Symbol == "AAPL" {
				aaplID = c.InstrumentID
			}
		}
		for _, c := range s.searchStocks(u, "?q=voo&kind=ETF") {
			if c.Symbol == "VOO" {
				vooID = c.InstrumentID
			}
		}
		require.NotEmpty(t, aaplID)
		require.NotEmpty(t, vooID)
		got := e.must(stockInv("AAPL", "instrumentId", aaplID))
		assert.Equal(t, aaplID, *got.InstrumentID)
		res := e.post(stockInv("AAPL", "instrumentId", vooID))
		require.Equal(t, http.StatusUnprocessableEntity, res.Status, "an ETF instrument cannot price a stock")
		res = e.post(etfInv("VOO", "instrumentId", aaplID))
		require.Equal(t, http.StatusUnprocessableEntity, res.Status)
		assert.Equal(t, "INSTRUMENT_NOT_FOUND", errCode(t, res))
	})

	t.Run("MANUAL stocks need no instrument; ID_STOCK still has no AUTO", func(t *testing.T) {
		m := e.must(clone(aapl, "name", "Manual AAPL"))
		assert.Nil(t, m.InstrumentID)
		assert.Equal(t, "MANUAL", m.Valuation.PriceSource)
		res := e.post(clone(bbca, "priceMode", "AUTO"))
		require.Equal(t, http.StatusUnprocessableEntity, res.Status)
		assert.Equal(t, "AUTO_PRICE_NOT_SUPPORTED", errCode(t, res))

		up := e.patch(m.ID, map[string]any{"priceMode": "AUTO"})
		require.Equal(t, http.StatusOK, up.Status, string(up.Body))
		var got investmentBody
		up.JSON(t, &got)
		assert.NotNil(t, got.InstrumentID, "switching to AUTO links the ticker")
	})

	t.Run("on-demand refresh works for stocks too", func(t *testing.T) {
		s.Massive.DayCloses["2026-11-24"] = map[string]string{"AAPL": "370"}
		s.Clock.Set(time.Date(2026, 11, 24, 23, 0, 0, 0, time.UTC))
		inv := e.must(stockInv("AAPL", "name", "Refreshable"))
		res := s.Do(http.MethodPost, invURL(e.sid, "/", inv.ID, "/refresh-price"), nil, bearer(e.u.AccessToken))
		require.Equal(t, http.StatusOK, res.Status, string(res.Body))
		var got investmentBody
		res.JSON(t, &got)
		assert.True(t, dec("370").Equal(dec(got.Valuation.Price)))
	})
}

func TestSwitchingToSnapshotModeNeedsNoCodeChange(t *testing.T) {
	ctx := context.Background()
	s := NewTestServer(t)
	e := newInvEnv(s)
	e.setSpace(map[string]any{"baseCurrency": "USD"})
	// the same dependencies as the EOD service; only the strategy differs (MASSIVE_MODE=SNAPSHOT)
	strategy, err := marketdata.NewStrategy("SNAPSHOT", s.Massive)
	require.NoError(t, err)
	snap := marketdata.NewService(s.Tx, marketdata.Options{Crypto: s.CMC, Stocks: s.Massive, Strategy: strategy, Log: zerolog.Nop(), Now: s.Clock.Now})
	s.Investments.SetMarket(snap)

	open := time.Date(2026, 11, 18, 16, 0, 0, 0, time.UTC) // 11:00 New York, market open
	s.Clock.Set(open)
	s.Massive.SnapshotAsOf = open
	inv := e.must(stockInv("AAPL"))

	t.Run("the first price comes from the snapshot endpoint", func(t *testing.T) {
		assert.Equal(t, 1, s.Massive.SnapshotCalls)
		assert.Zero(t, s.Massive.GroupedCalls, "no end-of-day call in snapshot mode")
		assert.True(t, dec("352.10").Equal(dec(inv.Valuation.Price)), inv.Valuation.Price)
		assert.Equal(t, "MARKET", inv.Valuation.PriceSource)
		assert.False(t, inv.Valuation.IsStale)
	})

	t.Run("stale after 30 minutes during market hours", func(t *testing.T) {
		s.Clock.Set(open.Add(29 * time.Minute))
		assert.False(t, e.get(inv.ID).Valuation.IsStale)
		s.Clock.Set(open.Add(31 * time.Minute))
		assert.True(t, e.get(inv.ID).Valuation.IsStale)
	})

	t.Run("a scheduled run refreshes the price during market hours", func(t *testing.T) {
		s.Massive.Snaps["AAPL"] = "353.75"
		now := open.Add(35 * time.Minute)
		s.Clock.Set(now)
		s.Massive.SnapshotAsOf = now
		s.Massive.SnapshotCalls = 0
		res, err := snap.RefreshStockPrices(ctx)
		require.NoError(t, err)
		require.Equal(t, "SUCCESS", res.Status, "%v", res.Err)
		assert.Equal(t, 1, res.Items)
		assert.Equal(t, 1, s.Massive.SnapshotCalls)
		got := e.get(inv.ID)
		assert.True(t, dec("353.75").Equal(dec(got.Valuation.Price)))
		assert.False(t, got.Valuation.IsStale)
		runs := s.jobRuns(marketdata.JobStockSnapshot)
		assert.Equal(t, "SUCCESS", runs[len(runs)-1].Status)
	})

	t.Run("outside market hours a run does nothing and the last price stays valid", func(t *testing.T) {
		s.Massive.SnapshotCalls = 0
		s.Clock.Set(time.Date(2026, 11, 18, 23, 0, 0, 0, time.UTC)) // after the close
		res, err := snap.RefreshStockPrices(ctx)
		require.NoError(t, err)
		assert.Equal(t, "SUCCESS", res.Status)
		assert.Zero(t, res.Items)
		assert.Zero(t, s.Massive.SnapshotCalls, "no calls outside the session")
		assert.False(t, e.get(inv.ID).Valuation.IsStale, "no new prices exist overnight, so the close is not stale")
		s.Clock.Set(time.Date(2026, 11, 14, 16, 0, 0, 0, time.UTC)) // a Saturday afternoon, long after the price
		assert.False(t, e.get(inv.ID).Valuation.IsStale)
	})

	t.Run("a free-tier key gets a clear error", func(t *testing.T) {
		s.Clock.Set(open.Add(2 * time.Hour))
		s.Massive.SnapErr = massive.ErrNotEntitled
		res, err := snap.RefreshStockPrices(ctx)
		require.NoError(t, err)
		assert.Equal(t, "FAILED", res.Status)
		runs := s.jobRuns(marketdata.JobStockSnapshot)
		last := runs[len(runs)-1]
		require.NotNil(t, last.Error)
		assert.Contains(t, *last.Error, "paid Massive plan")
		assert.True(t, dec("353.75").Equal(dec(e.get(inv.ID).Valuation.Price)), "the last price is kept")
	})
}

func TestStockJobsAreSingleInstance(t *testing.T) {
	ctx := context.Background()
	s := NewTestServer(t)
	e := newInvEnv(s)
	e.must(stockInv("AAPL"))
	other, err := testPool.Acquire(ctx)
	require.NoError(t, err)
	defer other.Release()
	tx, err := other.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1::text, 0))`, marketdata.JobStockEOD)
	require.NoError(t, err)

	s.Massive.GroupedCalls = 0
	res, err := s.Market.RefreshStockPrices(ctx)
	require.NoError(t, err)
	assert.Equal(t, "SKIPPED", res.Status)
	assert.Zero(t, s.Massive.GroupedCalls)
}

func TestStockPricesWithoutAMassiveKey(t *testing.T) {
	ctx := context.Background()
	s := NewTestServer(t)
	off := marketdata.NewService(s.Tx, marketdata.Options{})
	_, err := off.RefreshStockPrices(ctx)
	require.Error(t, err)
	id, err := off.ResolveInstrument(ctx, "US_STOCK", nil, "AAPL")
	require.Error(t, err, "nothing cached and no provider: %v", id)
	assert.Equal(t, uuid.Nil, id)
}
