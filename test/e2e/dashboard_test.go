//go:build e2e

package e2e

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type overviewBody struct {
	Currency         string                     `json:"currency"`
	Income           string                     `json:"income"`
	Expense          string                     `json:"expense"`
	Net              string                     `json:"net"`
	TransactionCount int                        `json:"transactionCount"`
	Period           struct{ From, To *string } `json:"period"`
	Previous         *struct {
		Income           string `json:"income"`
		Expense          string `json:"expense"`
		Net              string `json:"net"`
		TransactionCount int    `json:"transactionCount"`
	} `json:"previous"`
	Change struct {
		IncomePct  *string `json:"incomePct"`
		ExpensePct *string `json:"expensePct"`
		NetPct     *string `json:"netPct"`
	} `json:"change"`
	Warnings []struct{ Code, Message string } `json:"warnings"`
}

type breakdownBody struct {
	Currency string `json:"currency"`
	Type     string `json:"type"`
	Total    string `json:"total"`
	Items    []struct {
		CategoryID *string `json:"categoryId"`
		Name       string  `json:"name"`
		Icon       string  `json:"icon"`
		Color      string  `json:"color"`
		Amount     string  `json:"amount"`
		Pct        string  `json:"pct"`
		Count      int     `json:"count"`
	} `json:"items"`
	Uncategorized *struct {
		Name   string `json:"name"`
		Amount string `json:"amount"`
		Pct    string `json:"pct"`
		Count  int    `json:"count"`
	} `json:"uncategorized"`
	Warnings []struct{ Code, Message string } `json:"warnings"`
}

type analyticsBody struct {
	Currency string `json:"currency"`
	Buckets  []struct {
		Label    string `json:"label"`
		FromDate string `json:"fromDate"`
		ToDate   string `json:"toDate"`
		Income   string `json:"income"`
		Expense  string `json:"expense"`
	} `json:"buckets"`
	Warnings []struct{ Code, Message string } `json:"warnings"`
}

type calendarBody struct {
	Currency string `json:"currency"`
	Month    string `json:"month"`
	Days     []struct {
		Date    string `json:"date"`
		Income  string `json:"income"`
		Expense string `json:"expense"`
		Net     string `json:"net"`
		Count   int    `json:"count"`
	} `json:"days"`
}

func dashURL(sid, what string) string { return "/api/v1/spaces/" + sid + "/dashboard/" + what }

type dashEnv struct {
	*world
	idr walletBody
}

// newDashEnv fixes "now" at Wednesday 18 November 2026, 12:00 UTC.
func newDashEnv(t *testing.T, s *TestServer) *dashEnv {
	t.Helper()
	s.Clock.Set(time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC))
	w := newWorld(t, s)
	require.NoError(t, s.Currency.Refresh(context.Background())) // 1 USD = 16000 IDR
	b := wal("Rupiah")
	b["currency"], b["initialBalance"] = "IDR", "100000000"
	return &dashEnv{world: w, idr: s.newWallet(w.u, w.sid, b)}
}

func (e *dashEnv) tx(typ string, w walletBody, amount, cat, at string) txnBody {
	body := map[string]any{"type": typ, "walletId": w.ID, "amount": amount, "occurredAt": at}
	if cat != "" {
		body["categoryId"] = cat
	}
	return e.must(body)
}

func (e *dashEnv) get(what, query string, out any) {
	e.s.t.Helper()
	res := e.s.Get(dashURL(e.sid, what)+query, bearer(e.u.AccessToken))
	require.Equal(e.s.t, http.StatusOK, res.Status, "%s%s: %s", what, query, res.Body)
	res.JSON(e.s.t, out)
}

func (e *dashEnv) overview(query string) overviewBody {
	var o overviewBody
	e.get("overview", query, &o)
	return o
}

