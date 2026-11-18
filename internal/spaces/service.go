// Package spaces implements spaces, membership, roles, invitations and the
// space middleware used by every space-scoped route.
package spaces

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/rs/zerolog"

	"github.com/JonathanRainee/hzzt/internal/db"
	"github.com/JonathanRainee/hzzt/internal/db/sqlc"
	"github.com/JonathanRainee/hzzt/internal/platform/apperror"
	"github.com/JonathanRainee/hzzt/internal/platform/events"
)

const (
	// MaxMembers is the member limit of a space, owner included.
	MaxMembers = 5
	// InvitationTTL is how long an invitation code stays valid.
	InvitationTTL = 7 * 24 * time.Hour
	// DefaultAvatarColor is used when a space is created without one.
	DefaultAvatarColor = "#4F46E5"
	// DefaultBaseCurrency is the base currency of new spaces.
	DefaultBaseCurrency = "USD"

	inviteAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789" // no 0 O 1 I
	inviteLen      = 8
)

// DB is what the service needs; *pgxpool.Pool and pgx.Tx both satisfy it.
type DB interface {
	sqlc.DBTX
	db.TxStarter
}

// SpaceCreated is emitted after a space is created ("space.created").
type SpaceCreated struct {
	SpaceID uuid.UUID
	UserID  uuid.UUID
}

func (SpaceCreated) EventName() string { return "space.created" }

// Options configures the service.
type Options struct {
	AppURL string
	Events *events.Bus
	Log    zerolog.Logger
	Now    func() time.Time
}

// Service holds the spaces business logic.
type Service struct {
	db   DB
	q    *sqlc.Queries
	opts Options
	now  func() time.Time
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
	return &Service{db: database, q: sqlc.New(database), opts: o, now: now}
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// NewInviteCode returns an 8-character code without ambiguous characters.
func NewInviteCode() (string, error) {
	buf := make([]byte, inviteLen)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	var b strings.Builder
	for _, x := range buf {
		// 256 is a multiple of 32, so the modulo is unbiased.
		b.WriteByte(inviteAlphabet[int(x)%len(inviteAlphabet)])
	}
	return b.String(), nil
}

// CreateInput describes a new space.
type CreateInput struct {
	Name         string
	AvatarEmoji  *string
	AvatarColor  string
	BaseCurrency string
}

// Create makes a space owned by userID. If activate is true it also becomes the user's active space.
func (s *Service) Create(ctx context.Context, userID uuid.UUID, in CreateInput, activate bool) (sqlc.Space, error) {
	if in.AvatarColor == "" {
		in.AvatarColor = DefaultAvatarColor
	}
	if in.BaseCurrency == "" {
		in.BaseCurrency = DefaultBaseCurrency
	}
	var sp sqlc.Space
	err := db.WithTx(ctx, s.db, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		var err error
		sp, err = q.CreateSpace(ctx, sqlc.CreateSpaceParams{
			Name: in.Name, AvatarEmoji: in.AvatarEmoji, AvatarColor: in.AvatarColor,
			BaseCurrency: in.BaseCurrency, CreatedByID: userID,
		})
		if err != nil {
			return fmt.Errorf("create space: %w", err)
		}
		if err := q.AddSpaceMember(ctx, sqlc.AddSpaceMemberParams{SpaceID: sp.ID, UserID: userID, Role: RoleOwner}); err != nil {
			return fmt.Errorf("add owner: %w", err)
		}
		if activate {
			return q.SetLastActiveSpace(ctx, sqlc.SetLastActiveSpaceParams{
				ID: userID, LastActiveSpaceID: uuid.NullUUID{UUID: sp.ID, Valid: true},
			})
		}
		return nil
	})
	if err != nil {
		return sqlc.Space{}, err
	}
	s.opts.Events.Publish(ctx, SpaceCreated{SpaceID: sp.ID, UserID: userID})
	return sp, nil
}

