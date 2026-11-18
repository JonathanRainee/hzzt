//go:build e2e

package e2e

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type goalBody struct {
	ID              string  `json:"id"`
	Name            string  `json:"name"`
	Icon            string  `json:"icon"`
	Color           string  `json:"color"`
	TargetAmount    string  `json:"targetAmount"`
	Currency        string  `json:"currency"`
	TargetDate      *string `json:"targetDate"`
	Progress        string  `json:"progress"`
	ProgressPct     string  `json:"progressPct"`
	Remaining       string  `json:"remaining"`
	AchievedAt      *string `json:"achievedAt"`
	RequiredMonthly *string `json:"requiredMonthly"`
	Allocations     []struct {
		WalletID       string  `json:"walletId"`
		WalletName     string  `json:"walletName"`
		WalletCurrency string  `json:"walletCurrency"`
		Amount         string  `json:"amount"`
		Converted      *string `json:"converted"`
	} `json:"allocations"`
	Warnings []struct{ Code, Message, Label string } `json:"warnings"`
}

type walletReserve struct {
	walletBody
	Reserved     string `json:"reserved"`
	Available    string `json:"available"`
	OverReserved bool   `json:"overReserved"`
}

func goalURL(sid string, rest ...string) string {
	return "/api/v1/spaces/" + sid + "/goals" + strings.Join(rest, "")
}

type goalEnv struct {
	*world
	idr walletBody
}

func newGoalEnv(t *testing.T, s *TestServer) *goalEnv {
	t.Helper()
	w := newWorld(t, s)                                          // Wallet A = 1000 USD, Wallet B = 500 USD
	require.NoError(t, s.Currency.Refresh(context.Background())) // 1 USD = 16000 IDR = 0.8 EUR
	b := wal("Rupiah")
	b["currency"], b["initialBalance"] = "IDR", "20000000"
	return &goalEnv{world: w, idr: s.newWallet(w.u, w.sid, b)}
}

func goal(name, target string) map[string]any {
	return map[string]any{"name": name, "icon": "🎯", "color": "#22c55e", "targetAmount": target}
}

func (e *goalEnv) post(body map[string]any) Response {
	return e.s.Do(http.MethodPost, goalURL(e.sid), body, bearer(e.u.AccessToken))
}

func (e *goalEnv) must(body map[string]any) goalBody {
	e.s.t.Helper()
	res := e.post(body)
	require.Equal(e.s.t, http.StatusCreated, res.Status, string(res.Body))
	var g goalBody
	res.JSON(e.s.t, &g)
	return g
}

func (e *goalEnv) get(id string) goalBody {
	e.s.t.Helper()
	res := e.s.Get(goalURL(e.sid, "/", id), bearer(e.u.AccessToken))
	require.Equal(e.s.t, http.StatusOK, res.Status, string(res.Body))
	var g goalBody
	res.JSON(e.s.t, &g)
	return g
}

func (e *goalEnv) patch(id string, body map[string]any) Response {
	return e.s.Do(http.MethodPatch, goalURL(e.sid, "/", id), body, bearer(e.u.AccessToken))
}

func (e *goalEnv) reserve(goalID, walletID, amount string) Response {
	return e.s.Do(http.MethodPut, goalURL(e.sid, "/", goalID, "/allocations/", walletID), map[string]any{"amount": amount}, bearer(e.u.AccessToken))
}

func (e *goalEnv) wallet(id string) walletReserve {
	e.s.t.Helper()
	res := e.s.Get(walURL(e.sid, "/", id), bearer(e.u.AccessToken))
	require.Equal(e.s.t, http.StatusOK, res.Status, string(res.Body))
	var w walletReserve
	res.JSON(e.s.t, &w)
	return w
}

