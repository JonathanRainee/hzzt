package auth

import (
	"context"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/JonathanRainee/hzzt/internal/db/sqlc"
	"github.com/JonathanRainee/hzzt/internal/platform/apperror"
	"github.com/JonathanRainee/hzzt/internal/platform/httpx"
)

type ctxKey int

const (
	userKey ctxKey = iota
	familyKey
)

// UserFromContext returns the authenticated user set by Middleware.
func UserFromContext(ctx context.Context) (sqlc.User, bool) {
	u, ok := ctx.Value(userKey).(sqlc.User)
	return u, ok
}

// FamilyFromContext returns the refresh-token family of the current session.
func FamilyFromContext(ctx context.Context) (uuid.UUID, bool) {
	f, ok := ctx.Value(familyKey).(uuid.UUID)
	return f, ok
}

// Middleware verifies the bearer token, loads the (non-deleted) user and puts
// it in the request context. Apply it to a route group; routes outside that
// group (register, login, refresh, ...) are public by construction.
func (s *Service) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := r.Header.Get("Authorization")
		scheme, token, ok := strings.Cut(h, " ")
		if !ok || !strings.EqualFold(scheme, "Bearer") || strings.TrimSpace(token) == "" {
			httpx.WriteError(w, r, apperror.Unauthenticated("Missing or malformed Authorization header"))
			return
		}
		claims, err := s.tokens.ParseAccess(strings.TrimSpace(token))
		if err != nil {
			httpx.WriteError(w, r, apperror.Unauthenticated("Invalid or expired access token"))
			return
		}
		uid, _ := claims.UserID()
		fam, _ := claims.FamilyID()
		user, err := s.UserByID(r.Context(), uid)
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		ctx := context.WithValue(r.Context(), userKey, user)
		ctx = context.WithValue(ctx, familyKey, fam)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
