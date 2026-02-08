.PHONY: build run docker-build docker-up docker-down clean test test-short test-coverage test-race

# Build the Go binary
build:
	go build -o server ./cmd/server

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
