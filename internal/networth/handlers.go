package networth

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/JonathanRainee/hzzt/internal/auth"
	"github.com/JonathanRainee/hzzt/internal/currency"
	"github.com/JonathanRainee/hzzt/internal/platform/httpx"
	"github.com/JonathanRainee/hzzt/internal/spaces"
)

// Handler exposes the net-worth endpoints.
type Handler struct {
	svc    *Service
	auth   *auth.Service
	spaces *spaces.Service
}

// NewHandler creates the HTTP layer.
func NewHandler(svc *Service, authSvc *auth.Service, spacesSvc *spaces.Service) *Handler {
	return &Handler{svc: svc, auth: authSvc, spaces: spacesSvc}
}

// Mount registers the routes on r (the /api/v1 router). Space routes carry the
// full path because the spaces handler owns the /spaces/{spaceId} mount point.
func (h *Handler) Mount(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(h.auth.Middleware, h.spaces.Middleware)
		r.Get("/spaces/{spaceId}/net-worth/summary", h.summary)
	})
}

// InvestmentsDTO is the investments section of the summary.
type InvestmentsDTO struct {
	Value           decimal.Decimal  `json:"value" swaggertype:"string"`
	Cost            decimal.Decimal  `json:"cost" swaggertype:"string"`
	UnrealizedPL    decimal.Decimal  `json:"unrealizedPl" swaggertype:"string"`
	UnrealizedPLPct *decimal.Decimal `json:"unrealizedPlPct" swaggertype:"string"` // a percentage; null when the cost is 0
}

// SummaryDTO is the net-worth summary in the space's base currency.
type SummaryDTO struct {
	Currency     string             `json:"currency" example:"USD"`
	TrackingMode string             `json:"trackingMode" enums:"FULL_NET_WORTH,CASH_ONLY"`
	AsOf         time.Time          `json:"asOf"`
	Cash         decimal.Decimal    `json:"cash" swaggertype:"string"`
	Investments  *InvestmentsDTO    `json:"investments,omitempty"`
	FixedAssets  *decimal.Decimal   `json:"fixedAssets,omitempty" swaggertype:"string"`
	Receivables  *decimal.Decimal   `json:"receivables,omitempty" swaggertype:"string"`
	Liabilities  *decimal.Decimal   `json:"liabilities,omitempty" swaggertype:"string"`
	NetWorth     decimal.Decimal    `json:"netWorth" swaggertype:"string"`
	Warnings     []currency.Warning `json:"warnings"`
}

// ToDTO converts a summary.
func ToDTO(s Summary) SummaryDTO {
	d := SummaryDTO{
		Currency: s.Currency, TrackingMode: s.TrackingMode, AsOf: s.AsOf, Cash: s.Cash, NetWorth: s.NetWorth,
		FixedAssets: s.FixedAssets, Receivables: s.Receivables, Liabilities: s.Liabilities, Warnings: s.Warnings,
	}
	if s.Investments != nil {
		d.Investments = &InvestmentsDTO{
			Value: s.Investments.Value, Cost: s.Investments.Cost, UnrealizedPL: s.Investments.UnrealizedPL,
			UnrealizedPLPct: s.Investments.UnrealizedPLPct,
		}
	}
	return d
}

// summary godoc
//
//	@Summary		Net-worth summary
//	@Description	netWorth = cash + investments (my share, including investments shared into this space) + fixedAssets + receivables − liabilities, all converted to the space base currency. Anything without a usable FX rate is left out and reported in `warnings`. In CASH_ONLY tracking mode only `cash` and `netWorth` (= cash) are returned.
//	@Tags			net-worth
//	@Security		BearerAuth
//	@Produce		json
//	@Param			spaceId	path		string	true	"Space ID"
//	@Success		200		{object}	SummaryDTO
//	@Failure		404		{object}	httpx.ErrorEnvelope
//	@Router			/spaces/{spaceId}/net-worth/summary [get]
func (h *Handler) summary(w http.ResponseWriter, r *http.Request) {
	s, err := h.svc.Compute(r.Context(), spaces.SpaceIDFromContext(r.Context()), nil)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(ToDTO(s))
}
