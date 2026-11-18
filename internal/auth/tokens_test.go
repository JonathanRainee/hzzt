package auth

import (
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAccessTokenRoundTrip(t *testing.T) {
	iss := NewTokenIssuer("secret-secret-secret-secret-1234", 15*time.Minute, nil)
	uid, fam := uuid.New(), uuid.New()

	tok, err := iss.IssueAccess(uid, fam)
	require.NoError(t, err)

	c, err := iss.ParseAccess(tok)
	require.NoError(t, err)
	gotU, _ := c.UserID()
	gotF, _ := c.FamilyID()
	assert.Equal(t, uid, gotU)
	assert.Equal(t, fam, gotF)
}

func TestAccessTokenRejections(t *testing.T) {
	now := time.Now()
	iss := NewTokenIssuer("secret-secret-secret-secret-1234", time.Minute, func() time.Time { return now })
	tok, err := iss.IssueAccess(uuid.New(), uuid.New())
	require.NoError(t, err)

	t.Run("expired", func(t *testing.T) {
		later := NewTokenIssuer("secret-secret-secret-secret-1234", time.Minute, func() time.Time { return now.Add(2 * time.Minute) })
		_, err := later.ParseAccess(tok)
		assert.ErrorIs(t, err, ErrInvalidToken)
	})
	t.Run("wrong secret", func(t *testing.T) {
		other := NewTokenIssuer("another-secret-another-secret-12", time.Minute, func() time.Time { return now })
		_, err := other.ParseAccess(tok)
		assert.ErrorIs(t, err, ErrInvalidToken)
	})
	t.Run("tampered", func(t *testing.T) {
		_, err := iss.ParseAccess(tok[:len(tok)-2] + "xx")
		assert.ErrorIs(t, err, ErrInvalidToken)
	})
	t.Run("alg none", func(t *testing.T) {
		c := Claims{Family: uuid.NewString(), RegisteredClaims: jwt.RegisteredClaims{
			Subject: uuid.NewString(), Issuer: jwtIssuer, ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour)),
		}}
		none, err := jwt.NewWithClaims(jwt.SigningMethodNone, c).SignedString(jwt.UnsafeAllowNoneSignatureType)
		require.NoError(t, err)
		_, err = iss.ParseAccess(none)
		assert.ErrorIs(t, err, ErrInvalidToken)
	})
	t.Run("garbage", func(t *testing.T) {
		_, err := iss.ParseAccess("not-a-jwt")
		assert.ErrorIs(t, err, ErrInvalidToken)
	})
}

func TestOpaqueToken(t *testing.T) {
	p1, h1, err := NewOpaqueToken()
	require.NoError(t, err)
	p2, _, err := NewOpaqueToken()
	require.NoError(t, err)
	assert.NotEqual(t, p1, p2)
	assert.Equal(t, h1, HashToken(p1))
	assert.NotContains(t, h1, p1)
	assert.Len(t, h1, 64)
	assert.False(t, strings.ContainsAny(p1, "+/="), "base64url without padding")
}

func TestPasswordPolicy(t *testing.T) {
	cases := []struct {
		name, pw, email string
		ok              bool
	}{
		{"ok", "correct horse", "a@b.com", true},
		{"too short", "short", "a@b.com", false},
		{"too long", strings.Repeat("a", 129), "a@b.com", false},
		{"max ok", strings.Repeat("a", 128), "a@b.com", true},
		{"equals email", "Person@Example.com", "person@example.com", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := ValidatePasswordPolicy("password", c.pw, c.email)
			assert.Equal(t, c.ok, err == nil)
		})
	}
}

func TestHashAndVerifyPassword(t *testing.T) {
	h, err := HashPassword("correct horse")
	require.NoError(t, err)
	assert.True(t, VerifyPassword("correct horse", h))
	assert.False(t, VerifyPassword("wrong", h))
	assert.False(t, VerifyPassword("x", "not-a-hash"))
}
