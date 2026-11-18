package period

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func loc(t *testing.T, name string) *time.Location {
	t.Helper()
	l, err := time.LoadLocation(name)
	require.NoError(t, err)
	return l
}

// at builds an instant from a local wall-clock time.
func at(l *time.Location, y int, m time.Month, d, h, min int) time.Time {
	return time.Date(y, m, d, h, min, 0, 0, l)
}

func TestResolve(t *testing.T) {
	jkt := loc(t, "Asia/Jakarta") // UTC+7, no DST
	utc := time.UTC
	la := loc(t, "America/Los_Angeles")

	cases := []struct {
		name     string
		preset   string
		l        *time.Location
		now      time.Time
		startDay int
		from, to time.Time // local wall-clock bounds, as instants
	}{
		{"today midday", "TODAY", utc, at(utc, 2026, 11, 18, 12, 0), 1, at(utc, 2026, 11, 18, 0, 0), at(utc, 2026, 11, 19, 0, 0)},
		{"acceptance: today in Jakarta at 23:30 local is only that local day", "TODAY", jkt, at(jkt, 2026, 11, 17, 23, 30), 1,
			at(jkt, 2026, 11, 17, 0, 0), at(jkt, 2026, 11, 18, 0, 0)},
		{"today just after local midnight", "TODAY", jkt, at(jkt, 2026, 11, 18, 0, 1), 1, at(jkt, 2026, 11, 18, 0, 0), at(jkt, 2026, 11, 19, 0, 0)},
		{"UTC instant that is already tomorrow in Jakarta", "TODAY", jkt, at(utc, 2026, 11, 17, 18, 0), 1,
			at(jkt, 2026, 11, 18, 0, 0), at(jkt, 2026, 11, 19, 0, 0)},

		{"this week from a Wednesday", "THIS_WEEK", utc, at(utc, 2026, 11, 18, 9, 0), 1, at(utc, 2026, 11, 16, 0, 0), at(utc, 2026, 11, 23, 0, 0)},
		{"this week on Monday", "THIS_WEEK", utc, at(utc, 2026, 11, 16, 0, 0), 1, at(utc, 2026, 11, 16, 0, 0), at(utc, 2026, 11, 23, 0, 0)},
		{"this week on Sunday belongs to the Monday before", "THIS_WEEK", utc, at(utc, 2026, 11, 22, 23, 59), 1, at(utc, 2026, 11, 16, 0, 0), at(utc, 2026, 11, 23, 0, 0)},
		{"this week across a year end", "THIS_WEEK", utc, at(utc, 2027, 1, 1, 10, 0), 1, at(utc, 2026, 12, 28, 0, 0), at(utc, 2027, 1, 4, 0, 0)},

		{"last 7 days includes today", "LAST_7_DAYS", utc, at(utc, 2026, 11, 18, 12, 0), 1, at(utc, 2026, 11, 12, 0, 0), at(utc, 2026, 11, 19, 0, 0)},
		{"last 30 days", "LAST_30_DAYS", utc, at(utc, 2026, 11, 18, 12, 0), 1, at(utc, 2026, 10, 20, 0, 0), at(utc, 2026, 11, 19, 0, 0)},
		{"last 60 days", "LAST_60_DAYS", utc, at(utc, 2026, 11, 18, 12, 0), 1, at(utc, 2026, 9, 20, 0, 0), at(utc, 2026, 11, 19, 0, 0)},
		{"last 90 days over a leap day", "LAST_90_DAYS", utc, at(utc, 2028, 3, 31, 12, 0), 1, at(utc, 2028, 1, 2, 0, 0), at(utc, 2028, 4, 1, 0, 0)},
		{"last 90 days no leap day", "LAST_90_DAYS", utc, at(utc, 2026, 3, 31, 12, 0), 1, at(utc, 2026, 1, 1, 0, 0), at(utc, 2026, 4, 1, 0, 0)},

		{"this year", "THIS_YEAR", jkt, at(jkt, 2026, 7, 4, 8, 0), 1, at(jkt, 2026, 1, 1, 0, 0), at(jkt, 2027, 1, 1, 0, 0)},
		{"this year on new year's eve local", "THIS_YEAR", jkt, at(jkt, 2026, 12, 31, 23, 59), 1, at(jkt, 2026, 1, 1, 0, 0), at(jkt, 2027, 1, 1, 0, 0)},

		{"period start day 1 is the calendar month", "THIS_PERIOD", utc, at(utc, 2026, 11, 18, 0, 0), 1, at(utc, 2026, 11, 1, 0, 0), at(utc, 2026, 12, 1, 0, 0)},
		{"period default (0) is day 1", "THIS_PERIOD", utc, at(utc, 2026, 11, 18, 0, 0), 0, at(utc, 2026, 11, 1, 0, 0), at(utc, 2026, 12, 1, 0, 0)},
		{"start 15, after the 15th", "THIS_PERIOD", utc, at(utc, 2026, 11, 20, 0, 0), 15, at(utc, 2026, 11, 15, 0, 0), at(utc, 2026, 12, 15, 0, 0)},
		{"start 15, on the 15th", "THIS_PERIOD", utc, at(utc, 2026, 11, 15, 0, 0), 15, at(utc, 2026, 11, 15, 0, 0), at(utc, 2026, 12, 15, 0, 0)},
		{"start 15, before the 15th uses last month", "THIS_PERIOD", utc, at(utc, 2026, 11, 10, 0, 0), 15, at(utc, 2026, 10, 15, 0, 0), at(utc, 2026, 11, 15, 0, 0)},
		{"start 15, before the 15th in January spans the year end", "THIS_PERIOD", utc, at(utc, 2027, 1, 10, 0, 0), 15, at(utc, 2026, 12, 15, 0, 0), at(utc, 2027, 1, 15, 0, 0)},
		{"start 15, after the 15th in December spans the year end", "THIS_PERIOD", utc, at(utc, 2026, 12, 20, 0, 0), 15, at(utc, 2026, 12, 15, 0, 0), at(utc, 2027, 1, 15, 0, 0)},
		{"start 31 in a 30-day month clamps", "THIS_PERIOD", utc, at(utc, 2026, 11, 30, 12, 0), 31, at(utc, 2026, 11, 30, 0, 0), at(utc, 2026, 12, 31, 0, 0)},
		{"start 31, mid April belongs to the period that began Mar 31", "THIS_PERIOD", utc, at(utc, 2026, 4, 15, 0, 0), 31, at(utc, 2026, 3, 31, 0, 0), at(utc, 2026, 4, 30, 0, 0)},
		{"start 31 in February (non-leap) clamps to the 28th", "THIS_PERIOD", utc, at(utc, 2026, 2, 28, 12, 0), 31, at(utc, 2026, 2, 28, 0, 0), at(utc, 2026, 3, 31, 0, 0)},
		{"start 31 in February (leap) clamps to the 29th", "THIS_PERIOD", utc, at(utc, 2028, 2, 29, 12, 0), 31, at(utc, 2028, 2, 29, 0, 0), at(utc, 2028, 3, 31, 0, 0)},
		{"start 31 on Feb 27 (leap) is still in the January period", "THIS_PERIOD", utc, at(utc, 2028, 2, 27, 12, 0), 31, at(utc, 2028, 1, 31, 0, 0), at(utc, 2028, 2, 29, 0, 0)},
		{"start 30 in a leap February", "THIS_PERIOD", utc, at(utc, 2028, 3, 1, 0, 0), 30, at(utc, 2028, 2, 29, 0, 0), at(utc, 2028, 3, 30, 0, 0)},
		{"period honours the timezone", "THIS_PERIOD", jkt, at(utc, 2026, 11, 14, 18, 0), 15, at(jkt, 2026, 11, 15, 0, 0), at(jkt, 2026, 12, 15, 0, 0)},

		{"DST: today in Los Angeles on the spring-forward day is 23h long", "TODAY", la, at(la, 2026, 3, 8, 12, 0), 1, at(la, 2026, 3, 8, 0, 0), at(la, 2026, 3, 9, 0, 0)},
		{"DST: last 7 days stays midnight-aligned across the change", "LAST_7_DAYS", la, at(la, 2026, 3, 10, 12, 0), 1, at(la, 2026, 3, 4, 0, 0), at(la, 2026, 3, 11, 0, 0)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, err := Resolve(c.preset, Opts{Location: c.l, BudgetStartDay: c.startDay, Now: c.now})
			require.NoError(t, err)
			require.NotNil(t, r.From)
			require.NotNil(t, r.To)
			assert.True(t, c.from.Equal(*r.From), "from: want %s got %s", c.from, r.From.In(c.l))
			assert.True(t, c.to.Equal(*r.To), "to: want %s got %s", c.to, r.To.In(c.l))
		})
	}
}

