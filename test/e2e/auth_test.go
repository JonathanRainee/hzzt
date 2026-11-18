//go:build e2e

package e2e

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/JonathanRainee/hzzt/internal/auth"
	"github.com/JonathanRainee/hzzt/internal/platform/events"
)

const goodPassword = "correct horse battery"

type authBody struct {
	AccessToken string `json:"accessToken"`
	ExpiresIn   int    `json:"expiresIn"`
	User        struct {
		ID                string  `json:"id"`
		Email             string  `json:"email"`
		Name              string  `json:"name"`
		Timezone          string  `json:"timezone"`
		LastActiveSpaceID *string `json:"lastActiveSpaceId"`
	} `json:"user"`
}

type session struct {
	authBody
	RT string
}

func bearer(token string) [2]string { return [2]string{"Authorization", "Bearer " + token} }
func rtCookie(rt string) [2]string  { return [2]string{"Cookie", "rt=" + rt} }

func (s *TestServer) register(emailAddr string) session {
	s.t.Helper()
	res := s.Do(http.MethodPost, "/api/v1/auth/register", map[string]any{
		"email": emailAddr, "password": goodPassword, "name": "Jane",
	})
	require.Equal(s.t, http.StatusCreated, res.Status, string(res.Body))
	return s.parseSession(res)
}

func (s *TestServer) login(emailAddr, password string) Response {
	return s.Do(http.MethodPost, "/api/v1/auth/login", map[string]any{"email": emailAddr, "password": password})
}

func (s *TestServer) parseSession(res Response) session {
	s.t.Helper()
	var b authBody
	res.JSON(s.t, &b)
	c := res.Cookie("rt")
	require.NotNil(s.t, c, "rt cookie must be set")
	return session{authBody: b, RT: c.Value}
}

func (s *TestServer) refresh(rt string) Response {
	return s.Do(http.MethodPost, "/api/v1/auth/refresh", nil, rtCookie(rt))
}

func errCode(t *testing.T, res Response) string {
	t.Helper()
	var e struct {
		Error struct {
			Code    string         `json:"code"`
			Details map[string]any `json:"details"`
		} `json:"error"`
		RequestID string `json:"requestId"`
	}
	res.JSON(t, &e)
	assert.NotEmpty(t, e.RequestID)
	return e.Error.Code
}

func TestAuthFullFlow(t *testing.T) {
	s := NewTestServer(t)

	reg := s.register("Jane@Example.com ")
	assert.Equal(t, "jane@example.com", reg.User.Email)
	assert.Equal(t, 900, reg.ExpiresIn)
	assert.Equal(t, "UTC", reg.User.Timezone)

	// login → me
	res := s.login("jane@example.com", goodPassword)
	require.Equal(t, http.StatusOK, res.Status, string(res.Body))
	sess := s.parseSession(res)

	res = s.Get("/api/v1/auth/me", bearer(sess.AccessToken))
	require.Equal(t, http.StatusOK, res.Status)
	var me struct {
		ID, Email string
	}
	res.JSON(t, &me)
	assert.Equal(t, sess.User.ID, me.ID)

	// refresh rotates the token
	res = s.refresh(sess.RT)
	require.Equal(t, http.StatusOK, res.Status, string(res.Body))
	next := s.parseSession(res)
	assert.NotEqual(t, sess.RT, next.RT)
	assert.NotEmpty(t, next.AccessToken)

	// reusing the old token is detected and revokes the whole family
	res = s.refresh(sess.RT)
	require.Equal(t, http.StatusUnauthorized, res.Status)
	assert.Equal(t, "REFRESH_TOKEN_REUSED", errCode(t, res))

	res = s.refresh(next.RT)
	require.Equal(t, http.StatusUnauthorized, res.Status, "new token of the revoked family must fail")
	assert.Equal(t, "REFRESH_TOKEN_INVALID", errCode(t, res))
}

