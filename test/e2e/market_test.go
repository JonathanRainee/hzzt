//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/JonathanRainee/hzzt/internal/investments"
	"github.com/JonathanRainee/hzzt/internal/marketdata"
	"github.com/JonathanRainee/hzzt/internal/marketdata/coinmarketcap"
)

type coinBody struct {
	InstrumentID string `json:"instrumentId"`
	Symbol       string `json:"symbol"`
	Name         string `json:"name"`
	Rank         *int   `json:"rank"`
}

func (s *TestServer) searchCoins(u session, query string) []coinBody {
	s.t.Helper()
	res := s.Get("/api/v1/market/crypto/search"+query, bearer(u.AccessToken))
	require.Equal(s.t, http.StatusOK, res.Status, "%s: %s", query, res.Body)
	var out []coinBody
	res.JSON(s.t, &out)
	return out
}

func coinSymbols(cs []coinBody) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.Symbol
	}
	return out
}

type jobRow struct {
	Name, Status string
	Items        int
	Error        *string
}

func (s *TestServer) jobRuns(name string) []jobRow {
	s.t.Helper()
	rows, err := s.Tx.Query(context.Background(),
		`SELECT name, status, items_processed, error FROM job_runs WHERE name = $1 ORDER BY ctid`, name)
	require.NoError(s.t, err)
	defer rows.Close()
	var out []jobRow
	for rows.Next() {
		var j jobRow
		require.NoError(s.t, rows.Scan(&j.Name, &j.Status, &j.Items, &j.Error))
		out = append(out, j)
	}
	return out
}

func TestCoinSearch(t *testing.T) {
	s := NewTestServer(t)
	u := s.register("jane@example.com")

	t.Run("unauthenticated and validation", func(t *testing.T) {
		assert.Equal(t, http.StatusUnauthorized, s.Get("/api/v1/market/crypto/search?q=btc").Status)
		for _, q := range []string{"", "?q=", "?q=%20%20", "?q=" + strings.Repeat("x", 61), "?q=btc&limit=0", "?q=btc&limit=51", "?q=btc&limit=x"} {
			assert.Equal(t, http.StatusBadRequest, s.Get("/api/v1/market/crypto/search"+q, bearer(u.AccessToken)).Status, q)
		}
	})

	t.Run("ranked: exact symbol, then prefix, then name; tiers by market-cap rank", func(t *testing.T) {
		got := s.searchCoins(u, "?q=btc")
		assert.Equal(t, []string{"BTC", "BTC"}, coinSymbols(got), "two coins share the symbol, best rank first")
		assert.Equal(t, 1, *got[0].Rank)
		assert.Equal(t, "Bitcoin", got[0].Name)
		assert.Equal(t, "Bitcoin Clone", got[1].Name)
		assert.NotEmpty(t, got[0].InstrumentID)

		byName := s.searchCoins(u, "?q=bitcoin")
		assert.Equal(t, []string{"BTC", "WBTC", "BCH", "BTC"}, coinSymbols(byName), "name matches in rank order")

		// a symbol match outranks name matches: "SOL" is the exact symbol, 'Solana' only matches by name
		mixed := s.searchCoins(u, "?q=sol")
		assert.Equal(t, "SOL", mixed[0].Symbol)
	})

	t.Run("case-insensitive, by name, by prefix", func(t *testing.T) {
		assert.Equal(t, []string{"ETH", "USDT"}, coinSymbols(s.searchCoins(u, "?q=ETH")), "exact symbol first, then 'Tether' by name")
		assert.Equal(t, []string{"ETH"}, coinSymbols(s.searchCoins(u, "?q=ethereum")))
		assert.Equal(t, []string{"SOL"}, coinSymbols(s.searchCoins(u, "?q=sola")))
		assert.Equal(t, []string{"USDT"}, coinSymbols(s.searchCoins(u, "?q=tether")))
		assert.Equal(t, []string{"BTC", "BTC"}, coinSymbols(s.searchCoins(u, "?q=BT")), "symbol prefix")
	})

	t.Run("inactive coins and wildcard characters", func(t *testing.T) {
		assert.Empty(t, s.searchCoins(u, "?q=OLD"), "delisted coins are not offered")
		assert.Equal(t, []string{"P_C"}, coinSymbols(s.searchCoins(u, "?q=P_C")))
		assert.Equal(t, []string{"P_C"}, coinSymbols(s.searchCoins(u, "?q="+"100%25")), "% is literal")
		assert.Equal(t, []string{"P_C"}, coinSymbols(s.searchCoins(u, "?q=%25")), "a lone % is literal: only '100% Coin' contains one")
		assert.Empty(t, s.searchCoins(u, "?q=%25%25%25"), "%%% is not a wildcard")
		assert.Empty(t, s.searchCoins(u, "?q=_"), "a lone _ is literal too")
		assert.Empty(t, s.searchCoins(u, "?q=zzzz"))
	})

	t.Run("limit", func(t *testing.T) {
		assert.Len(t, s.searchCoins(u, "?q=bitcoin&limit=2"), 2)
		assert.Len(t, s.searchCoins(u, "?q=bitcoin&limit=50"), 4)
	})

	t.Run("served locally: no provider call", func(t *testing.T) {
		before := s.CMC.PriceCalls + s.CMC.MapCalls
		s.searchCoins(u, "?q=btc")
		assert.Equal(t, before, s.CMC.PriceCalls+s.CMC.MapCalls)
	})
}

