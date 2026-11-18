package goals

import (
	"bytes"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/JonathanRainee/hzzt/internal/auth"
	"github.com/JonathanRainee/hzzt/internal/currency"
	"github.com/JonathanRainee/hzzt/internal/platform/apperror"
	"github.com/JonathanRainee/hzzt/internal/platform/httpx"
	"github.com/JonathanRainee/hzzt/internal/platform/money"
	"github.com/JonathanRainee/hzzt/internal/spaces"
)

// Handler exposes the goal endpoints.
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
// manage goals. Space routes carry the full path because the spaces handler
// owns the /spaces/{spaceId} mount point.
func (h *Handler) Mount(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(h.auth.Middleware, h.spaces.Middleware)
		const p = "/spaces/{spaceId}/goals"
		r.Get(p, h.list)
		r.Post(p, h.create)
		r.Get(p+"/{id}", h.get)
		r.Patch(p+"/{id}", h.update)
		r.Delete(p+"/{id}", h.delete)
		r.Put(p+"/{id}/allocations/{walletId}", h.setAllocation)
	})
}

// --- DTOs ------------------------------------------------------------------

// AllocationDTO is the part of a wallet reserved for a goal.
type AllocationDTO struct {
	WalletID       uuid.UUID        `json:"walletId"`
	WalletName     string           `json:"walletName"`
	WalletIcon     string           `json:"walletIcon"`
	WalletCurrency string           `json:"walletCurrency" example:"IDR"`
	Amount         decimal.Decimal  `json:"amount" swaggertype:"string" example:"100"` // in the wallet's currency
	Converted      *decimal.Decimal `json:"converted" swaggertype:"string"`            // in the goal's currency; null without an FX rate
}

// GoalDTO is the public representation of a goal.
type GoalDTO struct {
	ID           uuid.UUID       `json:"id"`
	Name         string          `json:"name"`
	Icon         string          `json:"icon"`
	Color        string          `json:"color" example:"#22C55E"`
	TargetAmount decimal.Decimal `json:"targetAmount" swaggertype:"string" example:"1000"`
	Currency     string          `json:"currency" example:"USD"`
	TargetDate   *string         `json:"targetDate" example:"2026-12-31"`
	// Progress is the sum of the reservations converted to the goal's currency.
	Progress decimal.Decimal `json:"progress" swaggertype:"string"`
	// ProgressPct is a percentage (50.00 = half way); over 100 means exceeded.
	ProgressPct decimal.Decimal `json:"progressPct" swaggertype:"string"`
	Remaining   decimal.Decimal `json:"remaining" swaggertype:"string"`
	AchievedAt  *time.Time      `json:"achievedAt"`
	// RequiredMonthly is remaining / months until the target date; null without a date, after it, or once achieved.
	RequiredMonthly *decimal.Decimal   `json:"requiredMonthly" swaggertype:"string"`
	Allocations     []AllocationDTO    `json:"allocations"`
	Warnings        []currency.Warning `json:"warnings"`
	CreatedAt       time.Time          `json:"createdAt"`
}

func toDTO(g Goal) GoalDTO {
	d := GoalDTO{
		ID: g.ID, Name: g.Name, Icon: g.Icon, Color: g.Color, TargetAmount: g.TargetAmount, Currency: strings.TrimSpace(g.Currency),
		Progress: g.Progress, ProgressPct: g.ProgressPct, Remaining: g.Remaining, RequiredMonthly: g.RequiredMonthly,
		Warnings: g.Warnings, CreatedAt: g.CreatedAt.UTC(), Allocations: make([]AllocationDTO, 0, len(g.Allocations)),
	}
	if g.TargetDate != nil {
		s := g.TargetDate.Format("2006-01-02")
		d.TargetDate = &s
	}
	if g.AchievedAt != nil {
		t := g.AchievedAt.UTC()
		d.AchievedAt = &t
	}
	for _, a := range g.Allocations {
		d.Allocations = append(d.Allocations, AllocationDTO(a))
	}
	return d
}

