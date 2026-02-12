package server

import (
	"context"
	"fmt"
	"html/template"
	"net/http"
	"net/http/httptest"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/sessions"
	"github.com/labstack/echo/v4"
	"github.com/pscheid92/chatpulse/internal/config"
	"github.com/pscheid92/chatpulse/internal/domain"
	apperrors "github.com/pscheid92/chatpulse/internal/errors"
	"github.com/stretchr/testify/require"
	"testing"
)

// --- Mock implementations ---

type mockAppService struct {
	getUserByIDFn         func(ctx context.Context, userID uuid.UUID) (*domain.User, error)
	getUserByOverlayFn    func(ctx context.Context, overlayUUID uuid.UUID) (*domain.User, error)
	getConfigFn           func(ctx context.Context, userID uuid.UUID) (*domain.Config, error)
	upsertUserFn          func(ctx context.Context, twitchUserID, twitchUsername, accessToken, refreshToken string, tokenExpiry time.Time) (*domain.User, error)
	ensureSessionActiveFn func(ctx context.Context, overlayUUID uuid.UUID) error
	incrRefCountFn        func(ctx context.Context, sessionUUID uuid.UUID) error
	onSessionEmptyFn      func(ctx context.Context, sessionUUID uuid.UUID)
	resetSentimentFn      func(ctx context.Context, overlayUUID uuid.UUID) error
	saveConfigFn          func(ctx context.Context, userID uuid.UUID, forTrigger, againstTrigger, leftLabel, rightLabel string, decaySpeed float64, overlayUUID uuid.UUID) error
	rotateOverlayUUIDFn   func(ctx context.Context, userID uuid.UUID) (uuid.UUID, error)
}

func (m *mockAppService) GetUserByID(ctx context.Context, userID uuid.UUID) (*domain.User, error) {
	if m.getUserByIDFn != nil {
		return m.getUserByIDFn(ctx, userID)
	}
	return nil, fmt.Errorf("not implemented")
}

func (m *mockAppService) GetUserByOverlayUUID(ctx context.Context, overlayUUID uuid.UUID) (*domain.User, error) {
	if m.getUserByOverlayFn != nil {
		return m.getUserByOverlayFn(ctx, overlayUUID)
	}
	return nil, domain.ErrUserNotFound
}

func (m *mockAppService) GetConfig(ctx context.Context, userID uuid.UUID) (*domain.Config, error) {
	if m.getConfigFn != nil {
		return m.getConfigFn(ctx, userID)
	}
	return &domain.Config{
		ForTrigger: "yes", AgainstTrigger: "no",
		LeftLabel: "Against", RightLabel: "For", DecaySpeed: 1.0,
	}, nil
}

func (m *mockAppService) UpsertUser(ctx context.Context, twitchUserID, twitchUsername, accessToken, refreshToken string, tokenExpiry time.Time) (*domain.User, error) {
	if m.upsertUserFn != nil {
		return m.upsertUserFn(ctx, twitchUserID, twitchUsername, accessToken, refreshToken, tokenExpiry)
	}
	return nil, fmt.Errorf("not implemented")
}

func (m *mockAppService) EnsureSessionActive(ctx context.Context, overlayUUID uuid.UUID) error {
	if m.ensureSessionActiveFn != nil {
		return m.ensureSessionActiveFn(ctx, overlayUUID)
	}
	return nil
}

func (m *mockAppService) IncrRefCount(ctx context.Context, sessionUUID uuid.UUID) error {
	if m.incrRefCountFn != nil {
		return m.incrRefCountFn(ctx, sessionUUID)
	}
	return nil
}

func (m *mockAppService) OnSessionEmpty(ctx context.Context, sessionUUID uuid.UUID) {
	if m.onSessionEmptyFn != nil {
		m.onSessionEmptyFn(ctx, sessionUUID)
	}
}

func (m *mockAppService) ResetSentiment(ctx context.Context, overlayUUID uuid.UUID) error {
	if m.resetSentimentFn != nil {
		return m.resetSentimentFn(ctx, overlayUUID)
	}
	return nil
}

func (m *mockAppService) SaveConfig(ctx context.Context, userID uuid.UUID, forTrigger, againstTrigger, leftLabel, rightLabel string, decaySpeed float64, overlayUUID uuid.UUID) error {
	if m.saveConfigFn != nil {
		return m.saveConfigFn(ctx, userID, forTrigger, againstTrigger, leftLabel, rightLabel, decaySpeed, overlayUUID)
	}
	return nil
}

func (m *mockAppService) RotateOverlayUUID(ctx context.Context, userID uuid.UUID) (uuid.UUID, error) {
	if m.rotateOverlayUUIDFn != nil {
		return m.rotateOverlayUUIDFn(ctx, userID)
	}
	return uuid.New(), nil
}

type mockOAuthClient struct {
	result *twitchTokenResult
	err    error
}

func (m *mockOAuthClient) ExchangeCodeForToken(_ context.Context, _ string) (*twitchTokenResult, error) {
	return m.result, m.err
}

// --- Test helpers ---

func newTestServer(t *testing.T, app domain.AppService, opts ...func(*Server)) *Server {
	t.Helper()

	loginTmpl := template.Must(template.New("login.html").Parse(`Login {{.TwitchAuthURL}}`))
	dashTmpl := template.Must(template.New("dashboard.html").Parse(`Dashboard {{.Username}}`))
	overlayTmpl := template.Must(template.New("overlay.html").Parse(`Overlay {{.LeftLabel}} {{.RightLabel}}`))

	store := sessions.NewCookieStore([]byte("test-secret-key-32-bytes-long!!!"))
	store.Options = &sessions.Options{
		Path:   "/",
		MaxAge: 3600,
	}

	e := echo.New()
	// Install error middleware for tests to match production behavior
	e.Use(apperrors.Middleware())

	srv := &Server{
		echo:              e,
		config:            &config.Config{TwitchClientID: "test-client-id", TwitchRedirectURI: "http://localhost/auth/callback"},
		app:               app,
		sessionStore:      store,
		loginTemplate:     loginTmpl,
		dashboardTemplate: dashTmpl,
		overlayTemplate:   overlayTmpl,
	}

	for _, opt := range opts {
		opt(srv)
	}

	// Register routes so endpoints are available for testing
	srv.registerRoutes()

	return srv
}

func withOAuthClient(oauth twitchOAuthClient) func(*Server) {
	return func(s *Server) {
		s.oauthClient = oauth
	}
}

func withRedisHealthCheck(redis redisHealthChecker) func(*Server) {
	return func(s *Server) {
		s.redisHealthCheck = redis
	}
}

func withPostgresHealthCheck(pg postgresHealthChecker) func(*Server) {
	return func(s *Server) {
		s.postgresHealthCheck = pg
	}
}

// callHandler wraps a handler with error middleware, matching production behavior
func callHandler(handler echo.HandlerFunc, c echo.Context) error {
	return apperrors.Middleware()(handler)(c)
}

func setSessionUserID(t *testing.T, srv *Server, req *http.Request, rec *httptest.ResponseRecorder, userID uuid.UUID) {
	t.Helper()
	session, err := srv.sessionStore.Get(req, sessionName)
	require.NoError(t, err)
	session.Values[sessionKeyToken] = userID.String()
	require.NoError(t, session.Save(req, rec))
}
