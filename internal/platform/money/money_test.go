package money

import (
	"encoding/json"
	"testing"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDecimalAddition(t *testing.T) {
	a, err := decimal.NewFromString("0.1")
	require.NoError(t, err)
	b, err := decimal.NewFromString("0.2")
	require.NoError(t, err)
	assert.Equal(t, "0.3", a.Add(b).String())
}

func TestMinorUnits(t *testing.T) {
	assert.Equal(t, int32(0), MinorUnits("IDR"))
	assert.Equal(t, int32(0), MinorUnits("idr"))
	assert.Equal(t, int32(0), MinorUnits("JPY"))
	assert.Equal(t, int32(2), MinorUnits("USD"))
	assert.Equal(t, int32(3), MinorUnits("KWD"))
	assert.Equal(t, int32(2), MinorUnits("ZZZ"))
}

func TestRoundToMinor(t *testing.T) {
	d := decimal.RequireFromString("1234.5678")
	assert.Equal(t, "1235", RoundToMinor(d, "IDR").String())
	assert.Equal(t, "1234.57", RoundToMinor(d, "USD").String())
	assert.Equal(t, "2", RoundToMinor(decimal.RequireFromString("1.5"), "IDR").String())
}

func TestParse(t *testing.T) {
	for _, ok := range []string{"0", "134.26", "-5.5", "0.00000001"} {
		_, err := Parse(ok)
		assert.NoError(t, err, ok)
	}
	for _, bad := range []string{"", " 1", "1e3", "1,000", "1.", ".5", "abc", "+1"} {
		_, err := Parse(bad)
		assert.Error(t, err, bad)
	}
}

func TestJSONRoundTripIsString(t *testing.T) {
	type dto struct {
		Amount decimal.Decimal `json:"amount"`
	}
	b, err := json.Marshal(dto{Amount: decimal.RequireFromString("134.26")})
	require.NoError(t, err)
	assert.JSONEq(t, `{"amount":"134.26"}`, string(b))

	var out dto
	require.NoError(t, json.Unmarshal([]byte(`{"amount":"0.1"}`), &out))
	assert.Equal(t, "0.1", out.Amount.String())
}