type reserveRequest struct {
	WalletID uuid.UUID        `json:"walletId" validate:"required"`
	Amount   *decimal.Decimal `json:"amount" validate:"required" swaggertype:"string" example:"100"`
}

type createRequest struct {
	Name           string           `json:"name" validate:"required,min=1,max=120"`
	Icon           string           `json:"icon" validate:"required,max=64"`
	Color          string           `json:"color" validate:"required,max=16"`
	TargetAmount   *decimal.Decimal `json:"targetAmount" validate:"required" swaggertype:"string" example:"1000"`
	Currency       *string          `json:"currency" validate:"omitempty,max=8"`
	TargetDate     *string          `json:"targetDate" validate:"omitempty,max=10" example:"2026-12-31"`
	InitialReserve *reserveRequest  `json:"initialReserve"`
}

type nullableString struct {
	Set   bool
	Value *string
}

func (n *nullableString) UnmarshalJSON(b []byte) error {
	n.Set = true
	if bytes.Equal(bytes.TrimSpace(b), []byte("null")) {
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	n.Value = &s
	return nil
}

type updateRequest struct {
	Name         *string          `json:"name" validate:"omitempty,min=1,max=120"`
	Icon         *string          `json:"icon" validate:"omitempty,max=64"`
	Color        *string          `json:"color" validate:"omitempty,max=16"`
	TargetAmount *decimal.Decimal `json:"targetAmount" swaggertype:"string"`
	Currency     *string          `json:"currency" validate:"omitempty,max=8"`
	TargetDate   nullableString   `json:"targetDate" swaggertype:"string"`
}

type allocationRequest struct {
	Amount *decimal.Decimal `json:"amount" validate:"required" swaggertype:"string" example:"100"`
}

var hexColor = regexp.MustCompile(`^#[0-9A-Fa-f]{6}$`)

var maxAmount = decimal.New(1, 20)

func fieldsErr(f map[string]any) error {
	if len(f) == 0 {
		return nil
	}
	return apperror.Validation("Validation failed", f)
}

// checkAmount validates a decimal with at most 8 places; zeroOK allows 0 (an allocation of 0 removes it).
func checkAmount(f map[string]any, field string, d *decimal.Decimal, zeroOK bool) {
	switch {
	case d == nil:
	case d.IsNegative():
		f[field] = "must not be negative"
	case !zeroOK && !d.IsPositive():
		f[field] = "must be greater than 0"
	case !d.Round(8).Equal(*d):
		f[field] = "must have at most 8 decimal places"
	case d.GreaterThanOrEqual(maxAmount):
		f[field] = "is too large"
	}
}

func cleanName(f map[string]any, n *string) *string {
	if n == nil {
		return nil
	}
	v := strings.TrimSpace(*n)
	if v == "" || utf8.RuneCountInString(v) > 40 {
		f["name"] = "must be 1 to 40 characters"
	}
	return &v
}

func cleanIcon(f map[string]any, i *string) *string {
	if i == nil {
		return nil
	}
	v := strings.TrimSpace(*i)
	if v == "" || utf8.RuneCountInString(v) > 16 {
		f["icon"] = "must be an emoji (1 to 16 characters)"
	}
	return &v
}

func cleanColor(f map[string]any, c *string) *string {
	if c == nil {
		return nil
	}
	if !hexColor.MatchString(*c) {
		f["color"] = "must be a hex color like #22C55E"
	}
	up := strings.ToUpper(*c)
	return &up
}

func cleanCurrency(f map[string]any, c *string) *string {
	if c == nil {
		return nil
	}
	v := strings.ToUpper(strings.TrimSpace(*c))
	if !money.IsSupportedCurrency(v) {
		f["currency"] = "must be a supported ISO 4217 currency code"
	}
	return &v
}

func parseDate(f map[string]any, s *string) *time.Time {
	if s == nil {
		return nil
	}
	t, err := time.Parse("2006-01-02", strings.TrimSpace(*s))
	if err != nil {
		f["targetDate"] = "must be a date like 2026-12-31"
		return nil
	}
	return &t
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func goalID(r *http.Request) (uuid.UUID, error) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		return uuid.Nil, errGoalNF
	}
	return id, nil
}

