package config

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func valid() map[string]string {
	return map[string]string{
		"DATABASE_URL":      "postgres://x",
		"JWT_ACCESS_SECRET": "secret",
		"CORS_ORIGINS":      "http://a.com, http://b.com",
		"APP_URL":           "http://localhost:3000",
	}
}

func TestLoadDefaults(t *testing.T) {
	c, err := LoadFrom(env(valid()))
	require.NoError(t, err)
	assert.Equal(t, 8080, c.Port)
	assert.Equal(t, 15*time.Minute, c.JWTAccessTTL)
	assert.Equal(t, 30, c.RefreshTTLDays)
	assert.Equal(t, []string{"http://a.com", "http://b.com"}, c.CORSOrigins)
	assert.Equal(t, "EOD", c.MassiveMode)
	assert.False(t, c.IsProduction())
}

func TestLoadReportsAllProblems(t *testing.T) {
	_, err := LoadFrom(env(map[string]string{"PORT": "abc", "JWT_ACCESS_TTL": "x"}))
	require.Error(t, err)
	var ce *Error
	require.True(t, errors.As(err, &ce))
	assert.True(t, errors.Is(err, ErrInvalid))
	assert.Contains(t, err.Error(), "DATABASE_URL is required")
	assert.Contains(t, err.Error(), "JWT_ACCESS_SECRET is required")
	assert.Contains(t, err.Error(), "PORT must be")
	assert.Contains(t, err.Error(), "JWT_ACCESS_TTL must be")
	assert.Contains(t, err.Error(), "CORS_ORIGINS is required")
}

func TestResendRequiresKey(t *testing.T) {
	m := valid()
	m["EMAIL_DRIVER"] = "resend"
	_, err := LoadFrom(env(m))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "RESEND_API_KEY")
}

func TestProductionSecretLength(t *testing.T) {
	m := valid()
	m["ENV"] = "production"
	_, err := LoadFrom(env(m))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "at least 32")
}

func TestInvalidEnums(t *testing.T) {
	m := valid()
	m["MASSIVE_MODE"] = "weird"
	m["EMAIL_DRIVER"] = "smtp"
	_, err := LoadFrom(env(m))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "MASSIVE_MODE")
	assert.Contains(t, err.Error(), "EMAIL_DRIVER")
}
