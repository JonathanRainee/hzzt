package spaces

import (
	"context"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/JonathanRainee/hzzt/internal/auth"
	"github.com/JonathanRainee/hzzt/internal/db/sqlc"
	"github.com/JonathanRainee/hzzt/internal/platform/apperror"
	"github.com/JonathanRainee/hzzt/internal/platform/httpx"
)

// Roles.
const (
	RoleOwner  = "OWNER"
	RoleMember = "MEMBER"
)

type ctxKey int

const (
	spaceKey ctxKey = iota
	roleKey
)

// SpaceFromContext returns the space resolved by Middleware.
func SpaceFromContext(ctx context.Context) (sqlc.Space, bool) {
	s, ok := ctx.Value(spaceKey).(sqlc.Space)
	return s, ok
}

// RoleFromContext returns the caller's role in the resolved space.
func RoleFromContext(ctx context.Context) (string, bool) {
	r, ok := ctx.Value(roleKey).(string)
	return r, ok
}

// SpaceIDFromContext is the id every space-scoped query must filter by.
func SpaceIDFromContext(ctx context.Context) uuid.UUID {
	s, _ := SpaceFromContext(ctx)
	return s.ID
}

// Middleware resolves {spaceId}, requires the authenticated user to be a member
// (404 otherwise, so existence is never leaked) and stores space and role in the
// context. It must run after auth.Service.Middleware.
func (s *Service) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, ok := auth.UserFromContext(r.Context())
		if !ok {
			httpx.WriteError(w, r, apperror.Unauthenticated("Authentication required"))
			return
		}
		id, err := uuid.Parse(chi.URLParam(r, "spaceId"))
		if err != nil {
			httpx.WriteError(w, r, errSpaceNotFound)
			return
		}
		row, err := s.q.GetSpaceMembership(r.Context(), sqlc.GetSpaceMembershipParams{ID: id, UserID: user.ID})
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.WriteError(w, r, errSpaceNotFound)
			return
		}
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		ctx := context.WithValue(r.Context(), spaceKey, spaceFromRow(row))
		ctx = context.WithValue(ctx, roleKey, row.MemberRole)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// RequireRole allows only the listed roles (403 FORBIDDEN otherwise).
// It must run after Middleware.
func RequireRole(roles ...string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			role, _ := RoleFromContext(r.Context())
			for _, allowed := range roles {
				if role == allowed {
					next.ServeHTTP(w, r)
					return
				}
			}
			httpx.WriteError(w, r, apperror.Forbidden("You do not have permission to do this in this space"))
		})
	}
}

func spaceFromRow(r sqlc.GetSpaceMembershipRow) sqlc.Space {
	return sqlc.Space{
		ID: r.ID, Name: r.Name, AvatarEmoji: r.AvatarEmoji, AvatarColor: r.AvatarColor,
		BaseCurrency: r.BaseCurrency, AutoFxConversion: r.AutoFxConversion, TrackingMode: r.TrackingMode,
		BudgetPeriodStartDay: r.BudgetPeriodStartDay, CreatedByID: r.CreatedByID,
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt, DeletedAt: r.DeletedAt,
	}
}
