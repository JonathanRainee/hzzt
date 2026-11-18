//go:build e2e

package e2e

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/JonathanRainee/hzzt/internal/ledger"
)

// expectLedgerConsistent asserts that a wallet's cached balance equals the
// balance recomputed from its transactions. Every money phase calls it (and the
// harness runs ExpectAllLedgersConsistent at the end of every test).
func expectLedgerConsistent(t *testing.T, s *TestServer, walletID string) {
	t.Helper()
	ctx := context.Background()
	id := uuid.MustParse(walletID)
	var cached string
	require.NoError(t, s.Tx.QueryRow(ctx, `SELECT balance::text FROM wallets WHERE id = $1`, id).Scan(&cached))
	recomputed, err := ledger.RecomputeBalance(ctx, s.Tx, id)
	require.NoError(t, err)
	assert.True(t, dec(cached).Equal(recomputed), "wallet %s: cached balance %s != recomputed %s", walletID, cached, recomputed)
}

// ExpectAllLedgersConsistent checks every wallet in the test database transaction, deleted ones included.
func (s *TestServer) ExpectAllLedgersConsistent() {
	s.t.Helper()
	ctx := context.Background()
	rows, err := s.Tx.Query(ctx, `SELECT id FROM wallets`)
	require.NoError(s.t, err)
	var ids []string
	for rows.Next() {
		var id uuid.UUID
		require.NoError(s.t, rows.Scan(&id))
		ids = append(ids, id.String())
	}
	rows.Close()
	require.NoError(s.t, rows.Err())
	for _, id := range ids {
		expectLedgerConsistent(s.t, s, id)
	}
}
