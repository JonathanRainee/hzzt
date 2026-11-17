package httpx

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/JonathanRainee/hzzt/internal/platform/apperror"
)

func req(body string) *http.Request {
	return httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
}

func TestCursorRoundTrip(t *testing.T) {
	in := Cursor{CreatedAt: time.Date(2026, 1, 2, 3, 4, 5, 6000, time.UTC), ID: uuid.New()}
	out, err := DecodeCursor(EncodeCursor(in))
	require.NoError(t, err)
	assert.True(t, in.CreatedAt.Equal(out.CreatedAt))
	assert.Equal(t, in.ID, out.ID)
}

func TestCursorInvalid(t *testing.T) {
	for _, s := range []string{"", "!!!", "e30", "bm90LWpzb24"} { // "", junk, {}, not-json
		_, err := DecodeCursor(s)
		assert.ErrorIs(t, err, ErrInvalidCursor, s)
	}
}

type createIn struct {
	Name  string  `json:"name" validate:"required,min=2"`
	Kind  string  `json:"kind" validate:"required,oneof=a b"`
	Notes *string `json:"notes" validate:"omitempty,max=5"`
}

func TestDecodeAndValidateOK(t *testing.T) {
	v, err := DecodeAndValidate[createIn](req(`{"name":"abc","kind":"a"}`))
	require.NoError(t, err)
	assert.Equal(t, "abc", v.Name)
	assert.Nil(t, v.Notes)
}

func TestDecodeAndValidateFieldErrors(t *testing.T) {
	_, err := DecodeAndValidate[createIn](req(`{"name":"a","kind":"z"}`))
	ae, ok := apperror.As(err)
	require.True(t, ok)
	assert.Equal(t, 400, ae.HTTPStatus)
	assert.Equal(t, "VALIDATION_FAILED", ae.Code)
	assert.Contains(t, ae.Details, "name")
	assert.Contains(t, ae.Details, "kind")
}

func TestDecodeAndValidateMalformed(t *testing.T) {
	for _, body := range []string{``, `{`, `{"name":"abc","kind":"a","extra":1}`, `{"name":"abc","kind":"a"} {}`} {
		_, err := DecodeAndValidate[createIn](req(body))
		ae, ok := apperror.As(err)
		require.True(t, ok, body)
		assert.Equal(t, "VALIDATION_FAILED", ae.Code, body)
	}
}

func TestDecodeBodyTooLarge(t *testing.T) {
	rec := httptest.NewRecorder()
	r := req(strings.Repeat("x", 100))
	r.Body = http.MaxBytesReader(rec, r.Body, 10)
	_, err := DecodeAndValidate[createIn](r)
	require.Error(t, err)
	_, isApp := apperror.As(err)
	assert.False(t, isApp, "should pass MaxBytesError through for a 413")
	rec2 := httptest.NewRecorder()
	WriteError(rec2, req(""), err)
	assert.Equal(t, 413, rec2.Code)
}

type shape interface{ shape() }
type circle struct {
	Type   string `json:"type"`
	Radius int    `json:"radius" validate:"required,min=1"`
}
type square struct {
	Type string `json:"type"`
	Side int    `json:"side" validate:"required"`
}

func (*circle) shape() {}
func (*square) shape() {}

var shapes = map[string]func() shape{
	"circle": func() shape { return &circle{} },
	"square": func() shape { return &square{} },
}

func TestDecodeDiscriminated(t *testing.T) {
	v, err := DecodeDiscriminated(req(`{"type":"circle","radius":3}`), "type", shapes)
	require.NoError(t, err)
	assert.Equal(t, 3, v.(*circle).Radius)

	v, err = DecodeDiscriminated(req(`{"type":"square","side":2}`), "type", shapes)
	require.NoError(t, err)
	assert.Equal(t, 2, v.(*square).Side)
}

func TestDecodeDiscriminatedErrors(t *testing.T) {
	cases := map[string]string{
		"missing": `{"radius":3}`,
		"unknown": `{"type":"hex"}`,
		"invalid": `{"type":"circle"}`,
		"foreign": `{"type":"circle","radius":3,"side":1}`,
	}
	for name, body := range cases {
		_, err := DecodeDiscriminated(req(body), "type", shapes)
		ae, ok := apperror.As(err)
		require.True(t, ok, name)
		assert.Equal(t, "VALIDATION_FAILED", ae.Code, name)
	}
	_, err := DecodeDiscriminated(req(`{"type":"hex"}`), "type", shapes)
	ae, _ := apperror.As(err)
	assert.Equal(t, "must be one of: circle square", ae.Details["type"])
}
