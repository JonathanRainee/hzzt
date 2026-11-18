package currency

import (
	"context"
	"errors"
	"time"

	"github.com/robfig/cron/v3"
	"github.com/rs/zerolog"
)

// RefreshSchedule runs the FX refresh every 6 hours.
const RefreshSchedule = "@every 6h"

// StartJobs refreshes rates on boot when none exist and then every 6 hours.
// The returned function stops the scheduler.
func (s *Service) StartJobs(ctx context.Context, log zerolog.Logger) (stop func(), err error) {
	run := func(name string, fn func(context.Context) error) {
		jctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		switch err := fn(jctx); {
		case err == nil:
		case errors.Is(err, ErrRefreshBusy):
			log.Info().Str("job", name).Msg("fx refresh skipped: another instance holds the lock")
		default:
			log.Error().Err(err).Str("job", name).Msg("fx refresh failed")
		}
	}

	c := cron.New(cron.WithChain(cron.SkipIfStillRunning(cron.DiscardLogger)))
	if _, err := c.AddFunc(RefreshSchedule, func() { run("fx.refresh", s.Refresh) }); err != nil {
		return nil, err
	}
	c.Start()
	go run("fx.ensure", s.EnsureRates) // never block startup on the provider
	return func() { <-c.Stop().Done() }, nil
}