func TestRegisterValidationAndDuplicates(t *testing.T) {
	s := NewTestServer(t)

	bad := []struct {
		name string
		body map[string]any
		want string // field expected in details
	}{
		{"bad email", map[string]any{"email": "nope", "password": goodPassword, "name": "J"}, "email"},
		{"short password", map[string]any{"email": "a@b.co", "password": "short", "name": "J"}, "password"},
		{"password equals email", map[string]any{"email": "same@pass.com", "password": "same@pass.com", "name": "J"}, "password"},
		{"empty name", map[string]any{"email": "a@b.co", "password": goodPassword, "name": "  "}, "name"},
		{"long name", map[string]any{"email": "a@b.co", "password": goodPassword, "name": strings.Repeat("x", 61)}, "name"},
		{"bad timezone", map[string]any{"email": "a@b.co", "password": goodPassword, "name": "J", "timezone": "Mars/Base"}, "timezone"},
		{"Local timezone", map[string]any{"email": "a@b.co", "password": goodPassword, "name": "J", "timezone": "Local"}, "timezone"},
		{"missing everything", map[string]any{}, "email"},
	}
	for _, c := range bad {
		t.Run(c.name, func(t *testing.T) {
			res := s.Do(http.MethodPost, "/api/v1/auth/register", c.body)
			require.Equal(t, http.StatusBadRequest, res.Status, string(res.Body))
			var e struct {
				Error struct {
					Code    string         `json:"code"`
					Details map[string]any `json:"details"`
				} `json:"error"`
			}
			res.JSON(t, &e)
			assert.Equal(t, "VALIDATION_FAILED", e.Error.Code)
			assert.Contains(t, e.Error.Details, c.want)
		})
	}

	t.Run("unknown field rejected", func(t *testing.T) {
		res := s.Do(http.MethodPost, "/api/v1/auth/register", map[string]any{
			"email": "x@y.co", "password": goodPassword, "name": "J", "isAdmin": true})
		assert.Equal(t, http.StatusBadRequest, res.Status)
	})

	t.Run("timezone stored", func(t *testing.T) {
		res := s.Do(http.MethodPost, "/api/v1/auth/register", map[string]any{
			"email": "jkt@example.com", "password": goodPassword, "name": "J", "timezone": "Asia/Jakarta"})
		require.Equal(t, http.StatusCreated, res.Status, string(res.Body))
		assert.Equal(t, "Asia/Jakarta", s.parseSession(res).User.Timezone)
	})

	t.Run("duplicate email is case-insensitive", func(t *testing.T) {
		s.register("dupe@example.com")
		res := s.Do(http.MethodPost, "/api/v1/auth/register", map[string]any{
			"email": "DUPE@example.com", "password": goodPassword, "name": "J"})
		require.Equal(t, http.StatusConflict, res.Status)
		assert.Equal(t, "EMAIL_ALREADY_REGISTERED", errCode(t, res))
		// the failed insert must not poison later requests
		assert.Equal(t, http.StatusOK, s.login("dupe@example.com", goodPassword).Status)
	})
}

func TestRegisterSetsHardenedCookieAndNeverLeaksSecrets(t *testing.T) {
	s := NewTestServer(t)
	res := s.Do(http.MethodPost, "/api/v1/auth/register", map[string]any{
		"email": "cookie@example.com", "password": goodPassword, "name": "J"})
	require.Equal(t, http.StatusCreated, res.Status)

	c := res.Cookie("rt")
	require.NotNil(t, c)
	assert.True(t, c.HttpOnly)
	assert.Equal(t, "/api/v1/auth", c.Path)
	assert.Equal(t, http.SameSiteLaxMode, c.SameSite)
	assert.Equal(t, 30*24*3600, c.MaxAge)

	assert.NotContains(t, strings.ToLower(string(res.Body)), "password")
	assert.NotContains(t, string(res.Body), "$argon2id")
	assert.Equal(t, "no-store", res.Header.Get("Cache-Control"))

	// only hashes are stored for refresh tokens
	var n int
	require.NoError(t, s.Tx.QueryRow(context.Background(),
		`SELECT count(*) FROM refresh_tokens WHERE token_hash = $1`, c.Value).Scan(&n))
	assert.Zero(t, n, "plaintext refresh token must not be stored")
	require.NoError(t, s.Tx.QueryRow(context.Background(),
		`SELECT count(*) FROM refresh_tokens WHERE token_hash = $1`, auth.HashToken(c.Value)).Scan(&n))
	assert.Equal(t, 1, n)
}

func TestRegisterEmitsUserRegisteredEvent(t *testing.T) {
	s := NewTestServer(t)
	var got []auth.UserRegistered
	s.Events.Subscribe("user.registered", func(_ context.Context, e events.Event) error {
		got = append(got, e.(auth.UserRegistered))
		return nil
	})
	reg := s.register("event@example.com")
	require.Len(t, got, 1)
	assert.Equal(t, reg.User.ID, got[0].UserID.String())
	assert.Equal(t, "event@example.com", got[0].Email)
}

