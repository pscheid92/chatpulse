package server

import (
	"context"
	"database/sql"
	"fmt"
	"html/template"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/sessions"
	"github.com/labstack/echo/v4"
	"github.com/pscheid92/chatpulse/internal/config"
	"github.com/pscheid92/chatpulse/internal/models"
	"github.com/pscheid92/chatpulse/internal/sentiment"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

// --- Test helper ---

func newTestServer(t *testing.T, db dataStore, sent sentimentService) *Server {
	t.Helper()

	loginTmpl := template.Must(template.New("login.html").Parse(`Login {{.TwitchAuthURL}}`))
	dashTmpl := template.Must(template.New("dashboard.html").Parse(`Dashboard {{.Username}}`))
	overlayTmpl := template.Must(template.New("overlay.html").Parse(`Overlay {{.LeftLabel}} {{.RightLabel}}`))

	store := sessions.NewCookieStore([]byte("test-secret-key-32-bytes-long!!!"))
	store.Options = &sessions.Options{
		Path:   "/",
		MaxAge: 3600,
	}

	return &Server{
		echo:              echo.New(),
		config:            &config.Config{TwitchClientID: "test-client-id", TwitchRedirectURI: "http://localhost/auth/callback"},
		db:                db,
		sentiment:         sent,
		sessionStore:      store,
		loginTemplate:     loginTmpl,
		dashboardTemplate: dashTmpl,
		overlayTemplate:   overlayTmpl,
	}
}

// setSessionUserID creates a session cookie with the given user ID.
func setSessionUserID(t *testing.T, srv *Server, req *http.Request, rec *httptest.ResponseRecorder, userID uuid.UUID) {
	t.Helper()
	session, err := srv.sessionStore.Get(req, sessionName)
	require.NoError(t, err)
	session.Values[sessionKeyToken] = userID.String()
	require.NoError(t, session.Save(req, rec))
}

// --- requireAuth tests ---

func TestRequireAuth_NoSession(t *testing.T) {
	srv := newTestServer(t, &mockDataStore{}, &mockSentimentService{})
	e := srv.echo

	req := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	handler := srv.requireAuth(func(c echo.Context) error {
		return c.String(200, "ok")
	})

	err := handler(c)
	assert.NoError(t, err)
	assert.Equal(t, 302, rec.Code)
	assert.Contains(t, rec.Header().Get("Location"), "/auth/login")
}

func TestRequireAuth_InvalidUUID(t *testing.T) {
	srv := newTestServer(t, &mockDataStore{}, &mockSentimentService{})
	e := srv.echo

	req := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	rec := httptest.NewRecorder()

	// Set an invalid UUID in session
	session, err := srv.sessionStore.Get(req, sessionName)
	require.NoError(t, err)
	session.Values[sessionKeyToken] = "not-a-uuid"
	require.NoError(t, session.Save(req, rec))

	// Recreate request with cookies from recorder
	req2 := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	for _, cookie := range rec.Result().Cookies() {
		req2.AddCookie(cookie)
	}
	rec2 := httptest.NewRecorder()
	c := e.NewContext(req2, rec2)

	handler := srv.requireAuth(func(c echo.Context) error {
		return c.String(200, "ok")
	})

	err = handler(c)
	assert.NoError(t, err)
	assert.Equal(t, 302, rec2.Code)
}

func TestRequireAuth_ValidSession(t *testing.T) {
	srv := newTestServer(t, &mockDataStore{}, &mockSentimentService{})
	e := srv.echo
	userID := uuid.New()

	req := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	rec := httptest.NewRecorder()
	setSessionUserID(t, srv, req, rec, userID)

	// Recreate request with cookies
	req2 := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	for _, cookie := range rec.Result().Cookies() {
		req2.AddCookie(cookie)
	}
	rec2 := httptest.NewRecorder()
	c := e.NewContext(req2, rec2)

	var gotUserID uuid.UUID
	handler := srv.requireAuth(func(c echo.Context) error {
		gotUserID = c.Get("userID").(uuid.UUID)
		return c.String(200, "ok")
	})

	err := handler(c)
	assert.NoError(t, err)
	assert.Equal(t, 200, rec2.Code)
	assert.Equal(t, userID, gotUserID)
}

// --- handleResetSentiment tests ---

func TestHandleResetSentiment_BadUUID(t *testing.T) {
	srv := newTestServer(t, &mockDataStore{}, &mockSentimentService{})
	e := srv.echo

	req := httptest.NewRequest(http.MethodPost, "/api/reset/not-a-uuid", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("uuid")
	c.SetParamValues("not-a-uuid")
	c.Set("userID", uuid.New())

	err := srv.handleResetSentiment(c)
	assert.NoError(t, err)
	assert.Equal(t, 400, rec.Code)
}

func TestHandleResetSentiment_WrongUser(t *testing.T) {
	userID := uuid.New()
	overlayUUID := uuid.New()
	differentOverlay := uuid.New()

	db := &mockDataStore{
		getUserByIDFn: func(_ context.Context, _ uuid.UUID) (*models.User, error) {
			return &models.User{ID: userID, OverlayUUID: overlayUUID}, nil
		},
	}

	srv := newTestServer(t, db, &mockSentimentService{})
	e := srv.echo

	req := httptest.NewRequest(http.MethodPost, "/api/reset/"+differentOverlay.String(), nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("uuid")
	c.SetParamValues(differentOverlay.String())
	c.Set("userID", userID)

	err := srv.handleResetSentiment(c)
	assert.NoError(t, err)
	assert.Equal(t, 403, rec.Code)
}

func TestHandleResetSentiment_Success(t *testing.T) {
	userID := uuid.New()
	overlayUUID := uuid.New()
	var resetCalled bool

	db := &mockDataStore{
		getUserByIDFn: func(_ context.Context, _ uuid.UUID) (*models.User, error) {
			return &models.User{ID: userID, OverlayUUID: overlayUUID}, nil
		},
	}
	sent := &mockSentimentService{
		resetSessionFn: func(id uuid.UUID) {
			resetCalled = true
			assert.Equal(t, overlayUUID, id)
		},
	}

	srv := newTestServer(t, db, sent)
	e := srv.echo

	req := httptest.NewRequest(http.MethodPost, "/api/reset/"+overlayUUID.String(), nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("uuid")
	c.SetParamValues(overlayUUID.String())
	c.Set("userID", userID)

	err := srv.handleResetSentiment(c)
	assert.NoError(t, err)
	assert.Equal(t, 200, rec.Code)
	assert.True(t, resetCalled)
}

// --- handleSaveConfig tests ---

func TestHandleSaveConfig_BadDecay(t *testing.T) {
	srv := newTestServer(t, &mockDataStore{}, &mockSentimentService{})
	e := srv.echo

	form := url.Values{}
	form.Set("for_trigger", "yes")
	form.Set("against_trigger", "no")
	form.Set("left_label", "Left")
	form.Set("right_label", "Right")
	form.Set("decay_speed", "not-a-number")

	req := httptest.NewRequest(http.MethodPost, "/dashboard/config", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set("userID", uuid.New())

	err := srv.handleSaveConfig(c)
	assert.NoError(t, err)
	assert.Equal(t, 400, rec.Code)
	assert.Contains(t, rec.Body.String(), "Invalid decay speed")
}

func TestHandleSaveConfig_ValidationError(t *testing.T) {
	srv := newTestServer(t, &mockDataStore{}, &mockSentimentService{})
	e := srv.echo

	form := url.Values{}
	form.Set("for_trigger", "  ")
	form.Set("against_trigger", "no")
	form.Set("left_label", "Left")
	form.Set("right_label", "Right")
	form.Set("decay_speed", "1.0")

	req := httptest.NewRequest(http.MethodPost, "/dashboard/config", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set("userID", uuid.New())

	err := srv.handleSaveConfig(c)
	assert.NoError(t, err)
	assert.Equal(t, 400, rec.Code)
	assert.Contains(t, rec.Body.String(), "Validation error")
}

func TestHandleSaveConfig_Success(t *testing.T) {
	userID := uuid.New()
	overlayUUID := uuid.New()

	db := &mockDataStore{
		getUserByIDFn: func(_ context.Context, _ uuid.UUID) (*models.User, error) {
			return &models.User{ID: userID, OverlayUUID: overlayUUID}, nil
		},
	}
	sent := &mockSentimentService{}

	srv := newTestServer(t, db, sent)
	e := srv.echo

	form := url.Values{}
	form.Set("for_trigger", "PogChamp")
	form.Set("against_trigger", "NotLikeThis")
	form.Set("left_label", "Sad")
	form.Set("right_label", "Happy")
	form.Set("decay_speed", "1.5")

	req := httptest.NewRequest(http.MethodPost, "/dashboard/config", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set("userID", userID)

	err := srv.handleSaveConfig(c)
	assert.NoError(t, err)
	assert.Equal(t, 302, rec.Code)
	assert.Contains(t, rec.Header().Get("Location"), "/dashboard")
}

// --- handleDashboard tests ---

func TestHandleDashboard_DBError(t *testing.T) {
	db := &mockDataStore{
		getUserByIDFn: func(_ context.Context, _ uuid.UUID) (*models.User, error) {
			return nil, fmt.Errorf("db error")
		},
	}

	srv := newTestServer(t, db, &mockSentimentService{})
	e := srv.echo

	req := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set("userID", uuid.New())

	err := srv.handleDashboard(c)
	assert.NoError(t, err)
	assert.Equal(t, 500, rec.Code)
}

func TestHandleDashboard_Success(t *testing.T) {
	userID := uuid.New()
	overlayUUID := uuid.New()

	db := &mockDataStore{
		getUserByIDFn: func(_ context.Context, _ uuid.UUID) (*models.User, error) {
			return &models.User{ID: userID, OverlayUUID: overlayUUID, TwitchUsername: "testuser"}, nil
		},
	}

	srv := newTestServer(t, db, &mockSentimentService{})
	e := srv.echo

	req := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set("userID", userID)

	err := srv.handleDashboard(c)
	assert.NoError(t, err)
	assert.Equal(t, 200, rec.Code)
	assert.Contains(t, rec.Body.String(), "testuser")
}

// --- handleOverlay tests ---

func TestHandleOverlay_BadUUID(t *testing.T) {
	srv := newTestServer(t, &mockDataStore{}, &mockSentimentService{})
	e := srv.echo

	req := httptest.NewRequest(http.MethodGet, "/overlay/not-a-uuid", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("uuid")
	c.SetParamValues("not-a-uuid")

	err := srv.handleOverlay(c)
	assert.NoError(t, err)
	assert.Equal(t, 400, rec.Code)
}

func TestHandleOverlay_NotFound(t *testing.T) {
	srv := newTestServer(t, &mockDataStore{}, &mockSentimentService{})
	e := srv.echo

	overlayUUID := uuid.New()
	req := httptest.NewRequest(http.MethodGet, "/overlay/"+overlayUUID.String(), nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("uuid")
	c.SetParamValues(overlayUUID.String())

	err := srv.handleOverlay(c)
	assert.NoError(t, err)
	assert.Equal(t, 404, rec.Code)
}

func TestHandleOverlay_Success(t *testing.T) {
	userID := uuid.New()
	overlayUUID := uuid.New()

	db := &mockDataStore{
		getUserByOverlayFn: func(_ context.Context, _ uuid.UUID) (*models.User, error) {
			return &models.User{ID: userID, OverlayUUID: overlayUUID}, nil
		},
	}

	srv := newTestServer(t, db, &mockSentimentService{})
	e := srv.echo

	req := httptest.NewRequest(http.MethodGet, "/overlay/"+overlayUUID.String(), nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("uuid")
	c.SetParamValues(overlayUUID.String())

	err := srv.handleOverlay(c)
	assert.NoError(t, err)
	assert.Equal(t, 200, rec.Code)
	assert.Contains(t, rec.Body.String(), "Against")
	assert.Contains(t, rec.Body.String(), "For")
}

// --- handleRotateOverlayUUID tests ---

func TestHandleRotateOverlayUUID_DBError(t *testing.T) {
	db := &mockDataStore{
		rotateOverlayUUIDFn: func(_ context.Context, _ uuid.UUID) (uuid.UUID, error) {
			return uuid.Nil, fmt.Errorf("db error")
		},
	}

	srv := newTestServer(t, db, &mockSentimentService{})
	e := srv.echo

	req := httptest.NewRequest(http.MethodPost, "/api/rotate-overlay-uuid", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set("userID", uuid.New())

	err := srv.handleRotateOverlayUUID(c)
	assert.NoError(t, err)
	assert.Equal(t, 500, rec.Code)
}

func TestHandleRotateOverlayUUID_Success(t *testing.T) {
	newUUID := uuid.New()
	db := &mockDataStore{
		rotateOverlayUUIDFn: func(_ context.Context, _ uuid.UUID) (uuid.UUID, error) {
			return newUUID, nil
		},
	}

	srv := newTestServer(t, db, &mockSentimentService{})
	e := srv.echo

	req := httptest.NewRequest(http.MethodPost, "/api/rotate-overlay-uuid", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set("userID", uuid.New())

	err := srv.handleRotateOverlayUUID(c)
	assert.NoError(t, err)
	assert.Equal(t, 200, rec.Code)
	assert.Contains(t, rec.Body.String(), newUUID.String())
}

// --- handleLoginPage tests ---

func TestHandleLoginPage_Success(t *testing.T) {
	srv := newTestServer(t, &mockDataStore{}, &mockSentimentService{})
	e := srv.echo

	req := httptest.NewRequest(http.MethodGet, "/auth/login", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := srv.handleLoginPage(c)
	assert.NoError(t, err)
	assert.Equal(t, 200, rec.Code)
	assert.Contains(t, rec.Body.String(), "id.twitch.tv")
	assert.Contains(t, rec.Body.String(), "state=")
}

// --- handleLogout tests ---

func TestHandleLogout_Success(t *testing.T) {
	srv := newTestServer(t, &mockDataStore{}, &mockSentimentService{})
	e := srv.echo

	req := httptest.NewRequest(http.MethodPost, "/auth/logout", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := srv.handleLogout(c)
	assert.NoError(t, err)
	assert.Equal(t, 302, rec.Code)
	assert.Contains(t, rec.Header().Get("Location"), "/auth/login")
}
