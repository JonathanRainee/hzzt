package currency

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/JonathanRainee/hzzt/internal/auth"
	"github.com/JonathanRainee/hzzt/internal/platform/apperror"
	"github.com/JonathanRainee/hzzt/internal/platform/httpx"
	"github.com/JonathanRainee/hzzt/internal/platform/money"
	"github.com/JonathanRainee/hzzt/internal/spaces"
)

// Handler exposes the currency and FX endpoints.
type Handler struct {
	svc    *Service
	auth   *auth.Service
	spaces *spaces.Service
}

// NewHandler creates the HTTP layer.
func NewHandler(svc *Service, authSvc *auth.Service, spacesSvc *spaces.Service) *Handler {
	return &Handler{svc: svc, auth: authSvc, spaces: spacesSvc}
}

// Mount registers the routes on r (the /api/v1 router).
func (h *Handler) Mount(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(h.auth.Middleware)
		r.Get("/currencies", h.currencies)
		r.Get("/fx/latest", h.latest)
	})
	// Not a second r.Route("/spaces/{spaceId}") — the spaces handler owns that
	// mount point — so these routes carry the full path and the same middleware.
	r.Group(func(r chi.Router) {
		r.Use(h.auth.Middleware, h.spaces.Middleware)
		const p = "/spaces/{spaceId}/fx/manual-rates"
		r.Get(p, h.listManual)
		r.With(spaces.RequireRole(spaces.RoleOwner)).Put(p+"/{base}/{quote}", h.putManual)
		r.With(spaces.RequireRole(spaces.RoleOwner)).Delete(p+"/{base}/{quote}", h.deleteManual)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// LatestResponse is the live rate table re-based to `base`.
type LatestResponse struct {
	Base   string                     `json:"base" example:"IDR"`
	AsOf   *string                    `json:"asOf" example:"2026-11-18"`
	Source string                     `json:"source,omitempty" example:"freecurrencyapi"`
	Rates  map[string]decimal.Decimal `json:"rates" swaggertype:"object,string"`
}

// ManualRateDTO is one manual rate of a space.
type ManualRateDTO struct {
	Base      string          `json:"base" example:"USD"`
	Quote     string          `json:"quote" example:"IDR"`
	Rate      decimal.Decimal `json:"rate" swaggertype:"string" example:"16000"`
	UpdatedAt time.Time       `json:"updatedAt"`
}

type putManualRequest struct {
	Rate *decimal.Decimal `json:"rate" validate:"required" swaggertype:"string" example:"16000"`
}

func pathCurrency(r *http.Request, name string) (string, error) {
	c := strings.ToUpper(strings.TrimSpace(chi.URLParam(r, name)))
	if !money.IsSupportedCurrency(c) {
		return "", apperror.Validation("Validation failed", map[string]any{name: "must be a supported ISO 4217 currency code"})
	}
	return c, nil
}

// currencies godoc
//
//	@Summary	List supported currencies
//	@Tags		currency
//	@Security	BearerAuth
//	@Produce	json
//	@Success	200	{array}		Currency
//	@Failure	401	{object}	httpx.ErrorEnvelope
//	@Router		/currencies [get]
func (h *Handler) currencies(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, Catalog())
}

// latest godoc
//
//	@Summary		Latest FX rates re-based to a currency
//	@Description	rates[Q] is how many Q one unit of `base` buys. `asOf` is null and `rates` empty until the first provider refresh has completed.
//	@Tags			currency
//	@Security		BearerAuth
//	@Produce		json
//	@Param			base	query		string	false	"Base currency (default USD)"
//	@Success		200		{object}	LatestResponse
//	@Failure		400		{object}	httpx.ErrorEnvelope
//	@Router			/fx/latest [get]
func (h *Handler) latest(w http.ResponseWriter, r *http.Request) {
	base := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("base")))
	if base == "" {
		base = "USD"
	}
	if !money.IsSupportedCurrency(base) {
		httpx.WriteError(w, r, apperror.Validation("Validation failed", map[string]any{"base": "must be a supported ISO 4217 currency code"}))
		return
	}
	l, err := h.svc.LatestRebased(r.Context(), base)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	resp := LatestResponse{Base: l.Base, Source: l.Source, Rates: l.Rates}
	if l.AsOf != nil {
		d := l.AsOf.Format("2006-01-02")
		resp.AsOf = &d
	}
	writeJSON(w, http.StatusOK, resp)
}

