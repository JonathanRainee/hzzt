//go:build e2e

package e2e

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/JonathanRainee/hzzt/internal/users"
)

type prefsBody struct {
	HideAmounts    bool   `json:"hideAmounts"`
	KeyboardType   string `json:"keyboardType"`
	AppLockEnabled bool   `json:"appLockEnabled"`
}

const pinURL = "/api/v1/me/app-lock"

func (s *TestServer) verifyPIN(u session, pin string) Response {
	return s.Do(http.MethodPost, pinURL+"/verify", map[string]any{"pin": pin}, bearer(u.AccessToken))
}

func (s *TestServer) enableLock(u session, pin string) {
	s.t.Helper()
	res := s.Do(http.MethodPost, pinURL, map[string]any{"pin": pin, "password": goodPassword}, bearer(u.AccessToken))
	require.Equal(s.t, http.StatusNoContent, res.Status, string(res.Body))
}

func retryAfter(t *testing.T, res Response) float64 {
	t.Helper()
	var e struct {
		Error struct {
			Details map[string]any `json:"details"`
		} `json:"error"`
	}
	res.JSON(t, &e)
	v, ok := e.Error.Details["retryAfter"].(float64)
	require.True(t, ok, "retryAfter missing: %s", res.Body)
	return v
}

func TestPreferences(t *testing.T) {
	s := NewTestServer(t)
	u := s.register("jane@example.com")

	t.Run("unauthenticated", func(t *testing.T) {
		assert.Equal(t, http.StatusUnauthorized, s.Get("/api/v1/me/preferences").Status)
		assert.Equal(t, http.StatusUnauthorized, s.Do(http.MethodPatch, "/api/v1/me/preferences", map[string]any{}).Status)
		assert.Equal(t, http.StatusUnauthorized, s.verifyPIN(session{}, "1234").Status)
	})

	t.Run("defaults exist for a new user", func(t *testing.T) {
		var n int
		require.NoError(t, s.Tx.QueryRow(context.Background(),
			`SELECT count(*) FROM user_preferences WHERE user_id = $1`, u.User.ID).Scan(&n))
		assert.Equal(t, 1, n, "row must be created on user.registered")

		res := s.Get("/api/v1/me/preferences", bearer(u.AccessToken))
		require.Equal(t, http.StatusOK, res.Status)
		var p prefsBody
		res.JSON(t, &p)
		assert.Equal(t, prefsBody{HideAmounts: false, KeyboardType: "MINI_CALCULATOR", AppLockEnabled: false}, p)
	})

	t.Run("patch is partial", func(t *testing.T) {
		res := s.Do(http.MethodPatch, "/api/v1/me/preferences", map[string]any{"hideAmounts": true}, bearer(u.AccessToken))
		require.Equal(t, http.StatusOK, res.Status, string(res.Body))
		var p prefsBody
		res.JSON(t, &p)
		assert.True(t, p.HideAmounts)
		assert.Equal(t, "MINI_CALCULATOR", p.KeyboardType)

		res = s.Do(http.MethodPatch, "/api/v1/me/preferences", map[string]any{"keyboardType": "STANDARD_NUMERIC"}, bearer(u.AccessToken))
		require.Equal(t, http.StatusOK, res.Status)
		res.JSON(t, &p)
		assert.True(t, p.HideAmounts, "earlier value kept")
		assert.Equal(t, "STANDARD_NUMERIC", p.KeyboardType)
	})

	t.Run("validation", func(t *testing.T) {
		for name, body := range map[string]map[string]any{
			"bad enum":      {"keyboardType": "QWERTY"},
			"unknown field": {"appLockEnabled": true},
			"wrong type":    {"hideAmounts": "yes"},
		} {
			res := s.Do(http.MethodPatch, "/api/v1/me/preferences", body, bearer(u.AccessToken))
			assert.Equal(t, http.StatusBadRequest, res.Status, "%s: %s", name, res.Body)
		}
	})

	t.Run("preferences are per user", func(t *testing.T) {
		other := s.register("other@example.com")
		res := s.Get("/api/v1/me/preferences", bearer(other.AccessToken))
		var p prefsBody
		res.JSON(t, &p)
		assert.False(t, p.HideAmounts)
	})
}

