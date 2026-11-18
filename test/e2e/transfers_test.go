//go:build e2e

package e2e

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func trURL(sid string, rest ...string) string {
	return "/api/v1/spaces/" + sid + "/transfers" + strings.Join(rest, "")
}

func walletEnd(w walletBody) map[string]any {
	return map[string]any{"type": "WALLET", "walletId": w.ID}
}

func xfer(from, to walletBody, amount string) map[string]any {
	return map[string]any{"from": walletEnd(from), "to": walletEnd(to), "amount": amount}
}

type transferEnv struct {
	*world
	idr, eur walletBody
}

func newTransferEnv(t *testing.T, s *TestServer) *transferEnv {
	t.Helper()
	w := newWorld(t, s) // Wallet A = 1000 USD, Wallet B = 500 USD
	mk := func(name, cur, bal string) walletBody {
		b := wal(name)
		b["currency"], b["initialBalance"] = cur, bal
		return s.newWallet(w.u, w.sid, b)
	}
	return &transferEnv{world: w, idr: mk("Rupiah", "IDR", "2000000"), eur: mk("Euro", "EUR", "50")}
}

func (e *transferEnv) post(body map[string]any) Response {
	return e.s.Do(http.MethodPost, trURL(e.sid), body, bearer(e.u.AccessToken))
}

func (e *transferEnv) must(body map[string]any) txnBody {
	e.s.t.Helper()
	res := e.post(body)
	require.Equal(e.s.t, http.StatusCreated, res.Status, string(res.Body))
	var t txnBody
	res.JSON(e.s.t, &t)
	return t
}

func (e *transferEnv) patch(id string, body map[string]any) Response {
	return e.s.Do(http.MethodPatch, trURL(e.sid, "/", id), body, bearer(e.u.AccessToken))
}