func TestGoalCRUD(t *testing.T) {
	s := NewTestServer(t)
	e := newGoalEnv(t, s)

	t.Run("unauthenticated", func(t *testing.T) {
		assert.Equal(t, http.StatusUnauthorized, s.Get(goalURL(e.sid)).Status)
		assert.Equal(t, http.StatusUnauthorized, s.Do(http.MethodPost, goalURL(e.sid), goal("x", "1")).Status)
	})

	t.Run("create with defaults", func(t *testing.T) {
		g := e.must(clone(goal("  Holiday ", "1500.50"), "targetDate", "2027-05-18"))
		assert.Equal(t, "Holiday", g.Name)
		assert.Equal(t, "USD", g.Currency, "defaults to the space base currency")
		assert.Equal(t, "#22C55E", g.Color)
		assert.Equal(t, "2027-05-18", *g.TargetDate)
		assert.True(t, dec("0").Equal(dec(g.Progress)))
		assert.True(t, dec("0").Equal(dec(g.ProgressPct)))
		assert.True(t, dec("1500.5").Equal(dec(g.Remaining)))
		assert.Nil(t, g.AchievedAt)
		assert.Empty(t, g.Allocations)
		assert.Empty(t, g.Warnings)
		other := e.must(clone(goal("Rupiah goal", "50000000"), "currency", "idr"))
		assert.Equal(t, "IDR", other.Currency)
		assert.Nil(t, other.TargetDate)
		assert.Nil(t, other.RequiredMonthly)
	})

	t.Run("validation", func(t *testing.T) {
		mk := func(mod func(m map[string]any)) map[string]any { m := goal("Valid", "100"); mod(m); return m }
		for name, body := range map[string]map[string]any{
			"missing name":      mk(func(m map[string]any) { delete(m, "name") }),
			"blank name":        mk(func(m map[string]any) { m["name"] = " " }),
			"long name":         mk(func(m map[string]any) { m["name"] = strings.Repeat("x", 41) }),
			"missing icon":      mk(func(m map[string]any) { delete(m, "icon") }),
			"missing color":     mk(func(m map[string]any) { delete(m, "color") }),
			"bad color":         mk(func(m map[string]any) { m["color"] = "green" }),
			"no target":         mk(func(m map[string]any) { delete(m, "targetAmount") }),
			"zero target":       mk(func(m map[string]any) { m["targetAmount"] = "0" }),
			"negative target":   mk(func(m map[string]any) { m["targetAmount"] = "-5" }),
			"9 decimals":        mk(func(m map[string]any) { m["targetAmount"] = "1.123456789" }),
			"huge":              mk(func(m map[string]any) { m["targetAmount"] = "100000000000000000000" }),
			"bad currency":      mk(func(m map[string]any) { m["currency"] = "XXX" }),
			"bad date":          mk(func(m map[string]any) { m["targetDate"] = "soon" }),
			"unknown field":     mk(func(m map[string]any) { m["achieved"] = true }),
			"zero reserve":      mk(func(m map[string]any) { m["initialReserve"] = map[string]any{"walletId": e.a.ID, "amount": "0"} }),
			"reserve no wallet": mk(func(m map[string]any) { m["initialReserve"] = map[string]any{"amount": "5"} }),
		} {
			res := e.post(body)
			assert.Equal(t, http.StatusBadRequest, res.Status, "%s: %s", name, res.Body)
		}
	})

	t.Run("patch, including clearing the date", func(t *testing.T) {
		g := e.must(clone(goal("Car", "10000"), "targetDate", "2027-11-18"))
		res := e.patch(g.ID, map[string]any{"name": "New car", "targetAmount": "12000", "color": "#112233", "icon": "🚗", "currency": "eur"})
		require.Equal(t, http.StatusOK, res.Status, string(res.Body))
		var got goalBody
		res.JSON(t, &got)
		assert.Equal(t, "New car", got.Name)
		assert.True(t, dec("12000").Equal(dec(got.TargetAmount)))
		assert.Equal(t, "EUR", got.Currency)
		assert.Equal(t, "2027-11-18", *got.TargetDate, "untouched fields stay")

		res = e.patch(g.ID, map[string]any{"targetDate": nil})
		require.Equal(t, http.StatusOK, res.Status)
		res.JSON(t, &got)
		assert.Nil(t, got.TargetDate)

		for name, body := range map[string]map[string]any{
			"blank": {"name": ""}, "zero": {"targetAmount": "0"}, "color": {"color": "x"}, "currency": {"currency": "ZZZ"}, "date": {"targetDate": "x"},
		} {
			assert.Equal(t, http.StatusBadRequest, e.patch(g.ID, body).Status, name)
		}
	})

	t.Run("list and not found", func(t *testing.T) {
		res := s.Get(goalURL(e.sid), bearer(e.u.AccessToken))
		var l struct{ Items []goalBody }
		res.JSON(t, &l)
		assert.Len(t, l.Items, 3)
		for _, id := range []string{uuid.NewString(), "nope"} {
			r := e.patch(id, map[string]any{"name": "x"})
			require.Equal(t, http.StatusNotFound, r.Status)
			assert.Equal(t, "GOAL_NOT_FOUND", errCode(t, r))
			assert.Equal(t, http.StatusNotFound, s.Get(goalURL(e.sid, "/", id), bearer(e.u.AccessToken)).Status)
			assert.Equal(t, http.StatusNotFound, s.Do(http.MethodDelete, goalURL(e.sid, "/", id), nil, bearer(e.u.AccessToken)).Status)
			assert.Equal(t, http.StatusNotFound, e.reserve(id, e.a.ID, "1").Status)
		}
	})
}

