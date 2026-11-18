package marketdata

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/JonathanRainee/hzzt/internal/marketdata/massive"
)

// stubProvider returns bars for the dates in bars (date → ticker → close) and records every request.
type stubProvider struct {
	// refused lists dates the plan refuses (HTTP 403) because they are too recent.
	refused map[string]bool
	bars    map[string]map[string]string
	dates   []string
	snaps   map[string]string
	snapErr error
	err     error
}

func (s *stubProvider) SearchTickers(context.Context, string, string, bool, int, time.Duration) ([]massive.Ticker, error) {
	return nil, nil
}

func (s *stubProvider) GroupedDaily(_ context.Context, date time.Time, want map[string]bool) (map[string]massive.Bar, error) {
	day := date.Format("2006-01-02")
	s.dates = append(s.dates, day)
	if s.refused[day] {
		return nil, massive.ErrNotEntitled
	}
	if s.err != nil {
		return nil, s.err
	}
	out := map[string]massive.Bar{}
	for t, c := range s.bars[day] {
		if want[t] {
			out[t] = massive.Bar{Price: decimal.RequireFromString(c), Day: date}
		}
	}
	return out, nil
}

func (s *stubProvider) Snapshot(_ context.Context, tickers []string) (map[string]massive.Snap, error) {
	if s.snapErr != nil {
		return nil, s.snapErr
	}
	out := map[string]massive.Snap{}
	for _, t := range tickers {
		if p, ok := s.snaps[t]; ok {
			out[t] = massive.Snap{Price: decimal.RequireFromString(p), AsOf: time.Date(2026, 11, 18, 15, 0, 0, 0, time.UTC)}
		}
	}
	return out, nil
}

func utc(y int, m time.Month, d, h, min int) time.Time {
	return time.Date(y, m, d, h, min, 0, 0, time.UTC)
}

func TestEodUsesTheLastCloseAndSkipsWeekendsWithoutCalls(t *testing.T) {
	// 2026-11-13 is a Friday, 14/15 the weekend, 16 Monday.
	p := &stubProvider{bars: map[string]map[string]string{"2026-11-13": {"AAPL": "351.91", "VOO": "673.19"}}}
	s := &EodStrategy{Provider: p}

	// Sunday 22:30 UTC: the weekend run uses Friday's close
	q, err := s.Quotes(context.Background(), []string{"AAPL", "VOO"}, utc(2026, 11, 15, 22, 30))
	require.NoError(t, err)
	assert.Equal(t, "351.91", q["AAPL"].Price.String())
	assert.Equal(t, "673.19", q["VOO"].Price.String())
	assert.Equal(t, []string{"2026-11-13"}, p.dates, "Sunday and Saturday are skipped without an API call")
	assert.True(t, utc(2026, 11, 13, 21, 0).Equal(q["AAPL"].AsOf), "as_of is the session close: %s", q["AAPL"].AsOf)
}

func TestEodFallsBackOverHolidays(t *testing.T) {
	// Thursday 2026-11-26 is Thanksgiving: no bars; Wednesday has them.
	p := &stubProvider{bars: map[string]map[string]string{"2026-11-25": {"AAPL": "300"}}}
	q, err := (&EodStrategy{Provider: p}).Quotes(context.Background(), []string{"AAPL"}, utc(2026, 11, 27, 3, 0)) // 22:00 ET on the 26th
	require.NoError(t, err)
	assert.Equal(t, "300", q["AAPL"].Price.String())
	assert.Equal(t, []string{"2026-11-26", "2026-11-25"}, p.dates)
}

func TestEodUsesTheNewYorkDateNotTheUTCDate(t *testing.T) {
	// 02:00 UTC on Wednesday the 18th is still Tuesday evening in New York.
	p := &stubProvider{bars: map[string]map[string]string{"2026-11-17": {"AAPL": "1"}, "2026-11-18": {"AAPL": "2"}}}
	q, err := (&EodStrategy{Provider: p}).Quotes(context.Background(), []string{"AAPL"}, utc(2026, 11, 18, 2, 0))
	require.NoError(t, err)
	assert.Equal(t, "1", q["AAPL"].Price.String())
	assert.Equal(t, []string{"2026-11-17"}, p.dates)
}

