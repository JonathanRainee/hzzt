//go:build e2e

package e2e

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/JonathanRainee/hzzt/internal/currency"
)

type latestBody struct {
	Base   string            `json:"base"`
	AsOf   *string           `json:"asOf"`
	Source string            `json:"source"`
	Rates  map[string]string `json:"rates"`
}

func (s *TestServer) rowCount(table string) int {
	s.t.Helper()
	var n int
	require.NoError(s.t, s.Tx.QueryRow(context.Background(), `SELECT count(*) FROM `+table).Scan(&n))
	return n
}

func TestCurrenciesEndpoint(t *testing.T) {
	s := NewTestServer(t)
	assert.Equal(t, http.StatusUnauthorized, s.Get("/api/v1/currencies").Status)

	u := s.register("jane@example.com")
	res := s.Get("/api/v1/currencies", bearer(u.AccessToken))
	require.Equal(t, http.StatusOK, res.Status)
	var list []struct {
		Code       string `json:"code"`
		Name       string `json:"name"`
		Symbol     string `json:"symbol"`
		MinorUnits int    `json:"minorUnits"`
	}
	res.JSON(t, &list)
	require.NotEmpty(t, list)
	by := map[string]int{}
	for i, c := range list {
		by[c.Code] = i
		assert.NotEmpty(t, c.Name)
	}
	require.Contains(t, by, "IDR")
	assert.Equal(t, 0, list[by["IDR"]].MinorUnits)
	assert.Equal(t, "Rp", list[by["IDR"]].Symbol)
	assert.Equal(t, 2, list[by["USD"]].MinorUnits)
}

func TestFXLatest(t *testing.T) {
	s := NewTestServer(t)
	u := s.register("jane@example.com")
	get := func(q string) Response { return s.Get("/api/v1/fx/latest"+q, bearer(u.AccessToken)) }

	t.Run("unauthenticated", func(t *testing.T) {
		assert.Equal(t, http.StatusUnauthorized, s.Get("/api/v1/fx/latest").Status)
	})

	t.Run("empty before the first refresh", func(t *testing.T) {
		res := get("")
		require.Equal(t, http.StatusOK, res.Status)
		var b latestBody
		res.JSON(t, &b)
		assert.Nil(t, b.AsOf)
		assert.Empty(t, b.Rates)
	})

	require.NoError(t, s.Currency.Refresh(context.Background()))

	t.Run("USD base", func(t *testing.T) {
		res := get("")
		require.Equal(t, http.StatusOK, res.Status, string(res.Body))
		var b latestBody
		res.JSON(t, &b)
		assert.Equal(t, "USD", b.Base)
		require.NotNil(t, b.AsOf)
		assert.Equal(t, "fake", b.Source)
		assert.Equal(t, "16000", b.Rates["IDR"])
		assert.Equal(t, "0.8", b.Rates["EUR"])
		assert.NotContains(t, b.Rates, "USD")
		assert.NotContains(t, b.Rates, "SGD", "currencies the provider didn't return are absent")
	})

	t.Run("re-based to IDR", func(t *testing.T) {
		res := get("?base=idr")
		require.Equal(t, http.StatusOK, res.Status)
		var b latestBody
		res.JSON(t, &b)
		assert.Equal(t, "IDR", b.Base)
		assert.Equal(t, "0.0000625", b.Rates["USD"])
		assert.Equal(t, "0.00005", b.Rates["EUR"])
		assert.NotContains(t, b.Rates, "IDR")
	})

	t.Run("invalid base", func(t *testing.T) {
		for _, q := range []string{"?base=XXX", "?base=1", "?base=US"} {
			assert.Equal(t, http.StatusBadRequest, get(q).Status, q)
		}
	})
}

