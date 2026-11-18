package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

const jwtIssuer = "hzzt"

// Claims are the access-token claims: sub = user ID, fam = refresh family ID.
type Claims struct {
	Family string `json:"fam"`
	jwt.RegisteredClaims
}

// UserID parses the subject.
func (c Claims) UserID() (uuid.UUID, error) { return uuid.Parse(c.Subject) }

// FamilyID parses the refresh family.
func (c Claims) FamilyID() (uuid.UUID, error) { return uuid.Parse(c.Family) }

// TokenIssuer signs and verifies access JWTs (HS256).
type TokenIssuer struct {
	secret []byte
	ttl    time.Duration
	now    func() time.Time
}

// NewTokenIssuer creates an issuer. now may be nil (uses time.Now).
func NewTokenIssuer(secret string, ttl time.Duration, now func() time.Time) *TokenIssuer {
	if now == nil {
		now = time.Now
	}
	return &TokenIssuer{secret: []byte(secret), ttl: ttl, now: now}
}

// TTL is the access token lifetime.
func (t *TokenIssuer) TTL() time.Duration { return t.ttl }

// IssueAccess signs an access token for the user within a refresh family.
func (t *TokenIssuer) IssueAccess(userID, familyID uuid.UUID) (string, error) {
	now := t.now()
	claims := Claims{
		Family: familyID.String(),
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   userID.String(),
			Issuer:    jwtIssuer,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(t.ttl)),
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(t.secret)
}

// ErrInvalidToken is returned for any unusable access token (bad signature, expired, malformed).
var ErrInvalidToken = errors.New("invalid access token")

// ParseAccess verifies signature, issuer and expiry.
func (t *TokenIssuer) ParseAccess(token string) (*Claims, error) {
	var claims Claims
	_, err := jwt.ParseWithClaims(token, &claims,
		func(*jwt.Token) (any, error) { return t.secret, nil },
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithIssuer(jwtIssuer),
		jwt.WithExpirationRequired(),
		jwt.WithTimeFunc(t.now),
	)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidToken, err)
	}
	if _, err := claims.UserID(); err != nil {
		return nil, ErrInvalidToken
	}
	if _, err := claims.FamilyID(); err != nil {
		return nil, ErrInvalidToken
	}
	return &claims, nil
}

// NewOpaqueToken returns 32 random bytes, base64url-encoded, plus its stored hash.
// Used for refresh and password-reset tokens: only the hash is persisted.
func NewOpaqueToken() (plain, hash string, err error) {
	b := make([]byte, 32)
	if _, err = rand.Read(b); err != nil {
		return "", "", fmt.Errorf("generate token: %w", err)
	}
	plain = base64.RawURLEncoding.EncodeToString(b)
	return plain, HashToken(plain), nil
}

// HashToken is the sha256 hex digest stored in the database.
func HashToken(plain string) string {
	sum := sha256.Sum256([]byte(plain))
	return hex.EncodeToString(sum[:])
}