func TestEodGivesUpAfterFiveDaysBack(t *testing.T) {
	p := &stubProvider{} // no data at all
	q, err := (&EodStrategy{Provider: p}).Quotes(context.Background(), []string{"AAPL"}, utc(2026, 11, 18, 12, 0))
	require.NoError(t, err)
	assert.Empty(t, q)
	// Wed 18, Tue 17, Mon 16, (Sun 15, Sat 14 skipped), Fri 13 → six calendar days back is the limit
	assert.Equal(t, []string{"2026-11-18", "2026-11-17", "2026-11-16", "2026-11-13"}, p.dates)
}

func TestEodStopsAtTheFirstDayWithData(t *testing.T) {
	p := &stubProvider{bars: map[string]map[string]string{"2026-11-18": {"AAPL": "9"}, "2026-11-17": {"AAPL": "8"}}}
	q, err := (&EodStrategy{Provider: p}).Quotes(context.Background(), []string{"AAPL"}, utc(2026, 11, 18, 23, 0))
	require.NoError(t, err)
	assert.Equal(t, "9", q["AAPL"].Price.String())
	assert.Len(t, p.dates, 1, "one call returns the whole market")
}

func TestEodPropagatesProviderErrors(t *testing.T) {
	boom := errors.New("boom")
	_, err := (&EodStrategy{Provider: &stubProvider{err: boom}}).Quotes(context.Background(), []string{"AAPL"}, utc(2026, 11, 18, 12, 0))
	assert.ErrorIs(t, err, boom)
}

func TestSnapshotStrategy(t *testing.T) {
	p := &stubProvider{snaps: map[string]string{"AAPL": "352.1", "VOO": "673.5"}}
	s := &SnapshotStrategy{Provider: p}
	q, err := s.Quotes(context.Background(), []string{"AAPL", "VOO", "UNKNOWN"}, utc(2026, 11, 18, 15, 0))
	require.NoError(t, err)
	require.Len(t, q, 2)
	assert.Equal(t, "352.1", q["AAPL"].Price.String())
	assert.True(t, utc(2026, 11, 18, 15, 0).Equal(q["AAPL"].AsOf))

	p.snapErr = massive.ErrNotEntitled
	_, err = s.Quotes(context.Background(), []string{"AAPL"}, utc(2026, 11, 18, 15, 0))
	require.ErrorIs(t, err, massive.ErrNotEntitled)
	assert.Contains(t, err.Error(), "paid Massive plan", "the free-tier hint is spelled out")
	assert.Contains(t, err.Error(), "EOD")
}

func TestUsMarketHours(t *testing.T) {
	cases := []struct {
		name string
		at   time.Time
		open bool
	}{
		{"EST open (Nov): 14:30 UTC = 09:30", utc(2026, 11, 18, 14, 30), true},
		{"EST one minute before the open", utc(2026, 11, 18, 14, 29), false},
		{"EST mid-session", utc(2026, 11, 18, 18, 0), true},
		{"EST last minute", utc(2026, 11, 18, 20, 59), true},
		{"EST close: 21:00 UTC = 16:00", utc(2026, 11, 18, 21, 0), false},
		{"EDT open (Jul): 13:30 UTC = 09:30", utc(2026, 7, 15, 13, 30), true},
		{"EDT one minute before the open", utc(2026, 7, 15, 13, 29), false},
		{"EDT close: 20:00 UTC = 16:00", utc(2026, 7, 15, 20, 0), false},
		{"Saturday", utc(2026, 11, 14, 16, 0), false},
		{"Sunday", utc(2026, 11, 15, 16, 0), false},
		{"night", utc(2026, 11, 18, 3, 0), false},
	}
	for _, c := range cases {
		assert.Equal(t, c.open, UsMarketOpen(c.at), c.name)
	}
}

