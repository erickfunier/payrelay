.PHONY: run test lint coverage build docker-up

run:
	go run ./cmd/server

test:
	go test ./...

lint:
	go vet ./...

coverage:
	go test -coverpkg=./internal/... -coverprofile=coverage.out ./internal/...
	go tool cover -func=coverage.out

build:
	go build ./cmd/server

docker-up:
	docker compose up --build