func TestLoginErrors(t *testing.T) {
	s := NewTestServer(t)
	s.register("login@example.com")

	wrongPw := s.login("login@example.com", "wrong password")
	unknown := s.login("ghost@example.com", "wrong password")
	require.Equal(t, http.StatusUnauthorized, wrongPw.Status)
	require.Equal(t, http.StatusUnauthorized, unknown.Status)
	assert.Equal(t, "INVALID_CREDENTIALS", errCode(t, wrongPw))
	assert.Equal(t, "INVALID_CREDENTIALS", errCode(t, unknown))

	res := s.Do(http.MethodPost, "/api/v1/auth/login", map[string]any{"email": "bad"})
	assert.Equal(t, http.StatusBadRequest, res.Status)

	// email is normalized on login too
	assert.Equal(t, http.StatusOK, s.login("  LOGIN@example.com", goodPassword).Status)
}

func TestMeRequiresValidAccessToken(t *testing.T) {
	s := NewTestServer(t)
	reg := s.register("me@example.com")

	cases := map[string][][2]string{
		"no header":     nil,
		"wrong scheme":  {{"Authorization", "Basic abc"}},
		"garbage token": {bearer("garbage")},
		"wrong secret":  {bearer(mustToken(t, "another-secret-another-secret-123456", reg.User.ID, time.Now()))},
		"expired token": {bearer(mustToken(t, testJWTSecret, reg.User.ID, time.Now().Add(-time.Hour)))},
		"unknown user":  {bearer(mustToken(t, testJWTSecret, uuid.NewString(), time.Now()))},
	}
	for name, hdr := range cases {
		t.Run(name, func(t *testing.T) {
			res := s.Get("/api/v1/auth/me", hdr...)
			require.Equal(t, http.StatusUnauthorized, res.Status)
			assert.Equal(t, "UNAUTHENTICATED", errCode(t, res))
		})
	}

	t.Run("soft-deleted user is rejected", func(t *testing.T) {
		_, err := s.Tx.Exec(context.Background(), `UPDATE users SET deleted_at = now() WHERE id = $1`, reg.User.ID)
		require.NoError(t, err)
		res := s.Get("/api/v1/auth/me", bearer(reg.AccessToken))
		assert.Equal(t, http.StatusUnauthorized, res.Status)
		assert.Equal(t, http.StatusUnauthorized, s.login("me@example.com", goodPassword).Status)
	})
}

// mustToken signs an access token whose "now" is `at` (so it expires 15m after).
func mustToken(t *testing.T, secret, userID string, at time.Time) string {
	t.Helper()
	tok, err := auth.NewTokenIssuer(secret, 15*time.Minute, func() time.Time { return at }).
		IssueAccess(uuid.MustParse(userID), uuid.New())
	require.NoError(t, err)
	return tok
}

func TestRefreshWithoutOrWithBadCookie(t *testing.T) {
	s := NewTestServer(t)
	res := s.Do(http.MethodPost, "/api/v1/auth/refresh", nil)
	assert.Equal(t, http.StatusUnauthorized, res.Status)
	assert.Equal(t, "REFRESH_TOKEN_INVALID", errCode(t, res))

	res = s.refresh("not-a-real-token")
	assert.Equal(t, http.StatusUnauthorized, res.Status)
	assert.Equal(t, "REFRESH_TOKEN_INVALID", errCode(t, res))
}

func TestRefreshExpiredToken(t *testing.T) {
	s := NewTestServer(t)
	reg := s.register("expire@example.com")
	_, err := s.Tx.Exec(context.Background(), `UPDATE refresh_tokens SET expires_at = now() - interval '1 minute'`)
	require.NoError(t, err)
	res := s.refresh(reg.RT)
	require.Equal(t, http.StatusUnauthorized, res.Status)
	assert.Equal(t, "REFRESH_TOKEN_INVALID", errCode(t, res))
}

