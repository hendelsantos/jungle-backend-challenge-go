.PHONY: run build test test-race vet fmt check docker-build

run:
	go run ./cmd/server

build:
	go build -trimpath -o bin/server ./cmd/server

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
