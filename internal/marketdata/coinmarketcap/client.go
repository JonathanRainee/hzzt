// Package coinmarketcap is a small typed client for the CoinMarketCap Pro API:
// the coin map (free, used for local search) and batched simple prices.
//
// The API key is sent only in the X-CMC_PRO_API_KEY header and never appears in
// URLs, errors or logs.
package coinmarketcap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// DefaultBaseURL is the CoinMarketCap Pro API host.
const DefaultBaseURL = "https://pro-api.coinmarketcap.com"

// BatchSize is the number of ids requested per price call.
const BatchSize = 100

// MapLimit is how many coins (by rank) the coin map sync requests.
const MapLimit = 5000

// Errors a caller may react to.
var (
	// ErrRateLimited means the provider answered 429: stop the cycle and try later.
	ErrRateLimited = errors.New("coinmarketcap: rate limited")
	// ErrUnauthorized means the API key was rejected.
	ErrUnauthorized = errors.New("coinmarketcap: API key rejected")
)

// Coin is one entry of the coin map.
type Coin struct {
	ID     int64
	Symbol string
	Name   string
	Rank   int
	Active bool
}

// Quote is the latest USD price of one coin.
type Quote struct {
	ID    int64
	Price decimal.Decimal
	AsOf  time.Time
}

// Client talks to the CoinMarketCap Pro API.
type Client struct {
	BaseURL string
	apiKey  string
	http    *http.Client
	retries int
	backoff time.Duration
}

// New builds a client; baseURL "" uses the default host.
func New(baseURL, apiKey string) *Client {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"), apiKey: apiKey,
		http: &http.Client{Timeout: 10 * time.Second}, retries: 2, backoff: 500 * time.Millisecond,
	}
}

// status is the envelope CMC puts around every response. error_code is a number
// on some endpoints and a string on others.
type status struct {
	Timestamp    string          `json:"timestamp"`
	ErrorCode    json.RawMessage `json:"error_code"`
	ErrorMessage *string         `json:"error_message"`
	CreditCount  int             `json:"credit_count"`
}

func (s status) failed() error {
	code := strings.Trim(strings.TrimSpace(string(s.ErrorCode)), `"`)
	if code == "" || code == "0" || code == "null" {
		return nil
	}
	msg := ""
	if s.ErrorMessage != nil {
		msg = *s.ErrorMessage
	}
	return fmt.Errorf("coinmarketcap: error %s: %s", code, msg)
}

// get performs a GET with retries on network errors and 5xx. 429 and 401/403 are not retried.
func (c *Client) get(ctx context.Context, path string, q url.Values) ([]byte, error) {
	u := c.BaseURL + path + "?" + q.Encode()
	var last error
	for attempt := 0; attempt <= c.retries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(c.backoff * time.Duration(attempt)):
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("X-CMC_PRO_API_KEY", c.apiKey)
		req.Header.Set("Accept", "application/json")
		res, err := c.http.Do(req)
		if err != nil {
			last = scrub(err, c.apiKey)
			continue
		}
		body, rerr := io.ReadAll(io.LimitReader(res.Body, 8<<20))
		_ = res.Body.Close()
		switch {
		case rerr != nil:
			last = rerr
		case res.StatusCode == http.StatusTooManyRequests:
			return nil, ErrRateLimited
		case res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden:
			return nil, ErrUnauthorized
		case res.StatusCode >= 500:
			last = fmt.Errorf("coinmarketcap: HTTP %d", res.StatusCode)
		case res.StatusCode != http.StatusOK:
			return nil, fmt.Errorf("coinmarketcap: HTTP %d", res.StatusCode)
		default:
			return body, nil
		}
	}
	return nil, last
}

// scrub makes sure a transport error cannot carry the key.
func scrub(err error, key string) error {
	if key == "" {
		return err
	}
	return errors.New(strings.ReplaceAll(err.Error(), key, "[redacted]"))
}

// Map returns the top `limit` active coins by rank. This endpoint costs no credits.
func (c *Client) Map(ctx context.Context, limit int) ([]Coin, error) {
	q := url.Values{"listing_status": {"active"}, "sort": {"cmc_rank"}, "limit": {strconv.Itoa(limit)}}
	body, err := c.get(ctx, "/v1/cryptocurrency/map", q)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Data []struct {
			ID       int64  `json:"id"`
			Name     string `json:"name"`
			Symbol   string `json:"symbol"`
			Rank     *int   `json:"rank"`
			IsActive int    `json:"is_active"`
		} `json:"data"`
		Status status `json:"status"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("coinmarketcap: decode map: %w", err)
	}
	if err := resp.Status.failed(); err != nil {
		return nil, err
	}
	out := make([]Coin, 0, len(resp.Data))
	for _, d := range resp.Data {
		coin := Coin{ID: d.ID, Symbol: d.Symbol, Name: d.Name, Active: d.IsActive == 1}
		if d.Rank != nil {
			coin.Rank = *d.Rank
		}
		out = append(out, coin)
	}
	return out, nil
}

// Prices returns the latest USD prices for ids, batching BatchSize ids per call
// (a cycle costs ceil(len(ids)/100) calls). It stops at the first failed batch
// and returns what it has so far together with the error, so a rate limit keeps
// the prices already fetched.
func (c *Client) Prices(ctx context.Context, ids []int64) (map[int64]Quote, int, error) {
	out := make(map[int64]Quote, len(ids))
	calls := 0
	for start := 0; start < len(ids); start += BatchSize {
		end := min(start+BatchSize, len(ids))
		batch, err := c.priceBatch(ctx, ids[start:end])
		calls++
		if err != nil {
			return out, calls, err
		}
		for id, q := range batch {
			out[id] = q
		}
	}
	return out, calls, nil
}

func (c *Client) priceBatch(ctx context.Context, ids []int64) (map[int64]Quote, error) {
	strs := make([]string, len(ids))
	for i, id := range ids {
		strs[i] = strconv.FormatInt(id, 10)
	}
	body, err := c.get(ctx, "/v2/simple/price", url.Values{"id": {strings.Join(strs, ",")}})
	if err != nil {
		return nil, err
	}
	var resp struct {
		Data []struct {
			ID     int64 `json:"id"`
			Quotes []struct {
				Symbol string          `json:"symbol"`
				Price  json.RawMessage `json:"price"`
			} `json:"quotes"`
		} `json:"data"`
		Status status `json:"status"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("coinmarketcap: decode prices: %w", err)
	}
	if err := resp.Status.failed(); err != nil {
		return nil, err
	}
	asOf, err := time.Parse(time.RFC3339Nano, resp.Status.Timestamp)
	if err != nil {
		asOf = time.Now().UTC()
	}
	out := make(map[int64]Quote, len(resp.Data))
	for _, d := range resp.Data {
		for _, q := range d.Quotes {
			if q.Symbol != "USD" {
				continue
			}
			price, err := decimal.NewFromString(strings.TrimSpace(string(q.Price))) // no float64 round trip
			if err != nil || price.IsNegative() {
				continue
			}
			out[d.ID] = Quote{ID: d.ID, Price: price, AsOf: asOf.UTC()}
		}
	}
	return out, nil
}
