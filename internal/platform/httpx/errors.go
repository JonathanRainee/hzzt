package httpx

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"runtime/debug"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/rs/zerolog"

	"github.com/JonathanRainee/hzzt/internal/platform/apperror"
)

// ErrorBody is the error object inside the envelope.
type ErrorBody struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details"`
}

// ErrorEnvelope is the JSON shape of every error response.
type ErrorEnvelope struct {
	Error     ErrorBody `json:"error"`
	RequestID string    `json:"requestId"`
}

// WriteError writes the standard error envelope and logs the error.
// Unrecognized errors become 500 INTERNAL_ERROR with no detail leaked.
func WriteError(w http.ResponseWriter, r *http.Request, err error) {
	ae, ok := apperror.As(err)
	if !ok {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			ae = apperror.New(http.StatusRequestEntityTooLarge, apperror.CodePayloadTooLarge,
				"Request body too large").WithCause(err)
		} else {
			ae = apperror.Internal().WithCause(err)
		}
	}

	log := zerolog.Ctx(r.Context())
	ev := log.Warn()
	if ae.HTTPStatus >= 500 {
		ev = log.Error()
	}
	ev.Err(err).
		Str("requestId", middleware.GetReqID(r.Context())).
		Str("code", ae.Code).
		Int("status", ae.HTTPStatus).
		Msg("request failed")

	details := ae.Details
	if details == nil {
		details = map[string]any{}
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(ae.HTTPStatus)
	_ = json.NewEncoder(w).Encode(ErrorEnvelope{
		Error:     ErrorBody{Code: ae.Code, Message: ae.Message, Details: details},
		RequestID: middleware.GetReqID(r.Context()),
	})
}

// Recoverer converts panics into the 500 envelope instead of crashing.
// It must run after the request ID middleware so the ID is included.
func Recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			rec := recover()
			if rec == nil {
				return
			}
			if rec == http.ErrAbortHandler {
				panic(rec)
			}
			zerolog.Ctx(r.Context()).Error().
				Str("requestId", middleware.GetReqID(r.Context())).
				Str("stack", string(debug.Stack())).
				Msg("panic recovered")
			WriteError(w, r, apperror.Internal().WithCause(fmt.Errorf("panic: %v", rec)))
		}()
		next.ServeHTTP(w, r)
	})
}

// NotFoundHandler returns the 404 envelope for unknown routes.
func NotFoundHandler(w http.ResponseWriter, r *http.Request) {
	WriteError(w, r, apperror.NotFound("Route not found"))
}

// MethodNotAllowedHandler returns the 405 envelope.
func MethodNotAllowedHandler(w http.ResponseWriter, r *http.Request) {
	WriteError(w, r, apperror.New(http.StatusMethodNotAllowed, apperror.CodeMethodNotAllowed, "Method not allowed"))
}
