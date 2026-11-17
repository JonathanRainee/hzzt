//go:build e2e

package e2e

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHealthLive(t *testing.T) {
	s := NewTestServer(t)
	res := s.Get("/api/v1/health")
	require.Equal(t, http.StatusOK, res.Status)
	assert.JSONEq(t, `{"status":"ok"}`, string(res.Body))
}

func TestHealthReady(t *testing.T) {
	s := NewTestServer(t)
	res := s.Get("/api/v1/health/ready")
	require.Equal(t, http.StatusOK, res.Status)
	assert.JSONEq(t, `{"status":"ok"}`, string(res.Body))
}

func TestUnknownRouteReturnsEnvelope(t *testing.T) {
	s := NewTestServer(t)
	res := s.Get("/api/v1/does-not-exist")
	require.Equal(t, http.StatusNotFound, res.Status)

	var env struct {
		Error struct {
			Code    string         `json:"code"`
			Message string         `json:"message"`
			Details map[string]any `json:"details"`
		} `json:"error"`
		RequestID string `json:"requestId"`
	}
	res.JSON(t, &env)
	assert.Equal(t, "NOT_FOUND", env.Error.Code)
	assert.NotEmpty(t, env.RequestID)
}

func TestWrongMethodReturnsEnvelope(t *testing.T) {
	s := NewTestServer(t)
	res := s.Do(http.MethodPost, "/api/v1/health", nil)
	require.Equal(t, http.StatusMethodNotAllowed, res.Status)
	assert.Contains(t, string(res.Body), "METHOD_NOT_ALLOWED")
}

// Data written through the test transaction must not outlive the test.
func TestTransactionIsolationBetweenTests(t *testing.T) {
	const table = "e2e_isolation_probe"
	t.Run("write", func(t *testing.T) {
		s := NewTestServer(t)
		_, err := s.Tx.Exec(context.Background(), "CREATE TABLE "+table+" (id int)")
		require.NoError(t, err)
	})
	t.Run("rolled back", func(t *testing.T) {
		s := NewTestServer(t)
		var exists bool
		require.NoError(t, s.Tx.QueryRow(context.Background(),
			"SELECT to_regclass($1) IS NOT NULL", table).Scan(&exists))
		assert.False(t, exists)
	})
}