// userTZ is the requesting user's timezone (UTC if unusable); "today" for a goal is the user's today.
func userTZ(r *http.Request) *time.Location {
	user, _ := auth.UserFromContext(r.Context())
	if l, err := time.LoadLocation(user.Timezone); err == nil {
		return l
	}
	return time.UTC
}

// --- handlers --------------------------------------------------------------

// ListResponse lists goals.
type ListResponse struct {
	Items []GoalDTO `json:"items"`
}

// list godoc
//
//	@Summary		List savings goals with progress
//	@Description	Progress is the sum of the wallet reservations converted to the goal's currency, computed on read. `achievedAt` is set when progress first reaches the target and cleared if it drops below again.
//	@Tags			goals
//	@Security		BearerAuth
//	@Produce		json
//	@Param			spaceId	path		string	true	"Space ID"
//	@Success		200		{object}	ListResponse
//	@Failure		404		{object}	httpx.ErrorEnvelope
//	@Router			/spaces/{spaceId}/goals [get]
func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	gs, err := h.svc.List(r.Context(), spaces.SpaceIDFromContext(r.Context()), userTZ(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	out := ListResponse{Items: make([]GoalDTO, 0, len(gs))}
	for _, g := range gs {
		out.Items = append(out.Items, toDTO(g))
	}
	writeJSON(w, http.StatusOK, out)
}

// create godoc
//
//	@Summary		Create a goal
//	@Description	`initialReserve {walletId, amount}` optionally reserves part of a wallet right away (same rules as the allocations endpoint). Nothing is moved: a reservation only earmarks part of a balance.
//	@Tags			goals
//	@Security		BearerAuth
//	@Accept			json
//	@Produce		json
//	@Param			spaceId	path		string			true	"Space ID"
//	@Param			body	body		createRequest	true	"Goal"
//	@Success		201		{object}	GoalDTO
//	@Failure		400		{object}	httpx.ErrorEnvelope
//	@Failure		404		{object}	httpx.ErrorEnvelope
//	@Failure		422		{object}	httpx.ErrorEnvelope	"INSUFFICIENT_AVAILABLE_BALANCE"
//	@Router			/spaces/{spaceId}/goals [post]
func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.UserFromContext(r.Context())
	sp, _ := spaces.SpaceFromContext(r.Context())
	req, err := httpx.DecodeAndValidate[createRequest](r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	f := map[string]any{}
	in := CreateInput{Name: *cleanName(f, &req.Name), Icon: *cleanIcon(f, &req.Icon), Color: *cleanColor(f, &req.Color), TargetAmount: *req.TargetAmount}
	checkAmount(f, "targetAmount", req.TargetAmount, false)
	if c := cleanCurrency(f, req.Currency); c != nil {
		in.Currency = *c
	}
	in.TargetDate = parseDate(f, req.TargetDate)
	if rv := req.InitialReserve; rv != nil {
		checkAmount(f, "initialReserve.amount", rv.Amount, false)
		in.InitialReserve = &Reserve{WalletID: rv.WalletID, Amount: *rv.Amount}
	}
	if err := fieldsErr(f); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	g, err := h.svc.Create(r.Context(), sp, user.ID, in, userTZ(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, toDTO(g))
}

// get godoc
//
//	@Summary	Get a goal with progress and its reservations
//	@Tags		goals
//	@Security	BearerAuth
//	@Produce	json
//	@Param		spaceId	path		string	true	"Space ID"
//	@Param		id		path		string	true	"Goal ID"
//	@Success	200		{object}	GoalDTO
//	@Failure	404		{object}	httpx.ErrorEnvelope
//	@Router		/spaces/{spaceId}/goals/{id} [get]
func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	id, err := goalID(r)
	var g Goal
	if err == nil {
		g, err = h.svc.Get(r.Context(), spaces.SpaceIDFromContext(r.Context()), id, userTZ(r))
	}
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toDTO(g))
}

