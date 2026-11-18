package investments

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/JonathanRainee/hzzt/internal/auth"
	"github.com/JonathanRainee/hzzt/internal/platform/apperror"
	"github.com/JonathanRainee/hzzt/internal/platform/httpx"
	"github.com/JonathanRainee/hzzt/internal/spaces"
)

// Limits are the per-endpoint rate limiters.
type Limits struct {
	RefreshPrice *httpx.RateLimiter // per investment
}

// DefaultLimits: on-demand price refresh 1/min per investment.
func DefaultLimits() Limits {
	return Limits{RefreshPrice: httpx.NewRateLimiter(1, time.Minute)}
}

// Handler exposes the investment endpoints.
type Handler struct {
	svc    *Service
	auth   *auth.Service
	spaces *spaces.Service
	limits Limits
}

// NewHandler creates the HTTP layer.
func NewHandler(svc *Service, authSvc *auth.Service, spacesSvc *spaces.Service, limits Limits) *Handler {
	return &Handler{svc: svc, auth: authSvc, spaces: spacesSvc, limits: limits}
}

// Mount registers the routes on r (the /api/v1 router). Any space member may
// manage investments. Space routes carry the full path because the spaces
// handler owns the /spaces/{spaceId} mount point.
func (h *Handler) Mount(r chi.Router) {
	r.With(h.auth.Middleware).Get("/investment-types", h.types)

	r.Group(func(r chi.Router) {
		r.Use(h.auth.Middleware, h.spaces.Middleware)
		const p = "/spaces/{spaceId}/investments"
		r.Get(p, h.list)
		r.Post(p, h.create)
		r.Get(p+"/{id}", h.get)
		r.Patch(p+"/{id}", h.update)
		r.Delete(p+"/{id}", h.delete)
		r.Post(p+"/{id}/refresh-price", h.refreshPrice)
	})
}

// InvestmentDTO is the public representation of an investment with its valuation.
type InvestmentDTO struct {
	ID                       uuid.UUID        `json:"id"`
	AssetType                string           `json:"assetType" enums:"CRYPTO,ID_STOCK,US_STOCK,MUTUAL_FUND,GLOBAL_ETF"`
	PriceMode                string           `json:"priceMode" enums:"AUTO,MANUAL"`
	Name                     string           `json:"name"`
	Symbol                   *string          `json:"symbol"`
	Icon                     string           `json:"icon"`
	QuoteCurrency            string           `json:"quoteCurrency" example:"IDR"`
	Quantity                 decimal.Decimal  `json:"quantity" swaggertype:"string" example:"200"`
	AvgBuyPrice              decimal.Decimal  `json:"avgBuyPrice" swaggertype:"string"`
	ManualPrice              decimal.Decimal  `json:"manualPrice" swaggertype:"string"`
	Fees                     decimal.Decimal  `json:"fees" swaggertype:"string"`
	ManualCurrentValue       *decimal.Decimal `json:"manualCurrentValue" swaggertype:"string"`
	PurchaseDate             *string          `json:"purchaseDate" example:"2026-01-31"`
	EstimatedAnnualChangePct decimal.Decimal  `json:"estimatedAnnualChangePct" swaggertype:"string"`
	ManualFxRate             *decimal.Decimal `json:"manualFxRate" swaggertype:"string"`
	Details                  any              `json:"details" swaggertype:"object"`
	InstrumentID             *uuid.UUID       `json:"instrumentId"`
	IsShared                 bool             `json:"isShared"`
	Valuation                Valuation        `json:"valuation"`
	RealizedPL               RealizedPLDTO    `json:"realizedPl"`
	CreatedAt                time.Time        `json:"createdAt"`
	UpdatedAt                time.Time        `json:"updatedAt"`
}

// RealizedPLDTO is the total realized profit/loss of an investment's SELL transactions.
type RealizedPLDTO struct {
	Amount   decimal.Decimal  `json:"amount" swaggertype:"string" example:"5990"`
	Currency string           `json:"currency" example:"USD"`
	Base     *decimal.Decimal `json:"base" swaggertype:"string"`
	Sells    int              `json:"sells"`
}

