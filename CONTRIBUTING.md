# Contributing to ChatPulse

Thanks for your interest in contributing! Here's how to get started.

## Development Setup

### Prerequisites

- Go 1.26+
- Docker (for integration tests and local services)
- [golangci-lint](https://golangci-lint.run/welcome/install/)

### Getting Started

1. Fork and clone the repository
2. Copy the environment file:
   ```bash
   cp .env.example .env
   ```
3. Start local services:
   ```bash
   make docker-up
   ```
4. Install dependencies:
   ```bash
   make deps
   ```
5. Run the tests:
   ```bash
   make test-short   # Unit tests only (<2s, no Docker)
   make test         # Full suite with integration tests (~15s)
   ```

## Making Changes

1. Create a branch from `main`
2. Make your changes
3. Run the checks:
   ```bash
   make fmt
   make lint
   make test
   ```
4. Commit with a clear message describing *why* the change was made
5. Open a pull request against `main`

## Code Style

- Follow existing patterns in the codebase
- Run `make fmt` and `make lint` before committing
- golangci-lint is configured in `.golangci.yml` — CI will enforce it

## Testing

- Unit tests: `<feature>_test.go` alongside the code
- Integration tests: `<feature>_integration_test.go` using testcontainers
- Aim to test behavior, not implementation details
- Use `make test-short` for fast feedback during development

## Project Structure

The project follows a layered architecture with strict dependency rules:

```
Domain ← Platform ← App ← Adapter ← Main
```

- **Domain** (`internal/domain/`): Interfaces and types only — no internal dependencies
- **Platform** (`internal/platform/`): Cross-cutting infrastructure — stdlib only
- **App** (`internal/app/`): Business logic — depends on Domain + Platform
- **Adapter** (`internal/adapter/`): External integrations — depends on Domain + Platform + App
- **Main** (`cmd/`): Wiring only

See `CLAUDE.md` for detailed architecture documentation.

## Reporting Issues

- Use GitHub Issues for bugs and feature requests
- Include reproduction steps for bugs
- Check existing issues before opening a new one
