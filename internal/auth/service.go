// Package auth implements registration, login, JWT access tokens, rotating
// refresh tokens with reuse detection, password change and password reset.
package auth

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/rs/zerolog"

	"github.com/JonathanRainee/hzzt/internal/db"
	"github.com/JonathanRainee/hzzt/internal/db/sqlc"
	"github.com/JonathanRainee/hzzt/internal/platform/apperror"
	"github.com/JonathanRainee/hzzt/internal/platform/email"
	"github.com/JonathanRainee/hzzt/internal/platform/events"
)

// ResetTokenTTL is how long a password-reset link stays valid.
const ResetTokenTTL = 30 * time.Minute

// DB is what the service needs: queries plus the ability to open a
// (possibly nested) transaction. *pgxpool.Pool and pgx.Tx both satisfy it.
type DB interface {
	sqlc.DBTX
	db.TxStarter
}

// UserRegistered is emitted after a user is created ("user.registered").
// P3 subscribes to create the default space; P4 creates default preferences.
type UserRegistered struct {
	UserID uuid.UUID
	Email  string
	Name   string
}

func (UserRegistered) EventName() string { return "user.registered" }

// Options configures the service.
type Options struct {
	AccessSecret string
	AccessTTL    time.Duration
	RefreshTTL   time.Duration
	AppURL       string
	Email        email.Sender
	Events       *events.Bus
	Log          zerolog.Logger
	// Now is overridable in tests; defaults to time.Now.
	Now func() time.Time
}

// Service holds the auth business logic.
type Service struct {
	db     DB
	q      *sqlc.Queries
	tokens *TokenIssuer
	opts   Options
	now    func() time.Time
}

// NewService builds the service.
func NewService(database DB, o Options) *Service {
	now := o.Now
	if now == nil {
		now = time.Now
	}
	if o.Events == nil {
		o.Events = events.NewBus(o.Log)
	}
	return &Service{
		db:     database,
		q:      sqlc.New(database),
		tokens: NewTokenIssuer(o.AccessSecret, o.AccessTTL, now),
		opts:   o,
		now:    now,
	}
}

// Tokens exposes the issuer (used by tests and middleware).
func (s *Service) Tokens() *TokenIssuer { return s.tokens }

// ClientInfo is request metadata stored with refresh tokens.
type ClientInfo struct{ UserAgent, IP string }

// Session is the result of register/login/refresh.
type Session struct {
	AccessToken  string
	ExpiresIn    int // seconds
	RefreshToken string
	RefreshTTL   time.Duration
	User         sqlc.User
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// newRefresh stores a fresh refresh token in the family and returns its plaintext and row.
func (s *Service) newRefresh(ctx context.Context, q *sqlc.Queries, userID, family uuid.UUID, c ClientInfo) (string, sqlc.RefreshToken, error) {
	plain, hash, err := NewOpaqueToken()
	if err != nil {
		return "", sqlc.RefreshToken{}, err
	}
	row, err := q.CreateRefreshToken(ctx, sqlc.CreateRefreshTokenParams{
		UserID:    userID,
		FamilyID:  family,
		TokenHash: hash,
		ExpiresAt: s.now().Add(s.opts.RefreshTTL),
		UserAgent: strPtr(c.UserAgent),
		Ip:        strPtr(c.IP),
	})
	return plain, row, err
}

func (s *Service) session(user sqlc.User, family uuid.UUID, refresh string) (*Session, error) {
	access, err := s.tokens.IssueAccess(user.ID, family)
	if err != nil {
		return nil, fmt.Errorf("sign access token: %w", err)
	}
	return &Session{
		AccessToken:  access,
		ExpiresIn:    int(s.tokens.TTL().Seconds()),
		RefreshToken: refresh,
		RefreshTTL:   s.opts.RefreshTTL,
		User:         user,
	}, nil
}

// Register creates an account and its first session.
func (s *Service) Register(ctx context.Context, emailAddr, password, name, timezone string, c ClientInfo) (*Session, error) {
	if err := ValidatePasswordPolicy("password", password, emailAddr); err != nil {
		return nil, err
	}
	hash, err := HashPassword(password)
	if err != nil {
		return nil, fmt.Errorf("hash password: %w", err)
	}

	var sess *Session
	err = db.WithTx(ctx, s.db, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		user, err := q.CreateUser(ctx, sqlc.CreateUserParams{
			Email: emailAddr, PasswordHash: hash, Name: name, Timezone: timezone,
		})
		if err != nil {
			if isUniqueViolation(err) {
				return errEmailTaken
			}
			return fmt.Errorf("create user: %w", err)
		}
		family := uuid.New()
		refresh, _, err := s.newRefresh(ctx, q, user.ID, family, c)
		if err != nil {
			return fmt.Errorf("create refresh token: %w", err)
		}
		sess, err = s.session(user, family, refresh)
		return err
	})
	if err != nil {
		return nil, err
	}

	s.opts.Events.Publish(ctx, UserRegistered{UserID: sess.User.ID, Email: sess.User.Email, Name: sess.User.Name})
	// P3 may update last_active_space_id in a handler; return the freshest row.
	if u, err := s.q.GetUserByID(ctx, sess.User.ID); err == nil {
		sess.User = u
	}
	return sess, nil
}

