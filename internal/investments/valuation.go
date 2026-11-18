package investments

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"github.com/JonathanRainee/hzzt/internal/currency"
	"github.com/JonathanRainee/hzzt/internal/db/sqlc"
)

// StaleAfter is how old a market price may be before it is reported as stale.
const StaleAfter = 30 * time.Minute

// MarketPrice is the latest provider price of an investment's instrument (P11/P12).
type MarketPrice struct {
	Price decimal.Decimal
	AsOf  time.Time
	// StaleAfter is how old the price may get before it is reported stale;
	// zero means the default (StaleAfter, 30 minutes). Providers differ: crypto
	// refreshes every few minutes, end-of-day stock prices only once a day.
	StaleAfter time.Duration
}

// BaseValues are the valuation figures converted to the space base currency.
type BaseValues struct {
	Currency     string          `json:"currency" example:"IDR"`
	MarketValue  decimal.Decimal `json:"marketValue" swaggertype:"string"`
	CostBasis    decimal.Decimal `json:"costBasis" swaggertype:"string"`
	UnrealizedPL decimal.Decimal `json:"unrealizedPl" swaggertype:"string"`
	// FxRate is the rate used: base-currency units per 1 quote-currency unit.
	FxRate decimal.Decimal `json:"fxRate" swaggertype:"string" example:"16000"`
}

// Valuation is the computed worth of one investment.
type Valuation struct {
	Price       decimal.Decimal `json:"price" swaggertype:"string"`
	PriceSource string          `json:"priceSource" enums:"MARKET,MANUAL,MANUAL_VALUE"`
	PriceAsOf   *time.Time      `json:"priceAsOf"`
	IsStale     bool            `json:"isStale"`

	// Native figures, in the investment's quote currency.
	MarketValue  decimal.Decimal `json:"marketValue" swaggertype:"string"`
	CostBasis    decimal.Decimal `json:"costBasis" swaggertype:"string"`
	UnrealizedPL decimal.Decimal `json:"unrealizedPl" swaggertype:"string"`
	// UnrealizedPLPct is a percentage (12.5 means +12.5%); null when the cost basis is 0.
	UnrealizedPLPct *decimal.Decimal `json:"unrealizedPlPct" swaggertype:"string"`

	// Base holds the converted figures; null when no FX rate is available (see Warnings).
	Base     *BaseValues        `json:"base"`
	Warnings []currency.Warning `json:"warnings"`
}

// OwnershipFactor is the share of an investment that belongs to this space (P14
// changes it for shared investments). Today every investment is fully owned.
func OwnershipFactor(sqlc.Investment) decimal.Decimal { return decimal.NewFromInt(1) }

var hundred = decimal.NewFromInt(100)

