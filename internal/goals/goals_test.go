package goals

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func day(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }

func TestMonthsUntil(t *testing.T) {
	today := day(2026, 11, 18)
	cases := []struct {
		name   string
		target time.Time
		want   int
	}{
		{"today", day(2026, 11, 18), 0},
		{"yesterday", day(2026, 11, 17), 0},
		{"a year ago", day(2025, 11, 18), 0},
		{"tomorrow is within the first month", day(2026, 11, 19), 1},
		{"the end of this month", day(2026, 11, 30), 1},
		{"exactly one month", day(2026, 12, 18), 1},
		{"one month and a day rounds up", day(2026, 12, 19), 2},
		{"two months", day(2027, 1, 18), 2},
		{"six months", day(2027, 5, 18), 6},
		{"a year", day(2027, 11, 18), 12},
		{"a year and a day", day(2027, 11, 19), 13},
		{"two years", day(2028, 11, 18), 24},
		{"far future", day(2040, 1, 1), 158},
	}
	for _, c := range cases {
		assert.Equal(t, c.want, MonthsUntil(today, c.target), c.name)
	}

	t.Run("month ends", func(t *testing.T) {
		assert.Equal(t, 1, MonthsUntil(day(2026, 1, 31), day(2026, 2, 28)), "31 Jan → 28 Feb")
		assert.Equal(t, 2, MonthsUntil(day(2026, 1, 31), day(2026, 3, 1)))
		assert.Equal(t, 1, MonthsUntil(day(2028, 2, 29), day(2028, 3, 29)), "leap day")
		assert.Equal(t, 12, MonthsUntil(day(2027, 12, 31), day(2028, 12, 31)))
		assert.Equal(t, 3, MonthsUntil(day(2026, 12, 15), day(2027, 3, 14)), "across a year end")
	})
	t.Run("the clock's time of day is ignored", func(t *testing.T) {
		late := time.Date(2026, 11, 18, 23, 59, 0, 0, time.UTC)
		assert.Equal(t, 1, MonthsUntil(late, day(2026, 12, 18)))
		assert.Equal(t, 0, MonthsUntil(late, day(2026, 11, 18)))
	})
}
