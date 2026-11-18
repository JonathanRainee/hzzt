package db

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// TxStarter is satisfied by *pgxpool.Pool and by pgx.Tx (which starts a
// savepoint), so the same code runs in production and inside the e2e
// per-test transaction.
type TxStarter interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// WithTx runs fn inside a transaction: commit if fn returns nil, otherwise
// (or on panic) roll back. Every phase uses this one pattern.
func WithTx(ctx context.Context, s TxStarter, fn func(tx pgx.Tx) error) (err error) {
	tx, err := s.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback(context.WithoutCancel(ctx))
			panic(p)
		}
		if err != nil {
			_ = tx.Rollback(context.WithoutCancel(ctx))
		}
	}()
	if err = fn(tx); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}
	return nil
}