func TestReservingAgainstAWalletBalance(t *testing.T) {
	s := NewTestServer(t)
	e := newGoalEnv(t, s)
	b := wal("Small")
	b["initialBalance"] = "134.26"
	w := s.newWallet(e.u, e.sid, b)
	g := e.must(goal("Laptop", "1000"))

	t.Run("a wallet of 134.26 with a 100 reservation shows available 34.26", func(t *testing.T) {
		before := e.wallet(w.ID)
		assert.True(t, dec("0").Equal(dec(before.Reserved)))
		assert.True(t, dec("134.26").Equal(dec(before.Available)))
		assert.False(t, before.OverReserved)

		res := e.reserve(g.ID, w.ID, "100")
		require.Equal(t, http.StatusOK, res.Status, string(res.Body))
		after := e.wallet(w.ID)
		assert.True(t, dec("100").Equal(dec(after.Reserved)))
		assert.True(t, dec("34.26").Equal(dec(after.Available)))
		assert.True(t, dec("134.26").Equal(dec(after.Balance)), "no money moved")
		assert.False(t, after.OverReserved)
		expectLedgerConsistent(t, s, w.ID)
	})

	t.Run("reserving 50 more is refused", func(t *testing.T) {
		other := e.must(goal("Trip", "500"))
		res := e.reserve(other.ID, w.ID, "50")
		require.Equal(t, http.StatusUnprocessableEntity, res.Status, string(res.Body))
		assert.Equal(t, "INSUFFICIENT_AVAILABLE_BALANCE", errCode(t, res))
		var er struct {
			Error struct {
				Details map[string]any `json:"details"`
			} `json:"error"`
		}
		res.JSON(t, &er)
		assert.Equal(t, "34.26", er.Error.Details["available"])
		assert.Equal(t, "50", er.Error.Details["requested"])
		assert.True(t, dec("100").Equal(dec(e.wallet(w.ID).Reserved)), "nothing changed")
		assert.Empty(t, e.get(other.ID).Allocations)
		// 34.26 exactly is fine for the other goal
		require.Equal(t, http.StatusOK, e.reserve(other.ID, w.ID, "34.26").Status)
		assert.True(t, dec("0").Equal(dec(e.wallet(w.ID).Available)))
		assert.Equal(t, http.StatusUnprocessableEntity, e.reserve(other.ID, w.ID, "34.27").Status)
		require.Equal(t, http.StatusOK, e.reserve(other.ID, w.ID, "0").Status)
	})

	t.Run("this goal's own reservation counts as available when changing it", func(t *testing.T) {
		require.Equal(t, http.StatusOK, e.reserve(g.ID, w.ID, "134.26").Status, "100 + 34.26 available")
		assert.True(t, dec("0").Equal(dec(e.wallet(w.ID).Available)))
		res := e.reserve(g.ID, w.ID, "134.27")
		require.Equal(t, http.StatusUnprocessableEntity, res.Status)
		assert.Equal(t, "INSUFFICIENT_AVAILABLE_BALANCE", errCode(t, res))
		require.Equal(t, http.StatusOK, e.reserve(g.ID, w.ID, "100").Status, "lowering it is always fine")
	})

	t.Run("acceptance: an expense of 60 makes the wallet over-reserved", func(t *testing.T) {
		e.world.must(exp(w, "60", ""))
		got := e.wallet(w.ID)
		assert.True(t, dec("74.26").Equal(dec(got.Balance)))
		assert.True(t, dec("100").Equal(dec(got.Reserved)), "the reservation stays")
		assert.True(t, dec("-25.74").Equal(dec(got.Available)), "available goes negative")
		assert.True(t, got.OverReserved, "D-09: expenses may push a wallet below its reserves")
		// an over-reserved wallet takes no new reservations, but can still lower the existing one
		other := e.must(goal("Another", "10"))
		assert.Equal(t, http.StatusUnprocessableEntity, e.reserve(other.ID, w.ID, "1").Status)
		require.Equal(t, http.StatusOK, e.reserve(g.ID, w.ID, "74.26").Status)
		assert.False(t, e.wallet(w.ID).OverReserved)
		assert.Equal(t, http.StatusUnprocessableEntity, e.reserve(g.ID, w.ID, "74.27").Status, "raising it above what the wallet holds")
		expectLedgerConsistent(t, s, w.ID)
	})

	t.Run("the wallet list shows reserved, available and the flag", func(t *testing.T) {
		res := s.Get(walURL(e.sid), bearer(e.u.AccessToken))
		var l struct{ Items []walletReserve }
		res.JSON(t, &l)
		for _, it := range l.Items {
			if it.ID == w.ID {
				assert.True(t, dec("74.26").Equal(dec(it.Reserved)))
				assert.True(t, dec("0").Equal(dec(it.Available)))
			} else {
				assert.True(t, dec("0").Equal(dec(it.Reserved)), it.Name)
				assert.True(t, dec(it.Balance).Equal(dec(it.Available)))
			}
		}
	})

	t.Run("validation and references", func(t *testing.T) {
		for name, amount := range map[string]string{"negative": "-1", "decimals": "1.123456789", "huge": "100000000000000000000"} {
			assert.Equal(t, http.StatusBadRequest, e.reserve(g.ID, w.ID, amount).Status, name)
		}
		put := func(path string, body any) Response {
			return s.Do(http.MethodPut, goalURL(e.sid, "/", g.ID, "/allocations/", path), body, bearer(e.u.AccessToken))
		}
		assert.Equal(t, http.StatusBadRequest, put(w.ID, map[string]any{}).Status, "missing amount")
		assert.Equal(t, http.StatusBadRequest, put(w.ID, map[string]any{"amount": "1", "x": 1}).Status)
		res := put(uuid.NewString(), map[string]any{"amount": "1"})
		require.Equal(t, http.StatusNotFound, res.Status)
		assert.Equal(t, "WALLET_NOT_FOUND", errCode(t, res))
		assert.Equal(t, http.StatusNotFound, put("nope", map[string]any{"amount": "1"}).Status)
		other := s.register("other@example.com")
		ow := s.newWallet(other, spaceIDOf(other), wal("Theirs"))
		assert.Equal(t, http.StatusNotFound, put(ow.ID, map[string]any{"amount": "1"}).Status, "a wallet of another space")
		// removing a reservation that does not exist is fine
		assert.Equal(t, http.StatusOK, e.reserve(g.ID, e.b.ID, "0").Status)
	})
}