func TestAppLockEnableDisable(t *testing.T) {
	s := NewTestServer(t)
	u := s.register("jane@example.com")

	t.Run("verify before enabling", func(t *testing.T) {
		res := s.verifyPIN(u, "1234")
		require.Equal(t, http.StatusUnprocessableEntity, res.Status)
		assert.Equal(t, "APP_LOCK_NOT_ENABLED", errCode(t, res))
	})

	t.Run("enable validation", func(t *testing.T) {
		for name, body := range map[string]map[string]any{
			"short pin":     {"pin": "123", "password": goodPassword},
			"long pin":      {"pin": "1234567", "password": goodPassword},
			"non-digit pin": {"pin": "12a4", "password": goodPassword},
			"missing pw":    {"pin": "1234"},
			"missing pin":   {"password": goodPassword},
		} {
			res := s.Do(http.MethodPost, pinURL, body, bearer(u.AccessToken))
			assert.Equal(t, http.StatusBadRequest, res.Status, "%s: %s", name, res.Body)
		}
		res := s.Do(http.MethodPost, pinURL, map[string]any{"pin": "1234", "password": "wrong password!"}, bearer(u.AccessToken))
		require.Equal(t, http.StatusUnprocessableEntity, res.Status)
		assert.Equal(t, "INVALID_CURRENT_PASSWORD", errCode(t, res))
	})

	t.Run("enable, hash never leaks", func(t *testing.T) {
		s.enableLock(u, "123456")
		res := s.Get("/api/v1/me/preferences", bearer(u.AccessToken))
		var p prefsBody
		res.JSON(t, &p)
		assert.True(t, p.AppLockEnabled)
		body := strings.ToLower(string(res.Body))
		assert.NotContains(t, body, "hash")
		assert.NotContains(t, body, "argon2")
		assert.NotContains(t, body, "123456")

		var stored string
		require.NoError(t, s.Tx.QueryRow(context.Background(),
			`SELECT app_lock_pin_hash FROM user_preferences WHERE user_id = $1`, u.User.ID).Scan(&stored))
		assert.True(t, strings.HasPrefix(stored, "$argon2id$"))

		res = s.Do(http.MethodPost, pinURL, map[string]any{"pin": "1111", "password": goodPassword}, bearer(u.AccessToken))
		require.Equal(t, http.StatusConflict, res.Status)
		assert.Equal(t, "APP_LOCK_ALREADY_ENABLED", errCode(t, res))
	})

	t.Run("verify", func(t *testing.T) {
		res := s.verifyPIN(u, "123456")
		require.Equal(t, http.StatusOK, res.Status)
		var v struct{ Valid bool }
		res.JSON(t, &v)
		assert.True(t, v.Valid)

		res = s.verifyPIN(u, "000000")
		require.Equal(t, http.StatusOK, res.Status)
		res.JSON(t, &v)
		assert.False(t, v.Valid)
	})

	t.Run("disable requires the password", func(t *testing.T) {
		res := s.Do(http.MethodDelete, pinURL, map[string]any{"password": "wrong password!"}, bearer(u.AccessToken))
		require.Equal(t, http.StatusUnprocessableEntity, res.Status)
		assert.Equal(t, "INVALID_CURRENT_PASSWORD", errCode(t, res))
		assert.Equal(t, http.StatusBadRequest, s.Do(http.MethodDelete, pinURL, map[string]any{}, bearer(u.AccessToken)).Status)

		res = s.Do(http.MethodDelete, pinURL, map[string]any{"password": goodPassword}, bearer(u.AccessToken))
		require.Equal(t, http.StatusNoContent, res.Status, string(res.Body))
		res = s.Do(http.MethodDelete, pinURL, map[string]any{"password": goodPassword}, bearer(u.AccessToken))
		require.Equal(t, http.StatusUnprocessableEntity, res.Status)
		assert.Equal(t, "APP_LOCK_NOT_ENABLED", errCode(t, res))
		assert.Equal(t, "APP_LOCK_NOT_ENABLED", errCode(t, s.verifyPIN(u, "123456")))
	})
}

