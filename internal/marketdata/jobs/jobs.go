// Package jobs runs named background jobs one at a time across instances and
// records every run in job_runs.
package jobs

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"

	"github.com/JonathanRainee/hzzt/internal/db"
	"github.com/JonathanRainee/hzzt/internal/db/sqlc"
)

// Statuses written to job_runs.
const (
	StatusSuccess = "SUCCESS"
	StatusFailed  = "FAILED"
	StatusSkipped = "SKIPPED"
)

// maxErrorLen bounds what is stored in job_runs.error.
const maxErrorLen = 500

// DB is what the runner needs; *pgxpool.Pool and pgx.Tx both satisfy it.
type DB interface {
	sqlc.DBTX
	db.TxStarter
}

// Func is a job body. It receives a database transaction (a savepoint, so a
// failure rolls back only the job's own writes, never the job_runs record) and
// returns how many items it processed.
type Func func(ctx context.Context, tx pgx.Tx) (items int, err error)

// Result describes one run.
type Result struct {
	Status string
	Items  int
	Err    error // the job's own error, when Status is FAILED
}

// Runner executes jobs.
type Runner struct {
	db  DB
	log zerolog.Logger
	now func() time.Time
}

// New builds a runner; now may be nil.
func New(database DB, log zerolog.Logger, now func() time.Time) *Runner {
	if now == nil {
		now = time.Now
	}
	return &Runner{db: database, log: log, now: now}
}

// Run executes fn as the named job: it takes a database advisory lock keyed by
// the name (a run that finds it taken is recorded as SKIPPED and fn is not
// called), captures fn's error (recorded as FAILED with the job's writes rolled
// back) and always writes a job_runs row. The returned error is only for
// infrastructure problems (the database itself); a failing job is reported in Result.
func (r *Runner) Run(ctx context.Context, name string, fn Func) (Result, error) {
	var res Result
	started := r.now().UTC()
	err := db.WithTx(ctx, r.db, func(tx pgx.Tx) error {
		q := sqlc.New(tx)
		locked, err := q.TryJobLock(ctx, name)
		if err != nil {
			return fmt.Errorf("job lock: %w", err)
		}
		if !locked {
			res = Result{Status: StatusSkipped}
			return q.InsertJobRun(ctx, sqlc.InsertJobRunParams{
				Name: name, StartedAt: started, FinishedAt: r.now().UTC(), Status: StatusSkipped,
			})
		}

		var items int
		jobErr := db.WithTx(ctx, tx, func(sp pgx.Tx) error {
			var e error
			items, e = fn(ctx, sp)
			return e
		})
		res = Result{Status: StatusSuccess, Items: items}
		var msg *string
		if jobErr != nil {
			res = Result{Status: StatusFailed, Err: jobErr}
			m := truncate(jobErr.Error())
			msg = &m
			items = 0
		}
		return q.InsertJobRun(ctx, sqlc.InsertJobRunParams{
			Name: name, StartedAt: started, FinishedAt: r.now().UTC(), Status: res.Status,
			ItemsProcessed: int32(items), Error: msg,
		})
	})
	if err != nil {
		return Result{}, err
	}
	switch res.Status {
	case StatusFailed:
		r.log.Error().Err(res.Err).Str("job", name).Msg("job failed")
	case StatusSkipped:
		r.log.Info().Str("job", name).Msg("job skipped: already running elsewhere")
	}
	return res, nil
}

func truncate(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > maxErrorLen {
		return s[:maxErrorLen]
	}
	return s
}

// IsFailed reports whether the result is a failed run.
func (r Result) IsFailed() bool { return r.Status == StatusFailed }

// ErrOrNil returns the job's error for callers that want to treat failure as an error.
func (r Result) ErrOrNil() error {
	if r.Status == StatusFailed {
		return r.Err
	}
	return nil
}