func TestInitialReserveOnCreate(t *testing.T) {
	s := NewTestServer(t)
	e := newGoalEnv(t, s)

	g := e.must(clone(goal("Fund", "1000"), "initialReserve", map[string]any{"walletId": e.a.ID, "amount": "250"}))
	require.Len(t, g.Allocations, 1)
	assert.True(t, dec("250").Equal(dec(g.Allocations[0].Amount)))
	assert.Equal(t, "Wallet A", g.Allocations[0].WalletName)
	assert.True(t, dec("250").Equal(dec(g.Progress)))
	assert.True(t, dec("25").Equal(dec(g.ProgressPct)))
	assert.True(t, dec("750").Equal(dec(e.wallet(e.a.ID).Available)))

	count := func() int {
		var n int
		require.NoError(t, s.Tx.QueryRow(context.Background(), `SELECT count(*) FROM goals WHERE name = 'Too big'`).Scan(&n))
		return n
	}
	res := e.post(clone(goal("Too big", "1000"), "initialReserve", map[string]any{"walletId": e.a.ID, "amount": "800"}))
	require.Equal(t, http.StatusUnprocessableEntity, res.Status, string(res.Body))
	assert.Equal(t, "INSUFFICIENT_AVAILABLE_BALANCE", errCode(t, res))
	assert.Zero(t, count(), "a failed reservation leaves no goal behind")
	res = e.post(clone(goal("Too big", "1000"), "initialReserve", map[string]any{"walletId": uuid.NewString(), "amount": "1"}))
	require.Equal(t, http.StatusNotFound, res.Status)
	assert.Equal(t, "WALLET_NOT_FOUND", errCode(t, res))
	assert.Zero(t, count())
}

