package httpserver

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/pscheid92/chatpulse/internal/domain"
	"github.com/pscheid92/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- requireAuth tests ---

func TestRequireAuth_NoSession(t *testing.T) {
	srv := newTestServer(t, &mockAppService{})
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
	srv := newTestServer(t, &mockAppService{})
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
	userID := uuid.NewV4()
	srv := newTestServer(t, &mockAppService{
		getStreamerByIDFn: func(_ context.Context, id uuid.UUID) (*domain.Streamer, error) {
			if id == userID {
				return &domain.Streamer{ID: userID}, nil
			}
			return nil, domain.ErrStreamerNotFound
		},
	})
	e := srv.echo

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

func TestRequireAuth_StaleSession(t *testing.T) {
	srv := newTestServer(t, &mockAppService{
		getStreamerByIDFn: func(_ context.Context, _ uuid.UUID) (*domain.Streamer, error) {
			return nil, domain.ErrStreamerNotFound
		},
	})
	e := srv.echo
	userID := uuid.NewV4()

	req := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	rec := httptest.NewRecorder()
	setSessionUserID(t, srv, req, rec, userID)

	req2 := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	for _, cookie := range rec.Result().Cookies() {
		req2.AddCookie(cookie)
	}
	rec2 := httptest.NewRecorder()
	c := e.NewContext(req2, rec2)

	handler := srv.requireAuth(func(c echo.Context) error {
		return c.String(200, "ok")
	})

	err := handler(c)
	assert.NoError(t, err)
	assert.Equal(t, 302, rec2.Code)
	assert.Equal(t, "/auth/login", rec2.Header().Get("Location"))
}

// --- handleLanding tests ---

func TestHandleLanding_NoSession(t *testing.T) {
	srv := newTestServer(t, &mockAppService{})
	e := srv.echo

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := srv.handleLanding(c)
	assert.NoError(t, err)
	assert.Equal(t, 200, rec.Code)
	assert.Contains(t, rec.Body.String(), "Landing")
}

func TestHandleLanding_WithValidSession(t *testing.T) {
	srv := newTestServer(t, &mockAppService{
		getStreamerByIDFn: func(_ context.Context, _ uuid.UUID) (*domain.Streamer, error) {
			return &domain.Streamer{}, nil
		},
	})
	e := srv.echo
	userID := uuid.NewV4()

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	setSessionUserID(t, srv, req, rec, userID)

	req2 := httptest.NewRequest(http.MethodGet, "/", nil)
	for _, cookie := range rec.Result().Cookies() {
		req2.AddCookie(cookie)
	}
	rec2 := httptest.NewRecorder()
	c := e.NewContext(req2, rec2)

	err := srv.handleLanding(c)
	assert.NoError(t, err)
	assert.Equal(t, 302, rec2.Code)
	assert.Equal(t, "/dashboard", rec2.Header().Get("Location"))
}

func TestHandleLanding_InvalidSessionUUID(t *testing.T) {
	srv := newTestServer(t, &mockAppService{})
	e := srv.echo

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	session, err := srv.sessionStore.Get(req, sessionName)
	require.NoError(t, err)
	session.Values[sessionKeyToken] = "not-a-uuid"
	require.NoError(t, session.Save(req, rec))

	req2 := httptest.NewRequest(http.MethodGet, "/", nil)
	for _, cookie := range rec.Result().Cookies() {
		req2.AddCookie(cookie)
	}
	rec2 := httptest.NewRecorder()
	c := e.NewContext(req2, rec2)

	err = srv.handleLanding(c)
	assert.NoError(t, err)
	assert.Equal(t, 200, rec2.Code)
	assert.Contains(t, rec2.Body.String(), "Landing")
}

// --- handleLoginPage tests ---

func TestHandleLoginPage_Success(t *testing.T) {
	srv := newTestServer(t, &mockAppService{})
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
	srv := newTestServer(t, &mockAppService{})
	e := srv.echo

	req := httptest.NewRequest(http.MethodPost, "/auth/logout", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := srv.handleLogout(c)
	assert.NoError(t, err)
	assert.Equal(t, 302, rec.Code)
	assert.Contains(t, rec.Header().Get("Location"), "/auth/login")
}

// --- handleOAuthCallback tests ---

func setupOAuthCallbackRequest(t *testing.T, srv *Server, code, state string) (echo.Context, *httptest.ResponseRecorder) { //nolint:unparam // code is always "valid-code" in tests but kept as parameter for clarity
	t.Helper()

	// First, create a session with a stored OAuth state
	setupReq := httptest.NewRequest(http.MethodGet, "/auth/callback", nil)
	setupRec := httptest.NewRecorder()
	session, err := srv.sessionStore.Get(setupReq, sessionName)
	require.NoError(t, err)
	session.Values[sessionKeyOAuthState] = state
	require.NoError(t, session.Save(setupReq, setupRec))

	// Build the actual callback request with session cookie and query params
	url := fmt.Sprintf("/auth/callback?code=%s&state=%s", code, state)
	req := httptest.NewRequest(http.MethodGet, url, nil)
	for _, cookie := range setupRec.Result().Cookies() {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	c := srv.echo.NewContext(req, rec)

	return c, rec
}

func TestHandleOAuthCallback_Success(t *testing.T) {
	userID := uuid.NewV4()
	app := &mockAppService{
		upsertStreamerFn: func(_ context.Context, _, _, _, _ string, _ time.Time) (*domain.Streamer, error) {
			return &domain.Streamer{ID: userID, TwitchUsername: "testuser"}, nil
		},
	}
	oauth := &mockOAuthClient{
		result: &twitchTokenResult{
			AccessToken:  "access-token",
			RefreshToken: "refresh-token",
			ExpiresIn:    3600,
			UserID:       "12345",
			Username:     "testuser",
		},
	}

	srv := newTestServer(t, app, withOAuthClient(oauth), withTwitchService(&mockTwitchService{}))

	c, rec := setupOAuthCallbackRequest(t, srv, "valid-code", "valid-state")

	err := srv.handleOAuthCallback(c)
	assert.NoError(t, err)
	assert.Equal(t, 302, rec.Code)
	assert.Contains(t, rec.Header().Get("Location"), "/dashboard")
}

func TestHandleOAuthCallback_MissingCode(t *testing.T) {
	srv := newTestServer(t, &mockAppService{})
	e := srv.echo

	req := httptest.NewRequest(http.MethodGet, "/auth/callback", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	_ = callHandler(srv.handleOAuthCallback, c)
	assert.Equal(t, 400, rec.Code)
}

func TestHandleOAuthCallback_InvalidState(t *testing.T) {
	srv := newTestServer(t, &mockAppService{})

	// Set up session with one state, but send a different state in the request
	setupReq := httptest.NewRequest(http.MethodGet, "/auth/callback", nil)
	setupRec := httptest.NewRecorder()
	session, err := srv.sessionStore.Get(setupReq, sessionName)
	require.NoError(t, err)
	session.Values[sessionKeyOAuthState] = "expected-state"
	require.NoError(t, session.Save(setupReq, setupRec))

	req := httptest.NewRequest(http.MethodGet, "/auth/callback?code=valid-code&state=wrong-state", nil)
	for _, cookie := range setupRec.Result().Cookies() {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	c := srv.echo.NewContext(req, rec)

	_ = callHandler(srv.handleOAuthCallback, c)
	assert.Equal(t, 400, rec.Code)
}

func TestHandleOAuthCallback_ExchangeError(t *testing.T) {
	oauth := &mockOAuthClient{
		err: errors.New("exchange failed"),
	}

	srv := newTestServer(t, &mockAppService{}, withOAuthClient(oauth))

	c, rec := setupOAuthCallbackRequest(t, srv, "valid-code", "valid-state")

	_ = callHandler(srv.handleOAuthCallback, c)
	assert.Equal(t, 502, rec.Code) // External errors return 502
}

func TestHandleOAuthCallback_SubscribeCalledOnSuccess(t *testing.T) {
	userID := uuid.NewV4()
	twitchUserID := "12345"
	var subscribeCalled bool
	var subscribedBroadcasterID string

	app := &mockAppService{
		upsertStreamerFn: func(_ context.Context, _, _, _, _ string, _ time.Time) (*domain.Streamer, error) {
			return &domain.Streamer{ID: userID, TwitchUsername: "testuser"}, nil
		},
	}
	oauth := &mockOAuthClient{
		result: &twitchTokenResult{
			AccessToken:  "access-token",
			RefreshToken: "refresh-token",
			ExpiresIn:    3600,
			UserID:       twitchUserID,
			Username:     "testuser",
		},
	}
	ts := &mockTwitchService{
		subscribeFn: func(_ context.Context, _ uuid.UUID, broadcasterUserID string) error {
			subscribeCalled = true
			subscribedBroadcasterID = broadcasterUserID
			return nil
		},
	}

	srv := newTestServer(t, app, withOAuthClient(oauth), withTwitchService(ts))

	c, rec := setupOAuthCallbackRequest(t, srv, "valid-code", "valid-state")

	err := srv.handleOAuthCallback(c)
	assert.NoError(t, err)
	assert.Equal(t, 302, rec.Code)
	assert.Contains(t, rec.Header().Get("Location"), "/dashboard")
	assert.True(t, subscribeCalled, "Subscribe should be called after UpsertStreamer")
	assert.Equal(t, twitchUserID, subscribedBroadcasterID)
}

func TestHandleOAuthCallback_SubscribeFailureDoesNotBlockOAuth(t *testing.T) {
	userID := uuid.NewV4()

	app := &mockAppService{
		upsertStreamerFn: func(_ context.Context, _, _, _, _ string, _ time.Time) (*domain.Streamer, error) {
			return &domain.Streamer{ID: userID, TwitchUsername: "testuser"}, nil
		},
	}
	oauth := &mockOAuthClient{
		result: &twitchTokenResult{
			AccessToken:  "access-token",
			RefreshToken: "refresh-token",
			ExpiresIn:    3600,
			UserID:       "12345",
			Username:     "testuser",
		},
	}
	ts := &mockTwitchService{
		subscribeFn: func(_ context.Context, _ uuid.UUID, _ string) error {
			return errors.New("subscribe failed: network error")
		},
	}

	srv := newTestServer(t, app, withOAuthClient(oauth), withTwitchService(ts))

	c, rec := setupOAuthCallbackRequest(t, srv, "valid-code", "valid-state")

	err := srv.handleOAuthCallback(c)
	assert.NoError(t, err)
	assert.Equal(t, 302, rec.Code)
	assert.Contains(t, rec.Header().Get("Location"), "/dashboard")
}

func TestHandleOAuthCallback_ReloginIdempotent(t *testing.T) {
	userID := uuid.NewV4()
	subscribeCallCount := 0

	app := &mockAppService{
		upsertStreamerFn: func(_ context.Context, _, _, _, _ string, _ time.Time) (*domain.Streamer, error) {
			return &domain.Streamer{ID: userID, TwitchUsername: "testuser"}, nil
		},
	}
	oauth := &mockOAuthClient{
		result: &twitchTokenResult{
			AccessToken:  "access-token",
			RefreshToken: "refresh-token",
			ExpiresIn:    3600,
			UserID:       "12345",
			Username:     "testuser",
		},
	}
	ts := &mockTwitchService{
		subscribeFn: func(_ context.Context, _ uuid.UUID, _ string) error {
			subscribeCallCount++
			// Simulate 409 Conflict handled as success (returns nil)
			return nil
		},
	}

	srv := newTestServer(t, app, withOAuthClient(oauth), withTwitchService(ts))

	// First login
	c1, rec1 := setupOAuthCallbackRequest(t, srv, "valid-code", "valid-state")
	err := srv.handleOAuthCallback(c1)
	assert.NoError(t, err)
	assert.Equal(t, 302, rec1.Code)

	// Second login (re-login)
	c2, rec2 := setupOAuthCallbackRequest(t, srv, "valid-code", "valid-state-2")
	err = srv.handleOAuthCallback(c2)
	assert.NoError(t, err)
	assert.Equal(t, 302, rec2.Code)

	assert.Equal(t, 2, subscribeCallCount, "Subscribe should be called on each login (idempotent)")
}

func TestHandleOAuthCallback_DBError(t *testing.T) {
	app := &mockAppService{
		upsertStreamerFn: func(_ context.Context, _, _, _, _ string, _ time.Time) (*domain.Streamer, error) {
			return nil, errors.New("db error")
		},
	}
	oauth := &mockOAuthClient{
		result: &twitchTokenResult{
			AccessToken:  "access-token",
			RefreshToken: "refresh-token",
			ExpiresIn:    3600,
			UserID:       "12345",
			Username:     "testuser",
		},
	}

	srv := newTestServer(t, app, withOAuthClient(oauth))

	c, rec := setupOAuthCallbackRequest(t, srv, "valid-code", "valid-state")

	_ = callHandler(srv.handleOAuthCallback, c)
	assert.Equal(t, 500, rec.Code)
}
