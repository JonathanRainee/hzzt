package marketdata

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/shopspring/decimal"

	"github.com/JonathanRainee/hzzt/internal/marketdata/massive"
)

// Price modes (MASSIVE_MODE).
const (
	ModeEOD      = "EOD"
	ModeSnapshot = "SNAPSHOT"
)

// Staleness windows for stock prices.
const (
	EODStaleAfter      = 4 * 24 * time.Hour // a long weekend plus a day of slack
	SnapshotStaleAfter = 30 * time.Minute   // during market hours
	// eodLookbackDays is how many days back an empty result (weekend, holiday) may fall.
	eodLookbackDays = 5
	// eodRecentDays is how many of the most recent days the free plan may refuse
	// (HTTP 403) because the day's bars are not released yet.
	eodRecentDays = 1
	// usCloseUTC is when a US session's close is reported (21:00 UTC covers both DST states).
	usCloseOffset = 21 * time.Hour
)

// StockProvider is the Massive capability the service needs; *massive.Client satisfies it.
type StockProvider interface {
	SearchTickers(ctx context.Context, query, kind string, exact bool, limit int, maxWait time.Duration) ([]massive.Ticker, error)
	GroupedDaily(ctx context.Context, date time.Time, want map[string]bool) (map[string]massive.Bar, error)
	Snapshot(ctx context.Context, tickers []string) (map[string]massive.Snap, error)
}

// Quote is a price at a point in time.
type Quote struct {
	Price decimal.Decimal
	AsOf  time.Time
}

// PriceStrategy is how US stock prices are fetched. It is selected by
// MASSIVE_MODE, so switching between the free end-of-day data and the paid
// snapshots needs no code change.
type PriceStrategy interface {
	// Mode is the MASSIVE_MODE value.
	Mode() string
	// JobName is the job_runs name of the scheduled refresh.
	JobName() string
	// Schedule is the cron spec (evaluated in UTC) of the scheduled refresh.
	Schedule() string
	// Active reports whether a scheduled run should fetch at `now`.
	Active(now time.Time) bool
	// Quotes returns the latest price of each ticker it can find.
	Quotes(ctx context.Context, tickers []string, now time.Time) (map[string]Quote, error)
	// StaleAfter is how old a price may get at `now` before it is stale.
	StaleAfter(now time.Time) time.Duration
}

// NewStrategy builds the strategy for MASSIVE_MODE.
func NewStrategy(mode string, p StockProvider) (PriceStrategy, error) {
	switch mode {
	case ModeEOD:
		return &EodStrategy{Provider: p}, nil
	case ModeSnapshot:
		return &SnapshotStrategy{Provider: p}, nil
	}
	return nil, fmt.Errorf("unknown MASSIVE_MODE %q", mode)
}

var newYork = func() *time.Location {
	l, err := time.LoadLocation("America/New_York")
	if err != nil {
		return time.FixedZone("EST", -5*3600)
	}
	return l
}()

// UsMarketOpen reports whether the regular US session (09:30–16:00 New York,
// Monday to Friday) is on at t. Exchange holidays are not modelled; a snapshot
// run on a holiday just returns the previous prices.
func UsMarketOpen(t time.Time) bool {
	ny := t.In(newYork)
	switch ny.Weekday() {
	case time.Saturday, time.Sunday:
		return false
	}
	mins := ny.Hour()*60 + ny.Minute()
	return mins >= 9*60+30 && mins < 16*60
}

func isWeekend(d time.Time) bool { return d.Weekday() == time.Saturday || d.Weekday() == time.Sunday }

// --- EOD ---------------------------------------------------------------------------

// EodStrategy uses the free tier's grouped daily bars: one call returns every
// US ticker's close, of which only the wanted ones are kept. It runs daily after
// the US close; an empty result (weekend or holiday) falls back to the previous
// trading day, up to five days back. The free plan refuses the most recent days
// (403: not released yet), which also falls back to the day before.
type EodStrategy struct {
	Provider StockProvider
}

func (*EodStrategy) Mode() string    { return ModeEOD }
func (*EodStrategy) JobName() string { return JobStockEOD }

// Schedule is 22:30 UTC every day (after the US close in both DST states); weekends
// simply fall back to Friday's close.
func (*EodStrategy) Schedule() string { return "30 22 * * *" }

func (*EodStrategy) Active(time.Time) bool { return true }

func (*EodStrategy) StaleAfter(time.Time) time.Duration { return EODStaleAfter }

// Quotes fetches the most recent daily close of each ticker.
func (s *EodStrategy) Quotes(ctx context.Context, tickers []string, now time.Time) (map[string]Quote, error) {
	want := make(map[string]bool, len(tickers))
	for _, t := range tickers {
		want[t] = true
	}
	ny := now.In(newYork)
	day := time.Date(ny.Year(), ny.Month(), ny.Day(), 0, 0, 0, 0, time.UTC)
	out := map[string]Quote{}
	for back := 0; back <= eodLookbackDays && len(out) == 0; back++ {
		d := day.AddDate(0, 0, -back)
		if isWeekend(d) {
			continue // no session: do not spend a call
		}
		bars, err := s.Provider.GroupedDaily(ctx, d, want)
		if errors.Is(err, massive.ErrNotEntitled) && back <= eodRecentDays {
			continue // the free plan does not release today's (or yesterday's) bars yet: use the one before
		}
		if err != nil {
			return nil, err
		}
		for t, b := range bars {
			out[t] = Quote{Price: b.Price, AsOf: b.Day.Add(usCloseOffset)}
		}
	}
	return out, nil
}

// --- Snapshot ------------------------------------------------------------------------

// SnapshotStrategy uses the paid snapshot endpoint: the latest trade of the
// wanted tickers, refreshed every five minutes during US market hours.
type SnapshotStrategy struct {
	Provider StockProvider
}

func (*SnapshotStrategy) Mode() string    { return ModeSnapshot }
func (*SnapshotStrategy) JobName() string { return JobStockSnapshot }

// Schedule is every 5 minutes from 13:00 to 21:59 UTC on weekdays; Active narrows it to the session.
func (*SnapshotStrategy) Schedule() string { return "*/5 13-21 * * 1-5" }

func (*SnapshotStrategy) Active(now time.Time) bool { return UsMarketOpen(now) }

// StaleAfter is 30 minutes while the market is open. Outside the session no new
// prices exist, so the last one stays valid for as long as an end-of-day price.
func (*SnapshotStrategy) StaleAfter(now time.Time) time.Duration {
	if UsMarketOpen(now) {
		return SnapshotStaleAfter
	}
	return EODStaleAfter
}

// Quotes fetches the latest snapshot price of each ticker.
func (s *SnapshotStrategy) Quotes(ctx context.Context, tickers []string, _ time.Time) (map[string]Quote, error) {
	snaps, err := s.Provider.Snapshot(ctx, tickers)
	if err != nil {
		if errors.Is(err, massive.ErrNotEntitled) {
			return nil, fmt.Errorf("MASSIVE_MODE=SNAPSHOT needs a paid Massive plan (use EOD on the free tier): %w", err)
		}
		return nil, err
	}
	out := make(map[string]Quote, len(snaps))
	for t, sn := range snaps {
		out[t] = Quote{Price: sn.Price, AsOf: sn.AsOf}
	}
	return out, nil
}
