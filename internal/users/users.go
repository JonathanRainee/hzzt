// Package users implements the authenticated user's own profile (/me).
package users

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/JonathanRainee/hzzt/internal/auth"
	"github.com/JonathanRainee/hzzt/internal/db"
	"github.com/JonathanRainee/hzzt/internal/db/sqlc"
	"github.com/JonathanRainee/hzzt/internal/platform/apperror"
	"github.com/JonathanRainee/hzzt/internal/platform/httpx"
)

// Error codes owned by this package (see plan Appendix B).
const CodeOwnershipTransferRequired = "OWNERSHIP_TRANSFER_REQUIRED"

// DB is what the service needs; *pgxpool.Pool and pgx.Tx both satisfy it.
type DB interface {
	sqlc.DBTX
	db.TxStarter
}

// Service holds profile logic.
type Service struct {
	db DB
	q  *sqlc.Queries
}

// NewService builds the service.
func NewService(database DB) *Service { return &Service{db: database, q: sqlc.New(database)} }

// UpdateProfile applies a partial update of name and/or timezone.
func (s *Service) UpdateProfile(ctx context.Context, id uuid.UUID, name, tz *string) (sqlc.User, error) {
	u, err := s.q.UpdateUserProfile(ctx, sqlc.UpdateUserProfileParams{ID: id, Name: name, Timezone: tz})
	if errors.Is(err, pgx.ErrNoRows) {
		return u, apperror.Unauthenticated("User no longer exists")
	}
	return u, err
}

// Delete soft-deletes the account after verifying the password.
// It is blocked while the user owns a space that still has other members.
func (s *Service) Delete(ctx context.Context, user sqlc.User, password string) error {
	if !auth.VerifyPassword(password, user.PasswordHash) {
		return apperror.Unprocessable(auth.CodeInvalidCurrentPassword, "Password is incorrect")
	}
	return db.WithTx(ctx, s.db, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		n, err := q.CountOwnedSharedSpaces(ctx, user.ID)
		if err != nil {
			return err
		}
		if n > 0 {
			return apperror.Unprocessable(CodeOwnershipTransferRequired,
				"You own a space with other members; remove them or delete the space first")
		}
		if err := q.SoftDeleteOwnedSoleSpaces(ctx, user.ID); err != nil {
			return err
		}
		if err := q.RemoveUserMemberships(ctx, user.ID); err != nil {
			return err
		}
		if err := q.RevokeAllUserRefreshTokens(ctx, user.ID); err != nil {
			return err
		}
		return q.SoftDeleteUser(ctx, user.ID)
	})
}

// Handler exposes /me.
type Handler struct {
	svc  *Service
	auth *auth.Service
}

// NewHandler creates the HTTP layer.
func NewHandler(svc *Service, authSvc *auth.Service) *Handler {
	return &Handler{svc: svc, auth: authSvc}
}

// Mount registers /me on r (the /api/v1 router).
func (h *Handler) Mount(r chi.Router) {
	r.Route("/me", func(r chi.Router) {
		r.Use(h.auth.Middleware)
		r.Get("/", h.get)
		r.Patch("/", h.patch)
		r.Delete("/", h.delete)
	})
}

type patchRequest struct {
	Name     *string `json:"name" validate:"omitempty,min=1,max=60"`
	Timezone *string `json:"timezone" validate:"omitempty,max=64"`
}

type deleteRequest struct {
	Password string `json:"password" validate:"required,max=128"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// get godoc
//
//	@Summary	Get my profile (includes lastActiveSpaceId)
//	@Tags		me
//	@Security	BearerAuth
//	@Produce	json
//	@Success	200	{object}	auth.UserDTO
//	@Failure	401	{object}	httpx.ErrorEnvelope
//	@Router		/me [get]
func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.UserFromContext(r.Context())
	writeJSON(w, http.StatusOK, auth.ToUserDTO(user))
}

// patch godoc
//
//	@Summary	Update my name and/or timezone
//	@Tags		me
//	@Security	BearerAuth
//	@Accept		json
//	@Produce	json
//	@Param		body	body		patchRequest	true	"Fields to change"
//	@Success	200		{object}	auth.UserDTO
//	@Failure	400		{object}	httpx.ErrorEnvelope
//	@Failure	401		{object}	httpx.ErrorEnvelope
//	@Router		/me [patch]
func (h *Handler) patch(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.UserFromContext(r.Context())
	req, err := httpx.DecodeAndValidate[patchRequest](r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if req.Name != nil {
		n := strings.TrimSpace(*req.Name)
		if n == "" {
			httpx.WriteError(w, r, apperror.Validation("Validation failed", map[string]any{"name": "must not be blank"}))
			return
		}
		req.Name = &n
	}
	if req.Timezone != nil {
		tz, err := auth.NormalizeTimezone(*req.Timezone)
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		req.Timezone = &tz
	}
	u, err := h.svc.UpdateProfile(r.Context(), user.ID, req.Name, req.Timezone)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, auth.ToUserDTO(u))
}

// delete godoc
//
//	@Summary		Delete my account
//	@Description	Blocked with OWNERSHIP_TRANSFER_REQUIRED while I own a space that has other members.
//	@Tags			me
//	@Security		BearerAuth
//	@Accept			json
//	@Param			body	body	deleteRequest	true	"Password confirmation"
//	@Success		204
//	@Failure		401	{object}	httpx.ErrorEnvelope
//	@Failure		422	{object}	httpx.ErrorEnvelope	"OWNERSHIP_TRANSFER_REQUIRED / INVALID_CURRENT_PASSWORD"
//	@Router			/me [delete]
func (h *Handler) delete(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.UserFromContext(r.Context())
	req, err := httpx.DecodeAndValidate[deleteRequest](r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if err := h.svc.Delete(r.Context(), user, req.Password); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