func TestCoinMapSync(t *testing.T) {
	ctx := context.Background()
	s := NewTestServer(t)
	u := s.register("jane@example.com")

	t.Run("ensure only syncs when empty", func(t *testing.T) {
		require.NoError(t, s.Market.EnsureCoinMap(ctx))
		assert.Zero(t, s.CMC.MapCalls, "the harness already synced")
	})

	t.Run("sync is idempotent and follows rank and delisting changes", func(t *testing.T) {
		var before int
		require.NoError(t, s.Tx.QueryRow(ctx, `SELECT count(*) FROM market_instruments`).Scan(&before))
		s.CMC.Coins[3].Rank = 1       // SOL jumps to #1
		s.CMC.Coins[1].Active = false // ETH delisted
		res, err := s.Market.SyncCoinMap(ctx)
		require.NoError(t, err)
		assert.Equal(t, "SUCCESS", res.Status)
		assert.Equal(t, len(s.CMC.Coins), res.Items)
		var after int
		require.NoError(t, s.Tx.QueryRow(ctx, `SELECT count(*) FROM market_instruments`).Scan(&after))
		assert.Equal(t, before, after, "upserted, not duplicated")
		assert.NotContains(t, coinSymbols(s.searchCoins(u, "?q=ETH")), "ETH", "the delisted coin is no longer offered")
		got := s.searchCoins(u, "?q=sol")
		require.NotEmpty(t, got)
		assert.Equal(t, 1, *got[0].Rank)
		runs := s.jobRuns(marketdata.JobCoinMap)
		require.NotEmpty(t, runs)
		assert.Equal(t, "SUCCESS", runs[len(runs)-1].Status)
	})

	t.Run("an empty or failing map is a FAILED run and changes nothing", func(t *testing.T) {
		before := s.searchCoins(u, "?q=btc")
		s.CMC.Err = coinmarketcap.ErrRateLimited
		res, err := s.Market.SyncCoinMap(ctx)
		require.NoError(t, err, "a failing job is reported in the result, not as an infrastructure error")
		assert.Equal(t, "FAILED", res.Status)
		s.CMC.Err = nil
		full := s.CMC.Coins
		s.CMC.Coins = nil
		res, err = s.Market.SyncCoinMap(ctx)
		require.NoError(t, err)
		assert.Equal(t, "FAILED", res.Status)
		s.CMC.Coins = full
		assert.Equal(t, coinSymbols(before), coinSymbols(s.searchCoins(u, "?q=btc")))
		runs := s.jobRuns(marketdata.JobCoinMap)
		assert.Equal(t, "FAILED", runs[len(runs)-1].Status)
		require.NotNil(t, runs[len(runs)-1].Error)
	})
}

func cryptoInv(sym string, extra ...any) map[string]any {
	return clone(map[string]any{"assetType": "CRYPTO", "name": sym, "symbol": sym, "quantity": "0.5",
		"avgBuyPrice": "60000", "manualPrice": "65000", "priceMode": "AUTO"}, extra...)
}