// Login verifies credentials and starts a new refresh family.
func (s *Service) Login(ctx context.Context, emailAddr, password string, c ClientInfo) (*Session, error) {
	user, err := s.q.GetUserByEmail(ctx, emailAddr)
	if errors.Is(err, pgx.ErrNoRows) {
		burnPasswordCheck(password)
		return nil, errBadCreds
	}
	if err != nil {
		return nil, fmt.Errorf("get user: %w", err)
	}
	if !VerifyPassword(password, user.PasswordHash) {
		return nil, errBadCreds
	}

	var sess *Session
	err = db.WithTx(ctx, s.db, func(tx pgx.Tx) error {
		family := uuid.New()
		refresh, _, err := s.newRefresh(ctx, s.q.WithTx(tx), user.ID, family, c)
		if err != nil {
			return fmt.Errorf("create refresh token: %w", err)
		}
		sess, err = s.session(user, family, refresh)
		return err
	})
	return sess, err
}

// Refresh rotates a refresh token. Presenting an already-rotated token is
// treated as theft: the whole family is revoked (and that revocation is
// committed even though the call returns an error).
func (s *Service) Refresh(ctx context.Context, plain string, c ClientInfo) (*Session, error) {
	if plain == "" {
		return nil, errRTInvalid
	}
	var (
		sess   *Session
		reused bool
	)
	err := db.WithTx(ctx, s.db, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		old, err := q.GetRefreshTokenByHashForUpdate(ctx, HashToken(plain))
		if errors.Is(err, pgx.ErrNoRows) {
			return errRTInvalid
		}
		if err != nil {
			return fmt.Errorf("get refresh token: %w", err)
		}
		if old.RevokedAt != nil {
			if old.ReplacedByID.Valid { // was rotated already → reuse
				if err := q.RevokeRefreshFamily(ctx, old.FamilyID); err != nil {
					return fmt.Errorf("revoke family: %w", err)
				}
				reused = true
				return nil // commit the revocation
			}
			return errRTInvalid // revoked by logout / password change
		}
		if !s.now().Before(old.ExpiresAt) {
			return errRTInvalid
		}
		user, err := q.GetUserByID(ctx, old.UserID)
		if errors.Is(err, pgx.ErrNoRows) {
			return errRTInvalid
		}
		if err != nil {
			return fmt.Errorf("get user: %w", err)
		}
		next, row, err := s.newRefresh(ctx, q, user.ID, old.FamilyID, c)
		if err != nil {
			return fmt.Errorf("create refresh token: %w", err)
		}
		if err := q.RotateRefreshToken(ctx, sqlc.RotateRefreshTokenParams{
			ID: old.ID, ReplacedByID: uuid.NullUUID{UUID: row.ID, Valid: true},
		}); err != nil {
			return fmt.Errorf("rotate refresh token: %w", err)
		}
		sess, err = s.session(user, old.FamilyID, next)
		return err
	})
	if err != nil {
		return nil, err
	}
	if reused {
		return nil, errRTReused
	}
	return sess, nil
}

// Logout revokes the family of the presented refresh token. Unknown tokens are ignored.
func (s *Service) Logout(ctx context.Context, plain string) error {
	if plain == "" {
		return nil
	}
	return db.WithTx(ctx, s.db, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		row, err := q.GetRefreshTokenByHashForUpdate(ctx, HashToken(plain))
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		return q.RevokeRefreshFamily(ctx, row.FamilyID)
	})
}