func TestCreateTransfer(t *testing.T) {
	s := NewTestServer(t)
	e := newTransferEnv(t, s)

	t.Run("unauthenticated", func(t *testing.T) {
		assert.Equal(t, http.StatusUnauthorized, s.Do(http.MethodPost, trURL(e.sid), xfer(e.a, e.b, "1")).Status)
	})

	t.Run("same currency moves money between wallets", func(t *testing.T) {
		body := xfer(e.a, e.b, "100")
		body["note"] = "  rent split  "
		got := e.must(body)
		assert.Equal(t, "TRANSFER", got.Type)
		assert.Equal(t, "USD", got.Currency)
		assert.True(t, dec("100").Equal(dec(got.Amount)))
		require.NotNil(t, got.ToAmount)
		assert.True(t, dec("100").Equal(dec(*got.ToAmount)))
		assert.Equal(t, "Wallet A", got.Wallet.Name)
		assert.Equal(t, "Wallet B", got.ToWallet.Name)
		assert.Nil(t, got.Category)
		require.NotNil(t, got.Note)
		assert.Equal(t, "rent split", *got.Note)
		assert.True(t, dec("900").Equal(e.balance(e.a)))
		assert.True(t, dec("600").Equal(e.balance(e.b)))
		expectLedgerConsistent(t, s, e.a.ID)
		expectLedgerConsistent(t, s, e.b.ID)
	})

	t.Run("acceptance: 100 USD to an IDR wallet with toAmount 1,600,000", func(t *testing.T) {
		body := xfer(e.a, e.idr, "100.00")
		body["toAmount"] = "1600000"
		got := e.must(body)
		assert.True(t, dec("800").Equal(e.balance(e.a)))
		assert.True(t, dec("3600000").Equal(e.balance(e.idr)))
		var rate, toCur, cur string
		require.NoError(t, s.Tx.QueryRow(context.Background(),
			`SELECT fx_rate::text, to_currency, currency FROM transactions WHERE id = $1`, got.ID).Scan(&rate, &toCur, &cur))
		assert.True(t, dec("16000").Equal(dec(rate)), "stored rate = toAmount / amount")
		assert.Equal(t, "IDR", toCur)
		assert.Equal(t, "USD", cur)
		expectLedgerConsistent(t, s, e.a.ID)
		expectLedgerConsistent(t, s, e.idr.ID)
	})

	t.Run("a cross-currency transfer back works in the other direction", func(t *testing.T) {
		body := xfer(e.idr, e.eur, "1600000")
		body["toAmount"] = "90"
		e.must(body)
		assert.True(t, dec("2000000").Equal(e.balance(e.idr)))
		assert.True(t, dec("140").Equal(e.balance(e.eur)))
	})

	t.Run("negative balances are allowed", func(t *testing.T) {
		body := xfer(e.eur, e.b, "1000")
		body["toAmount"] = "1100"
		e.must(body)
		assert.True(t, dec("-860").Equal(e.balance(e.eur)), "140 - 1000")
		assert.True(t, dec("1700").Equal(e.balance(e.b)), "600 + 1100")
	})

	t.Run("occurredAt is honoured", func(t *testing.T) {
		body := xfer(e.a, e.b, "1")
		body["occurredAt"] = "2026-11-17T06:40:00Z"
		assert.Equal(t, "2026-11-17T06:40:00Z", e.must(body).OccurredAt)
	})

	t.Run("business rules", func(t *testing.T) {
		res := e.post(xfer(e.a, e.a, "5"))
		require.Equal(t, http.StatusUnprocessableEntity, res.Status)
		assert.Equal(t, "TRANSFER_SAME_WALLET", errCode(t, res))

		res = e.post(xfer(e.a, e.idr, "5"))
		require.Equal(t, http.StatusUnprocessableEntity, res.Status)
		assert.Equal(t, "TRANSFER_AMOUNT_REQUIRED", errCode(t, res))

		body := xfer(e.a, e.b, "5")
		body["toAmount"] = "6"
		assert.Equal(t, http.StatusBadRequest, e.post(body).Status, "same currency needs toAmount == amount")
		body["toAmount"] = "5.00"
		assert.Equal(t, http.StatusCreated, e.post(body).Status)
	})

	t.Run("investments are not supported yet", func(t *testing.T) {
		for _, side := range []string{"from", "to"} {
			body := xfer(e.a, e.b, "5")
			body[side] = map[string]any{"type": "INVESTMENT", "investmentId": uuid.NewString()}
			res := e.post(body)
			require.Equal(t, http.StatusUnprocessableEntity, res.Status, string(res.Body))
			assert.Equal(t, "NOT_SUPPORTED_YET", errCode(t, res), side)
		}
	})

	t.Run("validation", func(t *testing.T) {
		mk := func(mod func(m map[string]any)) map[string]any { m := xfer(e.a, e.b, "5"); mod(m); return m }
		for name, body := range map[string]map[string]any{
			"missing from":      mk(func(m map[string]any) { delete(m, "from") }),
			"missing to":        mk(func(m map[string]any) { delete(m, "to") }),
			"missing amount":    mk(func(m map[string]any) { delete(m, "amount") }),
			"zero amount":       mk(func(m map[string]any) { m["amount"] = "0" }),
			"negative":          mk(func(m map[string]any) { m["amount"] = "-1" }),
			"too many decimals": mk(func(m map[string]any) { m["amount"] = "1.123456789" }),
			"bad toAmount":      mk(func(m map[string]any) { m["toAmount"] = "-3" }),
			"unknown endpoint":  mk(func(m map[string]any) { m["from"] = map[string]any{"type": "BANK", "walletId": e.a.ID} }),
			"no type":           mk(func(m map[string]any) { m["from"] = map[string]any{"walletId": e.a.ID} }),
			"no walletId":       mk(func(m map[string]any) { m["to"] = map[string]any{"type": "WALLET"} }),
			"bad walletId":      mk(func(m map[string]any) { m["to"] = map[string]any{"type": "WALLET", "walletId": "x"} }),
			"flat wallet ids":   {"fromWalletId": e.a.ID, "toWalletId": e.b.ID, "amount": "1"},
			"long note":         mk(func(m map[string]any) { m["note"] = strings.Repeat("n", 1001) }),
			"bad date":          mk(func(m map[string]any) { m["occurredAt"] = "soon" }),
		} {
			res := e.post(body)
			assert.Equal(t, http.StatusBadRequest, res.Status, "%s: %s", name, res.Body)
		}
	})

	t.Run("wallets must be live and in this space", func(t *testing.T) {
		res := e.post(xfer(walletBody{ID: uuid.NewString()}, e.b, "1"))
		require.Equal(t, http.StatusNotFound, res.Status)
		assert.Equal(t, "WALLET_NOT_FOUND", errCode(t, res))
		res = e.post(xfer(e.a, walletBody{ID: uuid.NewString()}, "1"))
		assert.Equal(t, "WALLET_NOT_FOUND", errCode(t, res))

		other := s.register("other@example.com")
		ow := s.newWallet(other, spaceIDOf(other), wal("Theirs"))
		assert.Equal(t, "WALLET_NOT_FOUND", errCode(t, e.post(xfer(e.a, ow, "1"))))
		assert.Equal(t, "WALLET_NOT_FOUND", errCode(t, e.post(xfer(ow, e.a, "1"))))

		gone := s.newWallet(e.u, e.sid, wal("Gone"))
		require.Equal(t, http.StatusNoContent, s.Do(http.MethodDelete, walURL(e.sid, "/", gone.ID), nil, bearer(e.u.AccessToken)).Status)
		assert.Equal(t, "WALLET_NOT_FOUND", errCode(t, e.post(xfer(e.a, gone, "1"))))
	})
}

