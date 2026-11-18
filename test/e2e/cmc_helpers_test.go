//go:build e2e

package e2e

import (
	"context"
	"strconv"
	"time"

	"github.com/rs/zerolog"
	"github.com/shopspring/decimal"

	"github.com/JonathanRainee/hzzt/internal/marketdata/coinmarketcap"
)

// FakeCMC is a controllable CoinMarketCap provider.
type FakeCMC struct {
	now        func() time.Time
	Coins      []coinmarketcap.Coin
	Quotes     map[int64]string // id → USD price
	Err        error            // returned by every call when set
	MapCalls   int
	PriceCalls int // number of batched price calls
	IDsAsked   [][]int64
	// AsOf overrides the price timestamp (zero: the fake clock's now).
	AsOf time.Time
}

func (f *FakeCMC) Map(_ context.Context, limit int) ([]coinmarketcap.Coin, error) {
	f.MapCalls++
	if f.Err != nil {
		return nil, f.Err
	}
	if limit < len(f.Coins) {
		return f.Coins[:limit], nil
	}
	return f.Coins, nil
}

func (f *FakeCMC) Prices(_ context.Context, ids []int64) (map[int64]coinmarketcap.Quote, int, error) {
	out := map[int64]coinmarketcap.Quote{}
	calls := 0
	for start := 0; start < len(ids); start += coinmarketcap.BatchSize {
		end := min(start+coinmarketcap.BatchSize, len(ids))
		calls++
		f.PriceCalls++
		f.IDsAsked = append(f.IDsAsked, ids[start:end])
		if f.Err != nil {
			return out, calls, f.Err
		}
		asOf := f.AsOf
		if asOf.IsZero() {
			asOf = f.now().UTC()
		}
		for _, id := range ids[start:end] {
			if p, ok := f.Quotes[id]; ok {
				out[id] = coinmarketcap.Quote{ID: id, Price: decimal.RequireFromString(p), AsOf: asOf}
			}
		}
	}
	return out, calls, nil
}

func defaultCoins() []coinmarketcap.Coin {
	return []coinmarketcap.Coin{
		{ID: 1, Symbol: "BTC", Name: "Bitcoin", Rank: 1, Active: true},
		{ID: 1027, Symbol: "ETH", Name: "Ethereum", Rank: 2, Active: true},
		{ID: 825, Symbol: "USDT", Name: "Tether USDt", Rank: 3, Active: true},
		{ID: 5426, Symbol: "SOL", Name: "Solana", Rank: 5, Active: true},
		{ID: 3717, Symbol: "WBTC", Name: "Wrapped Bitcoin", Rank: 12, Active: true},
		{ID: 1831, Symbol: "BCH", Name: "Bitcoin Cash", Rank: 20, Active: true},
		{ID: 99001, Symbol: "BTC", Name: "Bitcoin Clone", Rank: 3000, Active: true}, // same symbol, lower rank
		{ID: 99002, Symbol: "OLD", Name: "Delisted Coin", Rank: 0, Active: false},
		{ID: 99003, Symbol: "P_C", Name: "100% Coin", Rank: 4000, Active: true},
	}
}

// bulkCoins builds n distinct coins for batching tests.
func bulkCoins(n int) ([]coinmarketcap.Coin, map[int64]string) {
	coins := make([]coinmarketcap.Coin, 0, n)
	prices := map[int64]string{}
	for i := 1; i <= n; i++ {
		id := int64(100000 + i)
		coins = append(coins, coinmarketcap.Coin{ID: id, Symbol: "C" + strconv.Itoa(i), Name: "Coin " + strconv.Itoa(i), Rank: i, Active: true})
		prices[id] = strconv.Itoa(i) + ".5"
	}
	return coins, prices
}

func zerologNop() zerolog.Logger { return zerolog.Nop() }
