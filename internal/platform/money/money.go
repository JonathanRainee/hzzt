// Package money holds decimal helpers. Money and quantities are always
// decimal.Decimal; float64 must never be used for them.
package money

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/shopspring/decimal"
)

// plain decimal literal: optional sign, digits, optional fraction. No exponent,
// no thousands separators, no whitespace.
var plainDecimal = regexp.MustCompile(`^-?\d+(\.\d+)?$`)

// Parse converts an API string such as "134.26" into a Decimal. It is stricter
// than decimal.NewFromString: exponent notation ("1e3") and blanks are rejected.
func Parse(s string) (decimal.Decimal, error) {
	if !plainDecimal.MatchString(s) {
		return decimal.Decimal{}, fmt.Errorf("invalid decimal %q", s)
	}
	return decimal.NewFromString(s)
}

// Format renders d as a plain string without exponent (e.g. "0.3").
func Format(d decimal.Decimal) string { return d.String() }

// minorUnits lists currencies whose minor-unit count is not 2.
// IDR is 0 by product decision (see plan section 5.2), though ISO 4217 says 2.
var minorUnits = map[string]int32{
	"IDR": 0, "JPY": 0, "KRW": 0, "VND": 0, "CLP": 0, "PYG": 0, "ISK": 0, "UGX": 0, "XAF": 0, "XOF": 0,
	"BHD": 3, "KWD": 3, "JOD": 3, "OMR": 3, "TND": 3,
}

// MinorUnits returns the number of decimal places for a currency code
// (case-insensitive). Unlisted currencies default to 2.
func MinorUnits(currency string) int32 {
	if n, ok := minorUnits[strings.ToUpper(strings.TrimSpace(currency))]; ok {
		return n
	}
	return 2
}

// RoundToMinor rounds d (half away from zero) to the currency's minor units.
// Use it only for allocation and presentation (plan section 5.2).
func RoundToMinor(d decimal.Decimal, currency string) decimal.Decimal {
	return d.Round(MinorUnits(currency))
}