func TestTransferHistoryAndSummary(t *testing.T) {
	s := NewTestServer(t)
	require.NoError(t, s.Currency.Refresh(context.Background())) // 1 USD = 16000 IDR
	e := newTransferEnv(t, s)
	at := "2026-11-10T08:00:00Z"
	body := xfer(e.a, e.idr, "100")
	body["toAmount"], body["occurredAt"] = "1600000", at
	tr := e.must(body)
	e.world.must(map[string]any{"type": "INCOME", "walletId": e.a.ID, "amount": "50", "occurredAt": at})
	e.world.must(map[string]any{"type": "EXPENSE", "walletId": e.idr.ID, "amount": "160000", "occurredAt": at})

	t.Run("a transfer shows in either wallet's history", func(t *testing.T) {
		src := ids(e.list("?types=TRANSFER&walletIds=" + e.a.ID))
		dst := ids(e.list("?types=TRANSFER&walletIds=" + e.idr.ID))
		assert.Equal(t, []string{tr.ID}, src)
		assert.Equal(t, []string{tr.ID}, dst)
		assert.Empty(t, ids(e.list("?types=TRANSFER&walletIds="+e.b.ID)))
	})

	t.Run("transfers stay out of income and expense totals", func(t *testing.T) {
		sm := e.summary("?from=2026-11-10&to=2026-11-10&types=INCOME,EXPENSE,TRANSFER")
		require.Len(t, sm.Items, 1)
		d := sm.Items[0]
		assert.True(t, dec("50").Equal(dec(d.Income)), d.Income)
		assert.True(t, dec("10").Equal(dec(d.Expense)), "160000 IDR = 10 USD: %s", d.Expense)
		assert.True(t, dec("100").Equal(dec(d.Transfer)), "in the source wallet's currency: %s", d.Transfer)
		assert.Equal(t, 3, d.Count)
	})

	t.Run("filtering the summary to a transfer-only view", func(t *testing.T) {
		sm := e.summary("?types=TRANSFER&from=2026-11-10&to=2026-11-10")
		require.Len(t, sm.Items, 1)
		assert.True(t, dec("0").Equal(dec(sm.Items[0].Income)))
		assert.True(t, dec("0").Equal(dec(sm.Items[0].Expense)))
		assert.Equal(t, 1, sm.Items[0].Count)
	})

	t.Run("the generic endpoints point to the transfer endpoints", func(t *testing.T) {
		res := s.Do(http.MethodPatch, txURL(e.sid, "/", tr.ID), map[string]any{"note": "x"}, bearer(e.u.AccessToken))
		assert.Equal(t, "USE_SPECIFIC_ENDPOINT", errCode(t, res))
		res = s.Do(http.MethodDelete, txURL(e.sid, "/", tr.ID), nil, bearer(e.u.AccessToken))
		assert.Equal(t, "USE_SPECIFIC_ENDPOINT", errCode(t, res))
	})
}

