//go:build e2e

// Package e2e holds end-to-end tests that drive the real router over HTTP
// against the test database (TEST_DATABASE_URL, docker service postgres_test).
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/JonathanRainee/hzzt/internal/config"
	"github.com/JonathanRainee/hzzt/internal/db"
	"github.com/JonathanRainee/hzzt/internal/db/sqlc"
	"github.com/JonathanRainee/hzzt/internal/server"
)

var testPool *pgxpool.Pool

// TestMain connects to the test database once and applies migrations.
func TestMain(m *testing.M) {
	os.Exit(run(m))
}

func run(m *testing.M) int {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		fmt.Fprintln(os.Stderr, "e2e: TEST_DATABASE_URL is not set (copy .env.example to .env and run via `make test-e2e`)")
		return 1
	}
	ctx := context.Background()
	pool, err := db.NewPool(ctx, url)
	if err != nil {
		fmt.Fprintf(os.Stderr, "e2e: connect to test database (is `make up` running?): %v\n", err)
		return 1
	}
	defer pool.Close()
	if err := db.Migrate(ctx, pool); err != nil {
		fmt.Fprintf(os.Stderr, "e2e: migrate test database: %v\n", err)
		return 1
	}
	testPool = pool
	return m.Run()
}

// TestServer is a running copy of the API whose database work happens inside
// a single transaction that is rolled back when the test ends.
type TestServer struct {
	*httptest.Server
	Tx pgx.Tx // for seeding and asserting data directly
	t  *testing.T
}

// NewTestServer starts the real router on a per-test transaction.
func NewTestServer(t *testing.T) *TestServer {
	t.Helper()
	ctx := context.Background()

	tx, err := testPool.Begin(ctx)
	require.NoError(t, err)
	t.Cleanup(func() {
		// Roll back with a fresh context: the test context may be done already.
		rctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := tx.Rollback(rctx); err != nil && err != pgx.ErrTxClosed {
			t.Errorf("rollback test transaction: %v", err)
		}
	})

	cfg := &config.Config{
		Env:         "test",
		CORSOrigins: []string{"http://localhost:3000"},
	}
	log := zerolog.Nop()
	srv := httptest.NewServer(server.NewRouter(cfg, log, sqlc.New(tx)))
	t.Cleanup(srv.Close)

	return &TestServer{Server: srv, Tx: tx, t: t}
}

// Response is a fully read HTTP response.
type Response struct {
	Status int
	Body   []byte
}

// JSON decodes the body into v.
func (r Response) JSON(t *testing.T, v any) {
	t.Helper()
	require.NoError(t, json.Unmarshal(r.Body, v), "body: %s", r.Body)
}

// Do sends a request; body (if non-nil) is JSON-encoded. headers are optional.
func (s *TestServer) Do(method, path string, body any, headers ...[2]string) Response {
	s.t.Helper()
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		require.NoError(s.t, err)
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, s.URL+path, rdr)
	require.NoError(s.t, err)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for _, h := range headers {
		req.Header.Set(h[0], h[1])
	}
	res, err := s.Client().Do(req)
	require.NoError(s.t, err)
	defer func() { _ = res.Body.Close() }()
	b, err := io.ReadAll(res.Body)
	require.NoError(s.t, err)
	return Response{Status: res.StatusCode, Body: b}
}

// Get is shorthand for Do(GET, path, nil).
func (s *TestServer) Get(path string, headers ...[2]string) Response {
	s.t.Helper()
	return s.Do(http.MethodGet, path, nil, headers...)
}
