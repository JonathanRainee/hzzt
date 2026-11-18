// Package massive is a small typed client for the Massive market data API
// (formerly Polygon.io): ticker search, grouped daily bars (EOD) and snapshots.
//
// The API key is sent in the Authorization header and never appears in URLs,
// errors or logs. Every request first passes a client-side token bucket so the
// plan's call budget (default 5 requests/minute on the free tier) is respected.
package massive

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/shopspring/decimal"
	"golang.org/x/time/rate"
)

// DefaultBaseURL is the Massive API host.
const DefaultBaseURL = "https://api.massive.com"

// DefaultRatePerMinute is the free-tier request budget.
const DefaultRatePerMinute = 5

// Errors a caller may react to.
var (
	ErrRateLimited  = errors.New("massive: rate limited")
	ErrUnauthorized = errors.New("massive: API key rejected")
	// ErrNotEntitled means the plan does not include this data (e.g. snapshots on the free tier).
	ErrNotEntitled = errors.New("massive: plan does not include this data")
	// ErrThrottled means the local token bucket would make the caller wait longer than it allowed.
	ErrThrottled = errors.New("massive: local rate limit, try again later")
)

// Throttle is a token bucket: at most ratePerMinute calls start per minute,
// evenly spaced. Burst is 1 so no window of one minute ever holds more calls
// than the budget. The clock and sleeper are injectable for tests.
type Throttle struct {
	l     *rate.Limiter
	now   func() time.Time
	sleep func(ctx context.Context, d time.Duration) error
}

// NewThrottle builds a throttle allowing ratePerMinute calls per minute.
func NewThrottle(ratePerMinute int) *Throttle {
	if ratePerMinute < 1 {
		ratePerMinute = DefaultRatePerMinute
	}
	return &Throttle{
		l:   rate.NewLimiter(rate.Every(time.Minute/time.Duration(ratePerMinute)), 1),
		now: time.Now,
		sleep: func(ctx context.Context, d time.Duration) error {
			t := time.NewTimer(d)
			defer t.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-t.C:
				return nil
			}
		},
	}
}

// Wait blocks until a call may start. maxWait bounds the delay: when the
// bucket would make the caller wait longer, nothing is reserved and
// ErrThrottled is returned. maxWait <= 0 means wait as long as it takes.
func (t *Throttle) Wait(ctx context.Context, maxWait time.Duration) error {
	now := t.now()
	r := t.l.ReserveN(now, 1)
	if !r.OK() {
		return ErrThrottled
	}
	d := r.DelayFrom(now)
	if maxWait > 0 && d > maxWait {
		r.CancelAt(now) // give the token back
		return ErrThrottled
	}
	if d <= 0 {
		return nil
	}
	if err := t.sleep(ctx, d); err != nil {
		r.CancelAt(t.now())
		return err
	}
	return nil
}

// Ticker is one entry of ticker search.
type Ticker struct {
	Symbol   string
	Name     string
	Type     string // CS, ETF, ...
	Exchange string
	Active   bool
}

// Bar is a daily bar from the grouped endpoint; Price is the close.
type Bar struct {
	Price decimal.Decimal
	Day   time.Time // the bar's session (UTC midnight of the trading date)
}

// Snap is the latest price from the snapshot endpoint.
type Snap struct {
	Price decimal.Decimal
	AsOf  time.Time
}

// Client talks to the Massive API.
type Client struct {
	BaseURL  string
	apiKey   string
	http     *http.Client
	throttle *Throttle
	retries  int
	backoff  time.Duration
}

// New builds a client; baseURL "" uses the default host and ratePerMinute <= 0 the default budget.
func New(baseURL, apiKey string, ratePerMinute int) *Client {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"), apiKey: apiKey,
		http: &http.Client{Timeout: 30 * time.Second}, throttle: NewThrottle(ratePerMinute),
		retries: 2, backoff: time.Second,
	}
}

// get performs a throttled GET. Network errors and 5xx are retried; every
// attempt takes its own token, so retries cannot exceed the budget either.
func (c *Client) get(ctx context.Context, path string, q url.Values, maxWait time.Duration) ([]byte, error) {
	u := c.BaseURL + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	var last error
	for attempt := 0; attempt <= c.retries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(c.backoff * time.Duration(attempt)):
			}
		}
		if err := c.throttle.Wait(ctx, maxWait); err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
		req.Header.Set("Accept", "application/json")
		res, err := c.http.Do(req)
		if err != nil {
			last = scrub(err, c.apiKey)
			continue
		}
		body, rerr := io.ReadAll(io.LimitReader(res.Body, 32<<20))
		_ = res.Body.Close()
		switch {
		case rerr != nil:
			last = rerr
		case res.StatusCode == http.StatusTooManyRequests:
			return nil, ErrRateLimited
		case res.StatusCode == http.StatusUnauthorized:
			return nil, ErrUnauthorized
		case res.StatusCode == http.StatusForbidden:
			return nil, ErrNotEntitled
		case res.StatusCode >= 500:
			last = fmt.Errorf("massive: HTTP %d", res.StatusCode)
		case res.StatusCode != http.StatusOK:
			return nil, fmt.Errorf("massive: HTTP %d", res.StatusCode)
		default:
			return body, nil
		}
	}
	return nil, last
}