func TestEditTransfer(t *testing.T) {
	s := NewTestServer(t)
	e := newTransferEnv(t, s)
	base := e.must(xfer(e.a, e.b, "100")) // A 900, B 600

	t.Run("change the amount (same currency)", func(t *testing.T) {
		res := e.patch(base.ID, map[string]any{"amount": "250", "note": "bigger"})
		require.Equal(t, http.StatusOK, res.Status, string(res.Body))
		var got txnBody
		res.JSON(t, &got)
		assert.True(t, dec("250").Equal(dec(got.Amount)))
		assert.True(t, dec("250").Equal(dec(*got.ToAmount)), "toAmount follows amount for same-currency transfers")
		assert.True(t, dec("750").Equal(e.balance(e.a)))
		assert.True(t, dec("750").Equal(e.balance(e.b)))
		expectLedgerConsistent(t, s, e.a.ID)
		expectLedgerConsistent(t, s, e.b.ID)
	})

	t.Run("redirect to a different destination wallet", func(t *testing.T) {
		res := e.patch(base.ID, map[string]any{"to": walletEnd(e.eur), "toAmount": "200"})
		require.Equal(t, http.StatusOK, res.Status, string(res.Body))
		assert.True(t, dec("500").Equal(e.balance(e.b)), "the old destination is restored")
		assert.True(t, dec("250").Equal(e.balance(e.eur)))
		assert.True(t, dec("750").Equal(e.balance(e.a)))
		expectLedgerConsistent(t, s, e.b.ID)
		expectLedgerConsistent(t, s, e.eur.ID)
	})

	t.Run("cross-currency edits need toAmount only when something material changes", func(t *testing.T) {
		// only the note → the agreed toAmount is kept
		res := e.patch(base.ID, map[string]any{"note": "just a note"})
		require.Equal(t, http.StatusOK, res.Status, string(res.Body))
		assert.True(t, dec("250").Equal(e.balance(e.eur)))
		// changing the amount without a new toAmount is ambiguous
		res = e.patch(base.ID, map[string]any{"amount": "300"})
		require.Equal(t, http.StatusUnprocessableEntity, res.Status)
		assert.Equal(t, "TRANSFER_AMOUNT_REQUIRED", errCode(t, res))
		assert.True(t, dec("750").Equal(e.balance(e.a)), "a rejected edit changes nothing")
		res = e.patch(base.ID, map[string]any{"amount": "300", "toAmount": "270"})
		require.Equal(t, http.StatusOK, res.Status, string(res.Body))
		assert.True(t, dec("700").Equal(e.balance(e.a)))
		assert.True(t, dec("320").Equal(e.balance(e.eur)))
	})

	t.Run("clear the note and move the date", func(t *testing.T) {
		res := e.patch(base.ID, map[string]any{"note": nil, "occurredAt": "2026-03-04T05:06:07Z"})
		require.Equal(t, http.StatusOK, res.Status)
		var got txnBody
		res.JSON(t, &got)
		assert.Nil(t, got.Note)
		assert.Equal(t, "2026-03-04T05:06:07Z", got.OccurredAt)
	})

	t.Run("rules", func(t *testing.T) {
		assert.Equal(t, "TRANSFER_SAME_WALLET", errCode(t, e.patch(base.ID, map[string]any{"to": walletEnd(e.a)})))
		assert.Equal(t, "NOT_SUPPORTED_YET", errCode(t, e.patch(base.ID, map[string]any{"from": map[string]any{"type": "INVESTMENT"}})))
		assert.Equal(t, "WALLET_NOT_FOUND", errCode(t, e.patch(base.ID, map[string]any{"to": walletEnd(walletBody{ID: uuid.NewString()})})))
		for name, body := range map[string]map[string]any{
			"zero amount": {"amount": "0"}, "bad from": {"from": map[string]any{"type": "X"}}, "unknown": {"type": "INCOME"},
			"long note": {"note": strings.Repeat("n", 1001)},
		} {
			assert.Equal(t, http.StatusBadRequest, e.patch(base.ID, body).Status, name)
		}
		expectLedgerConsistent(t, s, e.a.ID)
	})

	t.Run("only transfers can be edited here", func(t *testing.T) {
		exp := e.world.must(exp(e.a, "5", ""))
		res := e.patch(exp.ID, map[string]any{"note": "x"})
		require.Equal(t, http.StatusNotFound, res.Status)
		assert.Equal(t, "TRANSFER_NOT_FOUND", errCode(t, res))
		assert.Equal(t, http.StatusNotFound, e.patch(uuid.NewString(), map[string]any{"note": "x"}).Status)
		assert.Equal(t, http.StatusNotFound, e.patch("nope", map[string]any{"note": "x"}).Status)
	})
}

