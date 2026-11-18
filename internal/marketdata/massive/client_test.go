package massive

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testKey = "massive-test-secret-0123456789"

// Shapes copied from real responses.
const searchFixture = `{"results":[
 {"ticker":"AAPL","name":"Apple Inc.","market":"stocks","locale":"us","primary_exchange":"XNAS","type":"CS","active":true,"currency_name":"usd"},
 {"ticker":"AAPX","name":"T-Rex 2X Long Apple Daily Target ETF","market":"stocks","locale":"us","primary_exchange":"BATS","type":"ETF","active":true},
 {"name":"No ticker"}],
 "status":"OK","request_id":"abc","count":3}`

const groupedFixture = `{"queryCount":4,"resultsCount":4,"adjusted":true,"results":[
 {"T":"AAPL","v":37407537.28,"vw":351.8265,"o":352.83,"c":351.91,"h":355.36,"l":349.67,"t":1794949200000,"n":732378},
 {"T":"VOO","v":10039731.25,"vw":673.3716,"o":678.82,"c":673.19,"h":679.98,"l":668.27,"t":1794949200000,"n":479649},
 {"T":"MSFT","c":500,"t":1794949200000},
 {"T":"PENNY","c":0.00001234,"t":1794949200000}],
 "status":"DELAYED","request_id":"x","count":4}`

const snapshotFixture = `{"status":"OK","count":4,"tickers":[
 {"ticker":"AAPL","todaysChangePerc":1.2,"updated":1794949200000000000,"day":{"c":351.91},
  "lastTrade":{"p":352.1,"t":1794949260000000000},"prevDay":{"c":349}},
 {"ticker":"VOO","updated":1794949200000000000,"min":{"c":673.5},"day":{"c":673.19},"prevDay":{"c":670}},
 {"ticker":"MSFT","updated":1794949200000000000,"day":{"c":0},"prevDay":{"c":498.25}},
 {"ticker":"DEAD","day":{"c":0},"prevDay":{"c":0}}]}`

func newClient(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c := New(srv.URL, testKey, 600) // a high budget so HTTP tests don't wait
	c.backoff = time.Millisecond
	return c
}

func TestThrottleNeverExceedsTheBudget(t *testing.T) {
	for _, perMin := range []int{1, 5, 12, 60} {
		th := NewThrottle(perMin)
		now := time.Date(2026, 11, 18, 14, 0, 0, 0, time.UTC)
		th.now = func() time.Time { return now }
		th.sleep = func(_ context.Context, d time.Duration) error { now = now.Add(d); return nil }

		var starts []time.Time
		for i := 0; i < 5*perMin+3; i++ {
			require.NoError(t, th.Wait(context.Background(), 0))
			starts = append(starts, now)
		}
		for i, s := range starts {
			n := 0
			for _, o := range starts[i:] {
				if o.Before(s.Add(time.Minute)) {
					n++
				}
			}
			assert.LessOrEqual(t, n, perMin, "rate %d/min: calls starting in [%s, +1m)", perMin, s.Format("15:04:05"))
		}
		total := starts[len(starts)-1].Sub(starts[0])
		assert.GreaterOrEqual(t, total, time.Duration(5*perMin+2)*time.Minute/time.Duration(perMin), "the calls are spread out, not bunched")
	}
}

func TestThrottleFirstCallIsImmediateAndIdleTimeDoesNotBuildABurst(t *testing.T) {
	th := NewThrottle(5)
	now := time.Date(2026, 11, 18, 14, 0, 0, 0, time.UTC)
	var slept []time.Duration
	th.now = func() time.Time { return now }
	th.sleep = func(_ context.Context, d time.Duration) error { slept = append(slept, d); now = now.Add(d); return nil }

	require.NoError(t, th.Wait(context.Background(), 0))
	assert.Empty(t, slept, "the first call does not wait")
	now = now.Add(time.Hour) // a long idle spell
	require.NoError(t, th.Wait(context.Background(), 0))
	require.NoError(t, th.Wait(context.Background(), 0))
	require.Len(t, slept, 1, "after idling there is one token, not a stored burst")
	assert.Equal(t, 12*time.Second, slept[0])
}

func TestThrottleMaxWaitReservesNothing(t *testing.T) {
	th := NewThrottle(5)
	now := time.Date(2026, 11, 18, 14, 0, 0, 0, time.UTC)
	th.now = func() time.Time { return now }
	th.sleep = func(_ context.Context, d time.Duration) error { now = now.Add(d); return nil }

	require.NoError(t, th.Wait(context.Background(), 2*time.Second))
	for i := 0; i < 3; i++ {
		assert.ErrorIs(t, th.Wait(context.Background(), 2*time.Second), ErrThrottled, "12s delay > 2s allowed")
	}
	now = now.Add(12 * time.Second)
	assert.NoError(t, th.Wait(context.Background(), 2*time.Second), "refused calls did not use up tokens")
}

