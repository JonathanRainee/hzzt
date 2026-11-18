//go:build e2e

package e2e

import (
	"context"
	"errors"

	"github.com/shopspring/decimal"

	"github.com/JonathanRainee/hzzt/internal/currency"
)

// FakeFX is a controllable FxProvider.
type FakeFX struct {
	Set   currency.RateSet
	Err   error
	Calls int
}

func (f *FakeFX) Latest(context.Context) (currency.RateSet, error) {
	f.Calls++
	if f.Err != nil {
		return currency.RateSet{}, f.Err
	}
	return f.Set, nil
}

func dec(s string) decimal.Decimal { return decimal.RequireFromString(s) }

// defaultRates is a small, predictable USD-based table: 1 USD = 16000 IDR = 0.8 EUR.
func defaultRates(c *testClock) currency.RateSet {
	n := c.Now().UTC()
	return currency.RateSet{
		AsOf:   n.Truncate(24 * 60 * 60 * 1e9),
		Source: "fake",
		Rates: map[string]decimal.Decimal{
			"IDR": dec("16000"), "EUR": dec("0.8"), "JPY": dec("150"), "GBP": dec("0.75"),
		},
	}
}

var errProvider = errors.New("provider down")