func TestOverviewTotalsMatchTheTransactions(t *testing.T) {
	s := NewTestServer(t)
	e := newDashEnv(t, s)

	// inside THIS_PERIOD (November 2026)
	e.tx("INCOME", e.a, "3000", e.cat["Salary"], "2026-11-01T09:00:00Z")
	e.tx("INCOME", e.a, "200.50", "", "2026-11-10T09:00:00Z")
	e.tx("EXPENSE", e.a, "100", e.cat["Transport"], "2026-11-03T09:00:00Z")
	e.tx("EXPENSE", e.a, "50.25", e.cat["Snacks"], "2026-11-04T09:00:00Z")
	e.tx("EXPENSE", e.idr, "1600000", e.cat["Shopping"], "2026-11-05T09:00:00Z") // 100 USD
	// things that are neither income nor expense
	e.transfer(e.a, e.b, "70", "70", time.Date(2026, 11, 6, 9, 0, 0, 0, time.UTC))
	require.Equal(t, http.StatusOK, s.Do(http.MethodPut, walURL(e.sid, "/", e.b.ID, "/balance"), map[string]any{"newBalance": "9999"}, bearer(e.u.AccessToken)).Status) // an adjustment
	// outside the period
	e.tx("EXPENSE", e.a, "999", "", "2026-10-31T23:59:00Z")
	e.tx("INCOME", e.a, "999", "", "2026-12-01T00:00:00Z")

	o := e.overview("?period=THIS_PERIOD")
	assert.Equal(t, "USD", o.Currency)
	assert.True(t, dec("3200.5").Equal(dec(o.Income)), "income %s", o.Income)
	assert.True(t, dec("250.25").Equal(dec(o.Expense)), "100 + 50.25 + 1,600,000 IDR (= 100): %s", o.Expense)
	assert.True(t, dec("2950.25").Equal(dec(o.Net)))
	assert.Equal(t, 5, o.TransactionCount, "only income and expense: not the transfer, the adjustment or the opening balances")
	require.NotNil(t, o.Period.From)
	assert.Equal(t, "2026-11-01T00:00:00Z", *o.Period.From)
	assert.Equal(t, "2026-12-01T00:00:00Z", *o.Period.To)
	assert.Empty(t, o.Warnings)

	t.Run("the same totals as the transaction list and the daily summary", func(t *testing.T) {
		list := e.list("?from=2026-11-01&to=2026-11-30&types=INCOME,EXPENSE&limit=100")
		income, expense := dec("0"), dec("0")
		for _, it := range list.Items {
			amt := dec(it.Amount)
			if it.Currency == "IDR" {
				amt = amt.Div(dec("16000"))
			}
			if it.Type == "INCOME" {
				income = income.Add(amt)
			} else {
				expense = expense.Add(amt)
			}
		}
		assert.True(t, income.Equal(dec(o.Income)), "%s vs %s", income, o.Income)
		assert.True(t, expense.Equal(dec(o.Expense)))
		assert.Len(t, list.Items, o.TransactionCount)
		si, se := dec("0"), dec("0")
		for _, d := range e.summary("?from=2026-11-01&to=2026-11-30&types=INCOME,EXPENSE").Items {
			si, se = si.Add(dec(d.Income)), se.Add(dec(d.Expense))
		}
		assert.True(t, si.Equal(dec(o.Income)))
		assert.True(t, se.Equal(dec(o.Expense)))
	})

	t.Run("default period is THIS_PERIOD", func(t *testing.T) {
		assert.Equal(t, o.Income, e.overview("").Income)
	})

	t.Run("a currency without a rate is excluded with a warning", func(t *testing.T) {
		sg := wal("Singapore")
		sg["currency"], sg["initialBalance"] = "SGD", "1000"
		sgd := s.newWallet(e.u, e.sid, sg)
		e.tx("EXPENSE", sgd, "300", "", "2026-11-07T09:00:00Z")
		got := e.overview("")
		assert.True(t, dec("250.25").Equal(dec(got.Expense)), "the SGD expense is left out")
		assert.Equal(t, 6, got.TransactionCount, "but it is still a transaction")
		require.Len(t, got.Warnings, 1)
		assert.Equal(t, "FX_RATE_UNAVAILABLE", got.Warnings[0].Code)
	})
}

