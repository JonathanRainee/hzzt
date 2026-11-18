package httpx

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestRateLimiterWindow(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	l := NewRateLimiter(2, time.Minute)
	l.now = func() time.Time { return now }

	ok, _ := l.Allow("a")
	assert.True(t, ok)
	ok, _ = l.Allow("a")
	assert.True(t, ok)
	ok, retry := l.Allow("a")
	assert.False(t, ok)
	assert.Equal(t, time.Minute, retry)

	// other keys are independent
	ok, _ = l.Allow("b")
	assert.True(t, ok)

	// window rolls over
	now = now.Add(61 * time.Second)
	ok, _ = l.Allow("a")
	assert.True(t, ok)
}
