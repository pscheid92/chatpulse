# Build stage
FROM golang:1.26-alpine AS builder

ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_TIME=unknown

WORKDIR /build

# Copy go mod files
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build the binary with version info
RUN CGO_ENABLED=0 GOOS=linux go build \
      -ldflags "-X github.com/pscheid92/chatpulse/internal/platform/version.Version=${VERSION} \
                -X github.com/pscheid92/chatpulse/internal/platform/version.Commit=${COMMIT} \
                -X github.com/pscheid92/chatpulse/internal/platform/version.BuildTime=${BUILD_TIME}" \
      -o server ./cmd/server

# Run stage
FROM alpine:latest

WORKDIR /app

# Create non-root user
RUN addgroup -S appgroup && adduser -S appuser -G appgroup

# Copy binary from builder (templates are embedded via go:embed)
COPY --from=builder /build/server .

# Switch to non-root user
USER appuser

# Expose port
EXPOSE 8080

# Run the server
ENTRYPOINT ["/app/server"]