func scrub(err error, key string) error {
	if key == "" {
		return err
	}
	return errors.New(strings.ReplaceAll(err.Error(), key, "[redacted]"))
}

// kindTypes maps our instrument kinds to Massive ticker types.
var kindTypes = map[string]string{"STOCK": "CS", "ETF": "ETF"}

// SearchTickers searches active US tickers of one kind ("STOCK" → CS, "ETF" → ETF).
// exact looks a ticker up by symbol instead of free-text search. maxWait bounds
// the local throttle delay (ErrThrottled when exceeded).
func (c *Client) SearchTickers(ctx context.Context, query, kind string, exact bool, limit int, maxWait time.Duration) ([]Ticker, error) {
	typ, ok := kindTypes[kind]
	if !ok {
		return nil, fmt.Errorf("massive: unknown kind %q", kind)
	}
	q := url.Values{"market": {"stocks"}, "active": {"true"}, "type": {typ}, "limit": {fmt.Sprint(limit)}}
	if exact {
		q.Set("ticker", strings.ToUpper(strings.TrimSpace(query)))
	} else {
		q.Set("search", strings.TrimSpace(query))
	}
	body, err := c.get(ctx, "/v3/reference/tickers", q, maxWait)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Results []struct {
			Ticker   string `json:"ticker"`
			Name     string `json:"name"`
			Type     string `json:"type"`
			Exchange string `json:"primary_exchange"`
			Active   bool   `json:"active"`
		} `json:"results"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("massive: decode tickers: %w", err)
	}
	out := make([]Ticker, 0, len(resp.Results))
	for _, r := range resp.Results {
		if r.Ticker == "" {
			continue
		}
		out = append(out, Ticker{Symbol: r.Ticker, Name: r.Name, Type: r.Type, Exchange: r.Exchange, Active: r.Active})
	}
	return out, nil
}

// GroupedDaily returns the daily close of every ticker in want for one trading
// date (one call returns the whole US market; only wanted tickers are kept).
// A weekend or holiday yields an empty map and no error.
func (c *Client) GroupedDaily(ctx context.Context, date time.Time, want map[string]bool) (map[string]Bar, error) {
	day := date.Format("2006-01-02")
	body, err := c.get(ctx, "/v2/aggs/grouped/locale/us/market/stocks/"+day, url.Values{"adjusted": {"true"}}, 0)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Results []struct {
			T string      `json:"T"`
			C json.Number `json:"c"`
			// encoding/json matches keys case-insensitively, so without a field for
			// the bar timestamp "t" it would be decoded into the ticker "T".
			Time json.RawMessage `json:"t"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("massive: decode grouped daily: %w", err)
	}
	session := time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, time.UTC)
	out := map[string]Bar{}
	for _, r := range resp.Results {
		if !want[r.T] {
			continue
		}
		price, err := decimal.NewFromString(r.C.String()) // from the literal, no float64
		if err != nil || !price.IsPositive() {
			continue
		}
		out[r.T] = Bar{Price: price, Day: session}
	}
	return out, nil
}

// Snapshot returns the latest price of the given tickers (paid plans only:
// ErrNotEntitled on the free tier). Tickers are requested in batches.
func (c *Client) Snapshot(ctx context.Context, tickers []string) (map[string]Snap, error) {
	out := map[string]Snap{}
	const batch = 100
	for start := 0; start < len(tickers); start += batch {
		end := min(start+batch, len(tickers))
		body, err := c.get(ctx, "/v2/snapshot/locale/us/markets/stocks/tickers",
			url.Values{"tickers": {strings.Join(tickers[start:end], ",")}}, 0)
		if err != nil {
			return out, err
		}
		var resp struct {
			Tickers []struct {
				Ticker    string `json:"ticker"`
				Updated   int64  `json:"updated"` // nanoseconds
				LastTrade *struct {
					P json.Number `json:"p"`
					T int64       `json:"t"` // nanoseconds
				} `json:"lastTrade"`
				Min *struct {
					C json.Number `json:"c"`
				} `json:"min"`
				Day *struct {
					C json.Number `json:"c"`
				} `json:"day"`
				PrevDay *struct {
					C json.Number `json:"c"`
				} `json:"prevDay"`
			} `json:"tickers"`
		}
		if err := json.Unmarshal(body, &resp); err != nil {
			return out, fmt.Errorf("massive: decode snapshot: %w", err)
		}
		for _, t := range resp.Tickers {
			price, at := decimal.Zero, time.Time{}
			pick := func(n json.Number) {
				if p, err := decimal.NewFromString(n.String()); err == nil && p.IsPositive() && price.IsZero() {
					price = p
				}
			}
			if t.LastTrade != nil {
				pick(t.LastTrade.P)
				if price.IsPositive() && t.LastTrade.T > 0 {
					at = time.Unix(0, t.LastTrade.T).UTC()
				}
			}
			if t.Min != nil {
				pick(t.Min.C)
			}
			if t.Day != nil {
				pick(t.Day.C)
			}
			if t.PrevDay != nil {
				pick(t.PrevDay.C)
			}
			if price.IsZero() {
				continue
			}
			if at.IsZero() && t.Updated > 0 {
				at = time.Unix(0, t.Updated).UTC()
			}
			if at.IsZero() {
				at = time.Now().UTC()
			}
			out[t.Ticker] = Snap{Price: price, AsOf: at}
		}
	}
	return out, nil
}