func TestDeleteTransfer(t *testing.T) {
	s := NewTestServer(t)
	e := newTransferEnv(t, s)
	body := xfer(e.a, e.idr, "100")
	body["toAmount"] = "1600000"
	tr := e.must(body)
	require.True(t, dec("900").Equal(e.balance(e.a)))

	del := func(id string) Response {
		return s.Do(http.MethodDelete, trURL(e.sid, "/", id), nil, bearer(e.u.AccessToken))
	}
	require.Equal(t, http.StatusNoContent, del(tr.ID).Status)
	assert.True(t, dec("1000").Equal(e.balance(e.a)))
	assert.True(t, dec("2000000").Equal(e.balance(e.idr)))
	assert.Equal(t, http.StatusNotFound, s.Get(txURL(e.sid, "/", tr.ID), bearer(e.u.AccessToken)).Status)
	assert.Equal(t, http.StatusNotFound, del(tr.ID).Status)

	exp := e.world.must(exp(e.a, "5", ""))
	res := del(exp.ID)
	require.Equal(t, http.StatusNotFound, res.Status, "only transfers are deleted here")
	assert.Equal(t, "TRANSFER_NOT_FOUND", errCode(t, res))
	assert.True(t, dec("995").Equal(e.balance(e.a)))
}

func TestTransfersAreSpaceScoped(t *testing.T) {
	s := NewTestServer(t)
	e := newTransferEnv(t, s)
	outsider := s.register("outsider@example.com")
	member := s.register("member@example.com")
	s.join(e.u, e.sid, member)
	tr := e.must(xfer(e.a, e.b, "10"))

	t.Run("outsider gets 404 on every route", func(t *testing.T) {
		expectCrossSpace404(t, s, outsider, http.MethodPost, trURL(e.sid), xfer(e.a, e.b, "1"))
		expectCrossSpace404(t, s, outsider, http.MethodPatch, trURL(e.sid, "/", tr.ID), map[string]any{"note": "x"})
		expectCrossSpace404(t, s, outsider, http.MethodDelete, trURL(e.sid, "/", tr.ID), nil)
	})

	t.Run("a transfer id from another space is 404 in your own space", func(t *testing.T) {
		mine := spaceIDOf(outsider)
		for _, c := range []struct {
			method string
			body   any
		}{{http.MethodPatch, map[string]any{"note": "x"}}, {http.MethodDelete, nil}} {
			res := s.Do(c.method, trURL(mine, "/", tr.ID), c.body, bearer(outsider.AccessToken))
			require.Equal(t, http.StatusNotFound, res.Status)
			assert.Equal(t, "TRANSFER_NOT_FOUND", errCode(t, res))
		}
		assert.True(t, dec("990").Equal(e.balance(e.a)))
	})

	t.Run("members can transfer", func(t *testing.T) {
		res := s.Do(http.MethodPost, trURL(e.sid), xfer(e.a, e.b, "1"), bearer(member.AccessToken))
		require.Equal(t, http.StatusCreated, res.Status, string(res.Body))
	})
}

func TestManyTransfersKeepTheLedgerConsistent(t *testing.T) {
	// Many transfers in both directions between the same two wallets must leave
	// the balances exact and equal to the recomputed ones (also checked at teardown).
	s := NewTestServer(t)
	e := newTransferEnv(t, s)
	for i := 0; i < 10; i++ {
		e.must(xfer(e.a, e.b, "7.77"))
		e.must(xfer(e.b, e.a, "3.33"))
	}
	assert.True(t, dec("1000").Sub(dec("44.40")).Equal(e.balance(e.a)))
	assert.True(t, dec("500").Add(dec("44.40")).Equal(e.balance(e.b)))
}