func TestDSTDayLength(t *testing.T) {
	la := loc(t, "America/Los_Angeles")
	r, err := Resolve("TODAY", Opts{Location: la, Now: at(la, 2026, 3, 8, 12, 0)})
	require.NoError(t, err)
	assert.Equal(t, 23*time.Hour, r.To.Sub(*r.From))
}

func TestAllTimeIsUnbounded(t *testing.T) {
	r, err := Resolve("all_time", Opts{Now: time.Now()})
	require.NoError(t, err)
	assert.Nil(t, r.From)
	assert.Nil(t, r.To)
	assert.True(t, r.Contains(time.Unix(0, 0)))
}

func TestResolveUnknown(t *testing.T) {
	_, err := Resolve("YESTERDAY", Opts{Now: time.Now()})
	assert.Error(t, err)
	assert.False(t, IsPreset("YESTERDAY"))
	assert.True(t, IsPreset(" last_7_days "))
}

func TestContainsIsHalfOpen(t *testing.T) {
	r, err := Resolve("TODAY", Opts{Now: at(time.UTC, 2026, 11, 18, 12, 0)})
	require.NoError(t, err)
	assert.True(t, r.Contains(at(time.UTC, 2026, 11, 18, 0, 0)), "start is inclusive")
	assert.True(t, r.Contains(at(time.UTC, 2026, 11, 18, 23, 59)))
	assert.False(t, r.Contains(at(time.UTC, 2026, 11, 19, 0, 0)), "end is exclusive")
	assert.False(t, r.Contains(at(time.UTC, 2026, 11, 17, 23, 59)))
}

