// Package server builds the HTTP router and middleware chain.
package server

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/rs/zerolog"
	httpSwagger "github.com/swaggo/http-swagger/v2"

	_ "github.com/JonathanRainee/hzzt/docs" // registers the generated OpenAPI spec

	"github.com/JonathanRainee/hzzt/internal/config"
	"github.com/JonathanRainee/hzzt/internal/platform/httpx"
)

// NewRouter builds the router with the global middleware chain:
// request ID → recoverer → request logger → CORS → body limit.
//
// Still to be added by later tasks/phases: rate limiter, AuthMiddleware (P2) and
// SpaceMemberMiddleware (P3, applied per route group, not globally).
func NewRouter(cfg *config.Config, log zerolog.Logger, db Pinger) *chi.Mux {
	r := chi.NewRouter()

	// RequestID runs first so every envelope (including recovered panics)
	// carries a requestId; Recoverer sits directly after it.
	r.Use(middleware.RequestID)
	r.Use(httpx.Recoverer)
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

	r.Route("/api/v1", func(r chi.Router) {
		r.Get("/health", liveness)
		r.Get("/health/ready", readiness(db))
	})

	// Swagger UI is never exposed in production.
	if !cfg.IsProduction() {
		r.Get("/api/docs", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/api/docs/index.html", http.StatusMovedPermanently)
		})
		r.Get("/api/docs/*", httpSwagger.Handler(httpSwagger.URL("/api/docs/doc.json")))
	}

	r.NotFound(httpx.NotFoundHandler)
	r.MethodNotAllowed(httpx.MethodNotAllowedHandler)

	return r
}
