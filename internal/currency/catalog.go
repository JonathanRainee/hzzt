// Package currency owns the supported-currency catalog, FX rates (provider
// fetch + refresh job), the per-request ConversionContext and the only code
// that converts money between currencies.
package currency

import (
	"github.com/JonathanRainee/hzzt/internal/platform/money"
)

// Currency describes one supported currency.
type Currency struct {
	Code       string `json:"code"`
	Name       string `json:"name"`
	Symbol     string `json:"symbol"`
	MinorUnits int32  `json:"minorUnits"`
}

var names = map[string][2]string{ // code → {name, symbol}
	"AUD": {"Australian Dollar", "A$"}, "BRL": {"Brazilian Real", "R$"}, "CAD": {"Canadian Dollar", "CA$"},
	"CHF": {"Swiss Franc", "CHF"}, "CNY": {"Chinese Yuan", "CN¥"}, "CZK": {"Czech Koruna", "Kč"},
	"DKK": {"Danish Krone", "kr"}, "EUR": {"Euro", "€"}, "GBP": {"British Pound", "£"},
	"HKD": {"Hong Kong Dollar", "HK$"}, "IDR": {"Indonesian Rupiah", "Rp"}, "INR": {"Indian Rupee", "₹"},
	"JPY": {"Japanese Yen", "¥"}, "KRW": {"South Korean Won", "₩"}, "MXN": {"Mexican Peso", "MX$"},
	"MYR": {"Malaysian Ringgit", "RM"}, "NOK": {"Norwegian Krone", "kr"}, "NZD": {"New Zealand Dollar", "NZ$"},
	"PHP": {"Philippine Peso", "₱"}, "PLN": {"Polish Zloty", "zł"}, "SEK": {"Swedish Krona", "kr"},
	"SGD": {"Singapore Dollar", "S$"}, "THB": {"Thai Baht", "฿"}, "TRY": {"Turkish Lira", "₺"},
	"USD": {"US Dollar", "$"}, "ZAR": {"South African Rand", "R"},
}

// Catalog returns every supported currency (sorted by code), built from
// money.SupportedCurrencies so there is a single source of truth.
func Catalog() []Currency {
	out := make([]Currency, 0, len(money.SupportedCurrencies))
	for _, code := range money.SupportedCurrencies {
		n := names[code]
		if n[0] == "" {
			n = [2]string{code, code}
		}
		out = append(out, Currency{Code: code, Name: n[0], Symbol: n[1], MinorUnits: money.MinorUnits(code)})
	}
	return out
}