// SpaceWithRole is a space plus the caller's role and the member count.
type SpaceWithRole struct {
	Space       sqlc.Space
	Role        string
	MemberCount int
}

// List returns the user's live spaces.
func (s *Service) List(ctx context.Context, userID uuid.UUID) ([]SpaceWithRole, error) {
	rows, err := s.q.ListUserSpaces(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make([]SpaceWithRole, 0, len(rows))
	for _, r := range rows {
		out = append(out, SpaceWithRole{
			Space: sqlc.Space{
				ID: r.ID, Name: r.Name, AvatarEmoji: r.AvatarEmoji, AvatarColor: r.AvatarColor,
				BaseCurrency: r.BaseCurrency, AutoFxConversion: r.AutoFxConversion, TrackingMode: r.TrackingMode,
				BudgetPeriodStartDay: r.BudgetPeriodStartDay, CreatedByID: r.CreatedByID,
				CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
			},
			Role: r.MemberRole, MemberCount: int(r.MemberCount),
		})
	}
	return out, nil
}

// MemberCount returns the number of members of a space.
func (s *Service) MemberCount(ctx context.Context, spaceID uuid.UUID) (int, error) {
	n, err := s.q.CountSpaceMembers(ctx, spaceID)
	return int(n), err
}

// Update applies a partial update (params.ID is the space).
func (s *Service) Update(ctx context.Context, p sqlc.UpdateSpaceParams) (sqlc.Space, error) {
	sp, err := s.q.UpdateSpace(ctx, p)
	if errors.Is(err, pgx.ErrNoRows) {
		return sp, errSpaceNotFound
	}
	return sp, err
}

// Delete soft-deletes a space unless it is the caller's last one.
func (s *Service) Delete(ctx context.Context, userID, spaceID uuid.UUID) error {
	return db.WithTx(ctx, s.db, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		n, err := q.CountUserSpaces(ctx, userID)
		if err != nil {
			return err
		}
		if n <= 1 {
			return errLastSpace
		}
		if err := q.SoftDeleteSpace(ctx, spaceID); err != nil {
			return err
		}
		return q.RepointLastActiveForSpace(ctx, sqlc.RepointLastActiveForSpaceParams{SpaceID: spaceID})
	})
}

// Activate records the space as the user's last active one.
func (s *Service) Activate(ctx context.Context, userID, spaceID uuid.UUID) error {
	return s.q.SetLastActiveSpace(ctx, sqlc.SetLastActiveSpaceParams{
		ID: userID, LastActiveSpaceID: uuid.NullUUID{UUID: spaceID, Valid: true},
	})
}

// Members lists a space's members.
func (s *Service) Members(ctx context.Context, spaceID uuid.UUID) ([]sqlc.ListSpaceMembersRow, error) {
	return s.q.ListSpaceMembers(ctx, spaceID)
}

// RemoveMember removes targetID from the space (owner removal or self-leave).
func (s *Service) RemoveMember(ctx context.Context, spaceID, targetID uuid.UUID) error {
	return db.WithTx(ctx, s.db, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		m, err := q.GetSpaceMember(ctx, sqlc.GetSpaceMemberParams{SpaceID: spaceID, UserID: targetID})
		if errors.Is(err, pgx.ErrNoRows) {
			return apperror.NotFound("Member not found")
		}
		if err != nil {
			return err
		}
		if m.Role == RoleOwner {
			return errOwnerCannotLeave
		}
		if err := q.RemoveSpaceMember(ctx, sqlc.RemoveSpaceMemberParams{SpaceID: spaceID, UserID: targetID}); err != nil {
			return err
		}
		return q.RepointLastActiveForSpace(ctx, sqlc.RepointLastActiveForSpaceParams{
			SpaceID: spaceID, UserID: uuid.NullUUID{UUID: targetID, Valid: true},
		})
	})
}

// Invitation is a code plus its shareable link.
type Invitation struct {
	Code      string
	Link      string
	ExpiresAt time.Time
}

