package networth

import (
	"fmt"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/JonathanRainee/hzzt/internal/platform/apperror"
)

func series(n int) []Point {
	out := make([]Point, n)
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := range out {
		out[i] = Point{Date: start.AddDate(0, 0, i).Format("2006-01-02"), NetWorth: decimal.NewFromInt(int64(i))}
	}
	return out
}

func TestDownsample(t *testing.T) {
	t.Run("short series are returned untouched", func(t *testing.T) {
		for _, n := range []int{0, 1, 2, 50, 120} {
			assert.Equal(t, series(n), Downsample(series(n), 120), n)
		}
	})

	for _, n := range []int{121, 150, 365, 400, 1000, 5000} {
		t.Run(fmt.Sprintf("%d points", n), func(t *testing.T) {
			in := series(n)
			out := Downsample(in, 120)
			assert.LessOrEqual(t, len(out), 120)
			assert.GreaterOrEqual(t, len(out), 115, "close to the limit, not much below it")
			assert.Equal(t, in[0], out[0], "the first point is kept")
			assert.Equal(t, in[n-1], out[len(out)-1], "the last (live) point is kept")
			for i := 1; i < len(out); i++ {
				assert.True(t, out[i].Date > out[i-1].Date, "strictly increasing dates, no duplicates")
			}
			// evenly spread: no gap more than about twice the average
			avg := float64(n) / float64(len(out)-1)
			for i := 1; i < len(out); i++ {
				a, _ := time.Parse("2006-01-02", out[i-1].Date)
				b, _ := time.Parse("2006-01-02", out[i].Date)
				assert.LessOrEqual(t, float64(b.Sub(a).Hours()/24), 2*avg+1)
			}
		})
	}

	t.Run("a tiny limit still keeps both ends", func(t *testing.T) {
		out := Downsample(series(100), 2)
		require.Len(t, out, 2)
		assert.Equal(t, "2026-01-01", out[0].Date)
		assert.Equal(t, series(100)[99], out[1])
		assert.Len(t, Downsample(series(100), 3), 3)
	})
	t.Run("a limit below two is a no-op", func(t *testing.T) {
		assert.Len(t, Downsample(series(10), 1), 10)
	})
}

func TestRangeStart(t *testing.T) {
	today := time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC)
	cases := map[string]string{"1M": "2026-10-18", "3M": "2026-08-18", "6M": "2026-05-18", "1Y": "2025-11-18", "1m": "2026-10-18", " 3m ": "2026-08-18"}
	for in, want := range cases {
		from, ok, err := rangeStart(in, today)
		require.NoError(t, err, in)
		require.True(t, ok)
		assert.Equal(t, want, from.Format("2006-01-02"), in)
	}
	_, ok, err := rangeStart("ALL", today)
	require.NoError(t, err)
	assert.False(t, ok, "ALL has no lower bound")

	for _, bad := range []string{"", "2M", "1W", "YEAR", "0"} {
		_, _, err := rangeStart(bad, today)
		require.Error(t, err, bad)
		ae, ok := apperror.As(err)
		require.True(t, ok)
		assert.Equal(t, apperror.CodeValidationFailed, ae.Code)
	}
	t.Run("a month end clamps like Go dates do", func(t *testing.T) {
		from, _, _ := rangeStart("1M", time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC))
		assert.Equal(t, "2026-03-03", from.Format("2006-01-02"), "31 Mar − 1 month normalizes through 31 Feb")
	})
}

func TestUTCDate(t *testing.T) {
	jkt, _ := time.LoadLocation("Asia/Jakarta")
	assert.Equal(t, "2026-11-17", utcDate(time.Date(2026, 11, 18, 3, 0, 0, 0, jkt)).Format("2006-01-02"), "03:00 Jakarta is still the 17th in UTC")
	assert.Equal(t, "2026-11-18", utcDate(time.Date(2026, 11, 18, 23, 59, 0, 0, time.UTC)).Format("2006-01-02"))
}
