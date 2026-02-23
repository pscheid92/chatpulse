package httpserver

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/pscheid92/chatpulse/internal/domain"
	"github.com/pscheid92/uuid"
	"github.com/stretchr/testify/assert"
)

// --- handleSaveConfig tests ---

func TestHandleSaveConfig_BadMemorySeconds(t *testing.T) {
	srv := newTestServer(t, &mockAppService{})
	e := srv.echo

	form := url.Values{}
	form.Set("for_trigger", "yes")
	form.Set("against_trigger", "no")
	form.Set("against_label", "Left")
	form.Set("for_label", "Right")
	form.Set("memory_seconds", "not-a-number")
	form.Set("display_mode", "combined")
	form.Set("theme", "dark")

	req := httptest.NewRequest(http.MethodPost, "/dashboard/config", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set("userID", uuid.NewV4())

	_ = callHandler(srv.handleSaveConfig, c)
	assert.Equal(t, 400, rec.Code)
}

func TestHandleSaveConfig_MemorySecondsTooLow(t *testing.T) {
	srv := newTestServer(t, &mockAppService{})
	e := srv.echo

	form := url.Values{}
	form.Set("for_trigger", "yes")
	form.Set("against_trigger", "no")
	form.Set("against_label", "Left")
	form.Set("for_label", "Right")
	form.Set("memory_seconds", "2")
	form.Set("display_mode", "combined")
	form.Set("theme", "dark")

	req := httptest.NewRequest(http.MethodPost, "/dashboard/config", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set("userID", uuid.NewV4())

	_ = callHandler(srv.handleSaveConfig, c)
	assert.Equal(t, 400, rec.Code)
}

func TestHandleSaveConfig_MemorySecondsTooHigh(t *testing.T) {
	srv := newTestServer(t, &mockAppService{})
	e := srv.echo

	form := url.Values{}
	form.Set("for_trigger", "yes")
	form.Set("against_trigger", "no")
	form.Set("against_label", "Left")
	form.Set("for_label", "Right")
	form.Set("memory_seconds", "999")
	form.Set("display_mode", "combined")
	form.Set("theme", "dark")

	req := httptest.NewRequest(http.MethodPost, "/dashboard/config", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set("userID", uuid.NewV4())

	_ = callHandler(srv.handleSaveConfig, c)
	assert.Equal(t, 400, rec.Code)
}

func TestHandleSaveConfig_ValidationError(t *testing.T) {
	srv := newTestServer(t, &mockAppService{})
	e := srv.echo

	form := url.Values{}
	form.Set("for_trigger", "  ")
	form.Set("against_trigger", "no")
	form.Set("against_label", "Left")
	form.Set("for_label", "Right")
	form.Set("memory_seconds", "30")
	form.Set("display_mode", "combined")
	form.Set("theme", "dark")

	req := httptest.NewRequest(http.MethodPost, "/dashboard/config", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set("userID", uuid.NewV4())

	_ = callHandler(srv.handleSaveConfig, c)
	assert.Equal(t, 400, rec.Code)
}

func TestHandleSaveConfig_InvalidDisplayMode(t *testing.T) {
	srv := newTestServer(t, &mockAppService{})
	e := srv.echo

	form := url.Values{}
	form.Set("for_trigger", "yes")
	form.Set("against_trigger", "no")
	form.Set("against_label", "Left")
	form.Set("for_label", "Right")
	form.Set("memory_seconds", "30")
	form.Set("display_mode", "invalid")
	form.Set("theme", "dark")

	req := httptest.NewRequest(http.MethodPost, "/dashboard/config", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set("userID", uuid.NewV4())

	_ = callHandler(srv.handleSaveConfig, c)
	assert.Equal(t, 400, rec.Code)
}

func TestHandleSaveConfig_Success(t *testing.T) {
	userID := uuid.NewV4()
	overlayUUID := uuid.NewV4()

	app := &mockAppService{
		getStreamerByIDFn: func(_ context.Context, _ uuid.UUID) (*domain.Streamer, error) {
			return &domain.Streamer{ID: userID, OverlayUUID: overlayUUID}, nil
		},
	}

	srv := newTestServer(t, app)
	e := srv.echo

	form := url.Values{}
	form.Set("for_trigger", "PogChamp")
	form.Set("against_trigger", "NotLikeThis")
	form.Set("against_label", "Sad")
	form.Set("for_label", "Happy")
	form.Set("memory_seconds", "45")
	form.Set("display_mode", "split")
	form.Set("theme", "light")

	req := httptest.NewRequest(http.MethodPost, "/dashboard/config", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set("userID", userID)

	_ = callHandler(srv.handleSaveConfig, c)
	assert.Equal(t, 302, rec.Code)
	assert.Contains(t, rec.Header().Get("Location"), "/dashboard")
}

// --- handleDashboard tests ---

func TestHandleDashboard_DBError(t *testing.T) {
	app := &mockAppService{
		getStreamerByIDFn: func(_ context.Context, _ uuid.UUID) (*domain.Streamer, error) {
			return nil, errors.New("db error")
		},
	}

	srv := newTestServer(t, app)
	e := srv.echo

	req := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set("userID", uuid.NewV4())

	_ = callHandler(srv.handleDashboard, c)
	assert.Equal(t, 500, rec.Code)
}

func TestHandleDashboard_Success(t *testing.T) {
	userID := uuid.NewV4()
	overlayUUID := uuid.NewV4()

	app := &mockAppService{
		getStreamerByIDFn: func(_ context.Context, _ uuid.UUID) (*domain.Streamer, error) {
			return &domain.Streamer{ID: userID, OverlayUUID: overlayUUID, TwitchUsername: "testuser"}, nil
		},
	}

	srv := newTestServer(t, app)
	e := srv.echo

	req := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set("userID", userID)

	_ = callHandler(srv.handleDashboard, c)
	assert.Equal(t, 200, rec.Code)
}
