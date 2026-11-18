package dashboard

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/JonathanRainee/hzzt/internal/platform/period"
)

func rngOf(t *testing.T, from, to string, loc *time.Location) period.Range {
	t.Helper()
	r, err := period.Custom(from, to, loc)
	require.NoError(t, err)
	return r
}

func labels(bs []Bucket) []string {
	out := make([]string, len(bs))
	for i, b := range bs {
		out[i] = b.Label
	}
	return out
}

func bounds(bs []Bucket) []string {
	out := make([]string, len(bs))
	for i, b := range bs {
		out[i] = b.FromDate + ".." + b.ToDate
	}
	return out
}

func TestMonthLengthPeriodsUseWeekLikeBuckets(t *testing.T) {
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name     string
		from, to string
		want     []string
		labels   []string
	}{
		{"acceptance: November ends at 29–30 Nov", "2026-11-01", "2026-11-30",
			[]string{"2026-11-01..2026-11-07", "2026-11-08..2026-11-14", "2026-11-15..2026-11-21", "2026-11-22..2026-11-28", "2026-11-29..2026-11-30"},
			[]string{"1–7 Nov", "8–14 Nov", "15–21 Nov", "22–28 Nov", "29–30 Nov"}},
		{"a 31-day month", "2026-12-01", "2026-12-31",
			[]string{"2026-12-01..2026-12-07", "2026-12-08..2026-12-14", "2026-12-15..2026-12-21", "2026-12-22..2026-12-28", "2026-12-29..2026-12-31"},
			[]string{"1–7 Dec", "8–14 Dec", "15–21 Dec", "22–28 Dec", "29–31 Dec"}},
		{"February has exactly four buckets", "2027-02-01", "2027-02-28",
			[]string{"2027-02-01..2027-02-07", "2027-02-08..2027-02-14", "2027-02-15..2027-02-21", "2027-02-22..2027-02-28"},
			[]string{"1–7 Feb", "8–14 Feb", "15–21 Feb", "22–28 Feb"}},
		{"a leap February ends with a single day", "2028-02-01", "2028-02-29",
			[]string{"2028-02-01..2028-02-07", "2028-02-08..2028-02-14", "2028-02-15..2028-02-21", "2028-02-22..2028-02-28", "2028-02-29..2028-02-29"},
			[]string{"1–7 Feb", "8–14 Feb", "15–21 Feb", "22–28 Feb", "29 Feb"}},
		{"a budget period that starts on the 15th", "2026-10-15", "2026-11-14",
			[]string{"2026-10-15..2026-10-21", "2026-10-22..2026-10-28", "2026-10-29..2026-11-04", "2026-11-05..2026-11-11", "2026-11-12..2026-11-14"},
			[]string{"15–21 Oct", "22–28 Oct", "29 Oct–4 Nov", "5–11 Nov", "12–14 Nov"}},
		{"the last 30 days", "2026-10-20", "2026-11-18",
			[]string{"2026-10-20..2026-10-26", "2026-10-27..2026-11-02", "2026-11-03..2026-11-09", "2026-11-10..2026-11-16", "2026-11-17..2026-11-18"},
			[]string{"20–26 Oct", "27 Oct–2 Nov", "3–9 Nov", "10–16 Nov", "17–18 Nov"}},
		{"62 days is still weekly", "2026-09-17", "2026-11-17",
			nil, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			bs := BuildBuckets(rngOf(t, c.from, c.to, time.UTC), time.UTC, now, nil)
			if c.want == nil {
				assert.Len(t, bs, 9, "62 days = 8 full weeks + 6 days")
				return
			}
			assert.Equal(t, c.want, bounds(bs))
			assert.Equal(t, c.labels, labels(bs))
		})
	}
}

func TestShortPeriodsGetDailyBuckets(t *testing.T) {
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	today, err := period.Resolve("TODAY", period.Opts{Now: now})
	require.NoError(t, err)
	assert.Equal(t, []string{"18 Nov"}, labels(BuildBuckets(today, time.UTC, now, nil)))

	week, err := period.Resolve("THIS_WEEK", period.Opts{Now: now})
	require.NoError(t, err)
	assert.Equal(t, []string{"16 Nov", "17 Nov", "18 Nov", "19 Nov", "20 Nov", "21 Nov", "22 Nov"}, labels(BuildBuckets(week, time.UTC, now, nil)))

	l7, err := period.Resolve("LAST_7_DAYS", period.Opts{Now: now})
	require.NoError(t, err)
	bs := BuildBuckets(l7, time.UTC, now, nil)
	require.Len(t, bs, 7)
	assert.Equal(t, "12 Nov", bs[0].Label)
	assert.Equal(t, "18 Nov", bs[6].Label)

	assert.Len(t, BuildBuckets(rngOf(t, "2026-11-01", "2026-11-14", time.UTC), time.UTC, now, nil), 14, "14 days is the daily limit")
	assert.Len(t, BuildBuckets(rngOf(t, "2026-11-01", "2026-11-15", time.UTC), time.UTC, now, nil), 3, "15 days switch to weeks")
}

