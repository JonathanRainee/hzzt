package httpx

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/JonathanRainee/hzzt/internal/platform/apperror"
)

func decode(t *testing.T, rec *httptest.ResponseRecorder) errorEnvelope {
	t.Helper()
	var env errorEnvelope
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env))
	return env
}

func TestWriteErrorAppError(t *testing.T) {
	rec := httptest.NewRecorder()
	err := apperror.Validation("bad input", map[string]any{"email": "required"})
	WriteError(rec, httptest.NewRequest(http.MethodGet, "/", nil), err)
	assert.Equal(t, 400, rec.Code)
	env := decode(t, rec)
	assert.Equal(t, "VALIDATION_FAILED", env.Error.Code)
	assert.Equal(t, "required", env.Error.Details["email"])
}

func TestWriteErrorUnknownIsInternalAndHidden(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteError(rec, httptest.NewRequest(http.MethodGet, "/", nil), errors.New("pq: secret table"))
	assert.Equal(t, 500, rec.Code)
	assert.NotContains(t, rec.Body.String(), "secret")
	assert.Equal(t, "INTERNAL_ERROR", decode(t, rec).Error.Code)
	assert.JSONEq(t, `{}`, string(mustJSON(t, decode(t, rec).Error.Details)))
}

func TestWriteErrorBodyTooLarge(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteError(rec, httptest.NewRequest(http.MethodGet, "/", nil), &http.MaxBytesError{Limit: 1})
	assert.Equal(t, 413, rec.Code)
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return b
}
