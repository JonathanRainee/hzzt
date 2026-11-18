package users

import (
	"context"
	"net/http"
	"regexp"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/JonathanRainee/hzzt/internal/auth"
	"github.com/JonathanRainee/hzzt/internal/db"
	"github.com/JonathanRainee/hzzt/internal/db/sqlc"
	"github.com/JonathanRainee/hzzt/internal/platform/apperror"
	"github.com/JonathanRainee/hzzt/internal/platform/events"
	"github.com/JonathanRainee/hzzt/internal/platform/httpx"
)

// Error codes owned by this file (see plan Appendix B).
const (
	CodeAppLockTemporarilyLocked = "APP_LOCK_TEMPORARILY_LOCKED"
	CodeAppLockNotEnabled        = "APP_LOCK_NOT_ENABLED"
	CodeAppLockAlreadyEnabled    = "APP_LOCK_ALREADY_ENABLED"
	CodeInvalidPIN               = "INVALID_PIN"
)

// App-lock lockout policy.
const (
	MaxPINAttempts = 5
	LockDuration   = 5 * time.Minute
)

var pinPattern = regexp.MustCompile(`^\d{4,6}$`)

// Subscribe creates default preferences for new users.
func (s *Service) Subscribe(bus *events.Bus) {
	events.On(bus, func(ctx context.Context, e auth.UserRegistered) error {
		_, err := s.q.EnsureUserPreferences(ctx, e.UserID)
		return err
	})
}

// Preferences returns the user's preferences, creating defaults if missing.
func (s *Service) Preferences(ctx context.Context, userID uuid.UUID) (sqlc.UserPreference, error) {
	return s.q.EnsureUserPreferences(ctx, userID)
}

// UpdatePreferences applies a partial update.
func (s *Service) UpdatePreferences(ctx context.Context, userID uuid.UUID, hide *bool, keyboard *string) (sqlc.UserPreference, error) {
	var out sqlc.UserPreference
	err := db.WithTx(ctx, s.db, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		if _, err := q.EnsureUserPreferences(ctx, userID); err != nil {
			return err
		}
		var err error
		out, err = q.UpdateUserPreferences(ctx, sqlc.UpdateUserPreferencesParams{UserID: userID, HideAmounts: hide, KeyboardType: keyboard})
		return err
	})
	return out, err
}

func lockedError(until, now time.Time) error {
	secs := int(until.Sub(now).Seconds()) + 1
	return apperror.New(http.StatusTooManyRequests, CodeAppLockTemporarilyLocked,
		"Too many wrong PIN attempts; app lock is temporarily locked").
		WithDetails(map[string]any{"retryAfter": secs})
}

var (
	errNotEnabled     = apperror.Unprocessable(CodeAppLockNotEnabled, "App lock is not enabled")
	errAlreadyEnabled = apperror.Conflict(CodeAppLockAlreadyEnabled, "App lock is already enabled; change the PIN instead")
	errInvalidPIN     = apperror.Unprocessable(CodeInvalidPIN, "PIN is incorrect")
)

// EnableAppLock sets the PIN after re-checking the account password.
func (s *Service) EnableAppLock(ctx context.Context, user sqlc.User, pin, password string) error {
	if !auth.VerifyPassword(password, user.PasswordHash) {
		return apperror.Unprocessable(auth.CodeInvalidCurrentPassword, "Password is incorrect")
	}
	hash, err := auth.HashPassword(pin)
	if err != nil {
		return err
	}
	return db.WithTx(ctx, s.db, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		if _, err := q.EnsureUserPreferences(ctx, user.ID); err != nil {
			return err
		}
		p, err := q.LockUserPreferences(ctx, user.ID)
		if err != nil {
			return err
		}
		if p.AppLockEnabled {
			return errAlreadyEnabled
		}
		return q.EnableAppLock(ctx, sqlc.EnableAppLockParams{UserID: user.ID, AppLockPinHash: &hash})
	})
}

// DisableAppLock removes the PIN after re-checking the account password.
func (s *Service) DisableAppLock(ctx context.Context, user sqlc.User, password string) error {
	if !auth.VerifyPassword(password, user.PasswordHash) {
		return apperror.Unprocessable(auth.CodeInvalidCurrentPassword, "Password is incorrect")
	}
	return db.WithTx(ctx, s.db, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		if _, err := q.EnsureUserPreferences(ctx, user.ID); err != nil {
			return err
		}
		p, err := q.LockUserPreferences(ctx, user.ID)
		if err != nil {
			return err
		}
		if !p.AppLockEnabled {
			return errNotEnabled
		}
		return q.DisableAppLock(ctx, user.ID)
	})
}

