package marketdata

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/JonathanRainee/hzzt/internal/auth"
	"github.com/JonathanRainee/hzzt/internal/platform/apperror"
	"github.com/JonathanRainee/hzzt/internal/platform/httpx"
)

// Limits are the per-endpoint rate limiters.
type Limits struct {
	StockSearch *httpx.RateLimiter // per user
}

// DefaultLimits: stock search 20/min per user (each search may call the provider).
func DefaultLimits() Limits {
	return Limits{StockSearch: httpx.NewRateLimiter(20, time.Minute)}
}

// Handler exposes the market data endpoints.
type Handler struct {
	svc    *Service
	auth   *auth.Service
	limits Limits
}

// NewHandler creates the HTTP layer.
func NewHandler(svc *Service, authSvc *auth.Service, limits Limits) *Handler {
	return &Handler{svc: svc, auth: authSvc, limits: limits}
}

// Mount registers the routes on r (the /api/v1 router).
func (h *Handler) Mount(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(h.auth.Middleware)
		r.Get("/market/crypto/search", h.searchCrypto)
		r.Get("/market/stocks/search", h.searchStocks)
	})
}

// InstrumentDTO is a searchable coin.
type InstrumentDTO struct {
	InstrumentID uuid.UUID `json:"instrumentId"`
	Symbol       string    `json:"symbol" example:"BTC"`
	Name         string    `json:"name" example:"Bitcoin"`
	Rank         *int      `json:"rank" example:"1"`
}

const (
	defaultSearchLimit = 20
	maxSearchLimit     = 50
)

// searchCrypto godoc
//
//	@Summary		Search coins by symbol or name
//	@Description	Served from the local coin list (no provider call). Exact symbol matches first, then symbol prefixes, then name matches, each ordered by market-cap rank. Send the chosen `instrumentId` when creating an AUTO crypto investment.
//	@Tags			market
//	@Security		BearerAuth
//	@Produce		json
//	@Param			q		query		string	true	"Search text"
//	@Param			limit	query		int		false	"Max results (default 20, max 50)"
//	@Success		200		{array}		InstrumentDTO
//	@Failure		400		{object}	httpx.ErrorEnvelope
//	@Router			/market/crypto/search [get]
func (h *Handler) searchCrypto(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" || len([]rune(q)) > 60 {
		httpx.WriteError(w, r, apperror.Validation("Validation failed", map[string]any{"q": "is required (1 to 60 characters)"}))
		return
	}
	limit, err := searchLimit(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	res, err := h.svc.SearchCrypto(r.Context(), q, limit)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeInstruments(w, res)
}

func writeInstruments(w http.ResponseWriter, res []Instrument) {
	out := make([]InstrumentDTO, 0, len(res))
	for _, i := range res {
		out = append(out, InstrumentDTO{InstrumentID: i.ID, Symbol: i.Symbol, Name: i.Name, Rank: i.Rank})
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(out)
}

// searchLimit parses ?limit= (default 20, max 50).
func searchLimit(r *http.Request) (int, error) {
	v := r.URL.Query().Get("limit")
	if v == "" {
		return defaultSearchLimit, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 || n > maxSearchLimit {
		return 0, apperror.Validation("Validation failed", map[string]any{"limit": "must be between 1 and 50"})
	}
	return n, nil
}

// searchStocks godoc
//
//	@Summary		Search US stocks and ETFs by ticker or name
//	@Description	Checks the local instrument list first and asks Massive too when there are fewer than 5 local hits (throttled; results are cached as instruments). If the provider is throttled or unavailable the local hits are returned. Send the chosen `instrumentId` when creating an AUTO US_STOCK (kind=STOCK) or GLOBAL_ETF (kind=ETF) investment. Limited to 20 requests per minute per user.
//	@Tags			market
//	@Security		BearerAuth
//	@Produce		json
//	@Param			q		query		string	true	"Search text"
//	@Param			kind	query		string	false	"STOCK (default) or ETF"
//	@Param			limit	query		int		false	"Max results (default 20, max 50)"
//	@Success		200		{array}		InstrumentDTO
//	@Failure		400		{object}	httpx.ErrorEnvelope
//	@Failure		429		{object}	httpx.ErrorEnvelope
//	@Router			/market/stocks/search [get]
func (h *Handler) searchStocks(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.UserFromContext(r.Context())
	if err := h.limits.StockSearch.Check(w, user.ID.String()); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" || len([]rune(q)) > 60 {
		httpx.WriteError(w, r, apperror.Validation("Validation failed", map[string]any{"q": "is required (1 to 60 characters)"}))
		return
	}
	kind := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("kind")))
	if kind == "" {
		kind = KindStock
	}
	if kind != KindStock && kind != KindETF {
		httpx.WriteError(w, r, apperror.Validation("Validation failed", map[string]any{"kind": "must be STOCK or ETF"}))
		return
	}
	limit, err := searchLimit(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	res, err := h.svc.SearchStocks(r.Context(), q, kind, limit)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeInstruments(w, res)
}
