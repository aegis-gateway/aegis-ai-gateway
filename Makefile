.PHONY: all build test lint run dev migrate

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS := -ldflags="-s -w -X main.version=$(VERSION)"

all: lint test build

build:
	go build $(LDFLAGS) -o bin/gateway ./cmd/gateway
	go build $(LDFLAGS) -o bin/keygen ./cmd/keygen
	go build $(LDFLAGS) -o bin/migrate ./cmd/migrate

test:
	go test ./... -v -race -cover

test-integration:
	go test ./test/integration/... -v -tags=integration

lint:
	golangci-lint run ./...

run:
	go run ./cmd/gateway

dev:
	docker compose -f deploy/docker-compose.yaml up --build

dev-down:
	docker compose -f deploy/docker-compose.yaml down -v

migrate-up:
	go run ./cmd/migrate -direction up

migrate-down:
	go run ./cmd/migrate -direction down

keygen:
	go run ./cmd/keygen -org $(ORG) -team $(TEAM) -name $(NAME) -classification $(CLASS) -expires $(EXPIRES)

docker-build:
	docker build -t aegis-gateway:latest .
	docker build -t aegis-filter-nlp:latest filter-service/
