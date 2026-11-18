package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/rs/zerolog"
	_ "time/tzdata" // IANA timezones even in minimal containers

	"github.com/JonathanRainee/hzzt/internal/auth"
	"github.com/JonathanRainee/hzzt/internal/config"
	"github.com/JonathanRainee/hzzt/internal/db"
	"github.com/JonathanRainee/hzzt/internal/db/sqlc"
	"github.com/JonathanRainee/hzzt/internal/platform/email"
	"github.com/JonathanRainee/hzzt/internal/platform/events"
	"github.com/JonathanRainee/hzzt/internal/server"
	"github.com/JonathanRainee/hzzt/internal/spaces"
	"github.com/JonathanRainee/hzzt/internal/users"
)

// @title			Hzzt API
// @version		1.0
// @description	Asset management backend API.
// @BasePath		/api/v1
// @schemes		http https
//
// @securityDefinitions.apikey	BearerAuth
// @in							header
// @name						Authorization
// @description				JWT access token: "Bearer <token>"
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := db.NewPool(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	if cfg.RunMigrations {
		if err := db.Migrate(ctx, pool); err != nil {
			return err
		}
	}

	log := zerolog.New(os.Stdout).With().Timestamp().Logger()

	bus := events.NewBus(log)
	var sender email.Sender = email.Console{Log: log}
	if cfg.EmailDriver == "resend" {
		sender = email.Resend{APIKey: cfg.ResendAPIKey, From: cfg.EmailFrom}
	}
	authSvc := auth.NewService(pool, auth.Options{
		AccessSecret: cfg.JWTAccessSecret,
		AccessTTL:    cfg.JWTAccessTTL,
		RefreshTTL:   time.Duration(cfg.RefreshTTLDays) * 24 * time.Hour,
		AppURL:       cfg.AppURL,
		Email:        sender,
		Events:       bus,
		Log:          log,
	})
	authHandler := auth.NewHandler(authSvc, auth.DefaultLimits(), cfg.IsProduction())

	spacesSvc := spaces.NewService(pool, spaces.Options{AppURL: cfg.AppURL, Events: bus, Log: log})
	spacesSvc.Subscribe(bus) // user.registered → default space
	spacesHandler := spaces.NewHandler(spacesSvc, authSvc)
	usersSvc := users.NewService(pool, users.Options{})
	usersSvc.Subscribe(bus) // user.registered → default preferences
	usersHandler := users.NewHandler(usersSvc, authSvc, users.DefaultLimits())

	srv := &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.Port),
		Handler:           server.NewRouter(cfg, log, sqlc.New(pool), authHandler.Mount, usersHandler.Mount, spacesHandler.Mount),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info().Str("addr", srv.Addr).Str("env", cfg.Env).Msg("server listening")
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}

	log.Info().Msg("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}