func TestOverviewComparesWithThePreviousPeriod(t *testing.T) {
	s := NewTestServer(t)
	e := newDashEnv(t, s)
	e.tx("INCOME", e.a, "150", "", "2026-11-05T09:00:00Z")
	e.tx("EXPENSE", e.a, "30", "", "2026-11-06T09:00:00Z")
	e.tx("INCOME", e.a, "100", "", "2026-10-05T09:00:00Z") // previous month
	e.tx("EXPENSE", e.a, "60", "", "2026-10-06T09:00:00Z")
	e.tx("EXPENSE", e.a, "40", "", "2026-10-07T09:00:00Z")

	o := e.overview("?period=THIS_PERIOD")
	require.NotNil(t, o.Previous)
	assert.True(t, dec("100").Equal(dec(o.Previous.Income)))
	assert.True(t, dec("100").Equal(dec(o.Previous.Expense)))
	assert.Equal(t, 3, o.Previous.TransactionCount)
	require.NotNil(t, o.Change.IncomePct)
	assert.True(t, dec("50").Equal(dec(*o.Change.IncomePct)), "150 vs 100: +50%%")
	require.NotNil(t, o.Change.ExpensePct)
	assert.True(t, dec("-70").Equal(dec(*o.Change.ExpensePct)), "30 vs 100")
	assert.True(t, dec("120").Equal(dec(o.Net)))
	assert.True(t, dec("0").Equal(dec(o.Previous.Net)))
	assert.Nil(t, o.Change.NetPct, "a previous value of 0 gives null, not infinity")

	t.Run("null when the previous value is 0", func(t *testing.T) {
		got := e.overview("?period=THIS_YEAR")
		require.NotNil(t, got.Previous, "last year exists as a comparison")
		assert.True(t, dec("0").Equal(dec(got.Previous.Income)))
		assert.Nil(t, got.Change.IncomePct)
		assert.Nil(t, got.Change.ExpensePct)
	})
	t.Run("ALL_TIME has no previous period", func(t *testing.T) {
		got := e.overview("?period=ALL_TIME")
		assert.Nil(t, got.Previous)
		assert.Nil(t, got.Change.IncomePct)
		assert.Nil(t, got.Period.From)
		assert.True(t, dec("250").Equal(dec(got.Income)))
	})
	t.Run("a custom range is compared with the same number of days before it", func(t *testing.T) {
		e.tx("INCOME", e.a, "80", "", "2026-11-12T09:00:00Z")
		got := e.overview("?from=2026-11-10&to=2026-11-16") // 7 days
		assert.True(t, dec("80").Equal(dec(got.Income)))
		require.NotNil(t, got.Previous)
		assert.True(t, dec("150").Equal(dec(got.Previous.Income)), "3–9 Nov holds the 150 of the 5th")
		got = e.overview("?from=2026-11-03&to=2026-11-09")
		assert.True(t, dec("150").Equal(dec(got.Income)))
		assert.True(t, dec("0").Equal(dec(got.Previous.Income)))
	})
	t.Run("presets", func(t *testing.T) {
		today := e.overview("?period=TODAY")
		assert.True(t, dec("0").Equal(dec(today.Income)))
		require.NotNil(t, today.Previous)
		for _, p := range []string{"THIS_WEEK", "LAST_7_DAYS", "LAST_30_DAYS", "LAST_60_DAYS", "LAST_90_DAYS", "THIS_YEAR", "ALL_TIME", "this_period"} {
			assert.NotEmpty(t, e.overview("?period="+p).Currency, p)
		}
		l30 := e.overview("?period=LAST_30_DAYS")
		assert.True(t, dec("230").Equal(dec(l30.Income)), "150 + 80; the 5 Oct income is before 19 Oct")
		require.NotNil(t, l30.Previous)
	})
	t.Run("validation", func(t *testing.T) {
		for _, q := range []string{"?period=YESTERDAY", "?from=x", "?from=2026-12-01&to=2026-11-01"} {
			assert.Equal(t, http.StatusBadRequest, s.Get(dashURL(e.sid, "overview")+q, bearer(e.u.AccessToken)).Status, q)
		}
	})
}