func TestGoalProgressAcrossCurrencies(t *testing.T) {
	s := NewTestServer(t)
	e := newGoalEnv(t, s) // 1 USD = 16000 IDR = 0.8 EUR
	g := e.must(goal("Savings", "1000"))

	require.Equal(t, http.StatusOK, e.reserve(g.ID, e.a.ID, "100").Status)
	require.Equal(t, http.StatusOK, e.reserve(g.ID, e.idr.ID, "8000000").Status) // 500 USD
	got := e.get(g.ID)
	assert.True(t, dec("600").Equal(dec(got.Progress)), "100 USD + 8,000,000 IDR (= 500 USD): %s", got.Progress)
	assert.True(t, dec("60").Equal(dec(got.ProgressPct)))
	assert.True(t, dec("400").Equal(dec(got.Remaining)))
	require.Len(t, got.Allocations, 2)
	for _, a := range got.Allocations {
		require.NotNil(t, a.Converted, a.WalletName)
		if a.WalletCurrency == "IDR" {
			assert.True(t, dec("500").Equal(dec(*a.Converted)))
			assert.True(t, dec("8000000").Equal(dec(a.Amount)), "the wallet-currency amount is kept")
		}
	}

	t.Run("a goal in another currency converts both ways", func(t *testing.T) {
		eur := e.must(clone(goal("Euro goal", "800"), "currency", "EUR"))
		require.Equal(t, http.StatusOK, e.reserve(eur.ID, e.a.ID, "250").Status)        // 200 EUR
		require.Equal(t, http.StatusOK, e.reserve(eur.ID, e.idr.ID, "12000000").Status) // 600 EUR (the rest is reserved by the first goal)
		got := e.get(eur.ID)
		assert.True(t, dec("800").Equal(dec(got.Progress)), "200 + 600 EUR: %s", got.Progress)
		assert.True(t, dec("100").Equal(dec(got.ProgressPct)))
		assert.True(t, dec("0").Equal(dec(got.Remaining)), "never negative")
		assert.NotNil(t, got.AchievedAt)
	})

	t.Run("without an FX rate the reservation is left out with a warning", func(t *testing.T) {
		sg := wal("Singapore")
		sg["currency"], sg["initialBalance"] = "SGD", "500"
		sgd := s.newWallet(e.u, e.sid, sg)
		require.Equal(t, http.StatusOK, e.reserve(g.ID, sgd.ID, "200").Status)
		got := e.get(g.ID)
		assert.True(t, dec("600").Equal(dec(got.Progress)), "the SGD part cannot be converted: %s", got.Progress)
		require.Len(t, got.Warnings, 1)
		assert.Equal(t, "FX_RATE_UNAVAILABLE", got.Warnings[0].Code)
		for _, a := range got.Allocations {
			if a.WalletCurrency == "SGD" {
				assert.Nil(t, a.Converted)
				assert.True(t, dec("200").Equal(dec(a.Amount)))
			}
		}
	})
}

