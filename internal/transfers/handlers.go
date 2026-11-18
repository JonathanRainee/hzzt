package transfers

import (
	"bytes"
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
	"github.com/JonathanRainee/hzzt/internal/transactions"
)

// Handler exposes the transfer endpoints.
type Handler struct {
	svc    *Service
	auth   *auth.Service
	spaces *spaces.Service
}

// NewHandler creates the HTTP layer.
func NewHandler(svc *Service, authSvc *auth.Service, spacesSvc *spaces.Service) *Handler {
	return &Handler{svc: svc, auth: authSvc, spaces: spacesSvc}
}

// Mount registers the routes on r (the /api/v1 router). Any space member may
// transfer between wallets. Space routes carry the full path because the
// spaces handler owns the /spaces/{spaceId} mount point.
func (h *Handler) Mount(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(h.auth.Middleware, h.spaces.Middleware)
		const p = "/spaces/{spaceId}/transfers"
		r.Post(p, h.create)
		r.Patch(p+"/{id}", h.update)
		r.Delete(p+"/{id}", h.delete)
	})
}

// --- request contract --------------------------------------------------------

// Endpoint types of a transfer side.
const (
	endpointWallet     = "WALLET"
	endpointInvestment = "INVESTMENT"
)

// EndpointDoc documents one side of a transfer: `{type:"WALLET", walletId}`.
// `{type:"INVESTMENT"}` is reserved for P13 and answers 422 NOT_SUPPORTED_YET.
type EndpointDoc struct {
	Type         string     `json:"type" enums:"WALLET,INVESTMENT"`
	WalletID     *uuid.UUID `json:"walletId,omitempty"`
	InvestmentID *uuid.UUID `json:"investmentId,omitempty"`
}

type walletEndpoint struct {
	Type     string    `json:"type"`
	WalletID uuid.UUID `json:"walletId" validate:"required"`
}

type investmentEndpoint struct {
	Type         string     `json:"type"`
	InvestmentID *uuid.UUID `json:"investmentId"`
}

// endpoint is the decoded side of a transfer.
type endpoint interface{ walletID() (uuid.UUID, error) }

func (e *walletEndpoint) walletID() (uuid.UUID, error) { return e.WalletID, nil }
func (e *investmentEndpoint) walletID() (uuid.UUID, error) {
	return uuid.Nil, apperror.Unprocessable(CodeNotSupportedYet, "Investments as a transfer source or destination are not supported yet")
}

// decodeEndpoint decodes `from`/`to` with the discriminated-decode helper.
func decodeEndpoint(field string, raw json.RawMessage) (uuid.UUID, error) {
	if isAbsent(raw) {
		return uuid.Nil, apperror.Validation("Validation failed", map[string]any{field: "is required"})
	}
	e, err := httpx.DecodeDiscriminatedBytes(raw, "type", map[string]func() endpoint{
		endpointWallet:     func() endpoint { return &walletEndpoint{} },
		endpointInvestment: func() endpoint { return &investmentEndpoint{} },
	})
	if err != nil {
		if ae, ok := apperror.As(err); ok && ae.Code == apperror.CodeValidationFailed {
			f := map[string]any{}
			if len(ae.Details) == 0 {
				f[field] = ae.Message
			}
			for k, v := range ae.Details {
				f[field+"."+k] = v
			}
			return uuid.Nil, apperror.Validation("Validation failed", f)
		}
		return uuid.Nil, err
	}
	return e.walletID()
}

type createRequest struct {
	From       json.RawMessage  `json:"from" swaggertype:"object"`
	To         json.RawMessage  `json:"to" swaggertype:"object"`
	Amount     *decimal.Decimal `json:"amount" validate:"required" swaggertype:"string" example:"100.00"`
	ToAmount   *decimal.Decimal `json:"toAmount" swaggertype:"string" example:"1600000"`
	OccurredAt *time.Time       `json:"occurredAt"`
	Note       *string          `json:"note" validate:"omitempty,max=1000"`
}

type optional[T any] struct {
	Set   bool
	Value *T
}

func (o *optional[T]) UnmarshalJSON(b []byte) error {
	o.Set = true
	if bytes.Equal(bytes.TrimSpace(b), []byte("null")) {
		return nil
	}
	var v T
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	o.Value = &v
	return nil
}

type updateRequest struct {
	From       json.RawMessage  `json:"from" swaggertype:"object"`
	To         json.RawMessage  `json:"to" swaggertype:"object"`
	Amount     *decimal.Decimal `json:"amount" swaggertype:"string"`
	ToAmount   *decimal.Decimal `json:"toAmount" swaggertype:"string"`
	OccurredAt *time.Time       `json:"occurredAt"`
	Note       optional[string] `json:"note" swaggertype:"string"`
}

var maxMoney = decimal.New(1, 20)

func checkAmount(f map[string]any, field string, d *decimal.Decimal) {
	switch {
	case d == nil:
	case !d.IsPositive():
		f[field] = "must be greater than 0"
	case !d.Round(8).Equal(*d):
		f[field] = "must have at most 8 decimal places"
	case d.GreaterThanOrEqual(maxMoney):
		f[field] = "is too large"
	}
}

func isAbsent(raw json.RawMessage) bool {
	t := strings.TrimSpace(string(raw))
	return t == "" || t == "null"
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func transferID(r *http.Request) (uuid.UUID, error) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		return uuid.Nil, errTransferNF
	}
	return id, nil
}

// --- handlers ----------------------------------------------------------------

