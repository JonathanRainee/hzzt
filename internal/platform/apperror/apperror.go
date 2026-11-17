// Package apperror defines the single error type handlers return.
package apperror

import (
	"errors"
	"fmt"
	"net/http"
)

// Common error codes (SCREAMING_SNAKE_CASE). Domain packages define their
// own codes as constants and list them in the plan's Appendix B.
const (
	CodeValidationFailed = "VALIDATION_FAILED"
	CodeUnauthenticated  = "UNAUTHENTICATED"
	CodeForbidden        = "FORBIDDEN"
	CodeNotFound         = "NOT_FOUND"
	CodeMethodNotAllowed = "METHOD_NOT_ALLOWED"
	CodePayloadTooLarge  = "PAYLOAD_TOO_LARGE"
	CodeConflict         = "CONFLICT"
	CodeUnprocessable    = "UNPROCESSABLE"
	CodeRateLimited      = "RATE_LIMITED"
	CodeUpstream         = "UPSTREAM_PROVIDER_ERROR"
	CodeInternal         = "INTERNAL_ERROR"
)

// AppError is an error that knows how to present itself over HTTP.
type AppError struct {
	Code       string
	HTTPStatus int
	Message    string
	Details    map[string]any
	// Err is the wrapped internal cause. It is logged, never sent to clients.
	Err error
}

func (e *AppError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.Err)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func (e *AppError) Unwrap() error { return e.Err }

// New builds an AppError.
func New(status int, code, message string) *AppError {
	return &AppError{Code: code, HTTPStatus: status, Message: message}
}

// WithDetails returns a copy with details attached.
func (e *AppError) WithDetails(d map[string]any) *AppError {
	c := *e
	c.Details = d
	return &c
}

// WithCause returns a copy wrapping an internal cause (logged only).
func (e *AppError) WithCause(err error) *AppError {
	c := *e
	c.Err = err
	return &c
}

// As extracts an *AppError from err's chain.
func As(err error) (*AppError, bool) {
	var ae *AppError
	if errors.As(err, &ae) {
		return ae, true
	}
	return nil, false
}

func Validation(message string, fields map[string]any) *AppError {
	return New(http.StatusBadRequest, CodeValidationFailed, message).WithDetails(fields)
}
func Unauthenticated(message string) *AppError {
	return New(http.StatusUnauthorized, CodeUnauthenticated, message)
}
func Forbidden(message string) *AppError {
	return New(http.StatusForbidden, CodeForbidden, message)
}
func NotFound(message string) *AppError {
	return New(http.StatusNotFound, CodeNotFound, message)
}
func Conflict(code, message string) *AppError { return New(http.StatusConflict, code, message) }
func Unprocessable(code, message string) *AppError {
	return New(http.StatusUnprocessableEntity, code, message)
}
func RateLimited(message string) *AppError {
	return New(http.StatusTooManyRequests, CodeRateLimited, message)
}
func Upstream(message string) *AppError {
	return New(http.StatusBadGateway, CodeUpstream, message)
}
func Internal() *AppError {
	return New(http.StatusInternalServerError, CodeInternal, "Internal server error")
}
