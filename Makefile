.PHONY: run build test test-race vet fmt check docker-build migrate-up migrate-down migrate-version

run:
	go run ./cmd/server

build:
	go build -trimpath -o bin/server ./cmd/server
	go build -trimpath -o bin/migrate ./cmd/migrate

test:
	go test ./...

test-race:
	go test -race ./...

vet:
	go vet ./...

fmt:
	gofmt -w cmd internal

check: vet test test-race

docker-build:
	docker build -t jungle-backend-go:local .

# O binário resolvido com `go build` evita depender da toolchain em produção.
migrate-up:
	go build -trimpath -o bin/migrate ./cmd/migrate
	DATABASE_URL='$(DATABASE_URL)' ./bin/migrate -path migrations up

migrate-down:
	go build -trimpath -o bin/migrate ./cmd/migrate
	DATABASE_URL='$(DATABASE_URL)' ./bin/migrate -path migrations down $(N)

migrate-version:
	go build -trimpath -o bin/migrate ./cmd/migrate
	DATABASE_URL='$(DATABASE_URL)' ./bin/migrate -path migrations version
