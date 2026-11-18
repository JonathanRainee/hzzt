package transfers

import (
	"testing"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/JonathanRainee/hzzt/internal/db/sqlc"
	"github.com/JonathanRainee/hzzt/internal/platform/apperror"
)

func d(s string) decimal.Decimal   { return decimal.RequireFromString(s) }
func dp(s string) *decimal.Decimal { v := d(s); return &v }
func wal(cur string) sqlc.Wallet   { return sqlc.Wallet{ID: uuid.New(), Currency: cur} }
func code(t *testing.T, err error) string {
	t.Helper()
	ae, ok := apperror.As(err)
	require.True(t, ok, "%v", err)
	return ae.Code
}

func TestFinalize(t *testing.T) {
	usd1, usd2, idr := wal("USD"), wal("USD"), wal("IDR")

	t.Run("same currency defaults toAmount to amount and rate to 1", func(t *testing.T) {
		to, rate, err := finalize(usd1, usd2, d("100"), nil, nil)
		require.NoError(t, err)
		assert.True(t, d("100").Equal(to))
		assert.True(t, d("1").Equal(rate))
	})
	t.Run("same currency accepts an equal toAmount", func(t *testing.T) {
		_, _, err := finalize(usd1, usd2, d("100"), dp("100.00"), nil)
		assert.NoError(t, err)
	})
	t.Run("same currency rejects a different toAmount", func(t *testing.T) {
		_, _, err := finalize(usd1, usd2, d("100"), dp("99"), nil)
		assert.Equal(t, apperror.CodeValidationFailed, code(t, err))
	})
	t.Run("acceptance: 100 USD → IDR with toAmount 1,600,000 stores rate 16000", func(t *testing.T) {
		to, rate, err := finalize(usd1, idr, d("100.00"), dp("1600000"), nil)
		require.NoError(t, err)
		assert.True(t, d("1600000").Equal(to))
		assert.True(t, d("16000").Equal(rate))
	})
	t.Run("rate is rounded to 12 places", func(t *testing.T) {
		_, rate, err := finalize(usd1, idr, d("3"), dp("10"), nil)
		require.NoError(t, err)
		assert.Equal(t, "3.333333333333", rate.String())
	})
	t.Run("cross currency needs toAmount", func(t *testing.T) {
		_, _, err := finalize(usd1, idr, d("100"), nil, nil)
		assert.Equal(t, CodeTransferAmountRequired, code(t, err))
	})
	t.Run("an unchanged cross-currency edit can reuse the old toAmount", func(t *testing.T) {
		to, rate, err := finalize(usd1, idr, d("100"), nil, dp("1600000"))
		require.NoError(t, err)
		assert.True(t, d("1600000").Equal(to))
		assert.True(t, d("16000").Equal(rate))
	})
	t.Run("an explicit toAmount beats the fallback", func(t *testing.T) {
		to, _, err := finalize(usd1, idr, d("100"), dp("1500000"), dp("1600000"))
		require.NoError(t, err)
		assert.True(t, d("1500000").Equal(to))
	})
	t.Run("same wallet", func(t *testing.T) {
		_, _, err := finalize(usd1, usd1, d("1"), nil, nil)
		assert.Equal(t, CodeTransferSameWallet, code(t, err))
	})
	t.Run("absurd rates are rejected instead of overflowing the column", func(t *testing.T) {
		_, _, err := finalize(usd1, idr, d("0.00000001"), dp("99999999999999999999"), nil)
		assert.Equal(t, apperror.CodeValidationFailed, code(t, err))
	})
}

func TestDecodeEndpoint(t *testing.T) {
	id := uuid.New()
	got, err := decodeEndpoint("from", []byte(`{"type":"WALLET","walletId":"`+id.String()+`"}`))
	require.NoError(t, err)
	assert.Equal(t, id, got)

	_, err = decodeEndpoint("to", []byte(`{"type":"INVESTMENT","investmentId":"`+id.String()+`"}`))
	assert.Equal(t, CodeNotSupportedYet, code(t, err))
	_, err = decodeEndpoint("to", []byte(`{"type":"INVESTMENT"}`))
	assert.Equal(t, CodeNotSupportedYet, code(t, err))

	for name, raw := range map[string]string{
		"missing":       ``,
		"null":          `null`,
		"no type":       `{"walletId":"` + id.String() + `"}`,
		"unknown type":  `{"type":"BANK"}`,
		"no wallet id":  `{"type":"WALLET"}`,
		"bad wallet id": `{"type":"WALLET","walletId":"x"}`,
		"extra field":   `{"type":"WALLET","walletId":"` + id.String() + `","x":1}`,
		"not an object": `"w"`,
	} {
		_, err := decodeEndpoint("from", []byte(raw))
		require.Error(t, err, name)
		assert.Equal(t, apperror.CodeValidationFailed, code(t, err), name)
	}
}
