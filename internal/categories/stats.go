package categories

import (
	"context"
	"strings"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/JonathanRainee/hzzt/internal/currency"
	"github.com/JonathanRainee/hzzt/internal/db/sqlc"
	"github.com/JonathanRainee/hzzt/internal/platform/period"
)

// Stats is a category's usage in a period, in the space base currency.
type Stats struct {
	// Expense categories.
	Spent           *decimal.Decimal
	BudgetRemaining *decimal.Decimal // set when the category has a budget; may be negative
	BudgetUsedPct   *decimal.Decimal // spent / budget * 100, 2 decimals
	// Income categories.
	Received *decimal.Decimal
	Warnings []currency.Warning
}

// WithStats is a category plus its usage stats.
type WithStats struct {
	Category sqlc.Category
	Stats    Stats
}

// ListWithStats lists categories (typ "" means both) with their usage in rng.
// Amounts in currencies without a usable FX rate are excluded and reported per category.
func (s *Service) ListWithStats(ctx context.Context, spaceID uuid.UUID, typ string, rng period.Range) ([]WithStats, error) {
	cats, err := s.List(ctx, spaceID, typ)
	if err != nil {
		return nil, err
	}
	rows, err := s.q.CategoryStats(ctx, sqlc.CategoryStatsParams{SpaceID: spaceID, FromTs: rng.From, ToTs: rng.To})
	if err != nil {
		return nil, err
	}
	cc, err := s.currency.BuildContext(ctx, spaceID)
	if err != nil {
		return nil, err
	}

	byCat := map[uuid.UUID][]currency.MoneyItem{}
	for _, r := range rows {
		if !r.CategoryID.Valid {
			continue
		}
		byCat[r.CategoryID.UUID] = append(byCat[r.CategoryID.UUID], currency.MoneyItem{Amount: r.Total, Currency: r.Currency})
	}

	out := make([]WithStats, 0, len(cats))
	for _, c := range cats {
		agg := currency.Aggregate(byCat[c.ID], cc)
		total := agg.Total
		st := Stats{Warnings: agg.Warnings}
		if c.Type == TypeIncome {
			st.Received = &total
		} else {
			st.Spent = &total
			if c.BudgetAmount.Valid {
				budget := c.BudgetAmount.Decimal
				rem := budget.Sub(total)
				pct := total.Div(budget).Mul(decimal.NewFromInt(100)).Round(2)
				st.BudgetRemaining, st.BudgetUsedPct = &rem, &pct
			}
		}
		out = append(out, WithStats{Category: c, Stats: st})
	}
	return out, nil
}

// BaseCurrencyOf is a small helper for DTOs.
func BaseCurrencyOf(sp sqlc.Space) string { return strings.TrimSpace(sp.BaseCurrency) }
