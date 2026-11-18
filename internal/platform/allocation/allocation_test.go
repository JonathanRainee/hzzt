package allocation

import (
	"math/rand"
	"testing"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func d(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func strs(parts []decimal.Decimal) []string {
	out := make([]string, len(parts))
	for i, p := range parts {
		out[i] = p.String()
	}
	return out
}

func TestSplit(t *testing.T) {
	cases := []struct {
		name   string
		total  string
		shares []int
		units  int32
		want   []string
	}{
		{"acceptance: 50/30/20 of 1,000.01 USD", "1000.01", []int{5000, 3000, 2000}, 2, []string{"500.01", "300", "200"}},
		{"an even split needs no remainder", "100", []int{5000, 5000}, 2, []string{"50", "50"}},
		{"thirds: the cent goes to the first", "100", []int{1, 1, 1}, 2, []string{"33.34", "33.33", "33.33"}},
		{"thirds of 100.01: two leftover cents, earliest first", "100.01", []int{1, 1, 1}, 2, []string{"33.34", "33.34", "33.33"}},
		{"the largest remainder wins (1001 units: remainders .6333 .6333 .7334, two units left)", "10.01", []int{3333, 3333, 3334}, 2, []string{"3.34", "3.33", "3.34"}},
		{"zero minor units (IDR)", "1000", []int{3333, 3333, 3334}, 0, []string{"333", "333", "334"}},
		{"IDR with a leftover rupiah", "100", []int{1, 1, 1}, 0, []string{"34", "33", "33"}},
		{"three-decimal currency", "1", []int{1, 1, 1}, 3, []string{"0.334", "0.333", "0.333"}},
		{"a 100% single share", "123.45", []int{10000}, 2, []string{"123.45"}},
		{"a tiny share gets its floor", "1000", []int{9999, 1}, 2, []string{"999.9", "0.1"}},
		{"a zero total", "0", []int{5000, 5000}, 2, []string{"0", "0"}},
		{"a loss is split by magnitude", "-100", []int{1, 1, 1}, 2, []string{"-33.34", "-33.33", "-33.33"}},
		{"weights do not have to sum to 10,000", "90", []int{1, 2}, 2, []string{"30", "60"}},
		{"a non-positive weight gets nothing", "100", []int{5000, 0, 5000}, 2, []string{"50", "0", "50"}},
		{"sub-minor-unit totals round to the precision", "0.004", []int{1, 1}, 2, []string{"0", "0"}},
		{"0.005 rounds up and is shared out", "0.005", []int{1, 1}, 2, []string{"0.01", "0"}},
		{"big numbers keep precision", "99999999999999.99", []int{1, 1, 1}, 2, []string{"33333333333333.33", "33333333333333.33", "33333333333333.33"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Split(d(c.total), c.shares, c.units)
			require.Len(t, got, len(c.shares))
			assert.Equal(t, c.want, strs(got))
		})
	}
}

func TestSplitEdgeCases(t *testing.T) {
	assert.Empty(t, Split(d("10"), nil, 2))
	for _, got := range [][]decimal.Decimal{Split(d("10"), []int{0, 0}, 2), Split(d("10"), []int{-5, 0}, 2)} {
		assert.Equal(t, []string{"0", "0"}, strs(got), "no positive weight: nothing to allocate")
	}
}

// The parts must always add up exactly: checked over many random totals, share
// lists and currencies.
func TestSplitAlwaysAddsUpExactly(t *testing.T) {
	rng := rand.New(rand.NewSource(14))
	for i := 0; i < 20000; i++ {
		units := int32(rng.Intn(4)) // 0..3 decimals
		n := 1 + rng.Intn(10)
		shares := make([]int, n)
		bps := 10000
		for j := 0; j < n-1; j++ {
			s := 1 + rng.Intn(max(1, bps-(n-1-j)))
			if s > bps-(n-1-j) {
				s = bps - (n - 1 - j)
			}
			shares[j] = s
			bps -= s
		}
		shares[n-1] = bps

		// a random total with up to `units` decimals, positive or negative
		raw := decimal.New(rng.Int63n(1_000_000_000_000), -units)
		if rng.Intn(4) == 0 {
			raw = raw.Neg()
		}
		got := Split(raw, shares, units)
		require.Len(t, got, n)
		require.True(t, raw.Equal(Sum(got)), "total %s shares %v units %d → %v sums to %s", raw, shares, units, strs(got), Sum(got))

		// every part is within one minor unit of its exact proportional share, and never has the wrong sign
		unit := decimal.New(1, -units)
		for j, p := range got {
			exact := raw.Mul(decimal.NewFromInt(int64(shares[j]))).Div(decimal.NewFromInt(10000))
			require.True(t, p.Sub(exact).Abs().LessThanOrEqual(unit), "part %d: %s vs exact %s (unit %s)", j, p, exact, unit)
			require.True(t, p.Round(units).Equal(p), "part %s has more than %d decimals", p, units)
			if raw.IsPositive() {
				require.False(t, p.IsNegative())
			}
			if raw.IsNegative() {
				require.False(t, p.IsPositive())
			}
		}
	}
}

func TestSplitIsDeterministicAndOrderStable(t *testing.T) {
	a := Split(d("100"), []int{1, 1, 1}, 2)
	b := Split(d("100"), []int{1, 1, 1}, 2)
	assert.Equal(t, strs(a), strs(b))
	// equal remainders: the earlier share is favoured, always
	assert.Equal(t, "33.34", a[0].String())
}
