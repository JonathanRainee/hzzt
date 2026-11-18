// Package period turns a period preset ("TODAY", "THIS_PERIOD", ...) or a
// custom date range into a concrete time range in the requesting user's
// timezone. It is a pure function of its inputs.
package period

import (
	"fmt"
	"strings"
	"time"
)

// Presets (from the filter screen).
const (
	Today      = "TODAY"
	ThisWeek   = "THIS_WEEK" // Monday start
	ThisPeriod = "THIS_PERIOD"
	Last7Days  = "LAST_7_DAYS"
	Last30Days = "LAST_30_DAYS"
	Last60Days = "LAST_60_DAYS"
	Last90Days = "LAST_90_DAYS"
	ThisYear   = "THIS_YEAR"
	AllTime    = "ALL_TIME"
)

// Presets lists every valid preset name.
var Presets = []string{Today, ThisWeek, ThisPeriod, Last7Days, Last30Days, Last60Days, Last90Days, ThisYear, AllTime}

// Range is a half-open interval [From, To). A nil bound means unbounded.
type Range struct {
	From *time.Time
	To   *time.Time
}

// Contains reports whether t falls inside the range.
func (r Range) Contains(t time.Time) bool {
	return (r.From == nil || !t.Before(*r.From)) && (r.To == nil || t.Before(*r.To))
}

// Opts carries what the resolver needs besides the preset.
type Opts struct {
	// Location is the user's timezone (nil means UTC).
	Location *time.Location
	// BudgetStartDay is space.budget_period_start_day (1 to 31); 0 means 1.
	BudgetStartDay int
	// Now is the reference instant.
	Now time.Time
}

func (o Opts) loc() *time.Location {
	if o.Location == nil {
		return time.UTC
	}
	return o.Location
}

// IsPreset reports whether name (case-insensitive) is a known preset.
func IsPreset(name string) bool {
	up := strings.ToUpper(strings.TrimSpace(name))
	for _, p := range Presets {
		if p == up {
			return true
		}
	}
	return false
}

// Resolve returns the range for a preset. "Last N days" includes today, so
// LAST_7_DAYS covers today and the six days before it.
func Resolve(preset string, o Opts) (Range, error) {
	loc := o.loc()
	now := o.Now.In(loc)
	today := startOfDay(now)

	rng := func(from, to time.Time) (Range, error) {
		f, t := from.UTC(), to.UTC()
		return Range{From: &f, To: &t}, nil
	}

	switch strings.ToUpper(strings.TrimSpace(preset)) {
	case Today:
		return rng(today, addDays(today, 1))
	case ThisWeek:
		offset := (int(today.Weekday()) + 6) % 7 // Monday = 0
		start := addDays(today, -offset)
		return rng(start, addDays(start, 7))
	case ThisPeriod:
		from, to := budgetPeriod(today, o.BudgetStartDay)
		return rng(from, to)
	case Last7Days:
		return rng(addDays(today, -6), addDays(today, 1))
	case Last30Days:
		return rng(addDays(today, -29), addDays(today, 1))
	case Last60Days:
		return rng(addDays(today, -59), addDays(today, 1))
	case Last90Days:
		return rng(addDays(today, -89), addDays(today, 1))
	case ThisYear:
		start := time.Date(now.Year(), time.January, 1, 0, 0, 0, 0, loc)
		return rng(start, time.Date(now.Year()+1, time.January, 1, 0, 0, 0, 0, loc))
	case AllTime:
		return Range{}, nil
	default:
		return Range{}, fmt.Errorf("unknown period %q", preset)
	}
}