// checkPin runs one PIN attempt against the locked preferences row: it enforces
// the lockout, counts failures and resets the counter on success.
func (s *Service) checkPin(ctx context.Context, q *sqlc.Queries, userID uuid.UUID, pin string) (valid bool, p sqlc.UserPreference, err error) {
	if _, err = q.EnsureUserPreferences(ctx, userID); err != nil {
		return false, p, err
	}
	p, err = q.LockUserPreferences(ctx, userID) // row lock makes the counter atomic
	if err != nil {
		return false, p, err
	}
	if !p.AppLockEnabled || p.AppLockPinHash == nil {
		return false, p, errNotEnabled
	}
	now := s.now()
	if p.AppLockLockedUntil != nil && now.Before(*p.AppLockLockedUntil) {
		return false, p, lockedError(*p.AppLockLockedUntil, now)
	}
	if auth.VerifyPassword(pin, *p.AppLockPinHash) {
		if p.AppLockFailedAttempts != 0 || p.AppLockLockedUntil != nil {
			err = q.SetAppLockAttempts(ctx, sqlc.SetAppLockAttemptsParams{UserID: userID})
		}
		return true, p, err
	}
	attempts := p.AppLockFailedAttempts + 1
	var until *time.Time
	if attempts >= MaxPINAttempts {
		t := now.Add(LockDuration)
		until, attempts = &t, 0 // a fresh set of attempts once the lock expires
	}
	err = q.SetAppLockAttempts(ctx, sqlc.SetAppLockAttemptsParams{
		UserID: userID, AppLockFailedAttempts: attempts, AppLockLockedUntil: until,
	})
	return false, p, err
}

// VerifyPIN reports whether pin is correct. Wrong attempts are counted and
// committed; the 5th consecutive failure locks verification for 5 minutes.
func (s *Service) VerifyPIN(ctx context.Context, userID uuid.UUID, pin string) (bool, error) {
	var valid bool
	err := db.WithTx(ctx, s.db, func(tx pgx.Tx) error {
		var err error
		valid, _, err = s.checkPin(ctx, s.q.WithTx(tx), userID, pin)
		return err
	})
	return valid, err
}

// ChangePIN replaces the PIN. A wrong current PIN counts toward the lockout.
func (s *Service) ChangePIN(ctx context.Context, userID uuid.UUID, current, next string) error {
	hash, err := auth.HashPassword(next)
	if err != nil {
		return err
	}
	wrong := false
	err = db.WithTx(ctx, s.db, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		valid, _, err := s.checkPin(ctx, q, userID, current)
		if err != nil {
			return err
		}
		if !valid {
			wrong = true
			return nil // commit the failed-attempt counter
		}
		return q.SetAppLockPin(ctx, sqlc.SetAppLockPinParams{UserID: userID, AppLockPinHash: &hash})
	})
	if err != nil {
		return err
	}
	if wrong {
		return errInvalidPIN
	}
	return nil
}

// --- HTTP ------------------------------------------------------------------

// PreferencesDTO is the public representation of preferences (never includes the PIN hash).
type PreferencesDTO struct {
	HideAmounts    bool   `json:"hideAmounts"`
	KeyboardType   string `json:"keyboardType" enums:"MINI_CALCULATOR,STANDARD_NUMERIC"`
	AppLockEnabled bool   `json:"appLockEnabled"`
}

func toPreferencesDTO(p sqlc.UserPreference) PreferencesDTO {
	return PreferencesDTO{HideAmounts: p.HideAmounts, KeyboardType: p.KeyboardType, AppLockEnabled: p.AppLockEnabled}
}

type patchPreferencesRequest struct {
	HideAmounts  *bool   `json:"hideAmounts"`
	KeyboardType *string `json:"keyboardType" validate:"omitempty,oneof=MINI_CALCULATOR STANDARD_NUMERIC"`
}

type enableAppLockRequest struct {
	PIN      string `json:"pin" validate:"required,max=16"`
	Password string `json:"password" validate:"required,max=128"`
}

type verifyAppLockRequest struct {
	PIN string `json:"pin" validate:"required,max=16"`
}

// VerifyResponse is the result of a PIN check.
type VerifyResponse struct {
	Valid bool `json:"valid"`
}

type changePINRequest struct {
	CurrentPIN string `json:"currentPin" validate:"required,max=16"`
	NewPIN     string `json:"newPin" validate:"required,max=16"`
}

type disableAppLockRequest struct {
	Password string `json:"password" validate:"required,max=128"`
}

func checkPINFormat(field, pin string) error {
	if !pinPattern.MatchString(pin) {
		return apperror.Validation("Validation failed", map[string]any{field: "must be 4 to 6 digits"})
	}
	return nil
}

// getPreferences godoc
//
//	@Summary	Get my preferences (defaults are created on registration)
//	@Tags		me
//	@Security	BearerAuth
//	@Produce	json
//	@Success	200	{object}	PreferencesDTO
//	@Failure	401	{object}	httpx.ErrorEnvelope
//	@Router		/me/preferences [get]
func (h *Handler) getPreferences(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.UserFromContext(r.Context())
	p, err := h.svc.Preferences(r.Context(), user.ID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toPreferencesDTO(p))
}

