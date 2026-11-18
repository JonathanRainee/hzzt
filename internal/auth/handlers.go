package auth

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/JonathanRainee/hzzt/internal/db/sqlc"
	"github.com/JonathanRainee/hzzt/internal/platform/apperror"
	"github.com/JonathanRainee/hzzt/internal/platform/httpx"
)

// RefreshCookieName is the httpOnly cookie carrying the refresh token.
const RefreshCookieName = "rt"

const refreshCookiePath = "/api/v1/auth"

// Limits are the per-endpoint rate limiters.
type Limits struct {
	Login    *httpx.RateLimiter // per IP+email
	Register *httpx.RateLimiter // per IP
	Forgot   *httpx.RateLimiter // per IP
}

// DefaultLimits: login 5/min per IP+email, register 5/min per IP, forgot-password 3/15min per IP.
func DefaultLimits() Limits {
	return Limits{
		Login:    httpx.NewRateLimiter(5, time.Minute),
		Register: httpx.NewRateLimiter(5, time.Minute),
		Forgot:   httpx.NewRateLimiter(3, 15*time.Minute),
	}
}

// Handler exposes the auth endpoints.
type Handler struct {
	svc          *Service
	limits       Limits
	secureCookie bool
}

// NewHandler creates the HTTP layer. secureCookie should be true in production.
func NewHandler(svc *Service, limits Limits, secureCookie bool) *Handler {
	return &Handler{svc: svc, limits: limits, secureCookie: secureCookie}
}

// Mount registers /auth/* on r (expected to be the /api/v1 router).
func (h *Handler) Mount(r chi.Router) {
	r.Route("/auth", func(r chi.Router) {
		// Public: no AuthMiddleware.
		r.Post("/register", h.register)
		r.Post("/login", h.login)
		r.Post("/refresh", h.refresh)
		r.Post("/logout", h.logout)
		r.Post("/forgot-password", h.forgotPassword)
		r.Post("/reset-password", h.resetPassword)

		// Authenticated.
		r.Group(func(r chi.Router) {
			r.Use(h.svc.Middleware)
			r.Post("/logout-all", h.logoutAll)
			r.Post("/change-password", h.changePassword)
			r.Get("/me", h.me)
		})
	})
}

// --- request / response types -------------------------------------------

// normEmail trims and lowercases itself when decoded from JSON.
type normEmail string

func (e *normEmail) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	*e = normEmail(strings.ToLower(strings.TrimSpace(s)))
	return nil
}

// trimmed trims surrounding whitespace when decoded from JSON.
type trimmed string

func (t *trimmed) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	*t = trimmed(strings.TrimSpace(s))
	return nil
}

type registerRequest struct {
	Email    normEmail `json:"email" validate:"required,email,max=254" swaggertype:"string" example:"jane@example.com"`
	Password string    `json:"password" validate:"required,max=128" example:"correct horse battery"`
	Name     trimmed   `json:"name" validate:"required,min=1,max=60" swaggertype:"string" example:"Jane"`
	Timezone string    `json:"timezone" validate:"omitempty,max=64" example:"Asia/Jakarta"`
}

type loginRequest struct {
	Email    normEmail `json:"email" validate:"required,email,max=254" swaggertype:"string"`
	Password string    `json:"password" validate:"required,max=128"`
}

type changePasswordRequest struct {
	CurrentPassword string `json:"currentPassword" validate:"required,max=128"`
	NewPassword     string `json:"newPassword" validate:"required,max=128"`
}

type forgotPasswordRequest struct {
	Email normEmail `json:"email" validate:"required,email,max=254" swaggertype:"string"`
}

type resetPasswordRequest struct {
	Token       string `json:"token" validate:"required,max=256"`
	NewPassword string `json:"newPassword" validate:"required,max=128"`
}

// UserDTO is the public representation of a user (never includes the password hash).
type UserDTO struct {
	ID                uuid.UUID  `json:"id"`
	Email             string     `json:"email"`
	Name              string     `json:"name"`
	Timezone          string     `json:"timezone"`
	LastActiveSpaceID *uuid.UUID `json:"lastActiveSpaceId"`
	CreatedAt         time.Time  `json:"createdAt"`
}

// ToUserDTO converts a DB user.
func ToUserDTO(u sqlc.User) UserDTO {
	d := UserDTO{ID: u.ID, Email: u.Email, Name: u.Name, Timezone: u.Timezone, CreatedAt: u.CreatedAt.UTC()}
	if u.LastActiveSpaceID.Valid {
		id := u.LastActiveSpaceID.UUID
		d.LastActiveSpaceID = &id
	}
	return d
}

