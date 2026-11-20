package investments

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/JonathanRainee/hzzt/internal/db/sqlc"
)

func TestValueForViewerOwnerVersusParticipant(t *testing.T) {
	s := &Service{}
	i := inv(TypeUSStock, "USD", "1", "0", "1000.01", "0")
	i.ID = uuid.New()
	owner, bob, carol := uuid.New(), uuid.New(), uuid.New()
	rows := []sqlc.InvestmentParticipant{
		{ID: uuid.New(), Role: RoleOwner, ShareBps: 5000, UserID: uuid.NullUUID{UUID: owner, Valid: true}},
		{ID: uuid.New(), Role: RoleParticipant, ShareBps: 3000, UserID: uuid.NullUUID{UUID: bob, Valid: true}},
		{ID: uuid.New(), Role: RoleParticipant, ShareBps: 2000, UserID: uuid.NullUUID{UUID: carol, Valid: true}},
	}
	cc := ctx("USD", true, nil, nil)
	byID := func(id uuid.UUID) func(sqlc.InvestmentParticipant) bool {
		return func(p sqlc.InvestmentParticipant) bool { return p.ID == id }
	}
	isOwner := func(p sqlc.InvestmentParticipant) bool { return p.Role == RoleOwner }

	own, err := s.valueForViewer(i, nil, cc, now, rows, owner, isOwner, OwnershipOwner, RealizedPL{})
	require.NoError(t, err)
	b, err := s.valueForViewer(i, nil, cc, now, rows, bob, byID(rows[1].ID), OwnershipSharedIn, RealizedPL{})
	require.NoError(t, err)
	c, err := s.valueForViewer(i, nil, cc, now, rows, carol, byID(rows[2].ID), OwnershipSharedIn, RealizedPL{})
	require.NoError(t, err)

	assert.Equal(t, OwnershipOwner, own.Ownership)
	assert.Equal(t, OwnershipSharedIn, b.Ownership)
	assert.Equal(t, 5000, own.MyShareBps)
	assert.Equal(t, 3000, b.MyShareBps)
	assert.True(t, d("1000.01").Equal(b.TotalValuation.Base.MarketValue), "everyone sees the whole")
	sum := own.Valuation.Base.MarketValue.Add(b.Valuation.Base.MarketValue).Add(c.Valuation.Base.MarketValue)
	assert.True(t, d("1000.01").Equal(sum), "the parts add up exactly: %s", sum)
	for _, p := range b.Participants {
		if p.IsMe {
			assert.Equal(t, 3000, p.ShareBps)
		}
	}
}
