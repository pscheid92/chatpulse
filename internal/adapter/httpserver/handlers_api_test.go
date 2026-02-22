package httpserver

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/pscheid92/chatpulse/internal/domain"
	"github.com/pscheid92/uuid"
	"github.com/stretchr/testify/assert"
)

// --- handleResetSentiment tests ---

func TestHandleResetSentiment_BadUUID(t *testing.T) {
	srv := newTestServer(t, &mockAppService{})
	e := srv.echo

	req := httptest.NewRequest(http.MethodPost, "/api/reset/not-a-uuid", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("uuid")
	c.SetParamValues("not-a-uuid")
	c.Set("userID", uuid.NewV4())

	_ = callHandler(srv.handleResetSentiment, c)
	assert.Equal(t, 400, rec.Code)
}

func TestHandleResetSentiment_WrongUser(t *testing.T) {
	userID := uuid.NewV4()
	overlayUUID := uuid.NewV4()
	differentOverlay := uuid.NewV4()

	app := &mockAppService{
		getStreamerByIDFn: func(_ context.Context, _ uuid.UUID) (*domain.Streamer, error) {
			return &domain.Streamer{ID: userID, OverlayUUID: overlayUUID}, nil
		},
	}

	srv := newTestServer(t, app)
	e := srv.echo

	req := httptest.NewRequest(http.MethodPost, "/api/reset/"+differentOverlay.String(), nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("uuid")
	c.SetParamValues(differentOverlay.String())
	c.Set("userID", userID)

	_ = callHandler(srv.handleResetSentiment, c)
	assert.Equal(t, 400, rec.Code) // Returns 400 because it's a validation error
}

func TestHandleResetSentiment_Success(t *testing.T) {
	userID := uuid.NewV4()
	overlayUUID := uuid.NewV4()
	var resetCalled bool

	app := &mockAppService{
		getStreamerByIDFn: func(_ context.Context, _ uuid.UUID) (*domain.Streamer, error) {
			return &domain.Streamer{ID: userID, OverlayUUID: overlayUUID}, nil
		},
		resetSentimentFn: func(_ context.Context, id uuid.UUID) error {
			resetCalled = true
			assert.Equal(t, overlayUUID, id)
			return nil
		},
	}

	srv := newTestServer(t, app)
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
	app := &mockAppService{
		rotateOverlayUUIDFn: func(_ context.Context, _ uuid.UUID) (uuid.UUID, error) {
			return uuid.Nil, errors.New("db error")
		},
	}

	srv := newTestServer(t, app)
	e := srv.echo

	req := httptest.NewRequest(http.MethodPost, "/api/rotate-overlay-uuid", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set("userID", uuid.NewV4())

	_ = callHandler(srv.handleRotateOverlayUUID, c)
	assert.Equal(t, 500, rec.Code)
}

func TestHandleRotateOverlayUUID_Success(t *testing.T) {
	newUUID := uuid.NewV4()
	app := &mockAppService{
		rotateOverlayUUIDFn: func(_ context.Context, _ uuid.UUID) (uuid.UUID, error) {
			return newUUID, nil
		},
	}

	srv := newTestServer(t, app)
	e := srv.echo

	req := httptest.NewRequest(http.MethodPost, "/api/rotate-overlay-uuid", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set("userID", uuid.NewV4())

	err := srv.handleRotateOverlayUUID(c)
	assert.NoError(t, err)
	assert.Equal(t, 200, rec.Code)
	assert.Contains(t, rec.Body.String(), newUUID.String())
}