func TestOverviewUsesTheBudgetStartDayAndTimezone(t *testing.T) {
	s := NewTestServer(t)
	e := newDashEnv(t, s)
	_, err := s.Tx.Exec(context.Background(), `UPDATE spaces SET budget_period_start_day = 15 WHERE id = $1`, e.sid)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, s.Do(http.MethodPatch, "/api/v1/me", map[string]any{"timezone": "Asia/Jakarta"}, bearer(e.u.AccessToken)).Status)

	// now is 19:00 on 18 Nov in Jakarta; the period is 15 Nov .. 15 Dec local time
	e.tx("EXPENSE", e.a, "10", "", "2026-11-14T16:59:00Z") // 23:59 on the 14th in Jakarta: previous period
	e.tx("EXPENSE", e.a, "20", "", "2026-11-14T17:00:00Z") // 00:00 on the 15th: this period
	e.tx("EXPENSE", e.a, "30", "", "2026-10-20T00:00:00Z") // previous period
	o := e.overview("?period=THIS_PERIOD")
	assert.True(t, dec("20").Equal(dec(o.Expense)), "%s", o.Expense)
	require.NotNil(t, o.Previous)
	assert.True(t, dec("40").Equal(dec(o.Previous.Expense)), "15 Oct – 15 Nov: 10 + 30")
	assert.Equal(t, "2026-11-14T17:00:00Z", *o.Period.From, "15 Nov 00:00 Jakarta")
}

func TestCategoryBreakdown(t *testing.T) {
	s := NewTestServer(t)
	e := newDashEnv(t, s)
	e.tx("EXPENSE", e.a, "300", e.cat["Transport"], "2026-11-02T09:00:00Z")
	e.tx("EXPENSE", e.a, "100", e.cat["Transport"], "2026-11-03T09:00:00Z")
	e.tx("EXPENSE", e.idr, "4800000", e.cat["Transport"], "2026-11-04T09:00:00Z") // 300 USD
	e.tx("EXPENSE", e.a, "200", e.cat["Shopping"], "2026-11-05T09:00:00Z")
	e.tx("EXPENSE", e.a, "100", "", "2026-11-06T09:00:00Z") // uncategorized
	e.tx("INCOME", e.a, "1000", e.cat["Salary"], "2026-11-01T09:00:00Z")
	e.tx("EXPENSE", e.a, "50", e.cat["Gifts"], "2026-10-15T09:00:00Z") // another period
	e.transfer(e.a, e.b, "70", "70", time.Date(2026, 11, 6, 9, 0, 0, 0, time.UTC))

	var b breakdownBody
	e.get("category-breakdown", "?period=THIS_PERIOD&type=EXPENSE", &b)
	assert.Equal(t, "EXPENSE", b.Type)
	assert.Equal(t, "USD", b.Currency)
	assert.True(t, dec("1000").Equal(dec(b.Total)), "700 + 200 + 100: %s", b.Total)
	require.Len(t, b.Items, 2)
	assert.Equal(t, "Transport", b.Items[0].Name, "largest first")
	assert.True(t, dec("700").Equal(dec(b.Items[0].Amount)), "300 + 100 + 4,800,000 IDR (= 300)")
	assert.True(t, dec("70").Equal(dec(b.Items[0].Pct)))
	assert.Equal(t, 3, b.Items[0].Count)
	assert.Equal(t, "🚗", b.Items[0].Icon)
	assert.NotEmpty(t, b.Items[0].Color)
	require.NotNil(t, b.Items[0].CategoryID)
	assert.Equal(t, "Shopping", b.Items[1].Name)
	assert.True(t, dec("20").Equal(dec(b.Items[1].Pct)))
	require.NotNil(t, b.Uncategorized)
	assert.True(t, dec("100").Equal(dec(b.Uncategorized.Amount)))
	assert.True(t, dec("10").Equal(dec(b.Uncategorized.Pct)))
	assert.Equal(t, 1, b.Uncategorized.Count)
	assert.Equal(t, "Uncategorized", b.Uncategorized.Name)

	t.Run("percentages add up to about 100", func(t *testing.T) {
		sum := dec(b.Uncategorized.Pct)
		for _, it := range b.Items {
			sum = sum.Add(dec(it.Pct))
		}
		assert.True(t, dec("100").Equal(sum), sum.String())
	})
	t.Run("income, and an explicit default", func(t *testing.T) {
		var inc breakdownBody
		e.get("category-breakdown", "?type=income", &inc)
		assert.Equal(t, "INCOME", inc.Type)
		require.Len(t, inc.Items, 1)
		assert.Equal(t, "Salary", inc.Items[0].Name)
		assert.True(t, dec("100").Equal(dec(inc.Items[0].Pct)))
		assert.Nil(t, inc.Uncategorized)
		var def breakdownBody
		e.get("category-breakdown", "", &def)
		assert.Equal(t, "EXPENSE", def.Type)
	})
	t.Run("other periods and custom ranges", func(t *testing.T) {
		var oct breakdownBody
		e.get("category-breakdown", "?from=2026-10-01&to=2026-10-31", &oct)
		require.Len(t, oct.Items, 1)
		assert.Equal(t, "Gifts", oct.Items[0].Name)
		assert.True(t, dec("100").Equal(dec(oct.Items[0].Pct)))
		var all breakdownBody
		e.get("category-breakdown", "?period=ALL_TIME", &all)
		assert.True(t, dec("1050").Equal(dec(all.Total)))
	})
	t.Run("a deleted category still shows for its history", func(t *testing.T) {
		require.Equal(t, http.StatusNoContent, s.Do(http.MethodDelete, catURL(e.sid, "/", e.cat["Shopping"]), nil, bearer(e.u.AccessToken)).Status)
		var again breakdownBody
		e.get("category-breakdown", "", &again)
		found := false
		for _, it := range again.Items {
			found = found || it.Name == "Shopping"
		}
		assert.True(t, found)
	})
	t.Run("an empty period", func(t *testing.T) {
		var empty breakdownBody
		e.get("category-breakdown", "?period=TODAY", &empty)
		assert.True(t, dec("0").Equal(dec(empty.Total)))
		assert.Empty(t, empty.Items)
		assert.NotNil(t, empty.Items, "an empty list, not null")
		assert.Nil(t, empty.Uncategorized)
	})
	t.Run("validation", func(t *testing.T) {
		for _, q := range []string{"?type=TRANSFER", "?type=x", "?period=NOPE"} {
			assert.Equal(t, http.StatusBadRequest, s.Get(dashURL(e.sid, "category-breakdown")+q, bearer(e.u.AccessToken)).Status, q)
		}
	})
}