// AuthResponse is returned by register, login and refresh.
type AuthResponse struct {
	AccessToken string  `json:"accessToken"`
	ExpiresIn   int     `json:"expiresIn" example:"900"` // seconds
	User        UserDTO `json:"user"`
}

// MessageResponse is a generic acknowledgement.
type MessageResponse struct {
	Message string `json:"message"`
}

// --- helpers ---------------------------------------------------------------

func clientInfo(r *http.Request) ClientInfo {
	ua := r.UserAgent()
	if len(ua) > 256 {
		ua = ua[:256]
	}
	return ClientInfo{UserAgent: ua, IP: httpx.ClientIP(r)}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (h *Handler) setRefreshCookie(w http.ResponseWriter, token string, ttl time.Duration) {
	http.SetCookie(w, &http.Cookie{
		Name: RefreshCookieName, Value: token, Path: refreshCookiePath,
		MaxAge: int(ttl.Seconds()), HttpOnly: true, Secure: h.secureCookie, SameSite: http.SameSiteLaxMode,
	})
}

func (h *Handler) clearRefreshCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: RefreshCookieName, Value: "", Path: refreshCookiePath,
		MaxAge: -1, HttpOnly: true, Secure: h.secureCookie, SameSite: http.SameSiteLaxMode,
	})
}

func (h *Handler) respondSession(w http.ResponseWriter, status int, s *Session) {
	h.setRefreshCookie(w, s.RefreshToken, s.RefreshTTL)
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, status, AuthResponse{AccessToken: s.AccessToken, ExpiresIn: s.ExpiresIn, User: ToUserDTO(s.User)})
}

func normalizeTimezone(tz string) (string, error) {
	tz = strings.TrimSpace(tz)
	if tz == "" {
		return "UTC", nil
	}
	if tz == "Local" {
		return "", apperror.Validation("Validation failed", map[string]any{"timezone": "must be a valid IANA timezone"})
	}
	if _, err := time.LoadLocation(tz); err != nil {
		return "", apperror.Validation("Validation failed", map[string]any{"timezone": "must be a valid IANA timezone"})
	}
	return tz, nil
}

// --- handlers --------------------------------------------------------------

