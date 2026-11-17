# Asset Management App — Backend

Go modular-monolith REST API (chi, PostgreSQL 16, pgx, sqlc, goose).

The full implementation plan, architecture and conventions live in
[`plan/plan.md`](plan/plan.md). **Read Section 5 (Global conventions) before
writing code**; every phase must follow it.

## Prerequisites

| Tool | Used for |
|---|---|
| Go 1.23+ | build and run |
| Docker + Docker Compose | local Postgres (dev and test) |
| [goose](https://github.com/pressly/goose) | manual migration commands |
| [sqlc](https://sqlc.dev) | generating typed queries |
| [swag](https://github.com/swaggo/swag) | generating OpenAPI docs |
| [golangci-lint](https://golangci-lint.run) v2 | linting |

Install the Go tools with `go install` (binaries land in `~/go/bin`, make sure
`$HOME/go/bin` and `/usr/local/go/bin` are on your `PATH`):

```bash
go install github.com/pressly/goose/v3/cmd/goose@latest
go install github.com/sqlc-dev/sqlc/cmd/sqlc@latest
go install github.com/swaggo/swag/cmd/swag@latest
go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest
```

## Quick start

```bash
cp .env.example .env     # then edit secrets as needed
make up                  # Postgres dev (5432) + test (5433), waits until healthy
make migrate-up          # apply migrations to the dev database
make run                 # API on http://localhost:8080
```

Check it:

```bash
curl localhost:8080/api/v1/health          # {"status":"ok"}
curl localhost:8080/api/v1/health/ready    # 200 if the DB is reachable, 503 otherwise
```

Swagger UI: <http://localhost:8080/api/docs> (disabled when `ENV=production`).

Migrations also run automatically on boot unless `RUN_MIGRATIONS=false`.

## Makefile targets

| Target | What it does |
|---|---|
| `make up` / `make down` | Start / stop the dev and test Postgres containers |
| `make migrate-up` / `migrate-down` / `migrate-status` | Run goose against `DATABASE_URL` |
| `make migrate-test-up` | Run goose against `TEST_DATABASE_URL` (the e2e harness also does this itself) |
| `make sqlc-gen` | Regenerate `internal/db/sqlc` from `internal/db/queries` |
| `make swagger` | Regenerate `docs/` from the handler annotations |
| `make run` | `go run ./cmd/api` |
| `make test` | Unit tests |
| `make test-e2e` | End-to-end tests (build tag `e2e`) against the test database |
| `make lint` | `golangci-lint run` |

Generated code (`internal/db/sqlc`, `docs/`) is committed. Re-run the generator
and commit the result whenever you change queries, migrations or annotations.

## Environment variables

Configuration is read from the environment (`make` loads `.env`). Startup fails
with a list of every missing or invalid variable.

| Variable | Required | Default | Notes |
|---|---|---|---|
| `ENV` | no | `development` | `production` hides Swagger and enforces a 32+ char JWT secret |
| `PORT` | no | `8080` | |
| `DATABASE_URL` | **yes** | | Postgres connection string |
| `RUN_MIGRATIONS` | no | `true` | Apply embedded migrations on boot |
| `JWT_ACCESS_SECRET` | **yes** | | 32+ characters in production |
| `JWT_ACCESS_TTL` | no | `15m` | Go duration |
| `REFRESH_TTL_DAYS` | no | `30` | |
| `CORS_ORIGINS` | **yes** | | Comma-separated allowlist |
| `APP_URL` | **yes** | | Absolute URL of the frontend (used in emails/links) |
| `EMAIL_DRIVER` | no | `console` | `console` or `resend` |
| `RESEND_API_KEY` | if `resend` | | |
| `CMC_API_KEY` | no | | Needed from P11 (CoinMarketCap) |
| `MASSIVE_API_KEY` | no | | Needed from P12 |
| `MASSIVE_MODE` | no | `EOD` | `EOD` or `SNAPSHOT` |
| `FX_PROVIDER` | no | `frankfurter` | |
| `TEST_DATABASE_URL` | for e2e | | Read by the e2e harness, not by the app |

## Project layout

```
cmd/api/             entry point: config, pool, migrations, server, graceful shutdown
internal/config/     env parsing and validation
internal/server/     router, middleware chain, health handlers
internal/db/         pgx pool, migration runner
  migrations/        goose .sql files (embedded into the binary)
  queries/           sqlc input
  sqlc/              generated Go (committed)
internal/platform/   apperror, httpx (envelope, decoding, cursors), money, period
internal/<domain>/   one package per domain (auth, wallets, ledger, ...)
test/e2e/            end-to-end tests; test/fixtures/ for shared data
docs/                generated OpenAPI spec
plan/plan.md         implementation plan and conventions
```

See Section 4 of the plan for the cross-package rules (for example, only
`ledger` mutates wallet balances).

## Conventions (short version)

Full rules are in Section 5 of [`plan/plan.md`](plan/plan.md).

- Base path `/api/v1`, JSON with camelCase fields, UUID ids.
- Money and quantities are `decimal.Decimal` in Go and **strings** in JSON. Never `float64`.
- Every error uses the envelope
  `{"error":{"code","message","details"},"requestId"}`. Handlers return
  `apperror.AppError` and call `httpx.WriteError`.
- Request bodies go through `httpx.DecodeAndValidate[T]` (unknown fields are rejected).
- Every endpoint needs unit tests plus an e2e test in `test/e2e`.

## Testing

```bash
make up
make test         # unit tests
make test-e2e     # needs the test database from `make up`
make lint
```

Each e2e test runs the real router inside a database transaction that is rolled
back afterwards, so tests never leave data behind. Use `NewTestServer(t)` from
`test/e2e`.

## Git workflow

Work on a `feat/<name>` branch and commit one plan task at a time:

```
git commit -m "[feat]: <branch-name>" -m "[P1-T11] what was added"
```
