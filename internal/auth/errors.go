package auth

import (
	"net/http"

	"github.com/JonathanRainee/hzzt/internal/platform/apperror"
)

// Error codes owned by this package (see plan Appendix B).
const (
	CodeEmailAlreadyRegistered = "EMAIL_ALREADY_REGISTERED"
	CodeInvalidCredentials     = "INVALID_CREDENTIALS"
	CodeRefreshTokenReused     = "REFRESH_TOKEN_REUSED"
	CodeRefreshTokenInvalid    = "REFRESH_TOKEN_INVALID"
	CodeResetTokenInvalid      = "RESET_TOKEN_INVALID"
	CodeInvalidCurrentPassword = "INVALID_CURRENT_PASSWORD"
)

var (
	errEmailTaken = apperror.Conflict(CodeEmailAlreadyRegistered, "Email is already registered")
	errBadCreds   = apperror.New(http.StatusUnauthorized, CodeInvalidCredentials, "Invalid email or password")
	errRTReused   = apperror.New(http.StatusUnauthorized, CodeRefreshTokenReused, "Refresh token was already used; all sessions in this family were revoked")
	errRTInvalid  = apperror.New(http.StatusUnauthorized, CodeRefreshTokenInvalid, "Refresh token is invalid or expired")
	errResetBad   = apperror.Unprocessable(CodeResetTokenInvalid, "Reset token is invalid, expired or already used")
	errBadCurrent = apperror.Unprocessable(CodeInvalidCurrentPassword, "Current password is incorrect")
)
