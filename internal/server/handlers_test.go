package server

import (
	"context"
	"database/sql"
	"fmt"
	"html/template"
	"net/http"
	"net/http/httptest"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/sessions"
	"github.com/labstack/echo/v4"
	"github.com/pscheid92/chatpulse/internal/config"
	"github.com/pscheid92/chatpulse/internal/models"
	"github.com/pscheid92/chatpulse/internal/sentiment"
	"github.com/stretchr/testify/require"
	"testing"
)

// --- Mock implementations ---

type mockDataStore struct {
	getUserByIDFn       func(ctx context.Context, userID uuid.UUID) (*models.User, error)
	getUserByOverlayFn  func(ctx context.Context, overlayUUID uuid.UUID) (*models.User, error)
	getConfigFn         func(ctx context.Context, userID uuid.UUID) (*models.Config, error)
	updateConfigFn      func(ctx context.Context, userID uuid.UUID, forTrigger, againstTrigger, leftLabel, rightLabel string, decaySpeed float64) error
	upsertUserFn        func(ctx context.Context, twitchUserID, twitchUsername, accessToken, refreshToken string, tokenExpiry time.Time) (*models.User, error)
	rotateOverlayUUIDFn func(ctx context.Context, userID uuid.UUID) (uuid.UUID, error)
}

func (m *mockDataStore) GetUserByID(ctx context.Context, userID uuid.UUID) (*models.User, error) {
	if m.getUserByIDFn != nil {
		return m.getUserByIDFn(ctx, userID)
	}
	return nil, fmt.Errorf("not implemented")
}

func (m *mockDataStore) GetUserByOverlayUUID(ctx context.Context, overlayUUID uuid.UUID) (*models.User, error) {
	if m.getUserByOverlayFn != nil {
		return m.getUserByOverlayFn(ctx, overlayUUID)
	}
	return nil, sql.ErrNoRows
}

func (m *mockDataStore) GetConfig(ctx context.Context, userID uuid.UUID) (*models.Config, error) {
	if m.getConfigFn != nil {
		return m.getConfigFn(ctx, userID)
	}
	return &models.Config{
		ForTrigger: "yes", AgainstTrigger: "no",
		LeftLabel: "Against", RightLabel: "For", DecaySpeed: 1.0,
	}, nil
}

func (m *mockDataStore) UpdateConfig(ctx context.Context, userID uuid.UUID, forTrigger, againstTrigger, leftLabel, rightLabel string, decaySpeed float64) error {
	if m.updateConfigFn != nil {
		return m.updateConfigFn(ctx, userID, forTrigger, againstTrigger, leftLabel, rightLabel, decaySpeed)
	}
	return nil
}

func (m *mockDataStore) UpsertUser(ctx context.Context, twitchUserID, twitchUsername, accessToken, refreshToken string, tokenExpiry time.Time) (*models.User, error) {
	if m.upsertUserFn != nil {
		return m.upsertUserFn(ctx, twitchUserID, twitchUsername, accessToken, refreshToken, tokenExpiry)
	}
	return nil, fmt.Errorf("not implemented")
}

func (m *mockDataStore) RotateOverlayUUID(ctx context.Context, userID uuid.UUID) (uuid.UUID, error) {
	if m.rotateOverlayUUIDFn != nil {
		return m.rotateOverlayUUIDFn(ctx, userID)
	}
	return uuid.New(), nil
}

type mockSentimentService struct {
	activateSessionFn  func(ctx context.Context, sessionUUID uuid.UUID, userID uuid.UUID, broadcasterUserID string) error
	resetSessionFn     func(sessionUUID uuid.UUID)
	markDisconnectedFn func(sessionUUID uuid.UUID)
	cleanupOrphansFn   func(twitchManager sentiment.TwitchManager)
	updateSessionCfgFn func(sessionUUID uuid.UUID, snapshot models.ConfigSnapshot)
}

func (m *mockSentimentService) ActivateSession(ctx context.Context, sessionUUID uuid.UUID, userID uuid.UUID, broadcasterUserID string) error {
	if m.activateSessionFn != nil {
		return m.activateSessionFn(ctx, sessionUUID, userID, broadcasterUserID)
	}
	return nil
}

func (m *mockSentimentService) ResetSession(sessionUUID uuid.UUID) {
	if m.resetSessionFn != nil {
		m.resetSessionFn(sessionUUID)
	}
}

func (m *mockSentimentService) MarkDisconnected(sessionUUID uuid.UUID) {
	if m.markDisconnectedFn != nil {
		m.markDisconnectedFn(sessionUUID)
	}
}

func (m *mockSentimentService) CleanupOrphans(twitchManager sentiment.TwitchManager) {
	if m.cleanupOrphansFn != nil {
		m.cleanupOrphansFn(twitchManager)
	}
}

func (m *mockSentimentService) UpdateSessionConfig(sessionUUID uuid.UUID, snapshot models.ConfigSnapshot) {
	if m.updateSessionCfgFn != nil {
		m.updateSessionCfgFn(sessionUUID, snapshot)
	}
}

type mockOAuthClient struct {
	result *twitchTokenResult
	err    error
}

func (m *mockOAuthClient) ExchangeCodeForToken(_ context.Context, _ string) (*twitchTokenResult, error) {
	return m.result, m.err
}

// --- Test helpers ---

func newTestServer(t *testing.T, db dataStore, sent sentimentService, opts ...func(*Server)) *Server {
	t.Helper()

	loginTmpl := template.Must(template.New("login.html").Parse(`Login {{.TwitchAuthURL}}`))
	dashTmpl := template.Must(template.New("dashboard.html").Parse(`Dashboard {{.Username}}`))
	overlayTmpl := template.Must(template.New("overlay.html").Parse(`Overlay {{.LeftLabel}} {{.RightLabel}}`))

	store := sessions.NewCookieStore([]byte("test-secret-key-32-bytes-long!!!"))
	store.Options = &sessions.Options{
		Path:   "/",
		MaxAge: 3600,
	}

	srv := &Server{
		echo:              echo.New(),
		config:            &config.Config{TwitchClientID: "test-client-id", TwitchRedirectURI: "http://localhost/auth/callback"},
		db:                db,
		sentiment:         sent,
		sessionStore:      store,
		loginTemplate:     loginTmpl,
		dashboardTemplate: dashTmpl,
		overlayTemplate:   overlayTmpl,
	}

	for _, opt := range opts {
		opt(srv)
	}

	return srv
}

func withOAuthClient(oauth twitchOAuthClient) func(*Server) {
	return func(s *Server) {
		s.oauthClient = oauth
	}
}

func setSessionUserID(t *testing.T, srv *Server, req *http.Request, rec *httptest.ResponseRecorder, userID uuid.UUID) {
	t.Helper()
	session, err := srv.sessionStore.Get(req, sessionName)
	require.NoError(t, err)
	session.Values[sessionKeyToken] = userID.String()
	require.NoError(t, session.Save(req, rec))
}
