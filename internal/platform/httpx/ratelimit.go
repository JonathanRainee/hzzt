package httpx

import (
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/JonathanRainee/hzzt/internal/platform/apperror"
)

// RateLimiter is an in-memory fixed-window limiter (single instance in v1).
type RateLimiter struct {
	limit  int
	window time.Duration
	now    func() time.Time

	mu      sync.Mutex
	buckets map[string]*bucket
	lastGC  time.Time
}

type bucket struct {
	count   int
	resetAt time.Time
}

// NewRateLimiter allows limit hits per key per window.
func NewRateLimiter(limit int, window time.Duration) *RateLimiter {
	return &RateLimiter{limit: limit, window: window, now: time.Now, buckets: map[string]*bucket{}}
}

// Allow records a hit for key. When over the limit it returns false and how
// long until the window resets.
func (l *RateLimiter) Allow(key string) (ok bool, retryAfter time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()

	if now.Sub(l.lastGC) > l.window { // drop expired buckets so memory stays bounded
		for k, b := range l.buckets {
			if !now.Before(b.resetAt) {
				delete(l.buckets, k)
			}
		}
		l.lastGC = now
	}

	b, found := l.buckets[key]
	if !found || !now.Before(b.resetAt) {
		b = &bucket{resetAt: now.Add(l.window)}
		l.buckets[key] = b
	}
	if b.count >= l.limit {
		return false, b.resetAt.Sub(now)
	}
	b.count++
	return true, 0
}

// Check is Allow that returns a 429 RATE_LIMITED AppError (and sets Retry-After).
func (l *RateLimiter) Check(w http.ResponseWriter, key string) error {
	ok, retry := l.Allow(key)
	if ok {
		return nil
	}
	secs := int(retry.Seconds()) + 1
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	return apperror.RateLimited("Too many requests, try again later").
		WithDetails(map[string]any{"retryAfter": secs})
}

// Middleware rate-limits by keyFn(r).
func (l *RateLimiter) Middleware(keyFn func(*http.Request) string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if err := l.Check(w, keyFn(r)); err != nil {
				WriteError(w, r, err)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// ClientIP returns the peer IP. Proxy headers are deliberately ignored until
// the deployment (P18) defines a trusted proxy.
func ClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
