// Package config parses and validates environment variables at startup.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds all runtime configuration.
type Config struct {
	Env  string
	Port int

	DatabaseURL string

	JWTAccessSecret string
	JWTAccessTTL    time.Duration
	RefreshTTLDays  int

	CORSOrigins []string
	AppURL      string

	EmailDriver  string
	ResendAPIKey string

	CMCAPIKey     string
	MassiveAPIKey string
	MassiveMode   string

	FXProvider string
}

// IsProduction reports whether the app runs in production.
func (c Config) IsProduction() bool { return c.Env == "production" }

// Error lists every problem found while loading configuration.
type Error struct{ Problems []string }

func (e *Error) Error() string {
	return "invalid configuration:\n  - " + strings.Join(e.Problems, "\n  - ")
}

// Load reads configuration from the process environment.
func Load() (*Config, error) { return LoadFrom(os.Getenv) }

// LoadFrom reads configuration using getenv, collecting all problems
// into a single *Error instead of failing on the first one.
func LoadFrom(getenv func(string) string) (*Config, error) {
	var problems []string
	fail := func(format string, a ...any) { problems = append(problems, fmt.Sprintf(format, a...)) }

	str := func(key, def string) string {
		if v := strings.TrimSpace(getenv(key)); v != "" {
			return v
		}
		return def
	}
	required := func(key string) string {
		v := str(key, "")
		if v == "" {
			fail("%s is required", key)
		}
		return v
	}

	c := &Config{
		Env:           str("ENV", "development"),
		DatabaseURL:   required("DATABASE_URL"),
		AppURL:        required("APP_URL"),
		EmailDriver:   str("EMAIL_DRIVER", "console"),
		ResendAPIKey:  str("RESEND_API_KEY", ""),
		CMCAPIKey:     str("CMC_API_KEY", ""),
		MassiveAPIKey: str("MASSIVE_API_KEY", ""),
		MassiveMode:   strings.ToUpper(str("MASSIVE_MODE", "EOD")),
		FXProvider:    str("FX_PROVIDER", "frankfurter"),
	}

	// Port
	c.Port = 8080
	if v := str("PORT", ""); v != "" {
		p, err := strconv.Atoi(v)
		if err != nil || p < 1 || p > 65535 {
			fail("PORT must be an integer between 1 and 65535, got %q", v)
		} else {
			c.Port = p
		}
	}

	// JWT
	c.JWTAccessSecret = required("JWT_ACCESS_SECRET")
	if c.JWTAccessSecret != "" && len(c.JWTAccessSecret) < 32 && c.IsProduction() {
		fail("JWT_ACCESS_SECRET must be at least 32 characters in production")
	}
	c.JWTAccessTTL = 15 * time.Minute
	if v := str("JWT_ACCESS_TTL", ""); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			fail("JWT_ACCESS_TTL must be a positive duration like 15m, got %q", v)
		} else {
			c.JWTAccessTTL = d
		}
	}
	c.RefreshTTLDays = 30
	if v := str("REFRESH_TTL_DAYS", ""); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			fail("REFRESH_TTL_DAYS must be a positive integer, got %q", v)
		} else {
			c.RefreshTTLDays = n
		}
	}

	// URLs
	if c.AppURL != "" {
		if u, err := url.Parse(c.AppURL); err != nil || u.Scheme == "" || u.Host == "" {
			fail("APP_URL must be an absolute URL, got %q", c.AppURL)
		}
	}
	for _, o := range strings.Split(str("CORS_ORIGINS", ""), ",") {
		if o = strings.TrimSpace(o); o != "" {
			c.CORSOrigins = append(c.CORSOrigins, o)
		}
	}
	if len(c.CORSOrigins) == 0 {
		fail("CORS_ORIGINS is required (comma-separated list)")
	}

	// Enums
	switch c.EmailDriver {
	case "console", "resend":
	default:
		fail("EMAIL_DRIVER must be one of console, resend, got %q", c.EmailDriver)
	}
	if c.EmailDriver == "resend" && c.ResendAPIKey == "" {
		fail("RESEND_API_KEY is required when EMAIL_DRIVER=resend")
	}
	switch c.MassiveMode {
	case "EOD", "SNAPSHOT":
	default:
		fail("MASSIVE_MODE must be one of EOD, SNAPSHOT, got %q", c.MassiveMode)
	}

	if len(problems) > 0 {
		return nil, &Error{Problems: problems}
	}
	return c, nil
}

var _ error = (*Error)(nil)

// ErrInvalid can be used with errors.Is-style checks by callers.
var ErrInvalid = errors.New("invalid configuration")

// Is makes *Error match ErrInvalid.
func (e *Error) Is(target error) bool { return target == ErrInvalid }
