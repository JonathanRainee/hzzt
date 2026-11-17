// Package server builds the HTTP router and middleware chain.
package server

import (
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/rs/zerolog"

	"github.com/JonathanRainee/hzzt/internal/config"
	"github.com/JonathanRainee/hzzt/internal/platform/httpx"
)

// NewRouter builds the router with the global middleware chain:
// recoverer → request ID → request logger → CORS → body limit.
//
// Still to be added by later tasks/phases: the custom panic-recovery
// envelope (P1-T06), rate limiter, AuthMiddleware (P2) and
// SpaceMemberMiddleware (P3, applied per route group, not globally).
func NewRouter(cfg *config.Config, log zerolog.Logger) *chi.Mux {
	r := chi.NewRouter()

	r.Use(middleware.Recoverer) // replaced by the envelope-aware recoverer in P1-T06
	r.Use(middleware.RequestID)
	r.Use(httpx.RequestLogger(log))
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   cfg.CORSOrigins,
		AllowedMethods:   []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Authorization", "Content-Type", "X-Request-Id"},
		ExposedHeaders:   []string{"X-Request-Id"},
		AllowCredentials: true,
		MaxAge:           int((10 * time.Minute).Seconds()),
	}))
	r.Use(httpx.BodyLimit(httpx.MaxBodyBytes))

	return r
}