// Value computes the valuation of inv.
//
//  1. price = the market price when price_mode is AUTO and one exists, else manual_price.
//  2. marketValue = manual_current_value when set, else quantity × price.
//  3. costBasis = quantity × avg_buy_price + fees.
//  4. unrealizedPl = marketValue − costBasis; unrealizedPlPct = pl / costBasis × 100.
//  5. conversion to the base currency: with auto FX off and a manual_fx_rate on the
//     investment, that rate; otherwise the space's ConversionContext.
//  6. both values are scaled by the ownership factor.
func Value(inv sqlc.Investment, mp *MarketPrice, cc *currency.ConversionContext, now time.Time) Valuation {
	v := Valuation{PriceSource: SourceManual, Warnings: []currency.Warning{}}

	price := inv.ManualPrice
	if inv.PriceMode == PriceModeAuto && mp != nil {
		price = mp.Price
		v.PriceSource = SourceMarket
		asOf := mp.AsOf.UTC()
		v.PriceAsOf = &asOf
		limit := StaleAfter
		if mp.StaleAfter > 0 {
			limit = mp.StaleAfter
		}
		v.IsStale = now.Sub(mp.AsOf) > limit
	}
	v.Price = price

	factor := OwnershipFactor(inv)
	marketValue := inv.Quantity.Mul(price)
	if inv.ManualCurrentValue.Valid {
		marketValue = inv.ManualCurrentValue.Decimal
		v.PriceSource = SourceManualValue
	}
	cost := inv.Quantity.Mul(inv.AvgBuyPrice).Add(inv.Fees)

	v.MarketValue = marketValue.Mul(factor)
	v.CostBasis = cost.Mul(factor)
	v.UnrealizedPL = v.MarketValue.Sub(v.CostBasis)
	if !v.CostBasis.IsZero() {
		pct := v.UnrealizedPL.Div(v.CostBasis).Mul(hundred)
		v.UnrealizedPLPct = &pct
	}

	quote := strings.TrimSpace(inv.QuoteCurrency)
	rate, err := conversionRate(inv, quote, cc)
	if err != nil {
		v.Warnings = append(v.Warnings, currency.Warning{
			Code: currency.CodeFXRateUnavailable, Label: inv.Name,
			Message: fmt.Sprintf("No %s→%s rate; %s is left out of base-currency totals", quote, cc.BaseCurrency, inv.Name),
		})
		return v
	}
	v.Base = &BaseValues{
		Currency:     strings.TrimSpace(cc.BaseCurrency),
		MarketValue:  v.MarketValue.Mul(rate),
		CostBasis:    v.CostBasis.Mul(rate),
		UnrealizedPL: v.UnrealizedPL.Mul(rate),
		FxRate:       rate,
	}
	return v
}

// conversionRate picks the quote→base rate for one investment.
func conversionRate(inv sqlc.Investment, quote string, cc *currency.ConversionContext) (decimal.Decimal, error) {
	base := strings.TrimSpace(cc.BaseCurrency)
	if strings.EqualFold(quote, base) {
		return decimal.NewFromInt(1), nil
	}
	if !cc.AutoFX && inv.ManualFxRate.Valid {
		return inv.ManualFxRate.Decimal, nil // the per-investment override, only in manual FX mode
	}
	return cc.Rate(quote, base)
}

// Summary totals a portfolio in the base currency.
type Summary struct {
	Currency     string          `json:"currency" example:"IDR"`
	TotalValue   decimal.Decimal `json:"totalValue" swaggertype:"string"`
	TotalCost    decimal.Decimal `json:"totalCost" swaggertype:"string"`
	UnrealizedPL decimal.Decimal `json:"unrealizedPl" swaggertype:"string"`
	// UnrealizedPLPct is a percentage; null when the total cost is 0.
	UnrealizedPLPct *decimal.Decimal   `json:"unrealizedPlPct" swaggertype:"string"`
	Warnings        []currency.Warning `json:"warnings"`
}

// Summarize adds up the base-currency figures of the valuations. Investments
// without a usable FX rate are left out and reported once each in Warnings.
func Summarize(baseCurrency string, vals []Valuation) Summary {
	s := Summary{Currency: strings.TrimSpace(baseCurrency), Warnings: []currency.Warning{}}
	seen := map[string]bool{}
	for _, v := range vals {
		if v.Base == nil {
			for _, w := range v.Warnings {
				if !seen[w.Message] {
					seen[w.Message] = true
					s.Warnings = append(s.Warnings, w)
				}
			}
			continue
		}
		s.TotalValue = s.TotalValue.Add(v.Base.MarketValue)
		s.TotalCost = s.TotalCost.Add(v.Base.CostBasis)
		s.UnrealizedPL = s.UnrealizedPL.Add(v.Base.UnrealizedPL)
	}
	if !s.TotalCost.IsZero() {
		pct := s.UnrealizedPL.Div(s.TotalCost).Mul(hundred)
		s.UnrealizedPLPct = &pct
	}
	sort.SliceStable(s.Warnings, func(i, j int) bool { return s.Warnings[i].Message < s.Warnings[j].Message })
	return s
}