func TestLogoutRevokesFamilyAndClearsCookie(t *testing.T) {
	s := NewTestServer(t)
	a := s.register("logout@example.com")
	b := s.parseSession(s.login("logout@example.com", goodPassword)) // second device

	res := s.Do(http.MethodPost, "/api/v1/auth/logout", nil, rtCookie(a.RT))
	require.Equal(t, http.StatusNoContent, res.Status)
	c := res.Cookie("rt")
	require.NotNil(t, c)
	assert.Equal(t, -1, c.MaxAge)
	assert.Equal(t, "/api/v1/auth", c.Path)

	assert.Equal(t, http.StatusUnauthorized, s.refresh(a.RT).Status)
	assert.Equal(t, http.StatusOK, s.refresh(b.RT).Status, "other device keeps its session")

	// logout without a cookie is still a clean 204
	assert.Equal(t, http.StatusNoContent, s.Do(http.MethodPost, "/api/v1/auth/logout", nil).Status)
}

func TestLogoutAll(t *testing.T) {
	s := NewTestServer(t)
	a := s.register("all@example.com")
	b := s.parseSession(s.login("all@example.com", goodPassword))

	assert.Equal(t, http.StatusUnauthorized, s.Do(http.MethodPost, "/api/v1/auth/logout-all", nil).Status)

	res := s.Do(http.MethodPost, "/api/v1/auth/logout-all", nil, bearer(a.AccessToken))
	require.Equal(t, http.StatusNoContent, res.Status)
	assert.Equal(t, http.StatusUnauthorized, s.refresh(a.RT).Status)
	assert.Equal(t, http.StatusUnauthorized, s.refresh(b.RT).Status)
}

func TestChangePassword(t *testing.T) {
	s := NewTestServer(t)
	a := s.register("change@example.com")
	b := s.parseSession(s.login("change@example.com", goodPassword))
	path := "/api/v1/auth/change-password"

	assert.Equal(t, http.StatusUnauthorized, s.Do(http.MethodPost, path, map[string]any{
		"currentPassword": goodPassword, "newPassword": "brand new password"}).Status)

	res := s.Do(http.MethodPost, path, map[string]any{"currentPassword": "nope", "newPassword": "brand new password"}, bearer(a.AccessToken))
	require.Equal(t, http.StatusUnprocessableEntity, res.Status)
	assert.Equal(t, "INVALID_CURRENT_PASSWORD", errCode(t, res))

	res = s.Do(http.MethodPost, path, map[string]any{"currentPassword": goodPassword, "newPassword": "short"}, bearer(a.AccessToken))
	assert.Equal(t, http.StatusBadRequest, res.Status)

	res = s.Do(http.MethodPost, path, map[string]any{"currentPassword": goodPassword, "newPassword": "brand new password"}, bearer(a.AccessToken))
	require.Equal(t, http.StatusNoContent, res.Status, string(res.Body))

	assert.Equal(t, http.StatusOK, s.refresh(a.RT).Status, "current session survives")
	assert.Equal(t, http.StatusUnauthorized, s.refresh(b.RT).Status, "other sessions are revoked")
	assert.Equal(t, http.StatusUnauthorized, s.login("change@example.com", goodPassword).Status)
	assert.Equal(t, http.StatusOK, s.login("change@example.com", "brand new password").Status)
}

func (s *TestServer) forgot(emailAddr string) Response {
	return s.Do(http.MethodPost, "/api/v1/auth/forgot-password", map[string]any{"email": emailAddr})
}

func (s *TestServer) reset(token, pw string) Response {
	return s.Do(http.MethodPost, "/api/v1/auth/reset-password", map[string]any{"token": token, "newPassword": pw})
}

func lastResetToken(t *testing.T, s *TestServer) string {
	t.Helper()
	sent := s.Email.Sent()
	require.NotEmpty(t, sent)
	body := sent[len(sent)-1].Body
	i := strings.Index(body, "http://localhost:3000/reset-password?token=")
	require.GreaterOrEqual(t, i, 0, body)
	link := strings.Fields(body[i:])[0]
	return strings.TrimPrefix(link, "http://localhost:3000/reset-password?token=")
}

func TestForgotPasswordIsIdenticalForKnownAndUnknown(t *testing.T) {
	s := NewTestServer(t)
	s.register("known@example.com")

	known := s.forgot("known@example.com")
	unknown := s.forgot("unknown@example.com")
	assert.Equal(t, http.StatusOK, known.Status)
	assert.Equal(t, known.Status, unknown.Status)
	assert.JSONEq(t, string(known.Body), string(unknown.Body))

	sent := s.Email.Sent()
	require.Len(t, sent, 1, "only the registered address gets an email")
	assert.Equal(t, "known@example.com", sent[0].To)
}