func TestCustom(t *testing.T) {
	jkt := loc(t, "Asia/Jakarta")
	r, err := Custom("2026-11-01", "2026-11-30", jkt)
	require.NoError(t, err)
	assert.True(t, at(jkt, 2026, 11, 1, 0, 0).Equal(*r.From))
	assert.True(t, at(jkt, 2026, 12, 1, 0, 0).Equal(*r.To), "the to date is inclusive")

	r, err = Custom("", "2026-11-30", nil)
	require.NoError(t, err)
	assert.Nil(t, r.From)
	r, err = Custom("2026-11-30", "2026-11-30", nil)
	require.NoError(t, err)
	assert.Equal(t, 24*time.Hour, r.To.Sub(*r.From), "single day")
	r, err = Custom("", "", nil)
	require.NoError(t, err)
	assert.Nil(t, r.To)

	for name, c := range map[string][2]string{
		"bad from": {"11/01/2026", ""}, "bad to": {"", "tomorrow"}, "reversed": {"2026-12-01", "2026-11-01"},
		"impossible date": {"2026-02-30", ""},
	} {
		_, err := Custom(c[0], c[1], nil)
		assert.Error(t, err, name)
	}
}

func TestPrevious(t *testing.T) {
	jkt := loc(t, "Asia/Jakarta")
	utc := time.UTC
	cases := []struct {
		name     string
		preset   string
		l        *time.Location
		now      time.Time
		startDay int
		from, to time.Time
	}{
		{"yesterday", "TODAY", utc, at(utc, 2026, 11, 18, 12, 0), 1, at(utc, 2026, 11, 17, 0, 0), at(utc, 2026, 11, 18, 0, 0)},
		{"yesterday in Jakarta", "TODAY", jkt, at(jkt, 2026, 11, 18, 0, 30), 1, at(jkt, 2026, 11, 17, 0, 0), at(jkt, 2026, 11, 18, 0, 0)},
		{"last week", "THIS_WEEK", utc, at(utc, 2026, 11, 18, 9, 0), 1, at(utc, 2026, 11, 9, 0, 0), at(utc, 2026, 11, 16, 0, 0)},
		{"the previous calendar month", "THIS_PERIOD", utc, at(utc, 2026, 11, 18, 0, 0), 1, at(utc, 2026, 10, 1, 0, 0), at(utc, 2026, 11, 1, 0, 0)},
		{"the previous month is shorter (February)", "THIS_PERIOD", utc, at(utc, 2026, 3, 10, 0, 0), 1, at(utc, 2026, 2, 1, 0, 0), at(utc, 2026, 3, 1, 0, 0)},
		{"the previous budget period (start 15)", "THIS_PERIOD", utc, at(utc, 2026, 11, 20, 0, 0), 15, at(utc, 2026, 10, 15, 0, 0), at(utc, 2026, 11, 15, 0, 0)},
		{"budget period across a year end", "THIS_PERIOD", utc, at(utc, 2027, 1, 10, 0, 0), 15, at(utc, 2026, 11, 15, 0, 0), at(utc, 2026, 12, 15, 0, 0)},
		{"start 31 clamps in the previous period too", "THIS_PERIOD", utc, at(utc, 2026, 3, 10, 0, 0), 31, at(utc, 2026, 1, 31, 0, 0), at(utc, 2026, 2, 28, 0, 0)},
		{"the 7 days before the last 7 days", "LAST_7_DAYS", utc, at(utc, 2026, 11, 18, 12, 0), 1, at(utc, 2026, 11, 5, 0, 0), at(utc, 2026, 11, 12, 0, 0)},
		{"the 30 days before", "LAST_30_DAYS", utc, at(utc, 2026, 11, 18, 12, 0), 1, at(utc, 2026, 9, 20, 0, 0), at(utc, 2026, 10, 20, 0, 0)},
		{"60", "LAST_60_DAYS", utc, at(utc, 2026, 11, 18, 12, 0), 1, at(utc, 2026, 7, 22, 0, 0), at(utc, 2026, 9, 20, 0, 0)},
		{"90", "LAST_90_DAYS", utc, at(utc, 2026, 11, 18, 12, 0), 1, at(utc, 2026, 5, 23, 0, 0), at(utc, 2026, 8, 21, 0, 0)},
		{"last year", "THIS_YEAR", utc, at(utc, 2026, 11, 18, 12, 0), 1, at(utc, 2025, 1, 1, 0, 0), at(utc, 2026, 1, 1, 0, 0)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, ok, err := Previous(c.preset, Opts{Location: c.l, BudgetStartDay: c.startDay, Now: c.now})
			require.NoError(t, err)
			require.True(t, ok)
			assert.True(t, c.from.Equal(*r.From), "from: want %s got %s", c.from, r.From.In(c.l))
			assert.True(t, c.to.Equal(*r.To), "to: want %s got %s", c.to, r.To.In(c.l))
			// the previous period ends where the current one starts (or before it)
			cur, err := Resolve(c.preset, Opts{Location: c.l, BudgetStartDay: c.startDay, Now: c.now})
			require.NoError(t, err)
			assert.False(t, r.To.After(*cur.From), "must not overlap the current period")
		})
	}

	t.Run("abutting presets share their boundary", func(t *testing.T) {
		for _, p := range []string{"TODAY", "THIS_WEEK", "THIS_PERIOD", "THIS_YEAR"} {
			o := Opts{Location: utc, BudgetStartDay: 15, Now: at(utc, 2026, 11, 18, 12, 0)}
			cur, _ := Resolve(p, o)
			prev, _, _ := Previous(p, o)
			assert.True(t, prev.To.Equal(*cur.From), p)
		}
	})
	t.Run("ALL_TIME has no previous period", func(t *testing.T) {
		_, ok, err := Previous("ALL_TIME", Opts{Now: time.Now()})
		require.NoError(t, err)
		assert.False(t, ok)
	})
	t.Run("unknown preset", func(t *testing.T) {
		_, _, err := Previous("YESTERDAY", Opts{Now: time.Now()})
		assert.Error(t, err)
	})
}

func TestPreviousOfRange(t *testing.T) {
	r, err := Custom("2026-11-10", "2026-11-16", nil) // 7 days
	require.NoError(t, err)
	prev, ok := PreviousOfRange(r)
	require.True(t, ok)
	assert.True(t, at(time.UTC, 2026, 11, 3, 0, 0).Equal(*prev.From))
	assert.True(t, r.From.Equal(*prev.To), "the same length, ending where the range starts")
	assert.Equal(t, r.To.Sub(*r.From), prev.To.Sub(*prev.From))

	_, ok = PreviousOfRange(Range{})
	assert.False(t, ok)
	open, _ := Custom("2026-11-10", "", nil)
	_, ok = PreviousOfRange(open)
	assert.False(t, ok, "an open-ended range has no previous window")
}