func TestAnalyticsBuckets(t *testing.T) {
	s := NewTestServer(t)
	e := newDashEnv(t, s)
	put := func(typ, day, amount string) { e.tx(typ, e.a, amount, "", "2026-11-"+day+"T09:00:00Z") }
	put("EXPENSE", "01", "10")
	put("EXPENSE", "07", "5")
	put("INCOME", "08", "100")
	put("EXPENSE", "14", "20")
	put("EXPENSE", "15", "1")
	put("EXPENSE", "28", "40")
	put("EXPENSE", "29", "7")
	put("INCOME", "30", "50")
	e.transfer(e.a, e.b, "999", "999", time.Date(2026, 11, 29, 9, 0, 0, 0, time.UTC)) // not counted

	var a analyticsBody
	e.get("analytics", "?period=THIS_PERIOD", &a)
	require.Len(t, a.Buckets, 5)
	labels := []string{}
	for _, b := range a.Buckets {
		labels = append(labels, b.Label)
	}
	assert.Equal(t, []string{"1–7 Nov", "8–14 Nov", "15–21 Nov", "22–28 Nov", "29–30 Nov"}, labels, "acceptance: November ends at 29–30 Nov")
	assert.Equal(t, "2026-11-29", a.Buckets[4].FromDate)
	assert.Equal(t, "2026-11-30", a.Buckets[4].ToDate)

	type io struct{ income, expense string }
	want := []io{{"0", "15"}, {"100", "20"}, {"0", "1"}, {"0", "40"}, {"50", "7"}}
	for i, w := range want {
		assert.True(t, dec(w.income).Equal(dec(a.Buckets[i].Income)), "%s income %s", a.Buckets[i].Label, a.Buckets[i].Income)
		assert.True(t, dec(w.expense).Equal(dec(a.Buckets[i].Expense)), "%s expense %s", a.Buckets[i].Label, a.Buckets[i].Expense)
	}

	t.Run("the buckets add up to the overview", func(t *testing.T) {
		o := e.overview("?period=THIS_PERIOD")
		inc, exp := dec("0"), dec("0")
		for _, b := range a.Buckets {
			inc, exp = inc.Add(dec(b.Income)), exp.Add(dec(b.Expense))
		}
		assert.True(t, inc.Equal(dec(o.Income)))
		assert.True(t, exp.Equal(dec(o.Expense)))
	})
	t.Run("THIS_YEAR is monthly", func(t *testing.T) {
		var y analyticsBody
		e.get("analytics", "?period=THIS_YEAR", &y)
		require.Len(t, y.Buckets, 12)
		assert.Equal(t, "Jan", y.Buckets[0].Label)
		assert.Equal(t, "Nov", y.Buckets[10].Label)
		assert.True(t, dec("83").Equal(dec(y.Buckets[10].Expense)), "all of November's expenses: %s", y.Buckets[10].Expense)
		assert.True(t, dec("150").Equal(dec(y.Buckets[10].Income)))
		assert.True(t, dec("0").Equal(dec(y.Buckets[0].Expense)))
	})
	t.Run("short periods are daily", func(t *testing.T) {
		var w analyticsBody
		e.get("analytics", "?period=THIS_WEEK", &w)
		require.Len(t, w.Buckets, 7)
		assert.Equal(t, "16 Nov", w.Buckets[0].Label)
		e.get("analytics", "?from=2026-11-01&to=2026-11-07", &w)
		require.Len(t, w.Buckets, 7)
		assert.True(t, dec("10").Equal(dec(w.Buckets[0].Expense)))
		assert.True(t, dec("5").Equal(dec(w.Buckets[6].Expense)))
	})
	t.Run("ALL_TIME starts at the first transaction's month", func(t *testing.T) {
		e.tx("EXPENSE", e.a, "3", "", "2026-08-20T09:00:00Z")
		var all analyticsBody
		e.get("analytics", "?period=ALL_TIME", &all)
		assert.Equal(t, []string{"Aug", "Sep", "Oct", "Nov"}, func() []string {
			var l []string
			for _, b := range all.Buckets {
				l = append(l, b.Label)
			}
			return l
		}())
		assert.True(t, dec("3").Equal(dec(all.Buckets[0].Expense)))
	})
	t.Run("an unknown period is a 400", func(t *testing.T) {
		assert.Equal(t, http.StatusBadRequest, s.Get(dashURL(e.sid, "analytics")+"?period=NOPE", bearer(e.u.AccessToken)).Status)
	})
}

