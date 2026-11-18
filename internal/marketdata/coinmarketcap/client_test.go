package coinmarketcap

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testKey = "test-secret-key-0123456789abcdef"

// Fixtures shaped like the real responses of /v1/cryptocurrency/map and /v2/simple/price.
const mapFixture = `{"data":[
 {"id":1,"name":"Bitcoin","symbol":"BTC","slug":"bitcoin","rank":1,"is_active":1,"platform":null},
 {"id":1027,"name":"Ethereum","symbol":"ETH","slug":"ethereum","rank":2,"is_active":1,"platform":null},
 {"id":825,"name":"Tether USDt","symbol":"USDT","slug":"tether","rank":3,"is_active":1,"platform":{"id":1,"name":"Ethereum"}},
 {"id":777,"name":"Retired","symbol":"RET","slug":"retired","rank":null,"is_active":0,"platform":null}],
 "status":{"timestamp":"2026-11-18T14:38:22.250Z","error_code":0,"error_message":null,"elapsed":19,"credit_count":0,"notice":null}}`

const priceFixture = `{"data":[
 {"id":1,"name":"Bitcoin","symbol":"BTC","slug":"bitcoin","quotes":[{"symbol":"USD","price":63403.27223329967}]},
 {"id":1027,"name":"Ethereum","symbol":"ETH","slug":"ethereum","quotes":[{"symbol":"USD","price":1769.7407335962278}]},
 {"id":825,"name":"Tether USDt","symbol":"USDT","slug":"tether","quotes":[{"symbol":"USD","price":0.9988375970024239}]}],
 "status":{"timestamp":"2026-11-18T14:38:21.226Z","error_code":"0","error_message":"","elapsed":10,"credit_count":1}}`

func newClient(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c := New(srv.URL, testKey)
	c.backoff = time.Millisecond
	return c
}

func TestMapParsesTheRealShape(t *testing.T) {
	var gotKey, gotPath, gotQuery string
	c := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotKey, gotPath, gotQuery = r.Header.Get("X-CMC_PRO_API_KEY"), r.URL.Path, r.URL.RawQuery
		_, _ = w.Write([]byte(mapFixture))
	})
	coins, err := c.Map(context.Background(), 5000)
	require.NoError(t, err)
	require.Len(t, coins, 4)
	assert.Equal(t, Coin{ID: 1, Symbol: "BTC", Name: "Bitcoin", Rank: 1, Active: true}, coins[0])
	assert.True(t, coins[2].Active)
	assert.False(t, coins[3].Active)
	assert.Zero(t, coins[3].Rank, "a null rank becomes 0")

	assert.Equal(t, testKey, gotKey, "the key travels in the header")
	assert.Equal(t, "/v1/cryptocurrency/map", gotPath)
	assert.Contains(t, gotQuery, "limit=5000")
	assert.Contains(t, gotQuery, "listing_status=active")
	assert.NotContains(t, gotQuery, testKey, "never in the URL")
}

func TestPricesParseWithoutFloatRounding(t *testing.T) {
	var gotPath, gotIDs string
	c := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotIDs = r.URL.Path, r.URL.Query().Get("id")
		assert.Equal(t, testKey, r.Header.Get("X-CMC_PRO_API_KEY"))
		_, _ = w.Write([]byte(priceFixture))
	})
	got, calls, err := c.Prices(context.Background(), []int64{1, 1027, 825})
	require.NoError(t, err)
	assert.Equal(t, 1, calls)
	assert.Equal(t, "/v2/simple/price", gotPath)
	assert.Equal(t, "1,1027,825", gotIDs)
	require.Len(t, got, 3)
	assert.Equal(t, "63403.27223329967", got[1].Price.String())
	assert.Equal(t, "0.9988375970024239", got[825].Price.String())
	assert.True(t, time.Date(2026, 11, 18, 14, 38, 21, 226000000, time.UTC).Equal(got[1].AsOf), "as_of is the provider timestamp: %s", got[1].AsOf)
}

func TestPricesBatchBy100(t *testing.T) {
	var calls atomic.Int32
	var sizes []int
	c := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		ids := strings.Split(r.URL.Query().Get("id"), ",")
		sizes = append(sizes, len(ids))
		var parts []string
		for _, id := range ids {
			parts = append(parts, fmt.Sprintf(`{"id":%s,"quotes":[{"symbol":"USD","price":2.5}]}`, id))
		}
		_, _ = w.Write([]byte(`{"data":[` + strings.Join(parts, ",") + `],"status":{"timestamp":"2026-11-18T14:38:21.226Z","error_code":"0"}}`))
	})
	for _, n := range []int{1, 99, 100, 101, 250, 300} {
		calls.Store(0)
		sizes = nil
		ids := make([]int64, n)
		for i := range ids {
			ids[i] = int64(i + 1)
		}
		got, reported, err := c.Prices(context.Background(), ids)
		require.NoError(t, err)
		assert.Len(t, got, n)
		want := (n + 99) / 100
		assert.EqualValues(t, want, calls.Load(), "%d coins cost ceil(n/100) calls", n)
		assert.Equal(t, want, reported)
		for _, sz := range sizes {
			assert.LessOrEqual(t, sz, 100)
		}
	}
	got, reported, err := c.Prices(context.Background(), nil)
	require.NoError(t, err)
	assert.Empty(t, got)
	assert.Zero(t, reported, "nothing to fetch, no call")
}