func TestStrategyMetadata(t *testing.T) {
	eod, snap := &EodStrategy{}, &SnapshotStrategy{}
	assert.Equal(t, ModeEOD, eod.Mode())
	assert.Equal(t, JobStockEOD, eod.JobName())
	assert.Equal(t, "30 22 * * *", eod.Schedule(), "daily after the US close")
	assert.True(t, eod.Active(utc(2026, 11, 14, 22, 30)))
	assert.Equal(t, 4*24*time.Hour, eod.StaleAfter(utc(2026, 11, 18, 12, 0)))

	assert.Equal(t, ModeSnapshot, snap.Mode())
	assert.Equal(t, JobStockSnapshot, snap.JobName())
	assert.Equal(t, "*/5 13-21 * * 1-5", snap.Schedule(), "every 5 minutes on weekdays")
	assert.True(t, snap.Active(utc(2026, 11, 18, 16, 0)))
	assert.False(t, snap.Active(utc(2026, 11, 18, 22, 0)), "no runs after the close")
	assert.Equal(t, 30*time.Minute, snap.StaleAfter(utc(2026, 11, 18, 16, 0)), "30 minutes during market hours")
	assert.Equal(t, 4*24*time.Hour, snap.StaleAfter(utc(2026, 11, 14, 16, 0)), "the last price stays valid while the market is closed")
}

func TestNewStrategySelectsByMode(t *testing.T) {
	p := &stubProvider{}
	s, err := NewStrategy("EOD", p)
	require.NoError(t, err)
	assert.IsType(t, &EodStrategy{}, s)
	s, err = NewStrategy("SNAPSHOT", p)
	require.NoError(t, err)
	assert.IsType(t, &SnapshotStrategy{}, s)
	_, err = NewStrategy("LIVE", p)
	assert.Error(t, err)
}

func TestProviderKindMapping(t *testing.T) {
	for at, want := range map[string][2]string{
		"CRYPTO": {ProviderCMC, KindCrypto}, "US_STOCK": {ProviderMassive, KindStock}, "GLOBAL_ETF": {ProviderMassive, KindETF},
	} {
		p, k, ok := providerKind(at)
		require.True(t, ok, at)
		assert.Equal(t, want, [2]string{p, k}, at)
	}
	for _, at := range []string{"ID_STOCK", "MUTUAL_FUND", "BOND"} {
		_, _, ok := providerKind(at)
		assert.False(t, ok, at)
	}
}

func TestEodFallsBackWhenTheFreePlanRefusesRecentDays(t *testing.T) {
	// the real free-tier behaviour: today's bars answer 403, yesterday's are available
	p := &stubProvider{
		refused: map[string]bool{"2026-11-18": true},
		bars:    map[string]map[string]string{"2026-11-17": {"AAPL": "351.91"}},
	}
	q, err := (&EodStrategy{Provider: p}).Quotes(context.Background(), []string{"AAPL"}, utc(2026, 11, 18, 14, 0))
	require.NoError(t, err)
	assert.Equal(t, "351.91", q["AAPL"].Price.String())
	assert.Equal(t, []string{"2026-11-18", "2026-11-17"}, p.dates)

	// yesterday refused as well (data released with a delay): two days back
	p = &stubProvider{
		refused: map[string]bool{"2026-11-18": true, "2026-11-17": true},
		bars:    map[string]map[string]string{"2026-11-16": {"AAPL": "340"}},
	}
	q, err = (&EodStrategy{Provider: p}).Quotes(context.Background(), []string{"AAPL"}, utc(2026, 11, 18, 14, 0))
	require.NoError(t, err)
	assert.Equal(t, "340", q["AAPL"].Price.String())

	// a plan that refuses older days too is a real entitlement problem and is reported
	p = &stubProvider{refused: map[string]bool{"2026-11-18": true, "2026-11-17": true, "2026-11-16": true}}
	_, err = (&EodStrategy{Provider: p}).Quotes(context.Background(), []string{"AAPL"}, utc(2026, 11, 18, 14, 0))
	require.ErrorIs(t, err, massive.ErrNotEntitled)
}