// Custom builds a range from inclusive YYYY-MM-DD dates in the user's timezone.
// Either bound may be empty (unbounded).
func Custom(from, to string, loc *time.Location) (Range, error) {
	if loc == nil {
		loc = time.UTC
	}
	var r Range
	if from != "" {
		d, err := time.ParseInLocation("2006-01-02", from, loc)
		if err != nil {
			return Range{}, fmt.Errorf("from must be YYYY-MM-DD")
		}
		u := d.UTC()
		r.From = &u
	}
	if to != "" {
		d, err := time.ParseInLocation("2006-01-02", to, loc)
		if err != nil {
			return Range{}, fmt.Errorf("to must be YYYY-MM-DD")
		}
		u := addDays(d, 1).UTC() // the "to" day is inclusive
		r.To = &u
	}
	if r.From != nil && r.To != nil && !r.From.Before(*r.To) {
		return Range{}, fmt.Errorf("from must not be after to")
	}
	return r, nil
}

func startOfDay(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}

// addDays adds calendar days (not 24h blocks), so DST changes keep midnight alignment.
func addDays(t time.Time, n int) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day()+n, 0, 0, 0, 0, t.Location())
}

func daysIn(year int, month time.Month) int {
	return time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

// periodStart is the budget-period start in a given month; a start day beyond
// the month's length clamps to its last day (31 in February → 28/29).
func periodStart(year int, month time.Month, startDay int, loc *time.Location) time.Time {
	d := startDay
	if n := daysIn(year, month); d > n {
		d = n
	}
	return time.Date(year, month, d, 0, 0, 0, 0, loc)
}

// budgetPeriod returns the [from, to) budget period containing today.
func budgetPeriod(today time.Time, startDay int) (time.Time, time.Time) {
	if startDay < 1 {
		startDay = 1
	}
	if startDay > 31 {
		startDay = 31
	}
	loc := today.Location()
	y, m := today.Year(), today.Month()
	thisStart := periodStart(y, m, startDay, loc)
	if !today.Before(thisStart) {
		ny, nm := y, m+1
		if nm > 12 {
			ny, nm = y+1, 1
		}
		return thisStart, periodStart(ny, nm, startDay, loc)
	}
	py, pm := y, m-1
	if pm < 1 {
		py, pm = y-1, 12
	}
	return periodStart(py, pm, startDay, loc), thisStart
}

// Previous returns the period right before a preset's period, for "vs previous
// period" comparisons: yesterday for TODAY, last week, the previous budget
// period, the N days before a LAST_N_DAYS window, the previous year. ALL_TIME has
// none (ok is false).
func Previous(preset string, o Opts) (r Range, ok bool, err error) {
	loc := o.loc()
	now := o.Now.In(loc)
	today := startOfDay(now)
	span := func(from, to time.Time) (Range, bool, error) {
		f, t := from.UTC(), to.UTC()
		return Range{From: &f, To: &t}, true, nil
	}
	switch strings.ToUpper(strings.TrimSpace(preset)) {
	case Today:
		return span(addDays(today, -1), today)
	case ThisWeek:
		offset := (int(today.Weekday()) + 6) % 7
		start := addDays(today, -offset)
		return span(addDays(start, -7), start)
	case ThisPeriod:
		from, _ := budgetPeriod(today, o.BudgetStartDay)
		pf, pt := budgetPeriod(addDays(from, -1), o.BudgetStartDay)
		return span(pf, pt)
	case Last7Days:
		return span(addDays(today, -13), addDays(today, -6))
	case Last30Days:
		return span(addDays(today, -59), addDays(today, -29))
	case Last60Days:
		return span(addDays(today, -119), addDays(today, -59))
	case Last90Days:
		return span(addDays(today, -179), addDays(today, -89))
	case ThisYear:
		return span(time.Date(now.Year()-1, time.January, 1, 0, 0, 0, 0, loc), time.Date(now.Year(), time.January, 1, 0, 0, 0, 0, loc))
	case AllTime:
		return Range{}, false, nil
	default:
		return Range{}, false, fmt.Errorf("unknown period %q", preset)
	}
}

// PreviousOfRange is the window of the same length immediately before a bounded
// range (used for custom date ranges); ok is false when the range is unbounded.
func PreviousOfRange(r Range) (Range, bool) {
	if r.From == nil || r.To == nil {
		return Range{}, false
	}
	d := r.To.Sub(*r.From)
	f := r.From.Add(-d)
	return Range{From: &f, To: r.From}, true
}