func (s *Service) invitation(i sqlc.SpaceInvitation) Invitation {
	return Invitation{
		Code: i.Code, ExpiresAt: i.ExpiresAt.UTC(),
		Link: strings.TrimRight(s.opts.AppURL, "/") + "/invite/" + i.Code,
	}
}

// CreateInvitation revokes any active code and creates a new one.
func (s *Service) CreateInvitation(ctx context.Context, spaceID, userID uuid.UUID) (Invitation, error) {
	var inv sqlc.SpaceInvitation
	err := db.WithTx(ctx, s.db, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		if err := q.RevokeActiveInvitations(ctx, spaceID); err != nil {
			return err
		}
		for attempt := 0; attempt < 5; attempt++ {
			code, err := NewInviteCode()
			if err != nil {
				return err
			}
			// A savepoint keeps a code collision from aborting the outer transaction.
			err = db.WithTx(ctx, tx, func(sp pgx.Tx) error {
				var e error
				inv, e = s.q.WithTx(sp).CreateInvitation(ctx, sqlc.CreateInvitationParams{
					SpaceID: spaceID, Code: code, CreatedByID: userID, ExpiresAt: s.now().Add(InvitationTTL),
				})
				return e
			})
			if err == nil {
				return nil
			}
			if !isUniqueViolation(err) {
				return err
			}
		}
		return errors.New("could not generate a unique invitation code")
	})
	if err != nil {
		return Invitation{}, err
	}
	return s.invitation(inv), nil
}

// ActiveInvitation returns the current valid code, or nil.
func (s *Service) ActiveInvitation(ctx context.Context, spaceID uuid.UUID) (*Invitation, error) {
	i, err := s.q.GetActiveInvitation(ctx, sqlc.GetActiveInvitationParams{SpaceID: spaceID, ExpiresAt: s.now()})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	inv := s.invitation(i)
	return &inv, nil
}

// RevokeInvitation revokes any active code.
func (s *Service) RevokeInvitation(ctx context.Context, spaceID uuid.UUID) error {
	return s.q.RevokeActiveInvitations(ctx, spaceID)
}

// Accept joins userID to the space behind code as MEMBER.
func (s *Service) Accept(ctx context.Context, userID uuid.UUID, code string) (sqlc.Space, error) {
	code = strings.ToUpper(strings.TrimSpace(code))
	var sp sqlc.Space
	err := db.WithTx(ctx, s.db, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		inv, err := q.GetInvitationByCode(ctx, code)
		if errors.Is(err, pgx.ErrNoRows) {
			return errInvitationInvalid
		}
		if err != nil {
			return err
		}
		if inv.RevokedAt != nil || inv.SpaceDeletedAt != nil {
			return errInvitationInvalid
		}
		if !s.now().Before(inv.ExpiresAt) {
			return errInvitationExpired
		}
		// Lock the space row so two simultaneous joins cannot exceed the limit.
		if _, err := q.LockSpace(ctx, inv.SpaceID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return errInvitationInvalid
			}
			return err
		}
		if _, err := q.GetSpaceMember(ctx, sqlc.GetSpaceMemberParams{SpaceID: inv.SpaceID, UserID: userID}); err == nil {
			return errAlreadyMember
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		n, err := q.CountSpaceMembers(ctx, inv.SpaceID)
		if err != nil {
			return err
		}
		if n >= MaxMembers {
			return errMemberLimit
		}
		if err := q.AddSpaceMember(ctx, sqlc.AddSpaceMemberParams{SpaceID: inv.SpaceID, UserID: userID, Role: RoleMember}); err != nil {
			return err
		}
		row, err := q.GetSpaceMembership(ctx, sqlc.GetSpaceMembershipParams{ID: inv.SpaceID, UserID: userID})
		if err != nil {
			return err
		}
		sp = spaceFromRow(row)
		return nil
	})
	return sp, err
}
