package httpx

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"sync"

	"github.com/go-playground/validator/v10"

	"github.com/JonathanRainee/hzzt/internal/platform/apperror"
)

var (
	validatorOnce sync.Once
	validate      *validator.Validate
)

func validatorInstance() *validator.Validate {
	validatorOnce.Do(func() {
		validate = validator.New(validator.WithRequiredStructEnabled())
		// Report field errors under their JSON names.
		validate.RegisterTagNameFunc(func(f reflect.StructField) string {
			name := strings.SplitN(f.Tag.Get("json"), ",", 2)[0]
			if name == "-" {
				return ""
			}
			if name == "" {
				return f.Name
			}
			return name
		})
	})
	return validate
}

// Validate runs struct-tag validation and returns a VALIDATION_FAILED
// AppError whose details are {field: message}.
func Validate(v any) error {
	err := validatorInstance().Struct(v)
	if err == nil {
		return nil
	}
	var verrs validator.ValidationErrors
	if !errors.As(err, &verrs) {
		return apperror.Validation("Validation failed", nil).WithCause(err)
	}
	fields := make(map[string]any, len(verrs))
	for _, fe := range verrs {
		fields[fieldPath(fe)] = fieldMessage(fe)
	}
	return apperror.Validation("Validation failed", fields)
}

// fieldPath drops the leading struct name from the namespace.
func fieldPath(fe validator.FieldError) string {
	ns := fe.Namespace()
	if i := strings.Index(ns, "."); i >= 0 {
		return ns[i+1:]
	}
	return ns
}

func fieldMessage(fe validator.FieldError) string {
	switch fe.Tag() {
	case "required":
		return "is required"
	case "min":
		return "must be at least " + fe.Param()
	case "max":
		return "must be at most " + fe.Param()
	case "oneof":
		return "must be one of: " + fe.Param()
	case "email":
		return "must be a valid email address"
	case "uuid", "uuid4":
		return "must be a valid UUID"
	default:
		return "is invalid (" + fe.Tag() + ")"
	}
}

// readBody reads the (size-limited) request body. A *http.MaxBytesError is
// returned untouched so WriteError maps it to 413.
func readBody(r *http.Request) ([]byte, error) {
	b, err := io.ReadAll(r.Body)
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return nil, err
		}
		return nil, apperror.Validation("Could not read request body", nil).WithCause(err)
	}
	if len(bytes.TrimSpace(b)) == 0 {
		return nil, apperror.Validation("Request body is required", nil)
	}
	return b, nil
}

// strictUnmarshal decodes JSON rejecting unknown fields and trailing data.
func strictUnmarshal(b []byte, dst any) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return apperror.Validation("Malformed JSON body: "+err.Error(), nil).WithCause(err)
	}
	if dec.More() {
		return apperror.Validation("Malformed JSON body: unexpected trailing data", nil)
	}
	return nil
}

// DecodeAndValidate decodes the JSON body into T and validates its struct tags.
// Use pointer fields in T for PATCH bodies so "not sent" stays distinguishable.
func DecodeAndValidate[T any](r *http.Request) (T, error) {
	var v T
	b, err := readBody(r)
	if err != nil {
		return v, err
	}
	if err := strictUnmarshal(b, &v); err != nil {
		return v, err
	}
	return v, Validate(v)
}

// DecodeDiscriminated is the Go equivalent of a Zod discriminated union.
// Pass 1 reads only the discriminator field (e.g. "type"); pass 2 decodes the
// whole body into the concrete struct returned by variants[kind]() — which
// must return a pointer — and validates it.
//
//	type CreateInvestment interface{ isCreateInvestment() }
//	v, err := httpx.DecodeDiscriminated(r, "type", map[string]func() CreateInvestment{
//	    "crypto": func() CreateInvestment { return &CryptoInput{} },
//	})
func DecodeDiscriminated[T any](r *http.Request, field string, variants map[string]func() T) (T, error) {
	var zero T
	b, err := readBody(r)
	if err != nil {
		return zero, err
	}

	var head map[string]json.RawMessage
	if err := json.Unmarshal(b, &head); err != nil {
		return zero, apperror.Validation("Malformed JSON body: "+err.Error(), nil).WithCause(err)
	}
	var kind string
	if raw, ok := head[field]; !ok || json.Unmarshal(raw, &kind) != nil || kind == "" {
		return zero, apperror.Validation("Validation failed", map[string]any{field: "is required"})
	}
	newVariant, ok := variants[kind]
	if !ok {
		allowed := make([]string, 0, len(variants))
		for k := range variants {
			allowed = append(allowed, k)
		}
		return zero, apperror.Validation("Validation failed", map[string]any{
			field: fmt.Sprintf("must be one of: %s", strings.Join(sortedCopy(allowed), " ")),
		})
	}

	v := newVariant()
	if err := strictUnmarshal(b, v); err != nil {
		return zero, err
	}
	return v, Validate(v)
}

func sortedCopy(s []string) []string {
	slices.Sort(s)
	return s
}