// LogoutAll revokes every refresh token of the user.
func (s *Service) LogoutAll(ctx context.Context, userID uuid.UUID) error {
	return s.q.RevokeAllUserRefreshTokens(ctx, userID)
}

// ChangePassword verifies the current password, sets the new one and revokes
// every session except the caller's (identified by its refresh family).
func (s *Service) ChangePassword(ctx context.Context, user sqlc.User, family uuid.UUID, current, next string) error {
	if !VerifyPassword(current, user.PasswordHash) {
		return errBadCurrent
	}
	if err := ValidatePasswordPolicy("newPassword", next, user.Email); err != nil {
		return err
	}
	hash, err := HashPassword(next)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}
	return db.WithTx(ctx, s.db, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		if err := q.UpdateUserPassword(ctx, sqlc.UpdateUserPasswordParams{ID: user.ID, PasswordHash: hash}); err != nil {
			return err
		}
		return q.RevokeOtherUserRefreshFamilies(ctx, sqlc.RevokeOtherUserRefreshFamiliesParams{
			UserID: user.ID, FamilyID: family,
		})
	})
}

// ForgotPassword issues a reset token and emails the link if the account
// exists. It never reveals whether it does.
func (s *Service) ForgotPassword(ctx context.Context, emailAddr string) error {
	user, err := s.q.GetUserByEmail(ctx, emailAddr)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("get user: %w", err)
	}
	plain, hash, err := NewOpaqueToken()
	if err != nil {
		return err
	}
	err = db.WithTx(ctx, s.db, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		if err := q.InvalidateUserResetTokens(ctx, user.ID); err != nil {
			return err
		}
		_, err := q.CreatePasswordResetToken(ctx, sqlc.CreatePasswordResetTokenParams{
			UserID: user.ID, TokenHash: hash, ExpiresAt: s.now().Add(ResetTokenTTL),
		})
		return err
	})
	if err != nil {
		return fmt.Errorf("store reset token: %w", err)
	}

	link := fmt.Sprintf("%s/reset-password?token=%s", trimSlash(s.opts.AppURL), url.QueryEscape(plain))
	if err := s.opts.Email.Send(ctx, email.Message{
		To:      user.Email,
		Subject: "Reset your password",
		Body:    fmt.Sprintf("Hi %s,\n\nUse this link to reset your password (valid for 30 minutes):\n%s\n\nIf you didn't ask for this, ignore this email.", user.Name, link),
	}); err != nil {
		// Do not fail the request: that would reveal the account exists.
		s.opts.Log.Error().Err(err).Msg("send password reset email")
	}
	return nil
}

func trimSlash(s string) string {
	for len(s) > 0 && s[len(s)-1] == '/' {
		s = s[:len(s)-1]
	}
	return s
}

// ResetPassword consumes a reset token, sets the password, and revokes all sessions.
func (s *Service) ResetPassword(ctx context.Context, plain, newPassword string) error {
	hashed, err := HashPassword(newPassword)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}
	return db.WithTx(ctx, s.db, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		tok, err := q.GetPasswordResetTokenByHashForUpdate(ctx, HashToken(plain))
		if errors.Is(err, pgx.ErrNoRows) {
			return errResetBad
		}
		if err != nil {
			return err
		}
		if tok.UsedAt != nil || !s.now().Before(tok.ExpiresAt) {
			return errResetBad
		}
		user, err := q.GetUserByID(ctx, tok.UserID)
		if errors.Is(err, pgx.ErrNoRows) {
			return errResetBad
		}
		if err != nil {
			return err
		}
		if err := ValidatePasswordPolicy("newPassword", newPassword, user.Email); err != nil {
			return err
		}
		if err := q.UpdateUserPassword(ctx, sqlc.UpdateUserPasswordParams{ID: user.ID, PasswordHash: hashed}); err != nil {
			return err
		}
		if err := q.MarkPasswordResetTokenUsed(ctx, tok.ID); err != nil {
			return err
		}
		return q.RevokeAllUserRefreshTokens(ctx, user.ID)
	})
}

// UserByID loads a live (not soft-deleted) user.
func (s *Service) UserByID(ctx context.Context, id uuid.UUID) (sqlc.User, error) {
	u, err := s.q.GetUserByID(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return u, apperror.Unauthenticated("User no longer exists")
	}
	return u, err
}