// patchPreferences godoc
//
//	@Summary	Update my preferences
//	@Tags		me
//	@Security	BearerAuth
//	@Accept		json
//	@Produce	json
//	@Param		body	body		patchPreferencesRequest	true	"Fields to change"
//	@Success	200		{object}	PreferencesDTO
//	@Failure	400		{object}	httpx.ErrorEnvelope
//	@Failure	401		{object}	httpx.ErrorEnvelope
//	@Router		/me/preferences [patch]
func (h *Handler) patchPreferences(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.UserFromContext(r.Context())
	req, err := httpx.DecodeAndValidate[patchPreferencesRequest](r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	p, err := h.svc.UpdatePreferences(r.Context(), user.ID, req.HideAmounts, req.KeyboardType)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toPreferencesDTO(p))
}

// enableAppLock godoc
//
//	@Summary		Enable the app lock
//	@Description	The app lock is a client-side UX gate that syncs across devices, NOT a security boundary: API access is governed by the JWT alone.
//	@Tags			me
//	@Security		BearerAuth
//	@Accept			json
//	@Param			body	body	enableAppLockRequest	true	"PIN (4 to 6 digits) and account password"
//	@Success		204
//	@Failure		400	{object}	httpx.ErrorEnvelope
//	@Failure		409	{object}	httpx.ErrorEnvelope	"APP_LOCK_ALREADY_ENABLED"
//	@Failure		422	{object}	httpx.ErrorEnvelope	"INVALID_CURRENT_PASSWORD"
//	@Router			/me/app-lock [post]
func (h *Handler) enableAppLock(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.UserFromContext(r.Context())
	req, err := httpx.DecodeAndValidate[enableAppLockRequest](r)
	if err == nil {
		err = checkPINFormat("pin", req.PIN)
	}
	if err == nil {
		err = h.svc.EnableAppLock(r.Context(), user, req.PIN, req.Password)
	}
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// verifyAppLock godoc
//
//	@Summary		Verify the app-lock PIN
//	@Description	5 consecutive wrong PINs lock verification for 5 minutes (429 APP_LOCK_TEMPORARILY_LOCKED with details.retryAfter in seconds). Rate limited to 10/min per user. Client-side UX gate only.
//	@Tags			me
//	@Security		BearerAuth
//	@Accept			json
//	@Produce		json
//	@Param			body	body		verifyAppLockRequest	true	"PIN"
//	@Success		200		{object}	VerifyResponse
//	@Failure		422		{object}	httpx.ErrorEnvelope	"APP_LOCK_NOT_ENABLED"
//	@Failure		429		{object}	httpx.ErrorEnvelope	"APP_LOCK_TEMPORARILY_LOCKED / RATE_LIMITED"
//	@Router			/me/app-lock/verify [post]
func (h *Handler) verifyAppLock(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.UserFromContext(r.Context())
	if err := h.limits.VerifyPIN.Check(w, user.ID.String()); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	req, err := httpx.DecodeAndValidate[verifyAppLockRequest](r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	valid, err := h.svc.VerifyPIN(r.Context(), user.ID, req.PIN)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, VerifyResponse{Valid: valid})
}

// changeAppLockPin godoc
//
//	@Summary	Change the app-lock PIN (a wrong current PIN counts toward the lockout)
//	@Tags		me
//	@Security	BearerAuth
//	@Accept		json
//	@Param		body	body	changePINRequest	true	"Current and new PIN"
//	@Success	204
//	@Failure	400	{object}	httpx.ErrorEnvelope
//	@Failure	422	{object}	httpx.ErrorEnvelope	"INVALID_PIN / APP_LOCK_NOT_ENABLED"
//	@Failure	429	{object}	httpx.ErrorEnvelope	"APP_LOCK_TEMPORARILY_LOCKED"
//	@Router		/me/app-lock/pin [patch]
func (h *Handler) changeAppLockPin(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.UserFromContext(r.Context())
	req, err := httpx.DecodeAndValidate[changePINRequest](r)
	if err == nil {
		err = checkPINFormat("newPin", req.NewPIN)
	}
	if err == nil {
		err = h.svc.ChangePIN(r.Context(), user.ID, req.CurrentPIN, req.NewPIN)
	}
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// disableAppLock godoc
//
//	@Summary	Disable the app lock (requires the account password)
//	@Tags		me
//	@Security	BearerAuth
//	@Accept		json
//	@Param		body	body	disableAppLockRequest	true	"Password"
//	@Success	204
//	@Failure	422	{object}	httpx.ErrorEnvelope	"INVALID_CURRENT_PASSWORD / APP_LOCK_NOT_ENABLED"
//	@Router		/me/app-lock [delete]
func (h *Handler) disableAppLock(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.UserFromContext(r.Context())
	req, err := httpx.DecodeAndValidate[disableAppLockRequest](r)
	if err == nil {
		err = h.svc.DisableAppLock(r.Context(), user, req.Password)
	}
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
