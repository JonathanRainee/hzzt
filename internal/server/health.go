package server

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/JonathanRainee/hzzt/internal/platform/apperror"
	"github.com/JonathanRainee/hzzt/internal/platform/httpx"
)

// Pinger checks database connectivity (satisfied by sqlc's *Queries).
type Pinger interface {
	Ping(ctx context.Context) (int32, error)
}

const readyTimeout = 2 * time.Second

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// liveness never touches the database.
func liveness(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// readiness runs SELECT 1 through the pool; 503 when the DB is unreachable.
func readiness(p Pinger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), readyTimeout)
		defer cancel()
		if _, err := p.Ping(ctx); err != nil {
			httpx.WriteError(w, r, apperror.New(http.StatusServiceUnavailable,
				"DATABASE_UNAVAILABLE", "Database is not reachable").WithCause(err))
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}
