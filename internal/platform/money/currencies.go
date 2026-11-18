package money

import (
	"slices"
	"strings"
)

// SupportedCurrencies is the set of ISO 4217 codes a space may use as its
// base currency, restricted to codes both FX providers (Frankfurter and
// freecurrencyapi) return. P5 builds the catalog and FX on top of it.
var SupportedCurrencies = []string{
	"AUD", "BRL", "CAD", "CHF", "CNY", "CZK", "DKK", "EUR", "GBP", "HKD",
	"IDR", "INR", "JPY", "KRW", "MXN", "MYR", "NOK", "NZD", "PHP", "PLN",
	"SEK", "SGD", "THB", "TRY", "USD", "ZAR",
}

// IsSupportedCurrency reports whether code (case-insensitive) is supported.
func IsSupportedCurrency(code string) bool {
	return slices.Contains(SupportedCurrencies, strings.ToUpper(strings.TrimSpace(code)))
}