func TestAnalyticsAndCalendarFollowLocalDays(t *testing.T) {
	s := NewTestServer(t)
	e := newDashEnv(t, s)
	require.Equal(t, http.StatusOK, s.Do(http.MethodPatch, "/api/v1/me", map[string]any{"timezone": "Asia/Jakarta"}, bearer(e.u.AccessToken)).Status)
	// 23:30 UTC on the 7th is 06:30 on the 8th in Jakarta
	e.tx("EXPENSE", e.a, "10", "", "2026-11-07T23:30:00Z")
	e.tx("EXPENSE", e.a, "20", "", "2026-11-07T16:30:00Z") // 23:30 on the 7th local
	e.tx("EXPENSE", e.a, "1", "", "2026-10-31T17:00:00Z")  // 00:00 on 1 Nov local

	var a analyticsBody
	e.get("analytics", "?period=THIS_PERIOD", &a)
	assert.True(t, dec("21").Equal(dec(a.Buckets[0].Expense)), "1–7 Nov local: 1 + 20: %s", a.Buckets[0].Expense)
	assert.True(t, dec("10").Equal(dec(a.Buckets[1].Expense)), "the 8th local")

	var c calendarBody
	e.get("calendar", "?month=2026-11", &c)
	days := map[string]int{}
	for i, d := range c.Days {
		days[d.Date] = i
	}
	assert.True(t, dec("1").Equal(dec(c.Days[days["2026-11-01"]].Expense)))
	assert.True(t, dec("20").Equal(dec(c.Days[days["2026-11-07"]].Expense)))
	assert.True(t, dec("10").Equal(dec(c.Days[days["2026-11-08"]].Expense)))
	e.get("calendar", "?month=2026-10", &c)
	assert.True(t, dec("0").Equal(dec(c.Days[30].Expense)), "31 Oct UTC 17:00 is already November locally")
}