func TestPricesSkipUnknownCoinsAndBadQuotes(t *testing.T) {
	c := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[
		 {"id":1,"quotes":[{"symbol":"USD","price":10}]},
		 {"id":2,"quotes":[{"symbol":"EUR","price":9}]},
		 {"id":3,"quotes":[{"symbol":"USD","price":"nope"}]},
		 {"id":4,"quotes":[{"symbol":"USD","price":-1}]},
		 {"id":5,"quotes":[]},
		 {"id":6,"quotes":[{"symbol":"USD","price":1.5e-7}]}],
		 "status":{"timestamp":"2026-11-18T14:38:21.226Z","error_code":"0"}}`))
	})
	got, _, err := c.Prices(context.Background(), []int64{1, 2, 3, 4, 5, 6, 7})
	require.NoError(t, err)
	assert.Len(t, got, 2, "only valid USD quotes are kept; id 7 was not returned at all")
	assert.True(t, decimal.RequireFromString("0.00000015").Equal(got[6].Price), "scientific notation parses: %s", got[6].Price)
}

func TestRetriesServerErrorsThenSucceeds(t *testing.T) {
	var calls atomic.Int32
	c := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) < 3 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte(priceFixture))
	})
	_, _, err := c.Prices(context.Background(), []int64{1})
	require.NoError(t, err)
	assert.EqualValues(t, 3, calls.Load(), "1 try + 2 retries on 5xx")
}

func TestGivesUpAfterRetries(t *testing.T) {
	var calls atomic.Int32
	c := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	})
	_, _, err := c.Prices(context.Background(), []int64{1})
	require.Error(t, err)
	assert.EqualValues(t, 3, calls.Load())
	assert.NotContains(t, err.Error(), testKey)
}

func TestRateLimitStopsTheCycleWithoutRetrying(t *testing.T) {
	var calls atomic.Int32
	c := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		// the first batch works, the second hits the limit
		if calls.Add(1) == 1 {
			ids := strings.Split(r.URL.Query().Get("id"), ",")
			var parts []string
			for _, id := range ids {
				parts = append(parts, fmt.Sprintf(`{"id":%s,"quotes":[{"symbol":"USD","price":1}]}`, id))
			}
			_, _ = w.Write([]byte(`{"data":[` + strings.Join(parts, ",") + `],"status":{"timestamp":"2026-11-18T14:38:21.226Z","error_code":"0"}}`))
			return
		}
		w.WriteHeader(http.StatusTooManyRequests)
	})
	ids := make([]int64, 350)
	for i := range ids {
		ids[i] = int64(i + 1)
	}
	got, reported, err := c.Prices(context.Background(), ids)
	require.ErrorIs(t, err, ErrRateLimited)
	assert.EqualValues(t, 2, calls.Load(), "429 is not retried and the remaining batches are not attempted")
	assert.Equal(t, 2, reported)
	assert.Len(t, got, 100, "what the first batch fetched is returned")
}

func TestAuthErrors(t *testing.T) {
	for _, code := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		var calls atomic.Int32
		c := newClient(t, func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			w.WriteHeader(code)
			_, _ = w.Write([]byte(`{"status":{"error_code":"1001","error_message":"This API Key is invalid."}}`))
		})
		_, err := c.Map(context.Background(), 10)
		require.ErrorIs(t, err, ErrUnauthorized, code)
		assert.EqualValues(t, 1, calls.Load(), "not retried")
		assert.NotContains(t, err.Error(), testKey)
	}
}

func TestProviderErrorInsideA200(t *testing.T) {
	for name, body := range map[string]string{
		"string code":  `{"data":[],"status":{"timestamp":"2026-11-18T14:38:21.226Z","error_code":"1008","error_message":"You've exceeded your API Key's HTTP request rate limit."}}`,
		"numeric code": `{"data":[],"status":{"timestamp":"2026-11-18T14:38:21.226Z","error_code":1006,"error_message":"Plan limit"}}`,
	} {
		c := newClient(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) })
		_, _, err := c.Prices(context.Background(), []int64{1})
		require.Error(t, err, name)
		assert.Contains(t, err.Error(), "error 100", name)
		_, err = c.Map(context.Background(), 1)
		require.Error(t, err, name)
	}
}

func TestMalformedBodies(t *testing.T) {
	c := newClient(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`<html>`)) })
	_, err := c.Map(context.Background(), 1)
	assert.Error(t, err)
	_, _, err = c.Prices(context.Background(), []int64{1})
	assert.Error(t, err)
}

func TestNoKeyInTransportErrors(t *testing.T) {
	c := New("http://127.0.0.1:1", testKey) // nothing listens there
	c.retries = 0
	_, err := c.Map(context.Background(), 1)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), testKey)
	assert.False(t, errors.Is(err, ErrUnauthorized))
	// the key is only ever in a header, so the URL cannot carry it either
	assert.NotContains(t, c.BaseURL, testKey)
}

func TestContextCancellation(t *testing.T) {
	c := newClient(t, func(w http.ResponseWriter, r *http.Request) { time.Sleep(200 * time.Millisecond) })
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := c.Map(ctx, 1)
	assert.Error(t, err)
}

func TestBatchSizeConstantsMatchThePlan(t *testing.T) {
	assert.Equal(t, 100, BatchSize)
	assert.Equal(t, 5000, MapLimit)
}