func TestAutoCryptoInvestmentLinksAndPrices(t *testing.T) {
	s := NewTestServer(t)
	e := newInvEnv(s)
	e.setSpace(map[string]any{"baseCurrency": "USD"})
	coinID := func(symbol string, rank int) string {
		for _, c := range s.searchCoins(e.u, "?q="+symbol) {
			if c.Symbol == symbol && c.Rank != nil && *c.Rank == rank {
				return c.InstrumentID
			}
		}
		t.Fatalf("coin %s #%d not found", symbol, rank)
		return ""
	}
	btc, clone3000 := coinID("BTC", 1), coinID("BTC", 3000)

	t.Run("symbol resolves to the highest-ranked coin and the first price is fetched at once", func(t *testing.T) {
		calls := s.CMC.PriceCalls
		got := e.must(cryptoInv("BTC"))
		require.NotNil(t, got.InstrumentID)
		assert.Equal(t, btc, *got.InstrumentID, "BTC → the #1 Bitcoin, not the clone")
		assert.Equal(t, "MARKET", got.Valuation.PriceSource)
		assert.True(t, dec("63000").Equal(dec(got.Valuation.Price)))
		assert.True(t, dec("31500").Equal(dec(got.Valuation.MarketValue)), "0.5 × 63000")
		assert.True(t, dec("1500").Equal(dec(got.Valuation.UnrealizedPL)), "31500 − 30000")
		require.NotNil(t, got.Valuation.PriceAsOf)
		assert.False(t, got.Valuation.IsStale)
		assert.Equal(t, calls+1, s.CMC.PriceCalls, "one immediate fetch for the new instrument")
		assert.True(t, dec("65000").Equal(dec(got.ManualPrice)), "the manual price is kept as the fallback")
	})

	t.Run("a second investment in the same coin does not refetch", func(t *testing.T) {
		calls := s.CMC.PriceCalls
		got := e.must(cryptoInv("btc", "name", "Cold storage BTC"))
		assert.Equal(t, btc, *got.InstrumentID)
		assert.Equal(t, calls, s.CMC.PriceCalls)
		assert.Equal(t, "MARKET", got.Valuation.PriceSource)
	})

	t.Run("an explicit instrumentId from search wins", func(t *testing.T) {
		got := e.must(cryptoInv("BTC", "instrumentId", clone3000))
		assert.Equal(t, clone3000, *got.InstrumentID)
		assert.Equal(t, "MANUAL", got.Valuation.PriceSource, "the clone has no price from the provider: falls back to the manual price")
		assert.True(t, dec("65000").Equal(dec(got.Valuation.Price)))
	})

	t.Run("unknown symbol or instrument → 422 INSTRUMENT_NOT_FOUND", func(t *testing.T) {
		for name, body := range map[string]map[string]any{
			"unknown symbol":    cryptoInv("NOPE"),
			"random instrument": cryptoInv("BTC", "instrumentId", uuid.NewString()),
			"delisted coin":     cryptoInv("OLD"),
		} {
			res := e.post(body)
			require.Equal(t, http.StatusUnprocessableEntity, res.Status, "%s: %s", name, res.Body)
			assert.Equal(t, "INSTRUMENT_NOT_FOUND", errCode(t, res), name)
		}
		var n int
		require.NoError(t, s.Tx.QueryRow(context.Background(), `SELECT count(*) FROM investments WHERE symbol = 'NOPE'`).Scan(&n))
		assert.Zero(t, n, "nothing is stored on failure")
	})

	t.Run("MANUAL crypto needs no link", func(t *testing.T) {
		got := e.must(cryptoInv("NOPE", "priceMode", "MANUAL"))
		assert.Nil(t, got.InstrumentID)
		assert.Equal(t, "MANUAL", got.Valuation.PriceSource)
	})

	t.Run("switching to AUTO links on update; changing the symbol relinks", func(t *testing.T) {
		m := e.must(cryptoInv("ETH", "priceMode", "MANUAL"))
		assert.Nil(t, m.InstrumentID)
		res := e.patch(m.ID, map[string]any{"priceMode": "AUTO"})
		require.Equal(t, http.StatusOK, res.Status, string(res.Body))
		var got investmentBody
		res.JSON(t, &got)
		require.NotNil(t, got.InstrumentID)
		assert.Equal(t, "MARKET", got.Valuation.PriceSource)
		assert.True(t, dec("1800").Equal(dec(got.Valuation.Price)))

		res = e.patch(m.ID, map[string]any{"symbol": "SOL"})
		require.Equal(t, http.StatusOK, res.Status, string(res.Body))
		var relinked investmentBody
		res.JSON(t, &relinked)
		assert.NotEqual(t, *got.InstrumentID, *relinked.InstrumentID)
		assert.True(t, dec("150").Equal(dec(relinked.Valuation.Price)), "now priced as SOL")

		res = e.patch(m.ID, map[string]any{"symbol": "NOPE"})
		require.Equal(t, http.StatusUnprocessableEntity, res.Status)
		assert.Equal(t, "INSTRUMENT_NOT_FOUND", errCode(t, res))
		assert.Equal(t, "SOL", *e.get(m.ID).Symbol, "a failed update changes nothing")

		res = e.patch(m.ID, map[string]any{"instrumentId": btc})
		require.Equal(t, http.StatusOK, res.Status)
		res.JSON(t, &relinked)
		assert.Equal(t, btc, *relinked.InstrumentID, "explicit relink")
	})

	t.Run("instrumentId must match the asset type; unsupported types reject it", func(t *testing.T) {
		// a coin is not a stock
		res := e.post(clone(aapl, "instrumentId", btc))
		require.Equal(t, http.StatusUnprocessableEntity, res.Status, string(res.Body))
		assert.Equal(t, "INSTRUMENT_NOT_FOUND", errCode(t, res))
		stock := e.must(aapl)
		res = e.patch(stock.ID, map[string]any{"instrumentId": btc})
		require.Equal(t, http.StatusUnprocessableEntity, res.Status)
		assert.Equal(t, "INSTRUMENT_NOT_FOUND", errCode(t, res))

		// types without automatic pricing cannot carry an instrument at all
		assert.Equal(t, http.StatusBadRequest, e.post(clone(bbca, "instrumentId", btc)).Status, "unknown field for ID_STOCK")
		idStock := e.must(bbca)
		assert.Equal(t, http.StatusBadRequest, e.patch(idStock.ID, map[string]any{"instrumentId": btc}).Status)
	})

	t.Run("a coin that disappears from AUTO use no longer refreshes", func(t *testing.T) {
		only := e.must(cryptoInv("USDT", "name", "Tether"))
		require.Equal(t, http.StatusNoContent, s.Do(http.MethodDelete, invURL(e.sid, "/", only.ID), nil, bearer(e.u.AccessToken)).Status)
		s.CMC.IDsAsked = nil
		res, err := s.Market.RefreshCryptoPrices(context.Background())
		require.NoError(t, err)
		for _, batch := range s.CMC.IDsAsked {
			assert.NotContains(t, batch, int64(825))
		}
		assert.Equal(t, "SUCCESS", res.Status)
	})
}

