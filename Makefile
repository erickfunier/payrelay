.PHONY: run test lint build docker-up

run:
	go run ./cmd/server

test:
	go test ./...

lint:
	go vet ./...

build:
	go build ./cmd/server

docker-up:
	docker compose up --build
