# Build stage
FROM golang:1.26-alpine AS builder

WORKDIR /build

# Copy go mod files
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build the binary
RUN CGO_ENABLED=0 GOOS=linux go build -o server ./cmd/server

# Run stage
FROM alpine:latest

WORKDIR /app

# Copy binary and web assets from builder
COPY --from=builder /build/server .
COPY --from=builder /build/web ./web

# Expose port
EXPOSE 8080

# Run the server
ENTRYPOINT ["/app/server"]
