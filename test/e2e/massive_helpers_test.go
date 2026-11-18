//go:build e2e

package e2e

import (
	"context"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"github.com/JonathanRainee/hzzt/internal/marketdata/massive"
)

// FakeMassive is a controllable Massive provider.
type FakeMassive struct {
	Tickers []massive.Ticker
	// Closes is the close of each ticker on any weekday that is not a holiday.
	Closes map[string]string
	// DayCloses overrides Closes for a date ("2006-01-02"); with Strict set, only
	// dates listed here have data (everything else is empty, like a holiday).
	DayCloses map[string]map[string]string
	Strict    bool
	// NotReleasedFrom ("2006-01-02"): dates on or after it answer ErrNotEntitled, like the
	// free plan does for the current day.
	NotReleasedFrom string
	Holidays        map[string]bool
	Snaps           map[string]string
	Err             error // returned by every call when set
	SearchErr       error
	SnapErr         error

	SearchCalls, GroupedCalls, SnapshotCalls int
	SearchQueries                            []string
	GroupedDates                             []string
	SnapshotAsOf                             time.Time
}

func newFakeMassive() *FakeMassive {
	return &FakeMassive{
		Tickers: []massive.Ticker{
			{Symbol: "AAPL", Name: "Apple Inc.", Type: "CS", Exchange: "XNAS", Active: true},
			{Symbol: "MSFT", Name: "Microsoft Corporation", Type: "CS", Exchange: "XNAS", Active: true},
			{Symbol: "TSLA", Name: "Tesla, Inc.", Type: "CS", Exchange: "XNAS", Active: true},
			{Symbol: "APLE", Name: "Apple Hospitality REIT, Inc.", Type: "CS", Exchange: "XNYS", Active: true},
			{Symbol: "OLDC", Name: "Delisted Corp", Type: "CS", Exchange: "XNYS", Active: false},
			{Symbol: "VOO", Name: "Vanguard S&P 500 ETF", Type: "ETF", Exchange: "ARCX", Active: true},
			{Symbol: "SPY", Name: "SPDR S&P 500 ETF Trust", Type: "ETF", Exchange: "ARCX", Active: true},
			{Symbol: "AAPX", Name: "T-Rex 2X Long Apple Daily Target ETF", Type: "ETF", Exchange: "BATS", Active: true},
		},
		Closes:   map[string]string{"AAPL": "351.91", "MSFT": "500", "TSLA": "400.5", "VOO": "673.19", "SPY": "600.25", "APLE": "15"},
		Holidays: map[string]bool{},
		Snaps:    map[string]string{"AAPL": "352.10", "VOO": "673.50", "MSFT": "501"},
	}
}

func (f *FakeMassive) SearchTickers(_ context.Context, query, kind string, exact bool, limit int, _ time.Duration) ([]massive.Ticker, error) {
	f.SearchCalls++
	f.SearchQueries = append(f.SearchQueries, kind+"|"+query)
	if f.Err != nil {
		return nil, f.Err
	}
	if f.SearchErr != nil {
		return nil, f.SearchErr
	}
	want := map[string]string{"STOCK": "CS", "ETF": "ETF"}[kind]
	q := strings.ToLower(strings.TrimSpace(query))
	var out []massive.Ticker
	for _, t := range f.Tickers {
		if t.Type != want || !t.Active {
			continue
		}
		match := strings.Contains(strings.ToLower(t.Symbol), q) || strings.Contains(strings.ToLower(t.Name), q)
		if exact {
			match = strings.EqualFold(t.Symbol, query)
		}
		if match {
			out = append(out, t)
		}
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (f *FakeMassive) GroupedDaily(_ context.Context, date time.Time, want map[string]bool) (map[string]massive.Bar, error) {
	f.GroupedCalls++
	day := date.Format("2006-01-02")
	f.GroupedDates = append(f.GroupedDates, day)
	if f.Err != nil {
		return nil, f.Err
	}
	if f.NotReleasedFrom != "" && day >= f.NotReleasedFrom {
		return nil, massive.ErrNotEntitled
	}
	closes := f.Closes
	if o, ok := f.DayCloses[day]; ok {
		closes = o
	} else if f.Strict {
		return map[string]massive.Bar{}, nil
	}
	if f.Holidays[day] || date.Weekday() == time.Saturday || date.Weekday() == time.Sunday {
		return map[string]massive.Bar{}, nil
	}
	out := map[string]massive.Bar{}
	session := time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, time.UTC)
	for t, c := range closes {
		if want[t] {
			out[t] = massive.Bar{Price: decimal.RequireFromString(c), Day: session}
		}
	}
	return out, nil
}

func (f *FakeMassive) Snapshot(_ context.Context, tickers []string) (map[string]massive.Snap, error) {
	f.SnapshotCalls++
	if f.Err != nil {
		return nil, f.Err
	}
	if f.SnapErr != nil {
		return nil, f.SnapErr
	}
	at := f.SnapshotAsOf
	if at.IsZero() {
		at = time.Now().UTC()
	}
	out := map[string]massive.Snap{}
	for _, t := range tickers {
		if p, ok := f.Snaps[t]; ok {
			out[t] = massive.Snap{Price: decimal.RequireFromString(p), AsOf: at}
		}
	}
	return out, nil
}
