.PHONY: build run docker-build docker-up docker-down clean test test-short test-coverage test-race sqlc

# Version information
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_TIME ?= $(shell date -u '+%Y-%m-%dT%H:%M:%SZ')

# Build flags
LDFLAGS = -X github.com/pscheid92/chatpulse/internal/version.Version=$(VERSION) \
          -X github.com/pscheid92/chatpulse/internal/version.Commit=$(COMMIT) \
          -X github.com/pscheid92/chatpulse/internal/version.BuildTime=$(BUILD_TIME)

# Build the Go binary
build:
	go build -ldflags "$(LDFLAGS)" -o server ./cmd/server

# Run the server locally
run: build
	./server

# Build Docker image
docker-build:
	docker build -t chatpulse .

# Start with Docker Compose
docker-up:
	docker compose up -d

# Stop Docker Compose
docker-down:
	docker compose down

# Clean build artifacts
clean:
	rm -f server
	go clean

# Run all tests (including integration tests)
test:
	go test -v ./...

# Run only fast unit tests (skip integration tests)
test-short:
	go test -short -v ./...

# Run only integration tests
test-integration:
	go test -v -run='Integration' ./...

# Alias for test-short
test-unit: test-short

# Run tests with coverage report
test-coverage:
	go test -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out | grep total
	go tool cover -html=coverage.out -o coverage.html

# Run tests with race detector (short mode for speed)
test-race:
	go test -race -short ./...

# Install dependencies
deps:
	go mod download
	go mod tidy

# Format code
fmt:
	go fmt ./...

# Run linter
lint:
	golangci-lint run

# Generate sqlc code
sqlc:
	sqlc generate
