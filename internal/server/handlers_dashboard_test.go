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
	"github.com/pscheid92/chatpulse/internal/models"
	"github.com/stretchr/testify/assert"
)

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
