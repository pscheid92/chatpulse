# Error Handling Guide

This document defines error handling standards for ChatPulse. Consistent error handling improves debugging, reduces log noise, and clarifies retry strategies.

## Why This Matters

**Problems solved by this guide:**
- **Double-logging eliminated:** Same error logged at multiple layers → cluttered logs, hard to find root cause
- **Consistent error propagation:** Clear rules for when to log vs when to return
- **Better production debugging:** Structured logging with consistent field names
- **Clearer retry strategies:** Distinguish transient failures (retry) from permanent failures (alert)

## Error Classification

We classify errors into 3 tiers:

### Tier A: Domain Errors (Sentinel Errors)

**Definition:** Expected business errors representing valid application states.

**Examples:**
```go
// internal/domain/errors.go
var (
    ErrUserNotFound         = errors.New("user not found")
    ErrConfigNotFound       = errors.New("config not found")
    ErrSubscriptionNotFound = errors.New("eventsub subscription not found")
)
```

**When they occur:**
- User lookup fails because user doesn't exist (valid case: first-time visitor)
- Config lookup fails because user hasn't saved settings yet (valid case: default config)
- EventSub subscription not found during deletion (valid case: already deleted)

**Handling rules:**
- ✅ **Return unwrapped** - Preserve sentinel for `errors.Is()` checks
- ❌ **Do NOT log at origin** - Caller decides if this is worth logging
- ✅ **Used for control flow** - Business logic branches on these errors

**Code pattern:**
```go
// Repository (origin) - return unwrapped sentinel
func (r *UserRepo) GetByID(ctx context.Context, id uuid.UUID) (*domain.User, error) {
    row := r.pool.QueryRow(ctx, `SELECT ... WHERE id = $1`, id)

    var user domain.User
    if err := row.Scan(&user.ID, &user.Username, ...); err != nil {
        if errors.Is(err, pgx.ErrNoRows) {
            return nil, domain.ErrUserNotFound  // Unwrapped sentinel, no log
        }
        r.logger.Error("failed to scan user", "user_id", id, "error", err)
        return nil, fmt.Errorf("failed to scan user %s: %w", id, err)
    }
    return &user, nil
}

// Handler (decision boundary) - check and decide
func (s *Server) handleDashboard(c echo.Context) error {
    userID := c.Get("user_id").(uuid.UUID)

    user, err := s.appSvc.GetUserByID(c.Request().Context(), userID)
    if err != nil {
        if errors.Is(err, domain.ErrUserNotFound) {
            // Expected case - redirect to login (no log)
            return c.Redirect(http.StatusFound, "/auth/login")
        }
        // Unexpected error - log it
        s.logger.Error("failed to get user", "user_id", userID, "error", err)
        return c.String(http.StatusInternalServerError, "Internal server error")
    }

    // ... render dashboard ...
}
```

**Why unwrapped?**
- Wrapping breaks `errors.Is()` checks
- Caller loses ability to distinguish domain errors from infrastructure errors
- Business logic depends on exact error identity

### Tier B: Infrastructure Errors

**Definition:** Unexpected failures in external systems (database, Redis, network, APIs).

**Examples:**
- Database connection failures
- Redis timeouts or connection pool exhausted
- Twitch API errors (rate limit, server error, network unreachable)
- File system errors

**Handling rules:**
- ✅ **Wrap with context** - `fmt.Errorf("failed to X: %w", err)`
- ✅ **Log at origin** - Infrastructure failures should always be logged
- ✅ **Return error up the stack** - Don't swallow, let caller handle

**Code pattern:**
```go
// Repository (origin) - log + wrap + return
func (r *UserRepo) UpdateTokens(ctx context.Context, userID uuid.UUID, accessToken, refreshToken string) error {
    encryptedAccess, err := r.crypto.Encrypt(accessToken)
    if err != nil {
        r.logger.Error("failed to encrypt access token",
            "user_id", userID,
            "error", err)
        return fmt.Errorf("failed to encrypt access token: %w", err)
    }

    result, err := r.pool.Exec(ctx,
        `UPDATE users SET access_token = $1, refresh_token = $2 WHERE id = $3`,
        encryptedAccess, encryptedRefresh, userID)
    if err != nil {
        r.logger.Error("failed to update tokens",
            "user_id", userID,
            "error", err)
        return fmt.Errorf("failed to update tokens for user %s: %w", userID, err)
    }

    if result.RowsAffected() == 0 {
        return domain.ErrUserNotFound  // Tier A error (unwrapped)
    }

    return nil
}
```