// register godoc
//
//	@Summary	Register an account
//	@Tags		auth
//	@Accept		json
//	@Produce	json
//	@Param		body	body		registerRequest	true	"Account details"
//	@Success	201		{object}	AuthResponse
//	@Failure	400		{object}	httpx.ErrorEnvelope
//	@Failure	409		{object}	httpx.ErrorEnvelope	"EMAIL_ALREADY_REGISTERED"
//	@Failure	429		{object}	httpx.ErrorEnvelope
//	@Router		/auth/register [post]
func (h *Handler) register(w http.ResponseWriter, r *http.Request) {
	if err := h.limits.Register.Check(w, httpx.ClientIP(r)); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	req, err := httpx.DecodeAndValidate[registerRequest](r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	tz, err := normalizeTimezone(req.Timezone)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	s, err := h.svc.Register(r.Context(), string(req.Email), req.Password, string(req.Name), tz, clientInfo(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	h.respondSession(w, http.StatusCreated, s)
}

// login godoc
//
//	@Summary	Log in with email and password
//	@Tags		auth
//	@Accept		json
//	@Produce	json
//	@Param		body	body		loginRequest	true	"Credentials"
//	@Success	200		{object}	AuthResponse
//	@Failure	400		{object}	httpx.ErrorEnvelope
//	@Failure	401		{object}	httpx.ErrorEnvelope	"INVALID_CREDENTIALS"
//	@Failure	429		{object}	httpx.ErrorEnvelope
//	@Router		/auth/login [post]
func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	req, err := httpx.DecodeAndValidate[loginRequest](r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if err := h.limits.Login.Check(w, httpx.ClientIP(r)+"|"+string(req.Email)); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	s, err := h.svc.Login(r.Context(), string(req.Email), req.Password, clientInfo(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	h.respondSession(w, http.StatusOK, s)
}

// refresh godoc
//
//	@Summary		Rotate the refresh token and get a new access token
//	@Description	Reads the httpOnly `rt` cookie. Presenting an already-rotated token revokes the whole token family (REFRESH_TOKEN_REUSED).
//	@Tags			auth
//	@Produce		json
//	@Success		200	{object}	AuthResponse
//	@Failure		401	{object}	httpx.ErrorEnvelope	"REFRESH_TOKEN_INVALID / REFRESH_TOKEN_REUSED"
//	@Router			/auth/refresh [post]
func (h *Handler) refresh(w http.ResponseWriter, r *http.Request) {
	var token string
	if c, err := r.Cookie(RefreshCookieName); err == nil {
		token = c.Value
	}
	s, err := h.svc.Refresh(r.Context(), token, clientInfo(r))
	if err != nil {
		if ae, ok := apperror.As(err); ok && ae.HTTPStatus == http.StatusUnauthorized {
			h.clearRefreshCookie(w)
		}
		httpx.WriteError(w, r, err)
		return
	}
	h.respondSession(w, http.StatusOK, s)
}

// logout godoc
//
//	@Summary	Revoke the current refresh token family and clear the cookie
//	@Tags		auth
//	@Success	204
//	@Router		/auth/logout [post]
func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(RefreshCookieName); err == nil {
		if err := h.svc.Logout(r.Context(), c.Value); err != nil {
			httpx.WriteError(w, r, err)
			return
		}
	}
	h.clearRefreshCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

// logoutAll godoc
//
//	@Summary	Revoke all of the user's refresh tokens
//	@Tags		auth
//	@Security	BearerAuth
//	@Success	204
//	@Failure	401	{object}	httpx.ErrorEnvelope
//	@Router		/auth/logout-all [post]
func (h *Handler) logoutAll(w http.ResponseWriter, r *http.Request) {
	user, _ := UserFromContext(r.Context())
	if err := h.svc.LogoutAll(r.Context(), user.ID); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	h.clearRefreshCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

// changePassword godoc
//
//	@Summary	Change password (revokes all other sessions)
//	@Tags		auth
//	@Security	BearerAuth
//	@Accept		json
//	@Param		body	body	changePasswordRequest	true	"Passwords"
//	@Success	204
//	@Failure	400	{object}	httpx.ErrorEnvelope
//	@Failure	401	{object}	httpx.ErrorEnvelope
//	@Failure	422	{object}	httpx.ErrorEnvelope	"INVALID_CURRENT_PASSWORD"
//	@Router		/auth/change-password [post]
func (h *Handler) changePassword(w http.ResponseWriter, r *http.Request) {
	req, err := httpx.DecodeAndValidate[changePasswordRequest](r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	user, _ := UserFromContext(r.Context())
	fam, _ := FamilyFromContext(r.Context())
	if err := h.svc.ChangePassword(r.Context(), user, fam, req.CurrentPassword, req.NewPassword); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// forgotPassword godoc
//
//	@Summary		Request a password reset email
//	@Description	Always responds 200, whether or not the email is registered.
//	@Tags			auth
//	@Accept			json
//	@Produce		json
//	@Param			body	body		forgotPasswordRequest	true	"Email"
//	@Success		200		{object}	MessageResponse
//	@Failure		429		{object}	httpx.ErrorEnvelope
//	@Router			/auth/forgot-password [post]
func (h *Handler) forgotPassword(w http.ResponseWriter, r *http.Request) {
	if err := h.limits.Forgot.Check(w, httpx.ClientIP(r)); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	req, err := httpx.DecodeAndValidate[forgotPasswordRequest](r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if err := h.svc.ForgotPassword(r.Context(), string(req.Email)); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, MessageResponse{Message: "If that email is registered, a reset link has been sent."})
}

// resetPassword godoc
//
//	@Summary	Reset password with an emailed token (revokes all sessions)
//	@Tags		auth
//	@Accept		json
//	@Param		body	body	resetPasswordRequest	true	"Token and new password"
//	@Success	204
//	@Failure	400	{object}	httpx.ErrorEnvelope
//	@Failure	422	{object}	httpx.ErrorEnvelope	"RESET_TOKEN_INVALID"
//	@Router		/auth/reset-password [post]
func (h *Handler) resetPassword(w http.ResponseWriter, r *http.Request) {
	req, err := httpx.DecodeAndValidate[resetPasswordRequest](r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if err := h.svc.ResetPassword(r.Context(), req.Token, req.NewPassword); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// me godoc
//
//	@Summary	Current user
//	@Tags		auth
//	@Security	BearerAuth
//	@Produce	json
//	@Success	200	{object}	UserDTO
//	@Failure	401	{object}	httpx.ErrorEnvelope
//	@Router		/auth/me [get]
func (h *Handler) me(w http.ResponseWriter, r *http.Request) {
	user, _ := UserFromContext(r.Context())
	writeJSON(w, http.StatusOK, ToUserDTO(user))
}