func TestGoalAchievement(t *testing.T) {
	ctx := context.Background()
	s := NewTestServer(t)
	e := newGoalEnv(t, s)
	s.Clock.Set(time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC))
	g := e.must(goal("Target", "500"))

	require.Equal(t, http.StatusOK, e.reserve(g.ID, e.a.ID, "499.99").Status)
	assert.Nil(t, e.get(g.ID).AchievedAt, "just below the target")

	t.Run("achievedAt is set when progress first reaches the target", func(t *testing.T) {
		res := e.reserve(g.ID, e.a.ID, "500")
		require.Equal(t, http.StatusOK, res.Status)
		var got goalBody
		res.JSON(t, &got)
		require.NotNil(t, got.AchievedAt)
		first := *got.AchievedAt
		assert.True(t, dec("100").Equal(dec(got.ProgressPct)))
		assert.True(t, dec("0").Equal(dec(got.Remaining)))
		assert.Nil(t, got.RequiredMonthly, "nothing left to save")

		s.Clock.Advance(48 * time.Hour)
		require.Equal(t, http.StatusOK, e.reserve(g.ID, e.b.ID, "50").Status)
		assert.Equal(t, first, *e.get(g.ID).AchievedAt, "staying achieved keeps the original time")
		require.Equal(t, http.StatusOK, e.reserve(g.ID, e.b.ID, "0").Status)
	})

	t.Run("it is cleared when progress drops below the target", func(t *testing.T) {
		require.Equal(t, http.StatusOK, e.reserve(g.ID, e.a.ID, "300").Status)
		assert.Nil(t, e.get(g.ID).AchievedAt)
		// and set again when it climbs back, with a new timestamp
		s.Clock.Advance(time.Hour)
		require.Equal(t, http.StatusOK, e.reserve(g.ID, e.a.ID, "500").Status)
		assert.NotNil(t, e.get(g.ID).AchievedAt)
	})

	t.Run("changing the target re-evaluates it", func(t *testing.T) {
		res := e.patch(g.ID, map[string]any{"targetAmount": "600"})
		require.Equal(t, http.StatusOK, res.Status)
		var got goalBody
		res.JSON(t, &got)
		assert.Nil(t, got.AchievedAt, "the bar moved up")
		res = e.patch(g.ID, map[string]any{"targetAmount": "400"})
		res.JSON(t, &got)
		assert.NotNil(t, got.AchievedAt)
		assert.True(t, dec("125").Equal(dec(got.ProgressPct)), "over 100 means exceeded: %s", got.ProgressPct)
	})

	t.Run("it follows FX moves: progress is computed on every read", func(t *testing.T) {
		idrGoal := e.must(goal("From rupiah", "1000"))
		require.Equal(t, http.StatusOK, e.reserve(idrGoal.ID, e.idr.ID, "16000000").Status) // 1000 USD
		require.NotNil(t, e.get(idrGoal.ID).AchievedAt)
		s.Clock.Advance(24 * time.Hour)
		s.FX.Set = defaultRates(s.Clock)
		s.FX.Set.Rates["IDR"] = dec("32000") // the rupiah halves
		require.NoError(t, s.Currency.Refresh(ctx))
		got := e.get(idrGoal.ID)
		assert.True(t, dec("500").Equal(dec(got.Progress)))
		assert.Nil(t, got.AchievedAt, "no longer reached")
	})
}