// listManual godoc
//
//	@Summary		List the space's manual FX rates
//	@Description	Used instead of live rates when the space has autoFxConversion off.
//	@Tags			currency
//	@Security		BearerAuth
//	@Produce		json
//	@Param			spaceId	path		string	true	"Space ID"
//	@Success		200		{array}		ManualRateDTO
//	@Failure		404		{object}	httpx.ErrorEnvelope
//	@Router			/spaces/{spaceId}/fx/manual-rates [get]
func (h *Handler) listManual(w http.ResponseWriter, r *http.Request) {
	rows, err := h.svc.ManualRates(r.Context(), spaces.SpaceIDFromContext(r.Context()))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	out := make([]ManualRateDTO, 0, len(rows))
	for _, m := range rows {
		out = append(out, ManualRateDTO{Base: m.Base, Quote: m.Quote, Rate: m.Rate, UpdatedAt: m.UpdatedAt.UTC()})
	}
	writeJSON(w, http.StatusOK, out)
}

// putManual godoc
//
//	@Summary	Set a manual FX rate (OWNER): 1 base = rate quote
//	@Tags		currency
//	@Security	BearerAuth
//	@Accept		json
//	@Produce	json
//	@Param		spaceId	path		string				true	"Space ID"
//	@Param		base	path		string				true	"Base currency"
//	@Param		quote	path		string				true	"Quote currency"
//	@Param		body	body		putManualRequest	true	"Rate"
//	@Success	200		{object}	ManualRateDTO
//	@Failure	400		{object}	httpx.ErrorEnvelope
//	@Failure	403		{object}	httpx.ErrorEnvelope
//	@Failure	404		{object}	httpx.ErrorEnvelope
//	@Router		/spaces/{spaceId}/fx/manual-rates/{base}/{quote} [put]
func (h *Handler) putManual(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.UserFromContext(r.Context())
	base, err := pathCurrency(r, "base")
	var quote string
	if err == nil {
		quote, err = pathCurrency(r, "quote")
	}
	var req putManualRequest
	if err == nil {
		req, err = httpx.DecodeAndValidate[putManualRequest](r)
	}
	if err == nil && base == quote {
		err = apperror.Validation("Validation failed", map[string]any{"quote": "must differ from base"})
	}
	var rate decimal.Decimal
	if err == nil {
		rate = req.Rate.Round(12)
		if !rate.IsPositive() || rate.GreaterThanOrEqual(decimal.New(1, 16)) {
			err = apperror.Validation("Validation failed", map[string]any{"rate": "must be greater than 0 and less than 10^16"})
		}
	}
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	m, err := h.svc.SetManualRate(r.Context(), spaces.SpaceIDFromContext(r.Context()), user.ID, base, quote, rate)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, ManualRateDTO{Base: m.Base, Quote: m.Quote, Rate: m.Rate, UpdatedAt: m.UpdatedAt.UTC()})
}

// deleteManual godoc
//
//	@Summary	Delete a manual FX rate (OWNER)
//	@Tags		currency
//	@Security	BearerAuth
//	@Param		spaceId	path	string	true	"Space ID"
//	@Param		base	path	string	true	"Base currency"
//	@Param		quote	path	string	true	"Quote currency"
//	@Success	204
//	@Failure	403	{object}	httpx.ErrorEnvelope
//	@Failure	404	{object}	httpx.ErrorEnvelope
//	@Router		/spaces/{spaceId}/fx/manual-rates/{base}/{quote} [delete]
func (h *Handler) deleteManual(w http.ResponseWriter, r *http.Request) {
	base, err := pathCurrency(r, "base")
	var quote string
	if err == nil {
		quote, err = pathCurrency(r, "quote")
	}
	if err == nil {
		err = h.svc.DeleteManualRate(r.Context(), spaces.SpaceIDFromContext(r.Context()), base, quote)
	}
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