func TestCalendar(t *testing.T) {
	s := NewTestServer(t)
	e := newDashEnv(t, s)
	e.tx("INCOME", e.a, "100", "", "2026-11-05T09:00:00Z")
	e.tx("EXPENSE", e.a, "30", "", "2026-11-05T10:00:00Z")
	e.tx("EXPENSE", e.a, "20", "", "2026-11-05T11:00:00Z")
	e.tx("EXPENSE", e.idr, "160000", "", "2026-11-18T09:00:00Z") // 10 USD
	e.tx("EXPENSE", e.a, "99", "", "2026-12-01T00:00:00Z")       // next month
	e.transfer(e.a, e.b, "70", "70", time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC))

	var c calendarBody
	e.get("calendar", "?month=2026-11", &c)
	assert.Equal(t, "2026-11", c.Month)
	assert.Equal(t, "USD", c.Currency)
	require.Len(t, c.Days, 30, "every day of the month is listed")
	assert.Equal(t, "2026-11-01", c.Days[0].Date)
	assert.Equal(t, "2026-11-30", c.Days[29].Date)

	d5 := c.Days[4]
	assert.Equal(t, "2026-11-05", d5.Date)
	assert.True(t, dec("100").Equal(dec(d5.Income)))
	assert.True(t, dec("50").Equal(dec(d5.Expense)))
	assert.True(t, dec("50").Equal(dec(d5.Net)))
	assert.Equal(t, 3, d5.Count, "income and expenses only: the transfer is not counted")
	d18 := c.Days[17]
	assert.True(t, dec("10").Equal(dec(d18.Expense)))
	assert.True(t, dec("-10").Equal(dec(d18.Net)))
	assert.Equal(t, 1, d18.Count)
	assert.Equal(t, 0, c.Days[0].Count)
	assert.True(t, dec("0").Equal(dec(c.Days[0].Net)))

	t.Run("month lengths", func(t *testing.T) {
		for month, n := range map[string]int{"2026-02": 28, "2028-02": 29, "2026-12": 31, "2026-04": 30} {
			var m calendarBody
			e.get("calendar", "?month="+month, &m)
			assert.Len(t, m.Days, n, month)
		}
	})
	t.Run("validation", func(t *testing.T) {
		for _, q := range []string{"", "?month=", "?month=2026-13", "?month=2026", "?month=November", "?month=2026-11-01"} {
			assert.Equal(t, http.StatusBadRequest, s.Get(dashURL(e.sid, "calendar")+q, bearer(e.u.AccessToken)).Status, q)
		}
	})
}

func TestDashboardIsSpaceScoped(t *testing.T) {
	s := NewTestServer(t)
	e := newDashEnv(t, s)
	e.tx("EXPENSE", e.a, "10", "", "2026-11-05T09:00:00Z")
	outsider := s.register("outsider@example.com")
	for _, what := range []string{"overview", "category-breakdown", "analytics", "calendar?month=2026-11"} {
		assert.Equal(t, http.StatusUnauthorized, s.Get(dashURL(e.sid, what)).Status, what)
		expectCrossSpace404(t, s, outsider, http.MethodGet, dashURL(e.sid, what), nil)
	}
	// and an outsider's own dashboard never sees this space's data
	mine := spaceIDOf(outsider)
	res := s.Get(dashURL(mine, "overview"), bearer(outsider.AccessToken))
	require.Equal(t, http.StatusOK, res.Status)
	var o overviewBody
	res.JSON(t, &o)
	assert.True(t, dec("0").Equal(dec(o.Expense)))
	assert.Zero(t, o.TransactionCount)
}