func TestPriceRefreshJob(t *testing.T) {
	ctx := context.Background()
	s := NewTestServer(t)
	e := newInvEnv(s)
	e.setSpace(map[string]any{"baseCurrency": "USD"})

	// 205 distinct coins, each held by one AUTO investment, inserted directly for speed
	coins, prices := bulkCoins(205)
	s.CMC.Coins = append(s.CMC.Coins, coins...)
	for k, v := range prices {
		s.CMC.Quotes[k] = v
	}
	_, err := s.Market.SyncCoinMap(ctx)
	require.NoError(t, err)
	_, err = s.Tx.Exec(ctx, `
		INSERT INTO investments (space_id, asset_type, price_mode, name, symbol, icon, quote_currency, quantity, avg_buy_price, manual_price, created_by_id, instrument_id)
		SELECT $1, 'CRYPTO', 'AUTO', i.name, i.symbol, '🪙', 'USD', 1, 1, 1, $2, i.id
		FROM market_instruments i WHERE i.external_id::bigint > 100000`, e.sid, e.u.User.ID)
	require.NoError(t, err)

	// noise that must NOT be fetched: a MANUAL holding of a coin, a deleted AUTO holding, a duplicate holding of one coin
	btc := e.must(cryptoInv("BTC", "priceMode", "MANUAL"))
	del := e.must(cryptoInv("SOL"))
	require.Equal(t, http.StatusNoContent, s.Do(http.MethodDelete, invURL(e.sid, "/", del.ID), nil, bearer(e.u.AccessToken)).Status)
	_ = btc
	_, err = s.Tx.Exec(ctx, `
		INSERT INTO investments (space_id, asset_type, price_mode, name, symbol, icon, quote_currency, quantity, avg_buy_price, manual_price, created_by_id, instrument_id)
		SELECT $1, 'CRYPTO', 'AUTO', 'Duplicate', i.symbol, '🪙', 'USD', 2, 1, 1, $2, i.id
		FROM market_instruments i WHERE i.external_id = '100001'`, e.sid, e.u.User.ID)
	require.NoError(t, err)

	s.CMC.PriceCalls, s.CMC.IDsAsked = 0, nil
	res, err := s.Market.RefreshCryptoPrices(ctx)
	require.NoError(t, err)
	require.Equal(t, "SUCCESS", res.Status, "%v", res.Err)
	assert.Equal(t, 205, res.Items, "distinct coins only")
	assert.Equal(t, 3, s.CMC.PriceCalls, "a cycle costs ceil(205/100) = 3 calls")
	asked := 0
	for _, b := range s.CMC.IDsAsked {
		assert.LessOrEqual(t, len(b), 100)
		asked += len(b)
	}
	assert.Equal(t, 205, asked)

	var stored int
	require.NoError(t, s.Tx.QueryRow(ctx, `SELECT count(*) FROM market_prices p JOIN market_instruments i ON i.id = p.instrument_id WHERE i.external_id::bigint > 100000`).Scan(&stored))
	assert.Equal(t, 205, stored)
	runs := s.jobRuns(marketdata.JobCryptoPrice)
	require.Len(t, runs, 1)
	assert.Equal(t, jobRow{Name: marketdata.JobCryptoPrice, Status: "SUCCESS", Items: 205}, runs[0])

	t.Run("nothing to do costs no calls", func(t *testing.T) {
		_, err := s.Tx.Exec(ctx, `UPDATE investments SET deleted_at = now() WHERE price_mode = 'AUTO'`)
		require.NoError(t, err)
		s.CMC.PriceCalls = 0
		res, err := s.Market.RefreshCryptoPrices(ctx)
		require.NoError(t, err)
		assert.Equal(t, "SUCCESS", res.Status)
		assert.Zero(t, res.Items)
		assert.Zero(t, s.CMC.PriceCalls)
	})
}

