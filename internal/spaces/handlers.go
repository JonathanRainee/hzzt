package spaces

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

	"github.com/JonathanRainee/hzzt/internal/auth"
	"github.com/JonathanRainee/hzzt/internal/db/sqlc"
	"github.com/JonathanRainee/hzzt/internal/platform/apperror"
	"github.com/JonathanRainee/hzzt/internal/platform/httpx"
	"github.com/JonathanRainee/hzzt/internal/platform/money"
)

// Handler exposes the spaces endpoints.
type Handler struct {
	svc  *Service
	auth *auth.Service
}

// NewHandler creates the HTTP layer.
func NewHandler(svc *Service, authSvc *auth.Service) *Handler {
	return &Handler{svc: svc, auth: authSvc}
}

// Mount registers /spaces/* and /invitations/* on r (the /api/v1 router).
func (h *Handler) Mount(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(h.auth.Middleware)

		r.Get("/spaces", h.list)
		r.Post("/spaces", h.create)
		r.Post("/invitations/{code}/accept", h.accept)

		r.Route("/spaces/{spaceId}", func(r chi.Router) {
			r.Use(h.svc.Middleware)

			r.Get("/", h.get)
			r.Post("/activate", h.activate)
			r.Get("/members", h.members)
			r.Delete("/members/{userId}", h.removeMember) // owner, or self (checked in handler)

			r.Group(func(r chi.Router) {
				r.Use(RequireRole(RoleOwner))
				r.Patch("/", h.update)
				r.Delete("/", h.delete)
				r.Post("/invitations", h.createInvitation)
				r.Get("/invitations/active", h.activeInvitation)
				r.Delete("/invitations/active", h.revokeInvitation)
			})
		})
	})
}

// --- DTOs ------------------------------------------------------------------

// SpaceDTO is the public representation of a space.
type SpaceDTO struct {
	ID                   uuid.UUID `json:"id"`
	Name                 string    `json:"name"`
	AvatarEmoji          *string   `json:"avatarEmoji"`
	AvatarColor          string    `json:"avatarColor"`
	BaseCurrency         string    `json:"baseCurrency"`
	AutoFxConversion     bool      `json:"autoFxConversion"`
	TrackingMode         string    `json:"trackingMode"`
	BudgetPeriodStartDay int       `json:"budgetPeriodStartDay"`
	Role                 string    `json:"role"`
	MemberCount          int       `json:"memberCount"`
	CreatedAt            time.Time `json:"createdAt"`
}

func toSpaceDTO(s sqlc.Space, role string, members int) SpaceDTO {
	return SpaceDTO{
		ID: s.ID, Name: s.Name, AvatarEmoji: s.AvatarEmoji, AvatarColor: s.AvatarColor,
		BaseCurrency: strings.TrimSpace(s.BaseCurrency), AutoFxConversion: s.AutoFxConversion,
		TrackingMode: s.TrackingMode, BudgetPeriodStartDay: int(s.BudgetPeriodStartDay),
		Role: role, MemberCount: members, CreatedAt: s.CreatedAt.UTC(),
	}
}

// MemberDTO is a space member.
type MemberDTO struct {
	UserID   uuid.UUID `json:"userId"`
	Name     string    `json:"name"`
	Email    string    `json:"email"`
	Role     string    `json:"role"`
	JoinedAt time.Time `json:"joinedAt"`
}