func TestRequiredMonthly(t *testing.T) {
	s := NewTestServer(t)
	e := newGoalEnv(t, s)
	s.Clock.Set(time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)) // the user's timezone is UTC

	t.Run("remaining divided by the months until the target date", func(t *testing.T) {
		g := e.must(clone(goal("Six months", "1000"), "targetDate", "2027-05-18"))
		require.NotNil(t, g.RequiredMonthly)
		assert.True(t, dec("166.67").Equal(dec(*g.RequiredMonthly)), "1000 / 6 rounded to cents: %s", *g.RequiredMonthly)
		require.Equal(t, http.StatusOK, e.reserve(g.ID, e.a.ID, "400").Status)
		got := e.get(g.ID)
		assert.True(t, dec("100").Equal(dec(*got.RequiredMonthly)), "600 left / 6 months")
	})
	t.Run("rounded to the currency's minor units", func(t *testing.T) {
		g := e.must(clone(goal("Rupiah", "1000000"), "currency", "IDR", "targetDate", "2027-02-18"))
		assert.True(t, dec("333333").Equal(dec(*g.RequiredMonthly)), "3 months; IDR has no decimals: %s", *g.RequiredMonthly)
	})
	t.Run("a target within the first month needs the whole remainder", func(t *testing.T) {
		g := e.must(clone(goal("Soon", "90"), "targetDate", "2026-11-25"))
		assert.True(t, dec("90").Equal(dec(*g.RequiredMonthly)))
	})
	t.Run("null when past, achieved or without a date", func(t *testing.T) {
		assert.Nil(t, e.must(clone(goal("Past", "100"), "targetDate", "2026-11-01")).RequiredMonthly)
		assert.Nil(t, e.must(clone(goal("Today", "100"), "targetDate", "2026-11-18")).RequiredMonthly)
		assert.Nil(t, e.must(goal("Undated", "100")).RequiredMonthly)
		done := e.must(clone(goal("Done", "100"), "targetDate", "2027-05-18", "initialReserve", map[string]any{"walletId": e.a.ID, "amount": "100"}))
		assert.Nil(t, done.RequiredMonthly)
	})
	t.Run("the user's timezone decides what today is", func(t *testing.T) {
		require.Equal(t, http.StatusOK, s.Do(http.MethodPatch, "/api/v1/me", map[string]any{"timezone": "Pacific/Kiritimati"}, bearer(e.u.AccessToken)).Status) // UTC+14
		s.Clock.Set(time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC))                                                                                             // already the 19th there
		g := e.must(clone(goal("Edge", "100"), "targetDate", "2026-11-19"))
		assert.Nil(t, g.RequiredMonthly, "the 19th is today for this user")
	})
}

func TestGoalsReleaseReservations(t *testing.T) {
	s := NewTestServer(t)
	e := newGoalEnv(t, s)
	g1, g2 := e.must(goal("One", "1000")), e.must(goal("Two", "1000"))
	require.Equal(t, http.StatusOK, e.reserve(g1.ID, e.a.ID, "300").Status)
	require.Equal(t, http.StatusOK, e.reserve(g2.ID, e.a.ID, "200").Status)
	require.Equal(t, http.StatusOK, e.reserve(g1.ID, e.b.ID, "100").Status)
	assert.True(t, dec("500").Equal(dec(e.wallet(e.a.ID).Reserved)))

	t.Run("deleting a goal releases all its reservations", func(t *testing.T) {
		require.Equal(t, http.StatusNoContent, s.Do(http.MethodDelete, goalURL(e.sid, "/", g1.ID), nil, bearer(e.u.AccessToken)).Status)
		assert.True(t, dec("200").Equal(dec(e.wallet(e.a.ID).Reserved)), "only goal Two's part is left")
		assert.True(t, dec("0").Equal(dec(e.wallet(e.b.ID).Reserved)))
		assert.True(t, dec(e.wallet(e.b.ID).Balance).Equal(dec(e.wallet(e.b.ID).Available)))
		assert.Equal(t, http.StatusNotFound, s.Get(goalURL(e.sid, "/", g1.ID), bearer(e.u.AccessToken)).Status)
		assert.Equal(t, http.StatusNotFound, e.reserve(g1.ID, e.a.ID, "1").Status)
		assert.Equal(t, http.StatusNotFound, s.Do(http.MethodDelete, goalURL(e.sid, "/", g1.ID), nil, bearer(e.u.AccessToken)).Status)
		var n int
		require.NoError(t, s.Tx.QueryRow(context.Background(), `SELECT count(*) FROM goal_allocations WHERE goal_id = $1`, g1.ID).Scan(&n))
		assert.Zero(t, n)
	})

	t.Run("deleting a wallet releases its reservations", func(t *testing.T) {
		require.Equal(t, http.StatusNoContent, s.Do(http.MethodDelete, walURL(e.sid, "/", e.a.ID)+"?force=true", nil, bearer(e.u.AccessToken)).Status)
		got := e.get(g2.ID)
		assert.Empty(t, got.Allocations)
		assert.True(t, dec("0").Equal(dec(got.Progress)))
		var n int
		require.NoError(t, s.Tx.QueryRow(context.Background(), `SELECT count(*) FROM goal_allocations WHERE wallet_id = $1`, e.a.ID).Scan(&n))
		assert.Zero(t, n)
	})

	t.Run("a deleted wallet cannot be reserved", func(t *testing.T) {
		assert.Equal(t, http.StatusNotFound, e.reserve(g2.ID, e.a.ID, "1").Status)
	})
}