func toDTO(it Item) InvestmentDTO {
	i := it.Investment
	d := InvestmentDTO{
		ID: i.ID, AssetType: i.AssetType, PriceMode: i.PriceMode, Name: i.Name, Symbol: i.Symbol, Icon: i.Icon,
		QuoteCurrency: strings.TrimSpace(i.QuoteCurrency), Quantity: i.Quantity, AvgBuyPrice: i.AvgBuyPrice,
		ManualPrice: i.ManualPrice, Fees: i.Fees, EstimatedAnnualChangePct: i.EstimatedAnnualChangePct,
		Details: it.Details, IsShared: i.IsShared, Valuation: it.Valuation,
		RealizedPL: RealizedPLDTO{
			Amount: it.Realized.Amount, Currency: strings.TrimSpace(i.QuoteCurrency), Base: it.Realized.Base, Sells: it.Realized.Sells,
		},
		CreatedAt: i.CreatedAt.UTC(), UpdatedAt: i.UpdatedAt.UTC(),
	}
	if i.ManualCurrentValue.Valid {
		v := i.ManualCurrentValue.Decimal
		d.ManualCurrentValue = &v
	}
	if i.ManualFxRate.Valid {
		v := i.ManualFxRate.Decimal
		d.ManualFxRate = &v
	}
	if i.PurchaseDate != nil {
		s := i.PurchaseDate.Format("2006-01-02")
		d.PurchaseDate = &s
	}
	if i.InstrumentID.Valid {
		id := i.InstrumentID.UUID
		d.InstrumentID = &id
	}
	return d
}