func TestStalenessAndOutages(t *testing.T) {
	ctx := context.Background()
	s := NewTestServer(t)
	e := newInvEnv(s)
	e.setSpace(map[string]any{"baseCurrency": "USD"})
	btc := e.must(cryptoInv("BTC"))
	require.Equal(t, "MARKET", btc.Valuation.PriceSource)
	require.False(t, btc.Valuation.IsStale)

	t.Run("a price older than 30 minutes is stale but still used", func(t *testing.T) {
		s.Clock.Advance(29 * time.Minute)
		assert.False(t, e.get(btc.ID).Valuation.IsStale)
		s.Clock.Advance(2 * time.Minute) // 31 minutes since the fetch
		got := e.get(btc.ID)
		assert.True(t, got.Valuation.IsStale)
		assert.Equal(t, "MARKET", got.Valuation.PriceSource)
		assert.True(t, dec("63000").Equal(dec(got.Valuation.Price)), "the last known price is kept")
	})

	t.Run("a provider outage keeps the last price, marks it stale and logs a FAILED run", func(t *testing.T) {
		s.CMC.Err = fmt.Errorf("provider down")
		res, err := s.Market.RefreshCryptoPrices(ctx)
		require.NoError(t, err)
		assert.Equal(t, "FAILED", res.Status)
		assert.Zero(t, res.Items)

		got := e.get(btc.ID)
		assert.True(t, dec("63000").Equal(dec(got.Valuation.Price)))
		assert.True(t, got.Valuation.IsStale)

		runs := s.jobRuns(marketdata.JobCryptoPrice)
		require.NotEmpty(t, runs)
		last := runs[len(runs)-1]
		assert.Equal(t, "FAILED", last.Status)
		require.NotNil(t, last.Error)
		assert.Contains(t, *last.Error, "provider down")
	})

	t.Run("a rate limit mid-cycle fails the whole cycle", func(t *testing.T) {
		s.CMC.Err = coinmarketcap.ErrRateLimited
		res, err := s.Market.RefreshCryptoPrices(ctx)
		require.NoError(t, err)
		assert.Equal(t, "FAILED", res.Status)
		assert.ErrorIs(t, res.Err, coinmarketcap.ErrRateLimited)
	})

	t.Run("recovery refreshes the price and clears the stale flag", func(t *testing.T) {
		s.CMC.Err = nil
		s.CMC.Quotes[1] = "64000"
		res, err := s.Market.RefreshCryptoPrices(ctx)
		require.NoError(t, err)
		assert.Equal(t, "SUCCESS", res.Status)
		got := e.get(btc.ID)
		assert.False(t, got.Valuation.IsStale)
		assert.True(t, dec("64000").Equal(dec(got.Valuation.Price)))
		assert.True(t, dec("32000").Equal(dec(got.Valuation.MarketValue)))
	})

	t.Run("a provider timestamp from the past is honoured", func(t *testing.T) {
		s.CMC.AsOf = s.Clock.Now().Add(-2 * time.Hour)
		_, err := s.Market.RefreshCryptoPrices(ctx)
		require.NoError(t, err)
		assert.True(t, e.get(btc.ID).Valuation.IsStale)
	})
}

