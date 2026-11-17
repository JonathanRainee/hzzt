package httpx

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
)

// Cursor is the keyset-pagination position: the last row's (createdAt, id).
// For lists sorted by another timestamp (e.g. occurred_at) put that value in
// CreatedAt; the name is kept to match the documented cursor shape.
type Cursor struct {
	CreatedAt time.Time `json:"createdAt"`
	ID        uuid.UUID `json:"id"`
}

// ErrInvalidCursor is returned by DecodeCursor for malformed input.
var ErrInvalidCursor = errors.New("invalid cursor")

// EncodeCursor returns the URL-safe base64 of the cursor JSON.
func EncodeCursor(c Cursor) string {
	c.CreatedAt = c.CreatedAt.UTC()
	b, _ := json.Marshal(c) // cannot fail: only time and UUID fields
	return base64.RawURLEncoding.EncodeToString(b)
}

// DecodeCursor parses a cursor produced by EncodeCursor.
func DecodeCursor(s string) (Cursor, error) {
	var c Cursor
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return c, ErrInvalidCursor
	}
	if err := json.Unmarshal(b, &c); err != nil || c.ID == uuid.Nil || c.CreatedAt.IsZero() {
		return Cursor{}, ErrInvalidCursor
	}
	return c, nil
}
