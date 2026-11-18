// Package allocation splits an amount between shares so that the parts always
// add up to the total exactly (largest-remainder method), at a currency's minor units.
package allocation

import (
	"sort"

	"github.com/shopspring/decimal"
)

// Split divides total between the shares (any positive weights, e.g. basis
// points) using the largest-remainder method at `minorUnits` decimal places:
//
//  1. every share gets floor(total × weight / Σweights) at that precision;
//  2. the units left over go, one each, to the shares with the largest
//     remainders (ties go to the earlier share).
//
// The parts always sum to total rounded to the precision. Negative totals (a
// loss) are split by magnitude and negated. Non-positive weights get nothing.
func Split(total decimal.Decimal, shares []int, minorUnits int32) []decimal.Decimal {
	out := make([]decimal.Decimal, len(shares))
	for i := range out {
		out[i] = decimal.Zero
	}
	var sum int64
	for _, s := range shares {
		if s > 0 {
			sum += int64(s)
		}
	}
	if sum == 0 || total.IsZero() {
		return out
	}

	neg := total.IsNegative()
	// work in integer minor units: total × 10^minorUnits, rounded half away from zero
	scale := decimal.New(1, minorUnits)
	units := total.Abs().Mul(scale).Round(0)

	type part struct {
		idx   int
		floor decimal.Decimal
		rem   decimal.Decimal
	}
	parts := make([]part, 0, len(shares))
	allocated := decimal.Zero
	for i, s := range shares {
		if s <= 0 {
			continue
		}
		exact := units.Mul(decimal.NewFromInt(int64(s))).Div(decimal.NewFromInt(sum))
		fl := exact.Floor()
		parts = append(parts, part{idx: i, floor: fl, rem: exact.Sub(fl)})
		allocated = allocated.Add(fl)
	}

	left := units.Sub(allocated).IntPart() // 0 ≤ left < len(parts)
	order := make([]int, len(parts))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool { return parts[order[a]].rem.GreaterThan(parts[order[b]].rem) })
	for k := int64(0); k < left && int(k) < len(order); k++ {
		parts[order[k]].floor = parts[order[k]].floor.Add(decimal.NewFromInt(1))
	}

	for _, p := range parts {
		v := p.floor.Div(scale)
		if neg {
			v = v.Neg()
		}
		out[p.idx] = v
	}
	return out
}

// Sum adds up parts (a small helper for callers and tests).
func Sum(parts []decimal.Decimal) decimal.Decimal {
	t := decimal.Zero
	for _, p := range parts {
		t = t.Add(p)
	}
	return t
}