// InvitationDTO is an invitation code with its link.
type InvitationDTO struct {
	Code      string    `json:"code"`
	Link      string    `json:"link"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// ActiveInvitationResponse wraps the current invitation, which may be null.
type ActiveInvitationResponse struct {
	Invitation *InvitationDTO `json:"invitation"`
}

type createRequest struct {
	Name         string  `json:"name" validate:"required,min=1,max=40"`
	AvatarEmoji  *string `json:"avatarEmoji"`
	AvatarColor  *string `json:"avatarColor"`
	BaseCurrency *string `json:"baseCurrency"`
}

// nullableString distinguishes "absent" from an explicit null (clear the value).
type nullableString struct {
	Set   bool
	Value *string
}

func (n *nullableString) UnmarshalJSON(b []byte) error {
	n.Set = true
	if bytes.Equal(bytes.TrimSpace(b), []byte("null")) {
		n.Value = nil
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
	Name             *string        `json:"name" validate:"omitempty,min=1,max=40"`
	AvatarEmoji      nullableString `json:"avatarEmoji" swaggertype:"string"`
	AvatarColor      *string        `json:"avatarColor"`
	BaseCurrency     *string        `json:"baseCurrency"`
	AutoFxConversion *bool          `json:"autoFxConversion"`
	TrackingMode     *string        `json:"trackingMode" validate:"omitempty,oneof=FULL_NET_WORTH CASH_ONLY"`
}

var hexColor = regexp.MustCompile(`^#[0-9A-Fa-f]{6}$`)

func validateEmoji(e *string, fields map[string]any) {
	if e == nil {
		return
	}
	if strings.TrimSpace(*e) == "" || utf8.RuneCountInString(*e) > 16 {
		fields["avatarEmoji"] = "must be a single emoji (1 to 16 characters)"
	}
}

func validateColor(c *string, fields map[string]any) {
	if c != nil && !hexColor.MatchString(*c) {
		fields["avatarColor"] = "must be a hex color like #4F46E5"
	}
}

func normalizeCurrency(c *string, fields map[string]any) *string {
	if c == nil {
		return nil
	}
	up := strings.ToUpper(strings.TrimSpace(*c))
	if !money.IsSupportedCurrency(up) {
		fields["baseCurrency"] = "must be a supported ISO 4217 currency code"
		return nil
	}
	return &up
}

func fieldsErr(fields map[string]any) error {
	if len(fields) == 0 {
		return nil
	}
	return apperror.Validation("Validation failed", fields)
}

func (r createRequest) check() (CreateInput, error) {
	fields := map[string]any{}
	name := strings.TrimSpace(r.Name)
	if name == "" {
		fields["name"] = "is required"
	}
	validateEmoji(r.AvatarEmoji, fields)
	validateColor(r.AvatarColor, fields)
	cur := normalizeCurrency(r.BaseCurrency, fields)
	if err := fieldsErr(fields); err != nil {
		return CreateInput{}, err
	}
	in := CreateInput{Name: name, AvatarEmoji: r.AvatarEmoji}
	if r.AvatarColor != nil {
		in.AvatarColor = strings.ToUpper(*r.AvatarColor)
	}
	if cur != nil {
		in.BaseCurrency = *cur
	}
	return in, nil
}

func (r updateRequest) check() (sqlc.UpdateSpaceParams, error) {
	fields := map[string]any{}
	p := sqlc.UpdateSpaceParams{AutoFxConversion: r.AutoFxConversion, TrackingMode: r.TrackingMode}
	if r.Name != nil {
		n := strings.TrimSpace(*r.Name)
		if n == "" {
			fields["name"] = "must not be blank"
		}
		p.Name = &n
	}
	validateEmoji(r.AvatarEmoji.Value, fields)
	p.SetAvatarEmoji, p.AvatarEmoji = r.AvatarEmoji.Set, r.AvatarEmoji.Value
	validateColor(r.AvatarColor, fields)
	if r.AvatarColor != nil {
		up := strings.ToUpper(*r.AvatarColor)
		p.AvatarColor = &up
	}
	p.BaseCurrency = normalizeCurrency(r.BaseCurrency, fields)
	return p, fieldsErr(fields)
}

// --- handlers --------------------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// list godoc
//
//	@Summary	List my spaces
//	@Tags		spaces
//	@Security	BearerAuth
//	@Produce	json
//	@Success	200	{array}		SpaceDTO
//	@Failure	401	{object}	httpx.ErrorEnvelope
//	@Router		/spaces [get]
func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.UserFromContext(r.Context())
	items, err := h.svc.List(r.Context(), user.ID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	out := make([]SpaceDTO, 0, len(items))
	for _, i := range items {
		out = append(out, toSpaceDTO(i.Space, i.Role, i.MemberCount))
	}
	writeJSON(w, http.StatusOK, out)
}

// create godoc
//
//	@Summary	Create a space (caller becomes OWNER)
//	@Tags		spaces
//	@Security	BearerAuth
//	@Accept		json
//	@Produce	json
//	@Param		body	body		createRequest	true	"Space"
//	@Success	201		{object}	SpaceDTO
//	@Failure	400		{object}	httpx.ErrorEnvelope
//	@Failure	401		{object}	httpx.ErrorEnvelope
//	@Router		/spaces [post]
func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.UserFromContext(r.Context())
	req, err := httpx.DecodeAndValidate[createRequest](r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	in, err := req.check()
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	sp, err := h.svc.Create(r.Context(), user.ID, in, false)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, toSpaceDTO(sp, RoleOwner, 1))
}