func TestYearAndLongPeriodsGetMonthlyBuckets(t *testing.T) {
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	year, err := period.Resolve("THIS_YEAR", period.Opts{Now: now})
	require.NoError(t, err)
	bs := BuildBuckets(year, time.UTC, now, nil)
	require.Len(t, bs, 12)
	assert.Equal(t, "Jan", bs[0].Label)
	assert.Equal(t, "Dec", bs[11].Label)
	assert.Equal(t, "2026-02-01..2026-02-28", bs[1].FromDate+".."+bs[1].ToDate)

	t.Run("longer windows follow calendar months, partial at the ends", func(t *testing.T) {
		l90, err := period.Resolve("LAST_90_DAYS", period.Opts{Now: now})
		require.NoError(t, err)
		bs := BuildBuckets(l90, time.UTC, now, nil)
		assert.Equal(t, "2026-08-21..2026-08-31", bounds(bs)[0], "a partial first month")
		assert.Equal(t, "2026-11-01..2026-11-18", bounds(bs)[len(bs)-1], "a partial last month")
		assert.Len(t, bs, 4)
	})
	t.Run("a range across two years labels the year", func(t *testing.T) {
		bs := BuildBuckets(rngOf(t, "2026-11-15", "2027-02-15", time.UTC), time.UTC, now, nil)
		assert.Equal(t, []string{"Nov 2026", "Dec 2026", "Jan 2027", "Feb 2027"}, labels(bs))
	})
	t.Run("ALL_TIME runs from the first transaction's month to now", func(t *testing.T) {
		first := time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)
		bs := BuildBuckets(period.Range{}, time.UTC, now, &first)
		assert.Equal(t, []string{"Aug", "Sep", "Oct", "Nov"}, labels(bs))
		assert.Equal(t, "2026-11-01..2026-11-18", bounds(bs)[3], "the current month stops today")
		bs = BuildBuckets(period.Range{}, time.UTC, now, nil)
		assert.Equal(t, []string{"Nov"}, labels(bs), "no transactions yet: just this month")
	})
}

func TestBucketsAreContiguousAndCoverTheRange(t *testing.T) {
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	for _, c := range [][2]string{{"2026-11-01", "2026-11-30"}, {"2026-02-10", "2027-03-05"}, {"2028-02-01", "2028-02-29"}, {"2026-12-25", "2027-01-08"}, {"2026-01-01", "2026-12-31"}} {
		bs := BuildBuckets(rngOf(t, c[0], c[1], time.UTC), time.UTC, now, nil)
		require.NotEmpty(t, bs)
		assert.Equal(t, c[0], bs[0].FromDate)
		assert.Equal(t, c[1], bs[len(bs)-1].ToDate)
		for i := 1; i < len(bs); i++ {
			prev, _ := time.Parse("2006-01-02", bs[i-1].ToDate)
			next, _ := time.Parse("2006-01-02", bs[i].FromDate)
			assert.Equal(t, prev.AddDate(0, 0, 1), next, "no gap or overlap between %s and %s", bs[i-1].Label, bs[i].Label)
		}
	}
}

func TestBucketsUseTheUsersTimezone(t *testing.T) {
	jkt, err := time.LoadLocation("Asia/Jakarta")
	require.NoError(t, err)
	// 1 Nov .. 30 Nov in Jakarta is 31 Oct 17:00 UTC .. 30 Nov 17:00 UTC
	r := rngOf(t, "2026-11-01", "2026-11-30", jkt)
	bs := BuildBuckets(r, jkt, time.Now(), nil)
	require.Len(t, bs, 5)
	assert.Equal(t, "2026-11-01", bs[0].FromDate, "local days, not UTC days")
	assert.Equal(t, "29–30 Nov", bs[4].Label)
}

func TestIndexOf(t *testing.T) {
	bs := BuildBuckets(rngOf(t, "2026-11-01", "2026-11-30", time.UTC), time.UTC, time.Now(), nil)
	day := func(s string) time.Time { d, _ := time.Parse("2006-01-02", s); return d }
	assert.Equal(t, 0, indexOf(bs, day("2026-11-01")))
	assert.Equal(t, 0, indexOf(bs, day("2026-11-07")))
	assert.Equal(t, 1, indexOf(bs, day("2026-11-08")))
	assert.Equal(t, 4, indexOf(bs, day("2026-11-29")))
	assert.Equal(t, 4, indexOf(bs, day("2026-11-30")))
	assert.Equal(t, -1, indexOf(bs, day("2026-10-31")))
	assert.Equal(t, -1, indexOf(bs, day("2026-12-01")))
}