func TestGoalsAreSpaceScoped(t *testing.T) {
	s := NewTestServer(t)
	e := newGoalEnv(t, s)
	outsider := s.register("outsider@example.com")
	member := s.register("member@example.com")
	s.join(e.u, e.sid, member)
	g := e.must(goal("Shared", "100"))

	t.Run("outsider gets 404 on every route", func(t *testing.T) {
		expectCrossSpace404(t, s, outsider, http.MethodGet, goalURL(e.sid), nil)
		expectCrossSpace404(t, s, outsider, http.MethodPost, goalURL(e.sid), goal("Hack", "1"))
		expectCrossSpace404(t, s, outsider, http.MethodGet, goalURL(e.sid, "/", g.ID), nil)
		expectCrossSpace404(t, s, outsider, http.MethodPatch, goalURL(e.sid, "/", g.ID), map[string]any{"name": "x"})
		expectCrossSpace404(t, s, outsider, http.MethodDelete, goalURL(e.sid, "/", g.ID), nil)
		expectCrossSpace404(t, s, outsider, http.MethodPut, goalURL(e.sid, "/", g.ID, "/allocations/", e.a.ID), map[string]any{"amount": "1"})
	})

	t.Run("a goal id from another space is 404 in your own space", func(t *testing.T) {
		mine := spaceIDOf(outsider)
		ow := s.newWallet(outsider, mine, wal("Mine"))
		for _, c := range []struct {
			method, path string
			body         any
		}{
			{http.MethodGet, goalURL(mine, "/", g.ID), nil},
			{http.MethodPatch, goalURL(mine, "/", g.ID), map[string]any{"name": "x"}},
			{http.MethodDelete, goalURL(mine, "/", g.ID), nil},
			{http.MethodPut, goalURL(mine, "/", g.ID, "/allocations/", ow.ID), map[string]any{"amount": "0"}},
		} {
			res := s.Do(c.method, c.path, c.body, bearer(outsider.AccessToken))
			require.Equal(t, http.StatusNotFound, res.Status, "%s %s", c.method, c.path)
		}
	})

	t.Run("members can manage goals", func(t *testing.T) {
		res := s.Do(http.MethodPost, goalURL(e.sid), goal("Member's", "5"), bearer(member.AccessToken))
		require.Equal(t, http.StatusCreated, res.Status, string(res.Body))
		assert.Equal(t, http.StatusOK, s.Do(http.MethodPut, goalURL(e.sid, "/", g.ID, "/allocations/", e.a.ID), map[string]any{"amount": "10"}, bearer(member.AccessToken)).Status)
	})
}
