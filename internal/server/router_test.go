package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/JonathanRainee/hzzt/internal/config"
)

func testRouter() http.Handler {
	r := NewRouter(&config.Config{CORSOrigins: []string{"http://allowed.test"}}, zerolog.Nop(), fakePinger{})
	r.Post("/echo", func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.ReadAll(r.Body); err != nil {
			http.Error(w, err.Error(), http.StatusRequestEntityTooLarge)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	r.Get("/panic", func(http.ResponseWriter, *http.Request) { panic("boom") })
	return r
}

func TestCORSAllowlist(t *testing.T) {
	h := testRouter()
	for origin, want := range map[string]string{
		"http://allowed.test": "http://allowed.test",
		"http://evil.test":    "",
	} {
		req := httptest.NewRequest(http.MethodOptions, "/echo", nil)
		req.Header.Set("Origin", origin)
		req.Header.Set("Access-Control-Request-Method", "POST")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		assert.Equal(t, want, rec.Header().Get("Access-Control-Allow-Origin"), origin)
	}
}

func TestBodyLimit(t *testing.T) {
	h := testRouter()
	req := httptest.NewRequest(http.MethodPost, "/echo", strings.NewReader(strings.Repeat("a", 150*1024)))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
}

func TestRequestIDHeaderAndPanicRecovery(t *testing.T) {
	h := testRouter()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/panic", nil))
	require.Equal(t, http.StatusInternalServerError, rec.Code)
	var env map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env))
	assert.NotEmpty(t, env["requestId"])
	e := env["error"].(map[string]any)
	assert.Equal(t, "INTERNAL_ERROR", e["code"])
	assert.NotContains(t, rec.Body.String(), "boom") // no internal detail leaked
}

func TestNotFoundEnvelope(t *testing.T) {
	rec := httptest.NewRecorder()
	testRouter().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/nope", nil))
	require.Equal(t, http.StatusNotFound, rec.Code)
	var env map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env))
	assert.NotEmpty(t, env["requestId"])
	assert.Equal(t, "NOT_FOUND", env["error"].(map[string]any)["code"])
}

type fakePinger struct{ err error }

func (f fakePinger) Ping(context.Context) (int32, error) { return 1, f.err }

func healthRouter(p Pinger) http.Handler {
	return NewRouter(&config.Config{CORSOrigins: []string{"http://allowed.test"}}, zerolog.Nop(), p)
}

func TestHealthLive(t *testing.T) {
	// Liveness must not depend on the DB: even a failing pinger returns 200.
	rec := httptest.NewRecorder()
	healthRouter(fakePinger{err: errors.New("down")}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/health", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, `{"status":"ok"}`, rec.Body.String())
}

func TestHealthReady(t *testing.T) {
	rec := httptest.NewRecorder()
	healthRouter(fakePinger{}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/health/ready", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, `{"status":"ok"}`, rec.Body.String())

	rec = httptest.NewRecorder()
	healthRouter(fakePinger{err: errors.New("conn refused")}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/health/ready", nil))
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.Contains(t, rec.Body.String(), "DATABASE_UNAVAILABLE")
	assert.NotContains(t, rec.Body.String(), "conn refused")
}