func TestJobsAreSingleInstance(t *testing.T) {
	ctx := context.Background()
	s := NewTestServer(t)
	e := newInvEnv(s)
	e.must(cryptoInv("BTC"))

	other, err := testPool.Acquire(ctx)
	require.NoError(t, err)
	defer other.Release()
	tx, err := other.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(ctx) }()
	// another instance holds the job's advisory lock
	_, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1::text, 0))`, marketdata.JobCryptoPrice)
	require.NoError(t, err)

	s.CMC.PriceCalls = 0
	res, err := s.Market.RefreshCryptoPrices(ctx)
	require.NoError(t, err)
	assert.Equal(t, "SKIPPED", res.Status)
	assert.Zero(t, s.CMC.PriceCalls, "the provider must not be called without the lock")
	runs := s.jobRuns(marketdata.JobCryptoPrice)
	require.Len(t, runs, 1)
	assert.Equal(t, "SKIPPED", runs[0].Status)
}

func TestRefreshPriceEndpoint(t *testing.T) {
	s := NewTestServer(t)
	e := newInvEnv(s)
	e.setSpace(map[string]any{"baseCurrency": "USD"})
	btc := e.must(cryptoInv("BTC"))
	refresh := func(id string) Response {
		return s.Do(http.MethodPost, invURL(e.sid, "/", id, "/refresh-price"), nil, bearer(e.u.AccessToken))
	}

	t.Run("unauthenticated", func(t *testing.T) {
		assert.Equal(t, http.StatusUnauthorized, s.Do(http.MethodPost, invURL(e.sid, "/", btc.ID, "/refresh-price"), nil).Status)
	})

	t.Run("refreshes and returns the new valuation", func(t *testing.T) {
		s.CMC.Quotes[1] = "70000"
		s.Clock.Advance(40 * time.Minute)
		require.True(t, e.get(btc.ID).Valuation.IsStale)
		res := refresh(btc.ID)
		require.Equal(t, http.StatusOK, res.Status, string(res.Body))
		var got investmentBody
		res.JSON(t, &got)
		assert.True(t, dec("70000").Equal(dec(got.Valuation.Price)))
		assert.False(t, got.Valuation.IsStale)
		assert.True(t, dec("35000").Equal(dec(got.Valuation.MarketValue)))
	})

	t.Run("a provider failure answers 502 and keeps the last price", func(t *testing.T) {
		s.CMC.Err = fmt.Errorf("provider down")
		res := refresh(btc.ID)
		require.Equal(t, http.StatusBadGateway, res.Status, string(res.Body))
		assert.Equal(t, "UPSTREAM_PROVIDER_ERROR", errCode(t, res))
		assert.NotContains(t, string(res.Body), "provider down", "internals are not leaked")
		assert.True(t, dec("70000").Equal(dec(e.get(btc.ID).Valuation.Price)))
		s.CMC.Err = nil
	})

	t.Run("only linked AUTO investments of auto-priced types", func(t *testing.T) {
		manual := e.must(cryptoInv("BTC", "priceMode", "MANUAL"))
		res := refresh(manual.ID)
		require.Equal(t, http.StatusUnprocessableEntity, res.Status)
		assert.Equal(t, "PRICE_MODE_NOT_AUTO", errCode(t, res))

		stock := e.must(bbca)
		res = refresh(stock.ID)
		require.Equal(t, http.StatusUnprocessableEntity, res.Status)
		assert.Equal(t, "AUTO_PRICE_NOT_SUPPORTED", errCode(t, res))

		us := e.must(clone(aapl, "priceMode", "AUTO"))
		_, err := s.Tx.Exec(context.Background(), `UPDATE investments SET instrument_id = NULL WHERE id = $1`, us.ID)
		require.NoError(t, err)
		res = refresh(us.ID) // an auto-priced type that lost its instrument link
		require.Equal(t, http.StatusUnprocessableEntity, res.Status)
		assert.Equal(t, "INSTRUMENT_NOT_LINKED", errCode(t, res))
	})

	t.Run("not found", func(t *testing.T) {
		assert.Equal(t, http.StatusNotFound, refresh(uuid.NewString()).Status)
		assert.Equal(t, http.StatusNotFound, refresh("nope").Status)
	})

	t.Run("isolation", func(t *testing.T) {
		outsider := s.register("outsider@example.com")
		expectCrossSpace404(t, s, outsider, http.MethodPost, invURL(e.sid, "/", btc.ID, "/refresh-price"), nil)
		res := s.Do(http.MethodPost, invURL(spaceIDOf(outsider), "/", btc.ID, "/refresh-price"), nil, bearer(outsider.AccessToken))
		require.Equal(t, http.StatusNotFound, res.Status)
		assert.Equal(t, "INVESTMENT_NOT_FOUND", errCode(t, res))
	})
}

func TestRefreshPriceIsRateLimitedPerInvestment(t *testing.T) {
	s := NewTestServerWithInvestmentLimits(t, investments.DefaultLimits())
	e := newInvEnv(s)
	a, b := e.must(cryptoInv("BTC")), e.must(cryptoInv("ETH"))
	refresh := func(id string) Response {
		return s.Do(http.MethodPost, invURL(e.sid, "/", id, "/refresh-price"), nil, bearer(e.u.AccessToken))
	}

	assert.Equal(t, http.StatusOK, refresh(a.ID).Status)
	res := refresh(a.ID)
	require.Equal(t, http.StatusTooManyRequests, res.Status, "1 per minute per investment")
	assert.Equal(t, "RATE_LIMITED", errCode(t, res))
	assert.NotEmpty(t, res.Header.Get("Retry-After"))
	assert.Equal(t, http.StatusOK, refresh(b.ID).Status, "another investment has its own budget")

	// a member of another space cannot spend this investment's budget
	outsider := s.register("outsider@example.com")
	for i := 0; i < 3; i++ {
		s.Do(http.MethodPost, invURL(spaceIDOf(outsider), "/", b.ID, "/refresh-price"), nil, bearer(outsider.AccessToken))
	}
}

func TestMarketDataDisabledWithoutKey(t *testing.T) {
	ctx := context.Background()
	s := NewTestServer(t)
	off := marketdata.NewService(s.Tx, marketdata.Options{})
	assert.False(t, off.Enabled())
	_, err := off.SyncCoinMap(ctx)
	require.Error(t, err)
	_, err = off.RefreshCryptoPrices(ctx)
	require.Error(t, err)
	require.Error(t, off.RefreshPrice(ctx, uuid.New()))
	off.EnsurePrice(ctx, uuid.New()) // best effort, never panics
	stop, err := off.StartJobs(ctx, zerologNop())
	require.NoError(t, err)
	stop()
	assert.True(t, s.Market.Enabled())
	// the coin list is still searchable and resolvable from the local table
	u := s.register("jane@example.com")
	assert.NotEmpty(t, s.searchCoins(u, "?q=btc"))
	id, err := off.ResolveInstrument(ctx, "CRYPTO", nil, "BTC")
	require.NoError(t, err)
	assert.NotEqual(t, uuid.Nil, id)
}