// ListResponse is the portfolio with a base-currency summary.
type ListResponse struct {
	Items   []InvestmentDTO `json:"items"`
	Summary Summary         `json:"summary"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func investmentID(r *http.Request) (uuid.UUID, error) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		return uuid.Nil, errInvestmentNF
	}
	return id, nil
}

// types godoc
//
//	@Summary		Investment type metadata for dynamic forms
//	@Description	Per asset type: the fixed or free quote currency, whether automatic pricing is supported, how quantity is expressed (ID_STOCK uses lots; quantity = lots × 100) and the fields of `details`.
//	@Tags			investments
//	@Security		BearerAuth
//	@Produce		json
//	@Success		200	{array}	TypeMeta
//	@Router			/investment-types [get]
func (h *Handler) types(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, Metas)
}

// list godoc
//
//	@Summary		List investments with valuations and a portfolio summary
//	@Description	`summary` totals the base-currency values. Investments without a usable FX rate are left out of the totals and reported in `summary.warnings`. `unrealizedPlPct` is a percentage (12.5 = +12.5%).
//	@Tags			investments
//	@Security		BearerAuth
//	@Produce		json
//	@Param			spaceId		path		string	true	"Space ID"
//	@Param			assetType	query		string	false	"CRYPTO, ID_STOCK, US_STOCK, MUTUAL_FUND or GLOBAL_ETF"
//	@Success		200			{object}	ListResponse
//	@Failure		400			{object}	httpx.ErrorEnvelope
//	@Failure		404			{object}	httpx.ErrorEnvelope
//	@Router			/spaces/{spaceId}/investments [get]
func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	at := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("assetType")))
	if at != "" {
		if _, ok := MetaFor(at); !ok {
			httpx.WriteError(w, r, apperror.Validation("Validation failed", map[string]any{
				"assetType": "must be one of: CRYPTO ID_STOCK US_STOCK MUTUAL_FUND GLOBAL_ETF"}))
			return
		}
	}
	res, err := h.svc.List(r.Context(), spaces.SpaceIDFromContext(r.Context()), at)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	out := ListResponse{Items: make([]InvestmentDTO, 0, len(res.Items)), Summary: res.Summary}
	for _, it := range res.Items {
		out.Items = append(out.Items, toDTO(it))
	}
	writeJSON(w, http.StatusOK, out)
}

// CreateRequestDoc documents the create body; the real decoding is keyed on assetType.
type CreateRequestDoc struct {
	AssetType string `json:"assetType" enums:"CRYPTO,ID_STOCK,US_STOCK,MUTUAL_FUND,GLOBAL_ETF"`
	Name      string `json:"name" example:"Bank Central Asia"`
	base
	Quantity *decimal.Decimal `json:"quantity,omitempty" swaggertype:"string" example:"0.5"`
	Details  map[string]any   `json:"details" swaggertype:"object"`
}

// create godoc
//
//	@Summary		Create an investment
//	@Description	The body is validated by `assetType` (see GET /investment-types). ID_STOCK takes `details.lots` and derives quantity = lots × 100. CRYPTO checks `details.stakedQuantity` <= quantity. `priceMode: AUTO` is accepted for CRYPTO, US_STOCK and GLOBAL_ETF and answers 422 AUTO_PRICE_NOT_SUPPORTED otherwise. An AUTO investment is linked to a market instrument: send the `instrumentId` picked from GET /market/crypto/search (CRYPTO) or GET /market/stocks/search (US_STOCK, GLOBAL_ETF), or the symbol is resolved to the highest-ranked coin / the exact ticker (422 INSTRUMENT_NOT_FOUND if none matches). Without a market price the manual price is used. Optional `funding: {walletId, amount?}` pays for the initial position from a wallet: it creates a BUY titled "Initial position {symbol}" (wallet debited quantity × avgBuyPrice + fees, converted to the wallet's currency unless `amount` is given) in the same database transaction.
//	@Tags			investments
//	@Security		BearerAuth
//	@Accept			json
//	@Produce		json
//	@Param			spaceId	path		string				true	"Space ID"
//	@Param			body	body		CreateRequestDoc	true	"Investment"
//	@Success		201		{object}	InvestmentDTO
//	@Failure		400		{object}	httpx.ErrorEnvelope
//	@Failure		404		{object}	httpx.ErrorEnvelope
//	@Failure		422		{object}	httpx.ErrorEnvelope	"AUTO_PRICE_NOT_SUPPORTED"
//	@Router			/spaces/{spaceId}/investments [post]
func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.UserFromContext(r.Context())
	sp, _ := spaces.SpaceFromContext(r.Context())
	in, err := httpx.DecodeDiscriminated(r, "assetType", Variants())
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	spec, err := in.spec(strings.TrimSpace(sp.BaseCurrency))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	it, err := h.svc.Create(r.Context(), sp.ID, user.ID, spec)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, toDTO(it))
}

// get godoc
//
//	@Summary	Get an investment with its valuation
//	@Tags		investments
//	@Security	BearerAuth
//	@Produce	json
//	@Param		spaceId	path		string	true	"Space ID"
//	@Param		id		path		string	true	"Investment ID"
//	@Success	200		{object}	InvestmentDTO
//	@Failure	404		{object}	httpx.ErrorEnvelope
//	@Router		/spaces/{spaceId}/investments/{id} [get]
func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	id, err := investmentID(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	it, err := h.svc.Get(r.Context(), spaces.SpaceIDFromContext(r.Context()), id)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toDTO(it))
}

// update godoc
//
//	@Summary		Update an investment
//	@Description	`assetType` is immutable (it may be sent only if unchanged). `details`, when sent, replaces the whole details object. `symbol`, `manualCurrentValue`, `purchaseDate` and `manualFxRate` accept null to clear.
//	@Tags			investments
//	@Security		BearerAuth
//	@Accept			json
//	@Produce		json
//	@Param			spaceId	path		string		true	"Space ID"
//	@Param			id		path		string		true	"Investment ID"
//	@Param			body	body		PatchInput	true	"Fields to change"
//	@Success		200		{object}	InvestmentDTO
//	@Failure		400		{object}	httpx.ErrorEnvelope
//	@Failure		404		{object}	httpx.ErrorEnvelope
//	@Failure		422		{object}	httpx.ErrorEnvelope	"AUTO_PRICE_NOT_SUPPORTED"
//	@Router			/spaces/{spaceId}/investments/{id} [patch]
func (h *Handler) update(w http.ResponseWriter, r *http.Request) {
	sp, _ := spaces.SpaceFromContext(r.Context())
	id, err := investmentID(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	p, err := httpx.DecodeAndValidate[PatchInput](r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	it, err := h.svc.Update(r.Context(), sp, id, p)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toDTO(it))
}

// delete godoc
//
//	@Summary	Delete an investment (soft delete)
//	@Tags		investments
//	@Security	BearerAuth
//	@Param		spaceId	path	string	true	"Space ID"
//	@Param		id		path	string	true	"Investment ID"
//	@Success	204
//	@Failure	404	{object}	httpx.ErrorEnvelope
//	@Router		/spaces/{spaceId}/investments/{id} [delete]
func (h *Handler) delete(w http.ResponseWriter, r *http.Request) {
	id, err := investmentID(r)
	if err == nil {
		err = h.svc.Delete(r.Context(), spaces.SpaceIDFromContext(r.Context()), id)
	}
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// refreshPrice godoc
//
//	@Summary		Refresh the market price of an AUTO investment now
//	@Description	Rate limited to 1 per minute per investment. On a provider failure the last known price is kept (it turns stale).
//	@Tags			investments
//	@Security		BearerAuth
//	@Produce		json
//	@Param			spaceId	path		string	true	"Space ID"
//	@Param			id		path		string	true	"Investment ID"
//	@Success		200		{object}	InvestmentDTO
//	@Failure		404		{object}	httpx.ErrorEnvelope
//	@Failure		422		{object}	httpx.ErrorEnvelope	"AUTO_PRICE_NOT_SUPPORTED / PRICE_MODE_NOT_AUTO / INSTRUMENT_NOT_LINKED"
//	@Failure		429		{object}	httpx.ErrorEnvelope
//	@Failure		502		{object}	httpx.ErrorEnvelope	"UPSTREAM_PROVIDER_ERROR"
//	@Router			/spaces/{spaceId}/investments/{id}/refresh-price [post]
func (h *Handler) refreshPrice(w http.ResponseWriter, r *http.Request) {
	id, err := investmentID(r)
	if err == nil {
		// keyed by space too, so a foreign id in your own space cannot use up someone else's budget
		err = h.limits.RefreshPrice.Check(w, spaces.SpaceIDFromContext(r.Context()).String()+"/"+id.String())
	}
	var it Item
	if err == nil {
		it, err = h.svc.RefreshPrice(r.Context(), spaces.SpaceIDFromContext(r.Context()), id)
	}
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toDTO(it))
}
