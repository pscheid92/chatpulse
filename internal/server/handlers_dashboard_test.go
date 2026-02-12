package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/pscheid92/chatpulse/internal/domain"
	"github.com/stretchr/testify/assert"
)

// --- handleSaveConfig tests ---

func TestHandleSaveConfig_BadDecay(t *testing.T) {
	srv := newTestServer(t, &mockAppService{})
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

	_ = callHandler(srv.handleSaveConfig, c)
	assert.Equal(t, 400, rec.Code)
}

func TestHandleSaveConfig_ValidationError(t *testing.T) {
	srv := newTestServer(t, &mockAppService{})
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

	_ = callHandler(srv.handleSaveConfig, c)
	assert.Equal(t, 400, rec.Code)
}

func TestHandleSaveConfig_Success(t *testing.T) {
	userID := uuid.New()
	overlayUUID := uuid.New()

	app := &mockAppService{
		getUserByIDFn: func(_ context.Context, _ uuid.UUID) (*domain.User, error) {
			return &domain.User{ID: userID, OverlayUUID: overlayUUID}, nil
		},
	}

	srv := newTestServer(t, app)
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

	_ = callHandler(srv.handleSaveConfig, c)
	assert.Equal(t, 302, rec.Code)
	assert.Contains(t, rec.Header().Get("Location"), "/dashboard")
}

// --- handleDashboard tests ---

func TestHandleDashboard_DBError(t *testing.T) {
	app := &mockAppService{
		getUserByIDFn: func(_ context.Context, _ uuid.UUID) (*domain.User, error) {
			return nil, fmt.Errorf("db error")
		},
	}

	srv := newTestServer(t, app)
	e := srv.echo

	req := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set("userID", uuid.New())

	_ = callHandler(srv.handleDashboard, c)
	assert.Equal(t, 500, rec.Code)
}

func TestHandleDashboard_Success(t *testing.T) {
	userID := uuid.New()
	overlayUUID := uuid.New()

	app := &mockAppService{
		getUserByIDFn: func(_ context.Context, _ uuid.UUID) (*domain.User, error) {
			return &domain.User{ID: userID, OverlayUUID: overlayUUID, TwitchUsername: "testuser"}, nil
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
