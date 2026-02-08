package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/pscheid92/chatpulse/internal/models"
	"github.com/stretchr/testify/assert"
)

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
