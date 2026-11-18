package marketdata

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/JonathanRainee/hzzt/internal/auth"
	"github.com/JonathanRainee/hzzt/internal/platform/apperror"
	"github.com/JonathanRainee/hzzt/internal/platform/httpx"
)

// Handler exposes the market data endpoints.
type Handler struct {
	svc  *Service
	auth *auth.Service
}

// NewHandler creates the HTTP layer.
func NewHandler(svc *Service, authSvc *auth.Service) *Handler {
	return &Handler{svc: svc, auth: authSvc}
}

// Mount registers the routes on r (the /api/v1 router).
func (h *Handler) Mount(r chi.Router) {
	r.With(h.auth.Middleware).Get("/market/crypto/search", h.searchCrypto)
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
	limit := defaultSearchLimit
	if v := r.URL.Query().Get("limit"); v != "" {
		n := 0
		for _, c := range v {
			if c < '0' || c > '9' {
				n = -1
				break
			}
			n = n*10 + int(c-'0')
			if n > maxSearchLimit {
				break
			}
		}
		if n < 1 || n > maxSearchLimit {
			httpx.WriteError(w, r, apperror.Validation("Validation failed", map[string]any{"limit": "must be between 1 and 50"}))
			return
		}
		limit = n
	}
	res, err := h.svc.SearchCrypto(r.Context(), q, limit)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	out := make([]InstrumentDTO, 0, len(res))
	for _, i := range res {
		out = append(out, InstrumentDTO{InstrumentID: i.ID, Symbol: i.Symbol, Name: i.Name, Rank: i.Rank})
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(out)
}