// get godoc
//
//	@Summary	Get a space
//	@Tags		spaces
//	@Security	BearerAuth
//	@Produce	json
//	@Param		spaceId	path		string	true	"Space ID"
//	@Success	200		{object}	SpaceDTO
//	@Failure	404		{object}	httpx.ErrorEnvelope
//	@Router		/spaces/{spaceId} [get]
func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	sp, _ := SpaceFromContext(r.Context())
	role, _ := RoleFromContext(r.Context())
	n, err := h.svc.MemberCount(r.Context(), sp.ID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toSpaceDTO(sp, role, n))
}

// update godoc
//
//	@Summary		Update a space (OWNER)
//	@Description	Changing baseCurrency never rewrites stored amounts; conversion happens at read time.
//	@Tags			spaces
//	@Security		BearerAuth
//	@Accept			json
//	@Produce		json
//	@Param			spaceId	path		string			true	"Space ID"
//	@Param			body	body		updateRequest	true	"Fields to change"
//	@Success		200		{object}	SpaceDTO
//	@Failure		400		{object}	httpx.ErrorEnvelope
//	@Failure		403		{object}	httpx.ErrorEnvelope
//	@Failure		404		{object}	httpx.ErrorEnvelope
//	@Router			/spaces/{spaceId} [patch]
func (h *Handler) update(w http.ResponseWriter, r *http.Request) {
	req, err := httpx.DecodeAndValidate[updateRequest](r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	p, err := req.check()
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	p.ID = SpaceIDFromContext(r.Context())
	sp, err := h.svc.Update(r.Context(), p)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	n, err := h.svc.MemberCount(r.Context(), sp.ID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toSpaceDTO(sp, RoleOwner, n))
}

// delete godoc
//
//	@Summary	Delete a space (OWNER, soft delete)
//	@Tags		spaces
//	@Security	BearerAuth
//	@Param		spaceId	path	string	true	"Space ID"
//	@Success	204
//	@Failure	403	{object}	httpx.ErrorEnvelope
//	@Failure	404	{object}	httpx.ErrorEnvelope
//	@Failure	422	{object}	httpx.ErrorEnvelope	"LAST_SPACE"
//	@Router		/spaces/{spaceId} [delete]
func (h *Handler) delete(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.UserFromContext(r.Context())
	if err := h.svc.Delete(r.Context(), user.ID, SpaceIDFromContext(r.Context())); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// activate godoc
//
//	@Summary	Mark the space as my active space
//	@Tags		spaces
//	@Security	BearerAuth
//	@Param		spaceId	path	string	true	"Space ID"
//	@Success	204
//	@Failure	404	{object}	httpx.ErrorEnvelope
//	@Router		/spaces/{spaceId}/activate [post]
func (h *Handler) activate(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.UserFromContext(r.Context())
	if err := h.svc.Activate(r.Context(), user.ID, SpaceIDFromContext(r.Context())); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// members godoc
//
//	@Summary	List space members
//	@Tags		spaces
//	@Security	BearerAuth
//	@Produce	json
//	@Param		spaceId	path		string	true	"Space ID"
//	@Success	200		{array}		MemberDTO
//	@Failure	404		{object}	httpx.ErrorEnvelope
//	@Router		/spaces/{spaceId}/members [get]
func (h *Handler) members(w http.ResponseWriter, r *http.Request) {
	rows, err := h.svc.Members(r.Context(), SpaceIDFromContext(r.Context()))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	out := make([]MemberDTO, 0, len(rows))
	for _, m := range rows {
		out = append(out, MemberDTO{UserID: m.UserID, Name: m.Name, Email: m.Email, Role: m.Role, JoinedAt: m.JoinedAt.UTC()})
	}
	writeJSON(w, http.StatusOK, out)
}

// removeMember godoc
//
//	@Summary	Remove a member (OWNER) or leave the space (self)
//	@Tags		spaces
//	@Security	BearerAuth
//	@Param		spaceId	path	string	true	"Space ID"
//	@Param		userId	path	string	true	"User ID"
//	@Success	204
//	@Failure	403	{object}	httpx.ErrorEnvelope
//	@Failure	404	{object}	httpx.ErrorEnvelope
//	@Failure	422	{object}	httpx.ErrorEnvelope	"OWNER_CANNOT_LEAVE"
//	@Router		/spaces/{spaceId}/members/{userId} [delete]
func (h *Handler) removeMember(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.UserFromContext(r.Context())
	role, _ := RoleFromContext(r.Context())
	target, err := uuid.Parse(chi.URLParam(r, "userId"))
	if err != nil {
		httpx.WriteError(w, r, apperror.NotFound("Member not found"))
		return
	}
	if role != RoleOwner && target != user.ID {
		httpx.WriteError(w, r, apperror.Forbidden("Only the owner can remove other members"))
		return
	}
	if err := h.svc.RemoveMember(r.Context(), SpaceIDFromContext(r.Context()), target); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func toInvitationDTO(i Invitation) *InvitationDTO {
	return &InvitationDTO{Code: i.Code, Link: i.Link, ExpiresAt: i.ExpiresAt}
}

// createInvitation godoc
//
//	@Summary	Create an invitation code (OWNER); revokes the previous one
//	@Tags		spaces
//	@Security	BearerAuth
//	@Produce	json
//	@Param		spaceId	path		string	true	"Space ID"
//	@Success	201		{object}	InvitationDTO
//	@Failure	403		{object}	httpx.ErrorEnvelope
//	@Failure	404		{object}	httpx.ErrorEnvelope
//	@Router		/spaces/{spaceId}/invitations [post]
func (h *Handler) createInvitation(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.UserFromContext(r.Context())
	inv, err := h.svc.CreateInvitation(r.Context(), SpaceIDFromContext(r.Context()), user.ID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, toInvitationDTO(inv))
}

// activeInvitation godoc
//
//	@Summary	Get the active invitation (OWNER); invitation is null if none
//	@Tags		spaces
//	@Security	BearerAuth
//	@Produce	json
//	@Param		spaceId	path		string	true	"Space ID"
//	@Success	200		{object}	ActiveInvitationResponse
//	@Failure	403		{object}	httpx.ErrorEnvelope
//	@Failure	404		{object}	httpx.ErrorEnvelope
//	@Router		/spaces/{spaceId}/invitations/active [get]
func (h *Handler) activeInvitation(w http.ResponseWriter, r *http.Request) {
	inv, err := h.svc.ActiveInvitation(r.Context(), SpaceIDFromContext(r.Context()))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	resp := ActiveInvitationResponse{}
	if inv != nil {
		resp.Invitation = toInvitationDTO(*inv)
	}
	writeJSON(w, http.StatusOK, resp)
}

// revokeInvitation godoc
//
//	@Summary	Revoke the active invitation (OWNER)
//	@Tags		spaces
//	@Security	BearerAuth
//	@Param		spaceId	path	string	true	"Space ID"
//	@Success	204
//	@Failure	403	{object}	httpx.ErrorEnvelope
//	@Failure	404	{object}	httpx.ErrorEnvelope
//	@Router		/spaces/{spaceId}/invitations/active [delete]
func (h *Handler) revokeInvitation(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.RevokeInvitation(r.Context(), SpaceIDFromContext(r.Context())); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// accept godoc
//
//	@Summary	Join a space with an invitation code (as MEMBER)
//	@Tags		spaces
//	@Security	BearerAuth
//	@Produce	json
//	@Param		code	path		string	true	"Invitation code"
//	@Success	200		{object}	SpaceDTO
//	@Failure	409		{object}	httpx.ErrorEnvelope	"ALREADY_A_MEMBER"
//	@Failure	422		{object}	httpx.ErrorEnvelope	"INVITATION_INVALID / INVITATION_EXPIRED / SPACE_MEMBER_LIMIT"
//	@Router		/invitations/{code}/accept [post]
func (h *Handler) accept(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.UserFromContext(r.Context())
	sp, err := h.svc.Accept(r.Context(), user.ID, chi.URLParam(r, "code"))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	n, err := h.svc.MemberCount(r.Context(), sp.ID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toSpaceDTO(sp, RoleMember, n))
}