// update godoc
//
//	@Summary	Update a goal (`targetDate: null` clears the date)
//	@Tags		goals
//	@Security	BearerAuth
//	@Accept		json
//	@Produce	json
//	@Param		spaceId	path		string			true	"Space ID"
//	@Param		id		path		string			true	"Goal ID"
//	@Param		body	body		updateRequest	true	"Fields to change"
//	@Success	200		{object}	GoalDTO
//	@Failure	400		{object}	httpx.ErrorEnvelope
//	@Failure	404		{object}	httpx.ErrorEnvelope
//	@Router		/spaces/{spaceId}/goals/{id} [patch]
func (h *Handler) update(w http.ResponseWriter, r *http.Request) {
	id, err := goalID(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	req, err := httpx.DecodeAndValidate[updateRequest](r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	f := map[string]any{}
	in := UpdateInput{
		Name: cleanName(f, req.Name), Icon: cleanIcon(f, req.Icon), Color: cleanColor(f, req.Color),
		Currency: cleanCurrency(f, req.Currency), TargetAmount: req.TargetAmount,
	}
	checkAmount(f, "targetAmount", req.TargetAmount, false)
	if req.TargetDate.Set {
		in.TargetDate = Optional[time.Time]{Set: true, Value: parseDate(f, req.TargetDate.Value)}
	}
	if err := fieldsErr(f); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	g, err := h.svc.Update(r.Context(), spaces.SpaceIDFromContext(r.Context()), id, in, userTZ(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toDTO(g))
}

// delete godoc
//
//	@Summary	Delete a goal and release all its reservations
//	@Tags		goals
//	@Security	BearerAuth
//	@Param		spaceId	path	string	true	"Space ID"
//	@Param		id		path	string	true	"Goal ID"
//	@Success	204
//	@Failure	404	{object}	httpx.ErrorEnvelope
//	@Router		/spaces/{spaceId}/goals/{id} [delete]
func (h *Handler) delete(w http.ResponseWriter, r *http.Request) {
	id, err := goalID(r)
	if err == nil {
		err = h.svc.Delete(r.Context(), spaces.SpaceIDFromContext(r.Context()), id)
	}
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// setAllocation godoc
//
//	@Summary		Reserve part of a wallet for a goal (0 removes the reservation)
//	@Description	`amount` is in the wallet's currency and replaces this goal's reservation on that wallet. It must not exceed the wallet's available balance plus this goal's current reservation (422 INSUFFICIENT_AVAILABLE_BALANCE). No money moves; expenses may later push the wallet below its reserves (`overReserved` on the wallet).
//	@Tags			goals
//	@Security		BearerAuth
//	@Accept			json
//	@Produce		json
//	@Param			spaceId		path		string				true	"Space ID"
//	@Param			id			path		string				true	"Goal ID"
//	@Param			walletId	path		string				true	"Wallet ID"
//	@Param			body		body		allocationRequest	true	"Amount"
//	@Success		200			{object}	GoalDTO
//	@Failure		400			{object}	httpx.ErrorEnvelope
//	@Failure		404			{object}	httpx.ErrorEnvelope
//	@Failure		422			{object}	httpx.ErrorEnvelope	"INSUFFICIENT_AVAILABLE_BALANCE"
//	@Router			/spaces/{spaceId}/goals/{id}/allocations/{walletId} [put]
func (h *Handler) setAllocation(w http.ResponseWriter, r *http.Request) {
	id, err := goalID(r)
	var wid uuid.UUID
	if err == nil {
		if wid, err = uuid.Parse(chi.URLParam(r, "walletId")); err != nil {
			err = errWalletNF
		}
	}
	var req allocationRequest
	if err == nil {
		req, err = httpx.DecodeAndValidate[allocationRequest](r)
	}
	if err == nil {
		f := map[string]any{}
		checkAmount(f, "amount", req.Amount, true)
		err = fieldsErr(f)
	}
	var g Goal
	if err == nil {
		g, err = h.svc.SetAllocation(r.Context(), spaces.SpaceIDFromContext(r.Context()), id, wid, *req.Amount, userTZ(r))
	}
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toDTO(g))
}