func TestAppLockLockout(t *testing.T) {
	s := NewTestServer(t)
	u := s.register("jane@example.com")
	s.enableLock(u, "4321")

	// four wrong attempts, then a success resets the counter
	for i := 0; i < 4; i++ {
		require.Equal(t, http.StatusOK, s.verifyPIN(u, "0000").Status)
	}
	assert.Equal(t, http.StatusOK, s.verifyPIN(u, "4321").Status)

	// five consecutive wrong attempts all answer valid=false …
	for i := 0; i < 5; i++ {
		res := s.verifyPIN(u, "0000")
		require.Equal(t, http.StatusOK, res.Status, "attempt %d: %s", i+1, res.Body)
	}
	// … and the 6th is locked out, even with the right PIN
	for _, pin := range []string{"0000", "4321"} {
		res := s.verifyPIN(u, pin)
		require.Equal(t, http.StatusTooManyRequests, res.Status, string(res.Body))
		assert.Equal(t, "APP_LOCK_TEMPORARILY_LOCKED", errCode(t, res))
		ra := retryAfter(t, res)
		assert.Greater(t, ra, 0.0)
		assert.LessOrEqual(t, ra, 301.0)
	}

	t.Run("change pin is locked too", func(t *testing.T) {
		res := s.Do(http.MethodPatch, pinURL+"/pin", map[string]any{"currentPin": "4321", "newPin": "9999"}, bearer(u.AccessToken))
		require.Equal(t, http.StatusTooManyRequests, res.Status)
		assert.Equal(t, "APP_LOCK_TEMPORARILY_LOCKED", errCode(t, res))
	})

	t.Run("lock expires", func(t *testing.T) {
		s.Clock.Advance(5*time.Minute + time.Second)
		res := s.verifyPIN(u, "4321")
		require.Equal(t, http.StatusOK, res.Status, string(res.Body))
		var v struct{ Valid bool }
		res.JSON(t, &v)
		assert.True(t, v.Valid)
	})

	t.Run("a fresh set of attempts after expiry", func(t *testing.T) {
		for i := 0; i < 5; i++ {
			require.Equal(t, http.StatusOK, s.verifyPIN(u, "0000").Status)
		}
		assert.Equal(t, http.StatusTooManyRequests, s.verifyPIN(u, "0000").Status)
		s.Clock.Advance(6 * time.Minute)
		// a single failure after expiry does not re-lock immediately
		assert.Equal(t, http.StatusOK, s.verifyPIN(u, "0000").Status)
		assert.Equal(t, http.StatusOK, s.verifyPIN(u, "4321").Status)
	})

	t.Run("lockout is per user", func(t *testing.T) {
		other := s.register("other@example.com")
		s.enableLock(other, "1111")
		assert.Equal(t, http.StatusOK, s.verifyPIN(other, "1111").Status)
	})
}

func TestAppLockChangePIN(t *testing.T) {
	s := NewTestServer(t)
	u := s.register("jane@example.com")
	change := func(cur, next string) Response {
		return s.Do(http.MethodPatch, pinURL+"/pin", map[string]any{"currentPin": cur, "newPin": next}, bearer(u.AccessToken))
	}

	assert.Equal(t, "APP_LOCK_NOT_ENABLED", errCode(t, change("1234", "5678")))
	s.enableLock(u, "1234")

	t.Run("validation", func(t *testing.T) {
		assert.Equal(t, http.StatusBadRequest, change("1234", "12").Status)
		assert.Equal(t, http.StatusBadRequest, change("1234", "abcd").Status)
		assert.Equal(t, http.StatusBadRequest, s.Do(http.MethodPatch, pinURL+"/pin", map[string]any{"newPin": "5678"}, bearer(u.AccessToken)).Status)
	})

	t.Run("wrong current pin counts toward lockout", func(t *testing.T) {
		for i := 0; i < 4; i++ {
			res := change("0000", "5678")
			require.Equal(t, http.StatusUnprocessableEntity, res.Status)
			assert.Equal(t, "INVALID_PIN", errCode(t, res))
		}
		// four failures were stored: one more wrong verify is the 5th and locks
		assert.Equal(t, http.StatusOK, s.verifyPIN(u, "0000").Status)
		assert.Equal(t, http.StatusTooManyRequests, s.verifyPIN(u, "1234").Status)
		s.Clock.Advance(6 * time.Minute)
	})

	t.Run("success replaces the pin and resets the counter", func(t *testing.T) {
		require.Equal(t, http.StatusNoContent, change("1234", "5678").Status)
		var v struct{ Valid bool }
		res := s.verifyPIN(u, "5678")
		res.JSON(t, &v)
		assert.True(t, v.Valid)
		res = s.verifyPIN(u, "1234")
		res.JSON(t, &v)
		assert.False(t, v.Valid, "old pin no longer works")
	})
}

func TestVerifyPINRateLimit(t *testing.T) {
	s := NewTestServerWithUserLimits(t, users.DefaultLimits())
	u := s.register("jane@example.com")
	s.enableLock(u, "1234")
	for i := 0; i < 10; i++ {
		require.Equal(t, http.StatusOK, s.verifyPIN(u, "1234").Status, "request %d", i+1)
	}
	res := s.verifyPIN(u, "1234")
	require.Equal(t, http.StatusTooManyRequests, res.Status)
	assert.Equal(t, "RATE_LIMITED", errCode(t, res))
}
