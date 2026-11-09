GO ?= go
BINARY ?= bin/runner
IMAGE ?= micro-ci-cd-runner:local

.PHONY: all build run test test-race test-docker vet fmt tidy lint docker compose-up compose-down health clean

all: fmt vet test build

build:
	$(GO) build -trimpath -ldflags "-s -w" -o $(BINARY) ./cmd/runner

run:
	$(GO) run ./cmd/runner

test:
	$(GO) test ./...

# The race detector requires cgo and a C compiler.
test-race:
	CGO_ENABLED=1 $(GO) test -race ./...

# Real container execution: image pull, clone, test, logs, cleanup.
test-docker:
	RUNNER_DOCKER_TEST=1 $(GO) test -tags=docker -run TestRunContainerEndToEnd -v ./internal/dockerx

vet:
	$(GO) vet ./...

fmt:
	gofmt -w .

tidy:
	$(GO) mod tidy

lint:
	golangci-lint run

docker:
	docker build -f deploy/Dockerfile -t $(IMAGE) .

compose-up:
	docker compose -f deploy/docker-compose.yml up -d --build

compose-down:
	docker compose -f deploy/docker-compose.yml down

health:
	curl -fsS http://localhost:$${PORT:-8080}/healthz

clean:
	rm -rf bin dist coverage.out
