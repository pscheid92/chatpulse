package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/pscheid92/chatpulse/internal/domain"
	"github.com/stretchr/testify/assert"
)

// --- handleOverlay tests ---

func TestHandleOverlay_BadUUID(t *testing.T) {
	srv := newTestServer(t, &mockAppService{})
	e := srv.echo

	req := httptest.NewRequest(http.MethodGet, "/overlay/not-a-uuid", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("uuid")
	c.SetParamValues("not-a-uuid")

	_ = callHandler(srv.handleOverlay, c)
	assert.Equal(t, 400, rec.Code)
}

func TestHandleOverlay_NotFound(t *testing.T) {
	srv := newTestServer(t, &mockAppService{})
	e := srv.echo

	overlayUUID := uuid.New()
	req := httptest.NewRequest(http.MethodGet, "/overlay/"+overlayUUID.String(), nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("uuid")
	c.SetParamValues(overlayUUID.String())

	_ = callHandler(srv.handleOverlay, c)
	assert.Equal(t, 404, rec.Code)
}

func TestHandleOverlay_Success(t *testing.T) {
	userID := uuid.New()
	overlayUUID := uuid.New()

	app := &mockAppService{
		getUserByOverlayFn: func(_ context.Context, _ uuid.UUID) (*domain.User, error) {
			return &domain.User{ID: userID, OverlayUUID: overlayUUID}, nil
		},
	}

	srv := newTestServer(t, app)
	e := srv.echo

	req := httptest.NewRequest(http.MethodGet, "/overlay/"+overlayUUID.String(), nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("uuid")
	c.SetParamValues(overlayUUID.String())

	_ = callHandler(srv.handleOverlay, c)
	assert.Equal(t, 200, rec.Code)}
