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

# Integration tests need a running gateway and database; `mise run
# test:integration` provides both. Delegate rather than keep a second,
# subtly different invocation here that fails on a clean machine.
test-integration:
	mise run test:integration

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

# MODELS is required, like ORG/TEAM/NAME. Written as -allowed-models=$(MODELS)
# rather than with a space so that an empty value produces keygen's own "required"
# message; with a space, the flag would swallow -classification as its value.
# Example: make keygen ORG=acme TEAM=platform NAME=svc MODELS=aegis-fast CLASS=INTERNAL EXPIRES=365d
keygen:
	go run ./cmd/keygen -org $(ORG) -team $(TEAM) -name $(NAME) -allowed-models=$(MODELS) -classification $(CLASS) -expires $(EXPIRES)

docker-build:
	docker build -t aegis-gateway:latest .
	docker build -t aegis-filter-nlp:latest filter-service/
