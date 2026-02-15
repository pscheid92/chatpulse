package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/pscheid92/chatpulse/internal/domain"
	"github.com/pscheid92/uuid"
	"github.com/stretchr/testify/assert"
)

// --- handleOverlay tests ---

func TestHandleOverlay_BadUUID(t *testing.T) {
	srv := newTestServer(t, &mockUserService{}, &mockConfigService{})
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
	srv := newTestServer(t, &mockUserService{}, &mockConfigService{})
	e := srv.echo

	overlayUUID := uuid.NewV4()
	req := httptest.NewRequest(http.MethodGet, "/overlay/"+overlayUUID.String(), nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("uuid")
	c.SetParamValues(overlayUUID.String())

	_ = callHandler(srv.handleOverlay, c)
	assert.Equal(t, 404, rec.Code)
}

func TestHandleOverlay_Success(t *testing.T) {
	userID := uuid.NewV4()
	overlayUUID := uuid.NewV4()

	users := &mockUserService{
		getUserByOverlayFn: func(_ context.Context, _ uuid.UUID) (*domain.User, error) {
			return &domain.User{ID: userID, OverlayUUID: overlayUUID}, nil
		},
	}

	srv := newTestServer(t, users, &mockConfigService{})
	e := srv.echo

	req := httptest.NewRequest(http.MethodGet, "/overlay/"+overlayUUID.String(), nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("uuid")
	c.SetParamValues(overlayUUID.String())

	_ = callHandler(srv.handleOverlay, c)
	assert.Equal(t, 200, rec.Code)
}