**Subcategories:**

#### B1: Transient Errors (Retry Recommended)
These errors are temporary - retrying may succeed.

**Examples:**
- Redis connection timeout (`i/o timeout`)
- Database connection pool exhausted (`connection refused`)
- Twitch API 500/503 (server error)
- Network timeouts

**Action:** Retry with exponential backoff or accept failure gracefully.

#### B2: Permanent Errors (Retry Won't Help)
These errors indicate misconfiguration or invalid requests - retrying won't fix them.

**Examples:**
- Twitch API 401 (invalid credentials - need to refresh OAuth token)
- Twitch API 404 (broadcaster doesn't exist)
- Database constraint violation (shouldn't happen if code is correct)

**Action:** Log error, alert on-call, don't retry automatically.

#### B3: Rate Limit Errors (Retry with Backoff)
Special case of transient errors with specific handling.

**Examples:**
- Twitch API 429 (rate limit exceeded)
- Redis `ERR command queue is full`

**Action:** Retry with exponential backoff, respect `Retry-After` header if provided.

### Tier C: Programming Errors (Panics)

**Definition:** Bugs in the code that should never happen in production.

**Examples:**
- Nil pointer dereference
- Index out of bounds
- Type assertion failure
- Invalid state (e.g., session exists but config is missing)

**Handling rules:**
- ✅ **Let panic propagate** - Echo middleware will recover
- ✅ **Middleware logs panic + stack trace** - Full context for debugging
- ❌ **Don't try/catch everywhere** - Trust middleware, avoid defensive programming

**Code pattern:**
```go
// Let panic propagate - this is a bug if it happens
func (e *Engine) GetCurrentValue(ctx context.Context, sessionUUID uuid.UUID) (float64, error) {
    config := e.sessions.GetSessionConfig(ctx, sessionUUID)

    // If config is nil here, that's a programming error (session should always have config)
    // Let it panic - middleware will catch it and log stack trace
    decaySpeed := config.DecaySpeed  // Will panic if config is nil - that's correct!

    // ...
}
```

**Why not defensive checks?**
- Clutters code with `if config == nil` checks that should never be true
- Hides bugs during development (should fail fast in tests)
- Middleware provides safety net for production (500 error + stack trace)

## Logging Rules

### Rule 1: Log at Decision Boundary, Not at Origin (Exception: Infrastructure Errors)

**Decision boundary:** Where you decide how to handle the error (typically handlers, middleware).

**Anti-pattern (double-logging):**
```go
// ❌ BAD: app/service.go logs...
func (s *Service) SaveConfig(ctx context.Context, userID uuid.UUID, cfg *domain.Config) error {
    if err := s.configs.Update(ctx, userID, cfg); err != nil {
        s.logger.Error("failed to save config", "error", err)  // Log 1
        return err
    }
    return nil
}

// ❌ BAD: Handler ALSO logs...
func (s *Server) handleSaveConfig(c echo.Context) error {
    if err := s.appSvc.SaveConfig(ctx, userID, cfg); err != nil {
        s.logger.Error("config save failed", "error", err)  // Log 2 (duplicate!)
        return c.JSON(500, map[string]string{"error": "Failed to save config"})
    }
    return c.JSON(200, map[string]string{"status": "ok"})
}
```

**Result:** Same error logged twice! Cluttered logs, hard to trace root cause.

**Correct pattern:**
```go
// ✅ GOOD: app/service.go does NOT log (no decision here)
func (s *Service) SaveConfig(ctx context.Context, userID uuid.UUID, cfg *domain.Config) error {
    if err := s.configs.Update(ctx, userID, cfg); err != nil {
        // Infrastructure error already logged by repository
        return fmt.Errorf("failed to save config: %w", err)
    }
    return nil
}

// ✅ GOOD: Handler logs once (decision boundary)
func (s *Server) handleSaveConfig(c echo.Context) error {
    if err := s.appSvc.SaveConfig(ctx, userID, cfg); err != nil {
        s.logger.Error("failed to save config",
            "user_id", userID,
            "error", err)  // Log once, at decision boundary
        return c.JSON(500, map[string]string{"error": "Failed to save config"})
    }
    return c.JSON(200, map[string]string{"status": "ok"})
}
```

### Rule 2: Infrastructure Errors ARE Logged at Origin

**Exception to Rule 1:** Infrastructure errors (Tier B) ARE logged at origin because they represent system failures, not business logic.

```go
// ✅ GOOD: Redis error logged at origin (infrastructure layer)
func (s *SessionRepo) CreateSession(ctx context.Context, overlayUUID uuid.UUID, ...) error {
    if err := s.rdb.HSet(ctx, key, "value", value, "last_update", now).Err(); err != nil {
        s.logger.Error("redis write failed",
            "session_uuid", overlayUUID,
            "operation", "create_session",
            "error", err)  // Log here - infrastructure failure
        return fmt.Errorf("failed to create session in redis: %w", err)
    }
    return nil
}
```

**Why log at origin for infrastructure errors?**
- Failure is in the infrastructure layer (Redis, database), not business logic
- Origin has the most context (which Redis command failed, which query, etc.)
- Handler doesn't know details (just sees "create session failed")

## Structured Logging Standards

Use Go's `slog` structured logging with consistent field names.

### Required Fields (All Error Logs)

- `"error"` - The error value itself
- `"message"` - Human-readable description (first arg to `.Error()`)

### Context Fields (Domain-Specific)

Use **snake_case** for consistency with Go conventions:

| Field Name | Type | Description | Example |
|------------|------|-------------|---------|
| `user_id` | uuid.UUID | User's internal ID | `"user_id", userID` |
| `session_uuid` | uuid.UUID | Session overlay UUID | `"session_uuid", overlayUUID` |
| `broadcaster_user_id` | string | Twitch broadcaster ID | `"broadcaster_user_id", "12345678"` |
| `operation` | string | Operation name | `"operation", "create_session"` |
| `key` | string | Redis key name | `"key", "session:abc-123"` |

### Consistent Field Names

❌ **Inconsistent (bad):**
```go
logger.Error("failed", "userID", id)      // camelCase
logger.Error("failed", "user", id)         // abbreviated
logger.Error("failed", "uid", id)          // too short
```

✅ **Consistent (good):**
```go
logger.Error("failed to get user", "user_id", id)  // snake_case, full word
```

### Example Logs

**Repository (infrastructure error):**
```go
r.logger.Error("failed to update user tokens",
    "user_id", userID,
    "operation", "update_tokens",
    "error", err)
```

**Handler (decision boundary):**
```go
s.logger.Error("failed to activate session",
    "session_uuid", overlayUUID,
    "broadcaster_user_id", broadcasterUserID,
    "user_id", userID,
    "error", err)
```

**Broadcaster (tick loop):**
```go
b.logger.Warn("redis timeout during tick",
    "session_uuid", sessionUUID,
    "timeout", timeout,
    "error", err)
```

## Code Examples

### Example 1: Repository Error Handling

```go
// internal/database/user_repository.go
func (r *UserRepo) GetByID(ctx context.Context, id uuid.UUID) (*domain.User, error) {
    row := r.pool.QueryRow(ctx, `SELECT id, username, ... FROM users WHERE id = $1`, id)

    var user domain.User
    if err := row.Scan(&user.ID, &user.Username, ...); err != nil {
        if errors.Is(err, pgx.ErrNoRows) {
            // Tier A: Domain error (unwrapped, no log)
            return nil, domain.ErrUserNotFound
        }
        // Tier B: Infrastructure error (log + wrap)
        r.logger.Error("failed to scan user row",
            "user_id", id,
            "error", err)
        return nil, fmt.Errorf("failed to scan user %s: %w", id, err)
    }

    return &user, nil
}
```

### Example 2: Handler Error Handling

```go
// internal/server/handlers_dashboard.go
func (s *Server) handleDashboard(c echo.Context) error {
    userID := c.Get("user_id").(uuid.UUID)  // Set by auth middleware

    // Get user
    user, err := s.appSvc.GetUserByID(c.Request().Context(), userID)
    if err != nil {
        if errors.Is(err, domain.ErrUserNotFound) {
            // Expected case - redirect (no log)
            return c.Redirect(http.StatusFound, "/auth/login")
        }
        // Unexpected error - log at decision boundary
        s.logger.Error("failed to get user for dashboard",
            "user_id", userID,
            "error", err)
        return c.String(http.StatusInternalServerError, "Internal server error")
    }

    // Get config
    config, err := s.appSvc.GetConfig(c.Request().Context(), userID)
    if err != nil {
        if errors.Is(err, domain.ErrConfigNotFound) {
            // Expected for first-time users - use defaults (no log)
            config = domain.DefaultConfig()
        } else {
            // Unexpected error - log it
            s.logger.Error("failed to get config",
                "user_id", userID,
                "error", err)
            return c.String(http.StatusInternalServerError, "Internal server error")
        }
    }

    // Render dashboard...
    return s.renderTemplate(c, "dashboard.html", map[string]interface{}{
        "user":   user,
        "config": config,
    })
}
```

### Example 3: Application Service Error Handling

```go
// internal/app/service.go
func (s *Service) EnsureSessionActive(ctx context.Context, userID uuid.UUID) (*domain.SessionSnapshot, error) {
    // Try to load from cache
    session, err := s.sessions.GetSessionByUser(ctx, userID)
    if err == nil {
        return session, nil  // Cache hit
    }

    // Infrastructure error - already logged by repository
    // Application layer does NOT log again (avoid double-logging)
    if !errors.Is(err, domain.ErrSessionNotFound) {
        return nil, fmt.Errorf("failed to get session: %w", err)
    }

    // Cache miss - activate from database
    user, err := s.users.GetByID(ctx, userID)
    if err != nil {
        // Already logged by repository if infrastructure error
        return nil, fmt.Errorf("failed to get user: %w", err)
    }

    config, err := s.configs.GetByUserID(ctx, userID)
    if err != nil {
        // Already logged by repository if infrastructure error
        return nil, fmt.Errorf("failed to get config: %w", err)
    }

    // Create session in Redis
    if err := s.sessions.CreateSession(ctx, user.OverlayUUID, user.TwitchUserID, config); err != nil {
        // Already logged by repository (Redis infrastructure error)
        return nil, fmt.Errorf("failed to create session: %w", err)
    }

    // Subscribe to Twitch EventSub (if configured)
    if s.twitch != nil {
        if err := s.twitch.Subscribe(ctx, user.OverlayUUID, user.TwitchUserID); err != nil {
            // Twitch errors already logged by TwitchService
            return nil, fmt.Errorf("failed to subscribe to eventsub: %w", err)
        }
    }

    return s.sessions.GetSessionByUser(ctx, userID)
}
```

## Testing Error Paths

### Unit Tests

Test that errors are returned (not logged) from repositories:

```go
func TestUserRepo_GetByID_NotFound(t *testing.T) {
    repo := setupTestRepo(t)

    _, err := repo.GetByID(context.Background(), uuid.New())

    // Should return sentinel error
    assert.ErrorIs(t, err, domain.ErrUserNotFound)

    // Should NOT log (check logs are empty)
    // If using test logger, assert no Error() calls
}
```

Test that handlers log errors at decision boundary:

```go
func TestHandleDashboard_UserNotFound(t *testing.T) {
    srv := newTestServer(t, &mockAppService{
        getUserErr: domain.ErrUserNotFound,
    })

    req := httptest.NewRequest("GET", "/dashboard", nil)
    rec := httptest.NewRecorder()

    // Should redirect (no error logged for expected case)
    err := srv.handleDashboard(srv.echo.NewContext(req, rec))
    assert.NoError(t, err)
    assert.Equal(t, http.StatusFound, rec.Code)

    // Check that logger did NOT log error (expected case)
    // If using test logger, assert no Error() calls
}
```

## Summary Table

| Error Type | Return | Log at Origin? | Wrap? | Example |
|------------|--------|----------------|-------|---------|
| **Tier A: Domain** | Unwrapped sentinel | ❌ No | ❌ No | `ErrUserNotFound` |
| **Tier B: Infrastructure** | Wrapped | ✅ Yes | ✅ Yes | Database connection failure |
| **Tier C: Programming** | Panic | ✅ Middleware | N/A | Nil pointer dereference |

## Related Documentation

- **[ADR-010: Eventual consistency philosophy](adr/010-eventual-consistency-philosophy.md)** - Context on why we accept eventual consistency (affects error handling for race conditions)
- **[ADR-006: Ref counting coordination](adr/006-ref-counting-coordination.md)** - Self-healing pattern (accept errors, clean up with timers)

## Linter Configuration

The following golangci-lint rules enforce error handling standards:

```yaml
# .golangci.yml
linters:
  enable:
    - errname      # Sentinel errors must be named Err*
    - errorlint    # Check %w wrapping (not %v)
    - wrapcheck    # Ensure errors are wrapped at appropriate layers
```

Run linter: `make lint`
