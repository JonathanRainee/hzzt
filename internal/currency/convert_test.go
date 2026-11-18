package currency

import (
	"errors"
	"testing"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func d(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func live(base string) *ConversionContext {
	return &ConversionContext{
		BaseCurrency: base, AutoFX: true,
		Rates: map[string]decimal.Decimal{"IDR": d("16000"), "EUR": d("0.8"), "JPY": d("150")},
	}
}

func TestConvertLive(t *testing.T) {
	cc := live("USD")
	cases := []struct {
		name     string
		amount   string
		from, to string
		want     string
	}{
		{"acceptance: 10 USD → IDR", "10", "USD", "IDR", "160000"},
		{"IDR → USD", "160000", "IDR", "USD", "10"},
		{"cross via USD: IDR → EUR", "16000", "IDR", "EUR", "0.8"},
		{"cross via USD: EUR → JPY", "2", "EUR", "JPY", "375"},
		{"same currency is identity", "123.456", "EUR", "EUR", "123.456"},
		{"case-insensitive codes", "1", "usd", " idr ", "16000"},
		{"zero stays zero", "0", "USD", "IDR", "0"},
		{"negative amounts keep sign", "-5", "USD", "EUR", "-4"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := Convert(d(c.amount), c.from, c.to, cc)
			require.NoError(t, err)
			assert.True(t, d(c.want).Equal(got), "want %s got %s", c.want, got)
		})
	}
}

func TestConvertRoundTripKeepsPrecision(t *testing.T) {
	cc := live("USD")
	cc.Rates["IDR"] = d("16234.567891")
	there, err := Convert(d("1234.56"), "USD", "IDR", cc)
	require.NoError(t, err)
	back, err := Convert(there, "IDR", "USD", cc)
	require.NoError(t, err)
	assert.True(t, back.Sub(d("1234.56")).Abs().LessThan(d("0.000000001")), back.String())
}

func TestConvertLiveMissingRate(t *testing.T) {
	cc := live("USD")
	_, err := Convert(d("1"), "USD", "GBP", cc)
	assert.ErrorIs(t, err, ErrRateUnavailable)
	_, err = Convert(d("1"), "GBP", "EUR", cc)
	assert.ErrorIs(t, err, ErrRateUnavailable)
	_, err = Convert(d("1"), "USD", "IDR", &ConversionContext{AutoFX: true})
	assert.ErrorIs(t, err, ErrRateUnavailable, "no rates loaded at all")
	// same currency never needs a rate
	got, err := Convert(d("5"), "GBP", "GBP", cc)
	require.NoError(t, err)
	assert.True(t, d("5").Equal(got))
}

func TestConvertManualMode(t *testing.T) {
	cc := live("USD")
	cc.AutoFX = false
	cc.ManualRates = map[string]decimal.Decimal{"USD/IDR": d("15000"), "EUR/USD": d("1.25")}

	t.Run("uses the manual rate even when a live rate exists", func(t *testing.T) {
		got, err := Convert(d("10"), "USD", "IDR", cc)
		require.NoError(t, err)
		assert.True(t, d("150000").Equal(got), got.String())
	})
	t.Run("inverse of a manual rate", func(t *testing.T) {
		got, err := Convert(d("150000"), "IDR", "USD", cc)
		require.NoError(t, err)
		assert.True(t, d("10").Sub(got).Abs().LessThan(d("0.000000001")), got.String())
	})
	t.Run("a missing manual rate is unavailable, not silently live", func(t *testing.T) {
		_, err := Convert(d("1"), "USD", "JPY", cc)
		assert.ErrorIs(t, err, ErrRateUnavailable)
	})
	t.Run("same currency still works", func(t *testing.T) {
		got, err := Convert(d("7"), "JPY", "JPY", cc)
		require.NoError(t, err)
		assert.True(t, d("7").Equal(got))
	})
}

func TestAggregate(t *testing.T) {
	cc := live("USD")
	res := Aggregate([]MoneyItem{
		{Label: "Cash", Amount: d("100"), Currency: "USD"},
		{Label: "Savings", Amount: d("160000"), Currency: "IDR"}, // 10 USD
		{Label: "More IDR", Amount: d("16000"), Currency: "IDR"}, // 1 USD
		{Label: "Pound jar", Amount: d("50"), Currency: "GBP"},   // no rate
	}, cc)

	assert.True(t, d("111").Equal(res.Total), res.Total.String())
	assert.Equal(t, "USD", res.Currency)

	require.Len(t, res.Breakdown, 2)
	assert.Equal(t, "IDR", res.Breakdown[0].Currency)
	assert.True(t, d("176000").Equal(res.Breakdown[0].Original))
	assert.True(t, d("11").Equal(res.Breakdown[0].Converted))
	assert.Equal(t, "USD", res.Breakdown[1].Currency)

	require.Len(t, res.Warnings, 1, "a missing rate is a warning, not an error")
	assert.Equal(t, CodeFXRateUnavailable, res.Warnings[0].Code)
	assert.Equal(t, "Pound jar", res.Warnings[0].Label)
	assert.Contains(t, res.Warnings[0].Message, "GBP")
}

func TestAggregateEmptyAndAllMissing(t *testing.T) {
	res := Aggregate(nil, live("EUR"))
	assert.True(t, res.Total.IsZero())
	assert.NotNil(t, res.Breakdown)
	assert.NotNil(t, res.Warnings)

	res = Aggregate([]MoneyItem{{Amount: d("5"), Currency: "GBP"}}, live("EUR"))
	assert.True(t, res.Total.IsZero())
	require.Len(t, res.Warnings, 1)
	assert.Contains(t, res.Warnings[0].Message, "GBP amount")
}

func TestAggregateManualMode(t *testing.T) {
	cc := &ConversionContext{BaseCurrency: "IDR", AutoFX: false,
		ManualRates: map[string]decimal.Decimal{"USD/IDR": d("15000")}, Rates: live("IDR").Rates}
	res := Aggregate([]MoneyItem{
		{Amount: d("2"), Currency: "USD"},
		{Amount: d("1000"), Currency: "IDR"},
		{Amount: d("1"), Currency: "EUR"}, // live rate exists but manual mode ignores it
	}, cc)
	assert.True(t, d("31000").Equal(res.Total), res.Total.String())
	require.Len(t, res.Warnings, 1)
	assert.True(t, errors.Is(ErrRateUnavailable, ErrRateUnavailable))
}