// create godoc
//
//	@Summary		Transfer between wallets
//	@Description	`from` and `to` are `{type:"WALLET", walletId}`. `toAmount` is required when the wallets use different currencies (the stored rate is toAmount / amount); for the same currency it must equal `amount` or be omitted. Negative balances are allowed. `type:"INVESTMENT"` answers 422 NOT_SUPPORTED_YET until investments can be transfer endpoints. Transfers never count as income or expense; they show in the `transfer` column of the daily summary and in the history of both wallets.
//	@Tags			transfers
//	@Security		BearerAuth
//	@Accept			json
//	@Produce		json
//	@Param			spaceId	path		string			true	"Space ID"
//	@Param			body	body		createRequest	true	"Transfer"
//	@Success		201		{object}	transactions.TransactionDTO
//	@Failure		400		{object}	httpx.ErrorEnvelope
//	@Failure		404		{object}	httpx.ErrorEnvelope	"space or wallet not found"
//	@Failure		422		{object}	httpx.ErrorEnvelope	"TRANSFER_SAME_WALLET / TRANSFER_AMOUNT_REQUIRED / NOT_SUPPORTED_YET"
//	@Router			/spaces/{spaceId}/transfers [post]
func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.UserFromContext(r.Context())
	req, err := httpx.DecodeAndValidate[createRequest](r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	in := Input{Amount: decimal.Zero, ToAmount: req.ToAmount, OccurredAt: req.OccurredAt, Note: req.Note}
	f := map[string]any{}
	checkAmount(f, "amount", req.Amount)
	checkAmount(f, "toAmount", req.ToAmount)
	var notSupported error
	for field, raw := range map[string]json.RawMessage{"from": req.From, "to": req.To} {
		id, err := decodeEndpoint(field, raw)
		if err != nil {
			if ae, ok := apperror.As(err); ok && ae.Code == CodeNotSupportedYet {
				notSupported = err
				continue
			}
			ae, _ := apperror.As(err)
			for k, v := range ae.Details {
				f[k] = v
			}
			continue
		}
		if field == "from" {
			in.FromWalletID = id
		} else {
			in.ToWalletID = id
		}
	}
	if len(f) > 0 {
		httpx.WriteError(w, r, apperror.Validation("Validation failed", f))
		return
	}
	if notSupported != nil {
		httpx.WriteError(w, r, notSupported)
		return
	}
	in.Amount = *req.Amount
	d, err := h.svc.Create(r.Context(), spaces.SpaceIDFromContext(r.Context()), user.ID, in)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, transactions.ToDTO(d))
}

// update godoc
//
//	@Summary		Edit a transfer
//	@Description	Any field may be sent; the old effect is reversed and the new one applied atomically. Changing the amount or either wallet of a cross-currency transfer requires `toAmount`.
//	@Tags			transfers
//	@Security		BearerAuth
//	@Accept			json
//	@Produce		json
//	@Param			spaceId	path		string			true	"Space ID"
//	@Param			id		path		string			true	"Transfer ID"
//	@Param			body	body		updateRequest	true	"Fields to change"
//	@Success		200		{object}	transactions.TransactionDTO
//	@Failure		400		{object}	httpx.ErrorEnvelope
//	@Failure		404		{object}	httpx.ErrorEnvelope
//	@Failure		422		{object}	httpx.ErrorEnvelope	"TRANSFER_SAME_WALLET / TRANSFER_AMOUNT_REQUIRED / NOT_SUPPORTED_YET"
//	@Router			/spaces/{spaceId}/transfers/{id} [patch]
func (h *Handler) update(w http.ResponseWriter, r *http.Request) {
	id, err := transferID(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	req, err := httpx.DecodeAndValidate[updateRequest](r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	p := Patch{Amount: req.Amount, ToAmount: req.ToAmount, OccurredAt: req.OccurredAt, Note: Optional[string](req.Note)}
	f := map[string]any{}
	checkAmount(f, "amount", req.Amount)
	checkAmount(f, "toAmount", req.ToAmount)
	if req.Note.Value != nil && len(*req.Note.Value) > 1000 {
		f["note"] = "must be at most 1000 characters"
	}
	var notSupported error
	for field, raw := range map[string]json.RawMessage{"from": req.From, "to": req.To} {
		if isAbsent(raw) {
			continue
		}
		wid, err := decodeEndpoint(field, raw)
		if err != nil {
			if ae, ok := apperror.As(err); ok && ae.Code == CodeNotSupportedYet {
				notSupported = err
				continue
			}
			ae, _ := apperror.As(err)
			for k, v := range ae.Details {
				f[k] = v
			}
			continue
		}
		if field == "from" {
			p.FromWalletID = &wid
		} else {
			p.ToWalletID = &wid
		}
	}
	if len(f) > 0 {
		httpx.WriteError(w, r, apperror.Validation("Validation failed", f))
		return
	}
	if notSupported != nil {
		httpx.WriteError(w, r, notSupported)
		return
	}
	d, err := h.svc.Update(r.Context(), spaces.SpaceIDFromContext(r.Context()), id, p)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, transactions.ToDTO(d))
}

// delete godoc
//
//	@Summary	Delete a transfer (both balances are restored)
//	@Tags		transfers
//	@Security	BearerAuth
//	@Param		spaceId	path	string	true	"Space ID"
//	@Param		id		path	string	true	"Transfer ID"
//	@Success	204
//	@Failure	404	{object}	httpx.ErrorEnvelope
//	@Router		/spaces/{spaceId}/transfers/{id} [delete]
func (h *Handler) delete(w http.ResponseWriter, r *http.Request) {
	id, err := transferID(r)
	if err == nil {
		err = h.svc.Delete(r.Context(), spaces.SpaceIDFromContext(r.Context()), id)
	}
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