func TestFXRefresh(t *testing.T) {
	ctx := context.Background()

	t.Run("idempotent within a day and updates in place", func(t *testing.T) {
		s := NewTestServer(t)
		require.NoError(t, s.Currency.Refresh(ctx))
		n := s.rowCount("fx_rates")
		assert.Equal(t, 4, n)
		require.NoError(t, s.Currency.Refresh(ctx))
		assert.Equal(t, n, s.rowCount("fx_rates"), "same as_of must upsert, not duplicate")

		s.FX.Set.Rates["IDR"] = dec("16100")
		require.NoError(t, s.Currency.Refresh(ctx))
		assert.Equal(t, n, s.rowCount("fx_rates"))
		var rate string
		require.NoError(t, s.Tx.QueryRow(ctx, `SELECT rate::text FROM fx_rates WHERE quote = 'IDR'`).Scan(&rate))
		assert.Equal(t, "16100.000000000000", rate)
	})

	t.Run("a new day adds history and latest wins", func(t *testing.T) {
		s := NewTestServer(t)
		require.NoError(t, s.Currency.Refresh(ctx))
		s.Clock.Advance(24 * time.Hour)
		s.FX.Set = defaultRates(s.Clock)
		s.FX.Set.Rates["IDR"] = dec("17000")
		require.NoError(t, s.Currency.Refresh(ctx))
		assert.Equal(t, 8, s.rowCount("fx_rates"))

		u := s.register("jane@example.com")
		var b latestBody
		s.Get("/api/v1/fx/latest", bearer(u.AccessToken)).JSON(t, &b)
		assert.Equal(t, "17000", b.Rates["IDR"])
	})

	t.Run("ensure only fetches when empty", func(t *testing.T) {
		s := NewTestServer(t)
		require.NoError(t, s.Currency.EnsureRates(ctx))
		assert.Equal(t, 1, s.FX.Calls)
		require.NoError(t, s.Currency.EnsureRates(ctx))
		assert.Equal(t, 1, s.FX.Calls)
	})

	t.Run("provider failure stores nothing", func(t *testing.T) {
		s := NewTestServer(t)
		s.FX.Err = errProvider
		require.Error(t, s.Currency.Refresh(ctx))
		assert.Zero(t, s.rowCount("fx_rates"))
		// and the failed attempt must not poison the connection
		s.FX.Err = nil
		require.NoError(t, s.Currency.Refresh(ctx))
	})

	t.Run("unusable payload is an error", func(t *testing.T) {
		s := NewTestServer(t)
		s.FX.Set.Rates = map[string]decimal.Decimal{"ABC": dec("1")}
		require.Error(t, s.Currency.Refresh(ctx))
		assert.Zero(t, s.rowCount("fx_rates"))
	})

	t.Run("only one instance runs the job at a time", func(t *testing.T) {
		s := NewTestServer(t)
		other, err := testPool.Acquire(ctx)
		require.NoError(t, err)
		defer other.Release()
		tx, err := other.Begin(ctx)
		require.NoError(t, err)
		defer func() { _ = tx.Rollback(ctx) }()
		_, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(7305001)`)
		require.NoError(t, err)

		err = s.Currency.Refresh(ctx)
		assert.ErrorIs(t, err, currency.ErrRefreshBusy)
		assert.Zero(t, s.FX.Calls, "the provider must not be called without the lock")
	})
}

func TestManualRatesEndpoints(t *testing.T) {
	s := NewTestServer(t)
	owner := s.register("owner@example.com")
	member := s.register("member@example.com")
	outsider := s.register("outsider@example.com")
	sid := spaceIDOf(owner)
	s.join(owner, sid, member)
	base := "/api/v1/spaces/" + sid + "/fx/manual-rates"

	type rate struct{ Base, Quote, Rate string }
	list := func(u session) []rate {
		res := s.Get(base, bearer(u.AccessToken))
		require.Equal(t, http.StatusOK, res.Status, string(res.Body))
		var out []rate
		res.JSON(t, &out)
		return out
	}

	t.Run("unauthenticated", func(t *testing.T) {
		assert.Equal(t, http.StatusUnauthorized, s.Get(base).Status)
		assert.Equal(t, http.StatusUnauthorized, s.Do(http.MethodPut, base+"/USD/IDR", map[string]any{"rate": "1"}).Status)
	})

	t.Run("empty list", func(t *testing.T) {
		assert.Empty(t, list(owner))
	})

	t.Run("owner sets rates; both roles read them", func(t *testing.T) {
		res := s.Do(http.MethodPut, base+"/usd/idr", map[string]any{"rate": "15000.5"}, bearer(owner.AccessToken))
		require.Equal(t, http.StatusOK, res.Status, string(res.Body))
		var r rate
		res.JSON(t, &r)
		assert.Equal(t, rate{"USD", "IDR", "15000.5"}, r)
		require.Equal(t, http.StatusOK, s.Do(http.MethodPut, base+"/EUR/IDR", map[string]any{"rate": 17000}, bearer(owner.AccessToken)).Status, "numeric JSON is accepted too")

		got := list(member)
		require.Len(t, got, 2)
		assert.Equal(t, "EUR", got[0].Base, "sorted by base, quote")
		assert.Equal(t, "15000.5", got[1].Rate)
	})

	t.Run("put replaces", func(t *testing.T) {
		require.Equal(t, http.StatusOK, s.Do(http.MethodPut, base+"/USD/IDR", map[string]any{"rate": "16000"}, bearer(owner.AccessToken)).Status)
		got := list(owner)
		require.Len(t, got, 2)
		assert.Equal(t, "16000", got[1].Rate)
	})

	t.Run("member cannot write", func(t *testing.T) {
		res := s.Do(http.MethodPut, base+"/USD/IDR", map[string]any{"rate": "1"}, bearer(member.AccessToken))
		require.Equal(t, http.StatusForbidden, res.Status)
		assert.Equal(t, "FORBIDDEN", errCode(t, res))
		assert.Equal(t, http.StatusForbidden, s.Do(http.MethodDelete, base+"/USD/IDR", nil, bearer(member.AccessToken)).Status)
	})

	t.Run("cross-space isolation", func(t *testing.T) {
		expectCrossSpace404(t, s, outsider, http.MethodGet, base, nil)
		expectCrossSpace404(t, s, outsider, http.MethodPut, base+"/USD/IDR", map[string]any{"rate": "1"})
		expectCrossSpace404(t, s, outsider, http.MethodDelete, base+"/USD/IDR", nil)
		// the outsider's own space has its own (empty) table
		assert.Empty(t, list2(t, s, outsider, "/api/v1/spaces/"+spaceIDOf(outsider)+"/fx/manual-rates"))
	})

	t.Run("validation", func(t *testing.T) {
		put := func(path string, body any) Response {
			return s.Do(http.MethodPut, base+path, body, bearer(owner.AccessToken))
		}
		for name, c := range map[string]struct {
			path string
			body any
		}{
			"zero":              {"/USD/IDR", map[string]any{"rate": "0"}},
			"negative":          {"/USD/IDR", map[string]any{"rate": "-1"}},
			"non numeric":       {"/USD/IDR", map[string]any{"rate": "abc"}},
			"missing":           {"/USD/IDR", map[string]any{}},
			"too big":           {"/USD/IDR", map[string]any{"rate": "10000000000000000"}},
			"rounds to zero":    {"/USD/IDR", map[string]any{"rate": "0.0000000000001"}},
			"same currency":     {"/USD/USD", map[string]any{"rate": "1"}},
			"unsupported base":  {"/XXX/IDR", map[string]any{"rate": "1"}},
			"unsupported quote": {"/USD/ZZZ", map[string]any{"rate": "1"}},
			"unknown field":     {"/USD/IDR", map[string]any{"rate": "1", "x": 1}},
		} {
			res := put(c.path, c.body)
			assert.Equal(t, http.StatusBadRequest, res.Status, "%s: %s", name, res.Body)
		}
	})

	t.Run("delete", func(t *testing.T) {
		res := s.Do(http.MethodDelete, base+"/EUR/IDR", nil, bearer(owner.AccessToken))
		require.Equal(t, http.StatusNoContent, res.Status)
		assert.Len(t, list(owner), 1)
		res = s.Do(http.MethodDelete, base+"/EUR/IDR", nil, bearer(owner.AccessToken))
		require.Equal(t, http.StatusNotFound, res.Status)
		assert.Equal(t, http.StatusBadRequest, s.Do(http.MethodDelete, base+"/XXX/IDR", nil, bearer(owner.AccessToken)).Status)
	})
}

func list2(t *testing.T, s *TestServer, u session, path string) []map[string]any {
	t.Helper()
	res := s.Get(path, bearer(u.AccessToken))
	require.Equal(t, http.StatusOK, res.Status, string(res.Body))
	var out []map[string]any
	res.JSON(t, &out)
	return out
}

func TestConversionContextForSpace(t *testing.T) {
	ctx := context.Background()
	s := NewTestServer(t)
	require.NoError(t, s.Currency.Refresh(ctx))
	owner := s.register("owner@example.com")
	sid := spaceIDOf(owner)
	spaceID := uuid.MustParse(sid)
	spacePath := "/api/v1/spaces/" + sid

	items := []currency.MoneyItem{
		{Label: "Cash", Amount: dec("10"), Currency: "USD"},
		{Label: "Savings", Amount: dec("160000"), Currency: "IDR"},
		{Label: "Pounds", Amount: dec("5"), Currency: "GBP"},
	}
	// make GBP unavailable live
	delete(s.FX.Set.Rates, "GBP")
	_, err := s.Tx.Exec(ctx, `DELETE FROM fx_rates WHERE quote = 'GBP'`)
	require.NoError(t, err)

	t.Run("auto mode uses live rates and warns on a missing one", func(t *testing.T) {
		cc, err := s.Currency.BuildContext(ctx, spaceID)
		require.NoError(t, err)
		assert.Equal(t, "USD", cc.BaseCurrency)
		assert.True(t, cc.AutoFX)
		got, err := currency.Convert(dec("10"), "USD", "IDR", cc)
		require.NoError(t, err)
		assert.True(t, dec("160000").Equal(got))

		res := currency.Aggregate(items, cc)
		assert.True(t, dec("20").Equal(res.Total), res.Total.String())
		require.Len(t, res.Warnings, 1)
		assert.Equal(t, "Pounds", res.Warnings[0].Label)
	})

	t.Run("manual mode uses the manual rate even when a live rate exists", func(t *testing.T) {
		require.Equal(t, http.StatusOK, s.Do(http.MethodPatch, spacePath, map[string]any{"autoFxConversion": false}, bearer(owner.AccessToken)).Status)
		require.Equal(t, http.StatusOK, s.Do(http.MethodPut, spacePath+"/fx/manual-rates/USD/IDR", map[string]any{"rate": "15000"}, bearer(owner.AccessToken)).Status)

		cc, err := s.Currency.BuildContext(ctx, spaceID)
		require.NoError(t, err)
		assert.False(t, cc.AutoFX)
		got, err := currency.Convert(dec("10"), "USD", "IDR", cc)
		require.NoError(t, err)
		assert.True(t, dec("150000").Equal(got), got.String())

		res := currency.Aggregate(items, cc)
		assert.True(t, dec("10").Add(dec("160000").Div(dec("15000"))).Sub(res.Total).Abs().LessThan(dec("0.000001")), res.Total.String())
	})

	t.Run("base currency change is honoured", func(t *testing.T) {
		require.Equal(t, http.StatusOK, s.Do(http.MethodPatch, spacePath, map[string]any{"autoFxConversion": true, "baseCurrency": "IDR"}, bearer(owner.AccessToken)).Status)
		cc, err := s.Currency.BuildContext(ctx, spaceID)
		require.NoError(t, err)
		assert.Equal(t, "IDR", cc.BaseCurrency)
		res := currency.Aggregate(items, cc)
		assert.True(t, dec("320000").Equal(res.Total), res.Total.String())
	})

	t.Run("deleted space", func(t *testing.T) {
		_, err := s.Currency.BuildContext(ctx, uuid.New())
		require.Error(t, err)
	})
}
