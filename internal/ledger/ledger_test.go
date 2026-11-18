package ledger

import (
	"testing"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/JonathanRainee/hzzt/internal/db/sqlc"
)

func d(s string) decimal.Decimal     { return decimal.RequireFromString(s) }
func nid(id uuid.UUID) uuid.NullUUID { return uuid.NullUUID{UUID: id, Valid: true} }
func sp(s string) *string            { return &s }

func TestDeltas(t *testing.T) {
	a, b := uuid.New(), uuid.New()
	cases := []struct {
		name string
		txn  sqlc.Transaction
		want []Delta
	}{
		{"income adds", sqlc.Transaction{Type: TypeIncome, Amount: d("50"), WalletID: nid(a)}, []Delta{{a, d("50")}}},
		{"expense subtracts", sqlc.Transaction{Type: TypeExpense, Amount: d("50.25"), WalletID: nid(a)}, []Delta{{a, d("-50.25")}}},
		{"adjustment in", sqlc.Transaction{Type: TypeAdjustment, Direction: sp("IN"), Amount: d("7"), WalletID: nid(a)}, []Delta{{a, d("7")}}},
		{"adjustment out", sqlc.Transaction{Type: TypeAdjustment, Direction: sp("OUT"), Amount: d("34.26"), WalletID: nid(a)}, []Delta{{a, d("-34.26")}}},
		{"opening in", sqlc.Transaction{Type: TypeOpeningBalance, Direction: sp("IN"), Amount: d("134.26"), WalletID: nid(a)}, []Delta{{a, d("134.26")}}},
		{"opening out (negative start)", sqlc.Transaction{Type: TypeOpeningBalance, Direction: sp("OUT"), Amount: d("10"), WalletID: nid(a)}, []Delta{{a, d("-10")}}},
		{"transfer moves between two wallets in their own currencies", sqlc.Transaction{
			Type: TypeTransfer, Amount: d("10"), WalletID: nid(a),
			ToWalletID: nid(b), ToAmount: decimal.NullDecimal{Decimal: d("160000"), Valid: true},
		}, []Delta{{a, d("-10")}, {b, d("160000")}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := Deltas(c.txn)
			require.NoError(t, err)
			require.Len(t, got, len(c.want))
			for i := range got {
				assert.Equal(t, c.want[i].WalletID, got[i].WalletID)
				assert.True(t, c.want[i].Amount.Equal(got[i].Amount), "want %s got %s", c.want[i].Amount, got[i].Amount)
			}
		})
	}
}

func TestDeltasForInvestmentTrades(t *testing.T) {
	a := uuid.New()
	buy := sqlc.Transaction{Type: TypeTransfer, Amount: d("32500"), WalletID: nid(a), InvestmentAction: sp("BUY")}
	got, err := Deltas(buy)
	require.NoError(t, err)
	require.Len(t, got, 1, "the investment side has no wallet")
	assert.Equal(t, a, got[0].WalletID)
	assert.True(t, d("-32500").Equal(got[0].Amount), "a BUY debits the wallet")

	sell := sqlc.Transaction{Type: TypeTransfer, Amount: d("31990"), WalletID: nid(a), InvestmentAction: sp("SELL")}
	got, err = Deltas(sell)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.True(t, d("31990").Equal(got[0].Amount), "a SELL credits the wallet")

	_, err = Deltas(sqlc.Transaction{Type: TypeTransfer, Amount: d("1"), WalletID: nid(a), InvestmentAction: sp("HOLD")})
	assert.Error(t, err)
	_, err = Deltas(sqlc.Transaction{Type: TypeTransfer, Amount: d("1"), InvestmentAction: sp("BUY")})
	assert.Error(t, err, "needs a wallet")
}

func TestDeltasForSettlements(t *testing.T) {
	a := uuid.New()
	got, err := Deltas(sqlc.Transaction{Type: TypeTransfer, Amount: d("200"), WalletID: nid(a), SettlementDirection: sp("IN")})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.True(t, d("200").Equal(got[0].Amount), "money received back credits the wallet")

	got, err = Deltas(sqlc.Transaction{Type: TypeTransfer, Amount: d("200"), WalletID: nid(a), SettlementDirection: sp("OUT")})
	require.NoError(t, err)
	assert.True(t, d("-200").Equal(got[0].Amount), "a debt payment debits it")

	_, err = Deltas(sqlc.Transaction{Type: TypeTransfer, Amount: d("1"), WalletID: nid(a), SettlementDirection: sp("SIDEWAYS")})
	assert.Error(t, err)
}

func TestDeltasRejectsMalformedTransactions(t *testing.T) {
	a := uuid.New()
	for name, txn := range map[string]sqlc.Transaction{
		"no wallet":             {Type: TypeIncome, Amount: d("1")},
		"adjustment no dir":     {Type: TypeAdjustment, Amount: d("1"), WalletID: nid(a)},
		"transfer no target":    {Type: TypeTransfer, Amount: d("1"), WalletID: nid(a)},
		"transfer no to_amount": {Type: TypeTransfer, Amount: d("1"), WalletID: nid(a), ToWalletID: nid(uuid.New())},
		"unknown type":          {Type: "MYSTERY", Amount: d("1"), WalletID: nid(a)},
	} {
		_, err := Deltas(txn)
		assert.Error(t, err, name)
	}
}

func TestDeltasReverseIsExactInverse(t *testing.T) {
	a := uuid.New()
	txn := sqlc.Transaction{Type: TypeExpense, Amount: d("0.1"), WalletID: nid(a)}
	ds, err := Deltas(txn)
	require.NoError(t, err)
	assert.True(t, ds[0].Amount.Add(ds[0].Amount.Neg()).IsZero())
}