func TestThrottleHonoursContext(t *testing.T) {
	th := NewThrottle(1)
	require.NoError(t, th.Wait(context.Background(), 0))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	assert.Error(t, th.Wait(ctx, 0), "a cancelled wait returns instead of blocking for a minute")
}

func TestClientWaitsForTheThrottleBeforeEveryCall(t *testing.T) {
	var calls atomic.Int32
	c := newClient(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1); _, _ = w.Write([]byte(`{"results":[]}`)) })
	c.throttle = NewThrottle(5)
	var slept []time.Duration
	now := time.Date(2026, 11, 18, 14, 0, 0, 0, time.UTC)
	c.throttle.now = func() time.Time { return now }
	c.throttle.sleep = func(_ context.Context, d time.Duration) error { slept = append(slept, d); now = now.Add(d); return nil }

	for i := 0; i < 4; i++ {
		_, err := c.SearchTickers(context.Background(), "apple", "STOCK", false, 10, 0)
		require.NoError(t, err)
	}
	assert.EqualValues(t, 4, calls.Load())
	assert.Equal(t, []time.Duration{12 * time.Second, 12 * time.Second, 12 * time.Second}, slept)

	// a caller that cannot wait gets ErrThrottled and no HTTP call is made
	calls.Store(0)
	_, err := c.SearchTickers(context.Background(), "apple", "STOCK", false, 10, time.Second)
	assert.ErrorIs(t, err, ErrThrottled)
	assert.Zero(t, calls.Load())
}

func TestRetriesTakeTokensToo(t *testing.T) {
	var calls atomic.Int32
	c := newClient(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(http.StatusBadGateway) })
	c.throttle = NewThrottle(5)
	waits := 0
	now := time.Date(2026, 11, 18, 14, 0, 0, 0, time.UTC)
	c.throttle.now = func() time.Time { return now }
	c.throttle.sleep = func(_ context.Context, d time.Duration) error { waits++; now = now.Add(d); return nil }
	_, err := c.SearchTickers(context.Background(), "a", "STOCK", false, 1, 0)
	require.Error(t, err)
	assert.EqualValues(t, 3, calls.Load(), "1 try + 2 retries")
	assert.Equal(t, 2, waits, "each retry waited for its own token")
}

func TestSearchTickers(t *testing.T) {
	var gotAuth, gotPath string
	var gotQuery map[string][]string
	c := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotPath, gotQuery = r.Header.Get("Authorization"), r.URL.Path, r.URL.Query()
		_, _ = w.Write([]byte(searchFixture))
	})
	got, err := c.SearchTickers(context.Background(), " apple ", "STOCK", false, 25, 0)
	require.NoError(t, err)
	require.Len(t, got, 2, "an entry without a ticker is skipped")
	assert.Equal(t, Ticker{Symbol: "AAPL", Name: "Apple Inc.", Type: "CS", Exchange: "XNAS", Active: true}, got[0])
	assert.Equal(t, "ETF", got[1].Type)

	assert.Equal(t, "Bearer "+testKey, gotAuth, "the key is in the header")
	assert.Equal(t, "/v3/reference/tickers", gotPath)
	assert.Equal(t, []string{"apple"}, gotQuery["search"])
	assert.Equal(t, []string{"CS"}, gotQuery["type"])
	assert.Equal(t, []string{"stocks"}, gotQuery["market"])
	assert.Equal(t, []string{"true"}, gotQuery["active"])
	assert.Equal(t, []string{"25"}, gotQuery["limit"])
	for k, v := range gotQuery {
		assert.NotContains(t, strings.Join(v, ","), testKey, "the key must not be in query param %s", k)
		assert.NotEqual(t, "apikey", strings.ToLower(k))
	}

	t.Run("ETF kind and exact ticker lookup", func(t *testing.T) {
		_, err := c.SearchTickers(context.Background(), "voo", "ETF", true, 1, 0)
		require.NoError(t, err)
		assert.Equal(t, []string{"ETF"}, gotQuery["type"])
		assert.Equal(t, []string{"VOO"}, gotQuery["ticker"])
		assert.Empty(t, gotQuery["search"])
	})
	t.Run("unknown kind", func(t *testing.T) {
		_, err := c.SearchTickers(context.Background(), "x", "BOND", false, 1, 0)
		assert.Error(t, err)
	})
}

func TestGroupedDailyKeepsOnlyWantedTickersWithoutFloatRounding(t *testing.T) {
	var gotPath, gotAdj string
	c := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAdj = r.URL.Path, r.URL.Query().Get("adjusted")
		_, _ = w.Write([]byte(groupedFixture))
	})
	date := time.Date(2026, 11, 17, 0, 0, 0, 0, time.UTC)
	got, err := c.GroupedDaily(context.Background(), date, map[string]bool{"AAPL": true, "VOO": true, "PENNY": true, "MISSING": true})
	require.NoError(t, err)
	assert.Equal(t, "/v2/aggs/grouped/locale/us/market/stocks/2026-11-17", gotPath)
	assert.Equal(t, "true", gotAdj)
	require.Len(t, got, 3, "MSFT was not wanted; MISSING was not returned")
	assert.Equal(t, "351.91", got["AAPL"].Price.String())
	assert.Equal(t, "673.19", got["VOO"].Price.String())
	assert.Equal(t, "0.00001234", got["PENNY"].Price.String())
	assert.True(t, date.Equal(got["AAPL"].Day))
}

