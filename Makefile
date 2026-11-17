include .env
export

MIGRATIONS_DIR := db/migrations
SQLC_CONFIG := sqlc.yaml

.PHONY: up down migrate-up migrate-down migrate-status sqlc-gen run test test-e2e lint

up:
	docker compose up -d
	@echo "Waiting for Postgres to be healthy..."
	@until docker compose ps postgres | grep -q "healthy"; do sleep 1; done
	@until docker compose ps postgres_test | grep -q "healthy"; do sleep 1; done
	@echo "Postgres (dev + test) is up."

down:
	docker compose down

migrate-up:
	goose -dir $(MIGRATIONS_DIR) postgres "$(DATABASE_URL)" up

migrate-down:
	goose -dir $(MIGRATIONS_DIR) postgres "$(DATABASE_URL)" down

migrate-status:
	goose -dir $(MIGRATIONS_DIR) postgres "$(DATABASE_URL)" status

migrate-test-up:
	goose -dir $(MIGRATIONS_DIR) postgres "$(TEST_DATABASE_URL)" up

sqlc-gen:
	sqlc generate -f $(SQLC_CONFIG)

run:
	go run ./cmd/api

test:
	go test ./...

test-e2e:
	go test -tags=e2e ./...

lint:
	golangci-lint run
