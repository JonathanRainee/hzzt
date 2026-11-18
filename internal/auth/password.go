package auth

import (
	"strings"
	"sync"

	"github.com/alexedwards/argon2id"

	"github.com/JonathanRainee/hzzt/internal/platform/apperror"
)

// Password policy bounds.
const (
	MinPasswordLen = 8
	MaxPasswordLen = 128
)

// HashPassword hashes a password or PIN with argon2id.
func HashPassword(password string) (string, error) {
	return argon2id.CreateHash(password, argon2id.DefaultParams)
}

// VerifyPassword reports whether password matches hash. Exported so other
// phases (P3 account deletion, P4 app lock) re-check passwords the same way.
func VerifyPassword(password, hash string) bool {
	ok, err := argon2id.ComparePasswordAndHash(password, hash)
	return err == nil && ok
}

// ValidatePasswordPolicy enforces 8–128 characters and rejects a password
// equal to the account email. Returns a VALIDATION_FAILED error keyed by field.
func ValidatePasswordPolicy(field, password, email string) error {
	n := len([]rune(password))
	switch {
	case n < MinPasswordLen:
		return apperror.Validation("Validation failed", map[string]any{field: "must be at least 8 characters"})
	case n > MaxPasswordLen:
		return apperror.Validation("Validation failed", map[string]any{field: "must be at most 128 characters"})
	case email != "" && strings.EqualFold(password, email):
		return apperror.Validation("Validation failed", map[string]any{field: "must not be the same as your email"})
	}
	return nil
}

var (
	dummyOnce sync.Once
	dummyHash string
)

// burnPasswordCheck runs a dummy comparison so login takes the same time
// whether or not the account exists (user-enumeration timing safety).
func burnPasswordCheck(password string) {
	dummyOnce.Do(func() { dummyHash, _ = HashPassword("timing-safety-dummy") })
	_ = VerifyPassword(password, dummyHash)
}
