package httpserver

import (
	"context"
	"errors"
	"html/template"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/sessions"
	"github.com/labstack/echo/v4"
	"github.com/pscheid92/chatpulse/internal/app"
	"github.com/pscheid92/chatpulse/internal/domain"
	"github.com/pscheid92/chatpulse/internal/platform/config"
	"github.com/pscheid92/uuid"
	"github.com/stretchr/testify/require"
)

// --- Mock implementations ---

type mockAppService struct {
	getStreamerByIDFn      func(ctx context.Context, streamerID uuid.UUID) (*domain.Streamer, error)
	getStreamerByOverlayFn func(ctx context.Context, overlayUUID uuid.UUID) (*domain.Streamer, error)
	upsertStreamerFn       func(ctx context.Context, twitchUserID, twitchUsername, accessToken, refreshToken string, tokenExpiry time.Time) (*domain.Streamer, error)
	getConfigFn            func(ctx context.Context, streamerID uuid.UUID) (*domain.OverlayConfigWithVersion, error)
	saveConfigFn           func(ctx context.Context, req app.SaveConfigRequest) error
	rotateOverlayUUIDFn    func(ctx context.Context, streamerID uuid.UUID) (uuid.UUID, error)
	resetSentimentFn       func(ctx context.Context, overlayUUID uuid.UUID) error
}

func (m *mockAppService) GetStreamerByID(ctx context.Context, streamerID uuid.UUID) (*domain.Streamer, error) {
	if m.getStreamerByIDFn != nil {
		return m.getStreamerByIDFn(ctx, streamerID)
	}
	return nil, errors.New("not implemented")
}

func (m *mockAppService) GetStreamerByOverlayUUID(ctx context.Context, overlayUUID uuid.UUID) (*domain.Streamer, error) {
	if m.getStreamerByOverlayFn != nil {
		return m.getStreamerByOverlayFn(ctx, overlayUUID)
	}
	return nil, domain.ErrStreamerNotFound
}

func (m *mockAppService) UpsertStreamer(ctx context.Context, twitchUserID, twitchUsername, accessToken, refreshToken string, tokenExpiry time.Time) (*domain.Streamer, error) {
	if m.upsertStreamerFn != nil {
		return m.upsertStreamerFn(ctx, twitchUserID, twitchUsername, accessToken, refreshToken, tokenExpiry)
	}
	return nil, errors.New("not implemented")
}

func (m *mockAppService) GetConfig(ctx context.Context, streamerID uuid.UUID) (*domain.OverlayConfigWithVersion, error) {
	if m.getConfigFn != nil {
		return m.getConfigFn(ctx, streamerID)
	}
	return &domain.OverlayConfigWithVersion{
		OverlayConfig: domain.OverlayConfig{
			MemorySeconds: 30,
			ForTrigger:    "yes", ForLabel: "For",
			AgainstTrigger: "no", AgainstLabel: "Against",
			DisplayMode: domain.DisplayModeCombined,
		},
	}, nil
}

func (m *mockAppService) SaveConfig(ctx context.Context, req app.SaveConfigRequest) error {
	if m.saveConfigFn != nil {
		return m.saveConfigFn(ctx, req)
	}
	return nil
}

func (m *mockAppService) RotateOverlayUUID(ctx context.Context, streamerID uuid.UUID) (uuid.UUID, error) {
	if m.rotateOverlayUUIDFn != nil {
		return m.rotateOverlayUUIDFn(ctx, streamerID)
	}
	return uuid.NewV4(), nil
}

func (m *mockAppService) ResetSentiment(ctx context.Context, overlayUUID uuid.UUID) error {
	if m.resetSentimentFn != nil {
		return m.resetSentimentFn(ctx, overlayUUID)
	}
	return nil
}

type mockTwitchService struct {
	subscribeFn   func(ctx context.Context, streamerID uuid.UUID, broadcasterUserID string) error
	unsubscribeFn func(ctx context.Context, streamerID uuid.UUID) error
}

func (m *mockTwitchService) Subscribe(ctx context.Context, streamerID uuid.UUID, broadcasterUserID string) error {
	if m.subscribeFn != nil {
		return m.subscribeFn(ctx, streamerID, broadcasterUserID)
	}
	return nil
}

func (m *mockTwitchService) Unsubscribe(ctx context.Context, streamerID uuid.UUID) error {
	if m.unsubscribeFn != nil {
		return m.unsubscribeFn(ctx, streamerID)
	}
	return nil
}

type mockOAuthClient struct {
	result *twitchTokenResult
	err    error
}

func (m *mockOAuthClient) ExchangeCodeForToken(_ context.Context, _ string) (*twitchTokenResult, error) {
	return m.result, m.err
}

// --- Test helpers ---

func newTestServer(t *testing.T, app appService, opts ...func(*Server)) *Server {
	t.Helper()

	tmpl := template.Must(template.New("landing.html").Parse(`Landing`))
	template.Must(tmpl.New("login.html").Parse(`Login {{.TwitchAuthURL}}`))
	template.Must(tmpl.New("dashboard.html").Parse(`Dashboard {{.Username}}`))
	template.Must(tmpl.New("overlay.html").Parse(`Overlay {{.AgainstLabel}} {{.ForLabel}}`))

	store := sessions.NewCookieStore([]byte("test-secret-key-32-bytes-long!!!"))
	store.Options = &sessions.Options{
		Path:   "/",
		MaxAge: 3600,
	}

	e := echo.New()

	srv := &Server{
		echo:         e,
		config:       &config.Config{TwitchClientID: "test-client-id", TwitchRedirectURI: "http://localhost/auth/callback"},
		app:          app,
		sessionStore: store,
		templates:    tmpl,
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

func withTwitchService(ts domain.EventSubService) func(*Server) {
	return func(s *Server) {
		s.twitchService = ts
	}
}

func withWebhookHandler(h http.Handler) func(*Server) {
	return func(s *Server) {
		s.webhookHandler = h
	}
}

func withHealthChecks(checks ...HealthCheck) func(*Server) {
	return func(s *Server) {
		s.healthChecks = checks
	}
}

// callHandler wraps a handler with error middleware, matching production behavior
func callHandler(handler echo.HandlerFunc, c echo.Context) error {
	return ErrorHandlingMiddleware()(handler)(c)
}

func setSessionUserID(t *testing.T, srv *Server, req *http.Request, rec *httptest.ResponseRecorder, userID uuid.UUID) {
	t.Helper()
	session, err := srv.sessionStore.Get(req, sessionName)
	require.NoError(t, err)
	session.Values[sessionKeyToken] = userID.String()
	require.NoError(t, session.Save(req, rec))
}