func TestGroupedDailyOnAWeekendIsEmptyNotAnError(t *testing.T) {
	c := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"queryCount":0,"resultsCount":0,"adjusted":true,"status":"OK","request_id":"x"}`))
	})
	got, err := c.GroupedDaily(context.Background(), time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC), map[string]bool{"AAPL": true})
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestSnapshotPicksTheFreshestPrice(t *testing.T) {
	var gotTickers string
	c := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotTickers = r.URL.Query().Get("tickers")
		_, _ = w.Write([]byte(snapshotFixture))
	})
	got, err := c.Snapshot(context.Background(), []string{"AAPL", "VOO", "MSFT", "DEAD"})
	require.NoError(t, err)
	assert.Equal(t, "AAPL,VOO,MSFT,DEAD", gotTickers)
	require.Len(t, got, 3, "a ticker with no usable price is skipped")
	assert.Equal(t, "352.1", got["AAPL"].Price.String(), "last trade beats the day close")
	assert.True(t, time.Unix(0, 1794949260000000000).UTC().Equal(got["AAPL"].AsOf), "as_of is the trade time")
	assert.Equal(t, "673.5", got["VOO"].Price.String(), "no last trade: the latest minute bar")
	assert.Equal(t, "498.25", got["MSFT"].Price.String(), "a zero day close falls back to the previous close")
	assert.True(t, time.Unix(0, 1794949200000000000).UTC().Equal(got["VOO"].AsOf), "otherwise the `updated` time")
}

func TestSnapshotBatches(t *testing.T) {
	var calls atomic.Int32
	c := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		assert.LessOrEqual(t, len(strings.Split(r.URL.Query().Get("tickers"), ",")), 100)
		_, _ = w.Write([]byte(`{"status":"OK","tickers":[]}`))
	})
	tickers := make([]string, 230)
	for i := range tickers {
		tickers[i] = "T" + string(rune('A'+i%26))
	}
	_, err := c.Snapshot(context.Background(), tickers)
	require.NoError(t, err)
	assert.EqualValues(t, 3, calls.Load())
}

func TestStatusCodes(t *testing.T) {
	cases := []struct {
		status int
		want   error
	}{
		{http.StatusUnauthorized, ErrUnauthorized},
		{http.StatusForbidden, ErrNotEntitled},
		{http.StatusTooManyRequests, ErrRateLimited},
	}
	for _, c := range cases {
		var calls atomic.Int32
		cl := newClient(t, func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			w.WriteHeader(c.status)
			_, _ = w.Write([]byte(`{"status":"NOT_AUTHORIZED","message":"You are not entitled to this data."}`))
		})
		_, err := cl.Snapshot(context.Background(), []string{"AAPL"})
		require.ErrorIs(t, err, c.want, c.status)
		assert.EqualValues(t, 1, calls.Load(), "%d is never retried", c.status)
		assert.NotContains(t, err.Error(), testKey)
	}
	cl := newClient(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusTeapot) })
	_, err := cl.Snapshot(context.Background(), []string{"AAPL"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "418")
}

func TestRetriesServerErrorsThenSucceeds(t *testing.T) {
	var calls atomic.Int32
	c := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(groupedFixture))
	})
	got, err := c.GroupedDaily(context.Background(), time.Now(), map[string]bool{"AAPL": true})
	require.NoError(t, err)
	assert.Len(t, got, 1)
	assert.EqualValues(t, 3, calls.Load())
}

func TestMalformedBodies(t *testing.T) {
	c := newClient(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`<html>`)) })
	_, err := c.SearchTickers(context.Background(), "a", "STOCK", false, 1, 0)
	assert.Error(t, err)
	_, err = c.GroupedDaily(context.Background(), time.Now(), nil)
	assert.Error(t, err)
	_, err = c.Snapshot(context.Background(), []string{"A"})
	assert.Error(t, err)
}

func TestNoKeyInTransportErrors(t *testing.T) {
	c := New("http://127.0.0.1:1", testKey, 600)
	c.retries = 0
	_, err := c.SearchTickers(context.Background(), "a", "STOCK", false, 1, 0)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), testKey)
	assert.False(t, errors.Is(err, ErrUnauthorized))
}

func TestDefaults(t *testing.T) {
	assert.Equal(t, 5, DefaultRatePerMinute)
	assert.NotNil(t, NewThrottle(0), "a non-positive rate falls back to the default")
}
