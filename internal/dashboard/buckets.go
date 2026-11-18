package dashboard

import (
	"fmt"
	"time"

	"github.com/JonathanRainee/hzzt/internal/platform/period"
)

// Bucket is one bar of the analytics chart: a run of whole local days.
type Bucket struct {
	// FromDate and ToDate are the first and last local days (inclusive), YYYY-MM-DD.
	FromDate string
	ToDate   string
	Label    string
	// first/last are the same days as dates for arithmetic.
	first, last time.Time
}

// Bucket sizing: a range up to this many days gets one bucket per day, up to
// monthLikeDays gets 7-day buckets from its start, anything longer calendar months.
const (
	dailyMaxDays = 14
	weeklyMax    = 62
)

func dateOf(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

func ymd(t time.Time) string { return t.Format("2006-01-02") }

func daysBetween(a, b time.Time) int { return int(b.Sub(a).Hours()/24 + 0.5) }

// BuildBuckets lays out the analytics buckets for a period, in the user's timezone:
//
//   - a full calendar year → one bucket per month;
//   - up to 14 days → one bucket per day;
//   - up to about two months (month-length periods included) → 7-day buckets
//     from the start: days 1–7, 8–14, 15–21, 22–28 and 29–end of the period;
//   - anything longer → calendar months (the first and last may be partial);
//   - an unbounded period (ALL_TIME) → months from firstTxn (or the current month) to now.
func BuildBuckets(rng period.Range, loc *time.Location, now time.Time, firstTxn *time.Time) []Bucket {
	var first, last time.Time // inclusive local days
	switch {
	case rng.From == nil || rng.To == nil:
		start := now
		if firstTxn != nil {
			start = *firstTxn
		}
		s := start.In(loc)
		first = time.Date(s.Year(), s.Month(), 1, 0, 0, 0, 0, time.UTC)
		last = dateOf(now.In(loc))
		if rng.To != nil {
			last = dateOf(rng.To.In(loc).Add(-time.Nanosecond))
		}
		return months(first, last)
	default:
		first = dateOf(rng.From.In(loc))
		last = dateOf(rng.To.In(loc).Add(-time.Nanosecond))
	}
	if last.Before(first) {
		return nil
	}
	if first.Month() == time.January && first.Day() == 1 && last.Month() == time.December && last.Day() == 31 && first.Year() == last.Year() {
		return months(first, last)
	}
	n := daysBetween(first, last) + 1
	switch {
	case n <= dailyMaxDays:
		return step(first, last, 1)
	case n <= weeklyMax:
		return step(first, last, 7)
	default:
		return months(first, last)
	}
}

// step cuts [first, last] into runs of `size` days.
func step(first, last time.Time, size int) []Bucket {
	var out []Bucket
	for s := first; !s.After(last); s = s.AddDate(0, 0, size) {
		e := s.AddDate(0, 0, size-1)
		if e.After(last) {
			e = last
		}
		out = append(out, Bucket{FromDate: ymd(s), ToDate: ymd(e), Label: rangeLabel(s, e), first: s, last: e})
	}
	return out
}

// months cuts [first, last] at calendar month boundaries.
func months(first, last time.Time) []Bucket {
	var out []Bucket
	for s := first; !s.After(last); {
		monthEnd := time.Date(s.Year(), s.Month()+1, 0, 0, 0, 0, 0, time.UTC)
		e := monthEnd
		if e.After(last) {
			e = last
		}
		label := s.Format("Jan")
		if s.Year() != last.Year() || s.Year() != first.Year() {
			label = s.Format("Jan 2006")
		}
		out = append(out, Bucket{FromDate: ymd(s), ToDate: ymd(e), Label: label, first: s, last: e})
		s = monthEnd.AddDate(0, 0, 1)
	}
	return out
}

// rangeLabel renders "18 Nov", "1–7 Nov" or "29 Oct–4 Nov".
func rangeLabel(s, e time.Time) string {
	switch {
	case s.Equal(e):
		return s.Format("2 Jan")
	case s.Month() == e.Month() && s.Year() == e.Year():
		return fmt.Sprintf("%d–%s", s.Day(), e.Format("2 Jan"))
	default:
		return fmt.Sprintf("%s–%s", s.Format("2 Jan"), e.Format("2 Jan"))
	}
}

// indexOf returns the bucket holding a local day, or -1.
func indexOf(buckets []Bucket, day time.Time) int {
	d := dateOf(day)
	for i, b := range buckets {
		if !d.Before(b.first) && !d.After(b.last) {
			return i
		}
	}
	return -1
}