func TestResetPasswordFlow(t *testing.T) {
	s := NewTestServer(t)
	reg := s.register("reset@example.com")
	other := s.parseSession(s.login("reset@example.com", goodPassword))

	require.Equal(t, http.StatusOK, s.forgot("reset@example.com").Status)
	token := lastResetToken(t, s)

	var n int
	require.NoError(t, s.Tx.QueryRow(context.Background(),
		`SELECT count(*) FROM password_reset_tokens WHERE token_hash = $1`, token).Scan(&n))
	assert.Zero(t, n, "plaintext reset token must not be stored")

	assert.Equal(t, http.StatusBadRequest, s.reset(token, "short").Status)
	// the failed attempt must not have consumed the token
	res := s.reset(token, "a brand new password")
	require.Equal(t, http.StatusNoContent, res.Status, string(res.Body))

	// all prior sessions fail, old password fails, new works
	assert.Equal(t, http.StatusUnauthorized, s.refresh(reg.RT).Status)
	assert.Equal(t, http.StatusUnauthorized, s.refresh(other.RT).Status)
	assert.Equal(t, http.StatusUnauthorized, s.login("reset@example.com", goodPassword).Status)
	assert.Equal(t, http.StatusOK, s.login("reset@example.com", "a brand new password").Status)

	// single use
	res = s.reset(token, "yet another password")
	require.Equal(t, http.StatusUnprocessableEntity, res.Status)
	assert.Equal(t, "RESET_TOKEN_INVALID", errCode(t, res))
}

func TestResetPasswordTokenExpiryAndGarbage(t *testing.T) {
	s := NewTestServer(t)
	s.register("expiry@example.com")
	require.Equal(t, http.StatusOK, s.forgot("expiry@example.com").Status)
	token := lastResetToken(t, s)

	_, err := s.Tx.Exec(context.Background(), `UPDATE password_reset_tokens SET expires_at = now() - interval '1 minute'`)
	require.NoError(t, err)
	res := s.reset(token, "a brand new password")
	require.Equal(t, http.StatusUnprocessableEntity, res.Status)
	assert.Equal(t, "RESET_TOKEN_INVALID", errCode(t, res))

	res = s.reset("garbage", "a brand new password")
	require.Equal(t, http.StatusUnprocessableEntity, res.Status)
	assert.Equal(t, "RESET_TOKEN_INVALID", errCode(t, res))
}

func TestNewForgotRequestInvalidatesOlderToken(t *testing.T) {
	s := NewTestServer(t)
	s.register("twice@example.com")
	s.forgot("twice@example.com")
	first := lastResetToken(t, s)
	s.forgot("twice@example.com")
	second := lastResetToken(t, s)

	assert.Equal(t, http.StatusUnprocessableEntity, s.reset(first, "a brand new password").Status)
	assert.Equal(t, http.StatusNoContent, s.reset(second, "a brand new password").Status)
}

func TestAuthRateLimits(t *testing.T) {
	s := NewTestServerWithLimits(t, auth.DefaultLimits())

	t.Run("login 5/min per IP+email", func(t *testing.T) {
		for i := 0; i < 5; i++ {
			assert.Equal(t, http.StatusUnauthorized, s.login("rl@example.com", "wrong password").Status)
		}
		res := s.login("rl@example.com", "wrong password")
		require.Equal(t, http.StatusTooManyRequests, res.Status)
		assert.Equal(t, "RATE_LIMITED", errCode(t, res))
		assert.NotEmpty(t, res.Header.Get("Retry-After"))
		// a different email has its own budget
		assert.Equal(t, http.StatusUnauthorized, s.login("other@example.com", "wrong password").Status)
	})

	t.Run("register 5/min per IP", func(t *testing.T) {
		for i := 0; i < 5; i++ {
			s.register(uuid.NewString() + "@example.com")
		}
		res := s.Do(http.MethodPost, "/api/v1/auth/register", map[string]any{
			"email": "sixth@example.com", "password": goodPassword, "name": "J"})
		assert.Equal(t, http.StatusTooManyRequests, res.Status)
	})

	t.Run("forgot-password 3 per 15min per IP", func(t *testing.T) {
		for i := 0; i < 3; i++ {
			assert.Equal(t, http.StatusOK, s.forgot("x@example.com").Status)
		}
		assert.Equal(t, http.StatusTooManyRequests, s.forgot("x@example.com").Status)
	})
}
