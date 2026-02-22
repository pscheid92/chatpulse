package httpserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/pscheid92/chatpulse/internal/app"
	"github.com/pscheid92/chatpulse/internal/domain"
	"github.com/pscheid92/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const csrfTokenCookieName = "csrf_token"

// TestCSRFProtection_ConfigSave verifies CSRF protection on config save endpoint
func TestCSRFProtection_ConfigSave(t *testing.T) {
	userID := uuid.NewV4()
	overlayUUID := uuid.NewV4()

	app := &mockAppService{
		getStreamerByIDFn: func(ctx context.Context, id uuid.UUID) (*domain.Streamer, error) {
			return &domain.Streamer{
				ID:          userID,
				OverlayUUID: overlayUUID,
			}, nil
		},
		saveConfigFn: func(_ context.Context, _ app.SaveConfigRequest) error {
			return nil
		},
	}

	srv := newTestServer(t, app)

	t.Run("rejects POST without CSRF token", func(t *testing.T) {
		formData := url.Values{}
		formData.Set("for_trigger", "yes")
		formData.Set("against_trigger", "no")
		formData.Set("against_label", "Against")
		formData.Set("for_label", "For")
		formData.Set("memory_seconds", "30")

		req := httptest.NewRequest(http.MethodPost, "/dashboard/config", strings.NewReader(formData.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()

		// Set authenticated session
		setSessionUserID(t, srv, req, rec, userID)

		srv.echo.ServeHTTP(rec, req)

		// Should be rejected with 400 Bad Request (missing CSRF token)
		// Echo's CSRF middleware returns 400, not 403
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("accepts POST with valid CSRF token", func(t *testing.T) {
		// First, GET the dashboard to obtain CSRF token
		getReq := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
		getRec := httptest.NewRecorder()
		setSessionUserID(t, srv, getReq, getRec, userID)

		srv.echo.ServeHTTP(getRec, getReq)
		require.Equal(t, http.StatusOK, getRec.Code)

		// Extract CSRF cookie
		cookies := getRec.Result().Cookies()
		var csrfCookie *http.Cookie
		for _, c := range cookies {
			if c.Name == csrfTokenCookieName {
				csrfCookie = c
				break
			}
		}
		require.NotNil(t, csrfCookie, "CSRF cookie should be set")

		// Now POST with CSRF token
		formData := url.Values{}
		formData.Set("for_trigger", "yes")
		formData.Set("against_trigger", "no")
		formData.Set("against_label", "Against")
		formData.Set("for_label", "For")
		formData.Set("memory_seconds", "30")
		formData.Set("display_mode", "combined")
		formData.Set(csrfTokenCookieName, csrfCookie.Value)

		postReq := httptest.NewRequest(http.MethodPost, "/dashboard/config", strings.NewReader(formData.Encode()))
		postReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		postReq.AddCookie(csrfCookie)
		postRec := httptest.NewRecorder()
		setSessionUserID(t, srv, postReq, postRec, userID)

		srv.echo.ServeHTTP(postRec, postReq)

		// Should succeed with redirect
		assert.Equal(t, http.StatusFound, postRec.Code)
	})
}

// TestCSRFProtection_ResetSentiment verifies CSRF protection on reset endpoint
func TestCSRFProtection_ResetSentiment(t *testing.T) {
	userID := uuid.NewV4()
	overlayUUID := uuid.NewV4()

	app := &mockAppService{
		getStreamerByIDFn: func(ctx context.Context, id uuid.UUID) (*domain.Streamer, error) {
			return &domain.Streamer{
				ID:          userID,
				OverlayUUID: overlayUUID,
			}, nil
		},
		getConfigFn: func(_ context.Context, _ uuid.UUID) (*domain.OverlayConfigWithVersion, error) {
			return &domain.OverlayConfigWithVersion{
				OverlayConfig: domain.OverlayConfig{
					MemorySeconds: 30,
					ForTrigger:    "yes", ForLabel: "For",
					AgainstTrigger: "no", AgainstLabel: "Against",
				},
			}, nil
		},
		resetSentimentFn: func(ctx context.Context, overlayUUID uuid.UUID) error {
			return nil
		},
	}

	srv := newTestServer(t, app)

	t.Run("rejects POST without CSRF token", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/reset/"+overlayUUID.String(), nil)
		rec := httptest.NewRecorder()
		setSessionUserID(t, srv, req, rec, userID)

		srv.echo.ServeHTTP(rec, req)

		// Echo's CSRF middleware returns 400 Bad Request, not 403
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("accepts POST with valid CSRF token in header", func(t *testing.T) {
		// First, GET the dashboard to obtain CSRF token
		getReq := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
		getRec := httptest.NewRecorder()
		setSessionUserID(t, srv, getReq, getRec, userID)

		srv.echo.ServeHTTP(getRec, getReq)
		require.Equal(t, http.StatusOK, getRec.Code)

		// Extract CSRF cookie
		cookies := getRec.Result().Cookies()
		var csrfCookie *http.Cookie
		for _, c := range cookies {
			if c.Name == csrfTokenCookieName {
				csrfCookie = c
				break
			}
		}
		require.NotNil(t, csrfCookie, "CSRF cookie should be set")

		// Now POST with CSRF token in header
		postReq := httptest.NewRequest(http.MethodPost, "/api/reset/"+overlayUUID.String(), nil)
		postReq.Header.Set("X-CSRF-Token", csrfCookie.Value)
		postReq.AddCookie(csrfCookie)
		postRec := httptest.NewRecorder()
		setSessionUserID(t, srv, postReq, postRec, userID)

		srv.echo.ServeHTTP(postRec, postReq)

		assert.Equal(t, http.StatusOK, postRec.Code)
	})
}

// TestCSRFProtection_RotateOverlayUUID verifies CSRF protection on rotate UUID endpoint
func TestCSRFProtection_RotateOverlayUUID(t *testing.T) {
	userID := uuid.NewV4()
	overlayUUID := uuid.NewV4()

	app := &mockAppService{
		getStreamerByIDFn: func(ctx context.Context, id uuid.UUID) (*domain.Streamer, error) {
			return &domain.Streamer{
				ID:          userID,
				OverlayUUID: overlayUUID,
			}, nil
		},
		getConfigFn: func(_ context.Context, _ uuid.UUID) (*domain.OverlayConfigWithVersion, error) {
			return &domain.OverlayConfigWithVersion{
				OverlayConfig: domain.OverlayConfig{
					MemorySeconds: 30,
					ForTrigger:    "yes", ForLabel: "For",
					AgainstTrigger: "no", AgainstLabel: "Against",
				},
			}, nil
		},
		rotateOverlayUUIDFn: func(ctx context.Context, userID uuid.UUID) (uuid.UUID, error) {
			return uuid.NewV4(), nil
		},
	}

	srv := newTestServer(t, app)

	t.Run("rejects POST without CSRF token", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/rotate-overlay-uuid", nil)
		rec := httptest.NewRecorder()
		setSessionUserID(t, srv, req, rec, userID)

		srv.echo.ServeHTTP(rec, req)

		// Echo's CSRF middleware returns 400 Bad Request, not 403
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("accepts POST with valid CSRF token", func(t *testing.T) {
		// First, GET the dashboard to obtain CSRF token
		getReq := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
		getRec := httptest.NewRecorder()
		setSessionUserID(t, srv, getReq, getRec, userID)

		srv.echo.ServeHTTP(getRec, getReq)
		require.Equal(t, http.StatusOK, getRec.Code)

		// Extract CSRF cookie
		cookies := getRec.Result().Cookies()
		var csrfCookie *http.Cookie
		for _, c := range cookies {
			if c.Name == csrfTokenCookieName {
				csrfCookie = c
				break
			}
		}
		require.NotNil(t, csrfCookie, "CSRF cookie should be set")

		// Now POST with CSRF token
		postReq := httptest.NewRequest(http.MethodPost, "/api/rotate-overlay-uuid", nil)
		postReq.Header.Set("X-CSRF-Token", csrfCookie.Value)
		postReq.AddCookie(csrfCookie)
		postRec := httptest.NewRecorder()
		setSessionUserID(t, srv, postReq, postRec, userID)

		srv.echo.ServeHTTP(postRec, postReq)

		assert.Equal(t, http.StatusOK, postRec.Code)
	})
}

// TestCSRFProtection_Logout verifies CSRF protection on logout endpoint
func TestCSRFProtection_Logout(t *testing.T) {
	userID := uuid.NewV4()
	overlayUUID := uuid.NewV4()

	app := &mockAppService{
		getStreamerByIDFn: func(ctx context.Context, id uuid.UUID) (*domain.Streamer, error) {
			return &domain.Streamer{
				ID:          userID,
				OverlayUUID: overlayUUID,
			}, nil
		},
		getConfigFn: func(_ context.Context, _ uuid.UUID) (*domain.OverlayConfigWithVersion, error) {
			return &domain.OverlayConfigWithVersion{
				OverlayConfig: domain.OverlayConfig{
					MemorySeconds: 30,
					ForTrigger:    "yes", ForLabel: "For",
					AgainstTrigger: "no", AgainstLabel: "Against",
				},
			}, nil
		},
	}

	srv := newTestServer(t, app)

	t.Run("rejects POST without CSRF token", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/auth/logout", nil)
		rec := httptest.NewRecorder()
		setSessionUserID(t, srv, req, rec, userID)

		srv.echo.ServeHTTP(rec, req)

		// Echo's CSRF middleware returns 400 Bad Request, not 403
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("accepts POST with valid CSRF token", func(t *testing.T) {
		// First, GET the dashboard to obtain CSRF token
		getReq := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
		getRec := httptest.NewRecorder()
		setSessionUserID(t, srv, getReq, getRec, userID)

		srv.echo.ServeHTTP(getRec, getReq)
		require.Equal(t, http.StatusOK, getRec.Code)

		// Extract CSRF cookie
		cookies := getRec.Result().Cookies()
		var csrfCookie *http.Cookie
		for _, c := range cookies {
			if c.Name == csrfTokenCookieName {
				csrfCookie = c
				break
			}
		}
		require.NotNil(t, csrfCookie, "CSRF cookie should be set")

		// Now POST logout with CSRF token
		formData := url.Values{}
		formData.Set(csrfTokenCookieName, csrfCookie.Value)

		postReq := httptest.NewRequest(http.MethodPost, "/auth/logout", strings.NewReader(formData.Encode()))
		postReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		postReq.AddCookie(csrfCookie)
		postRec := httptest.NewRecorder()
		setSessionUserID(t, srv, postReq, postRec, userID)

		srv.echo.ServeHTTP(postRec, postReq)

		// Should redirect to login
		assert.Equal(t, http.StatusFound, postRec.Code)
	})
}

// TestCSRFProtection_WebhookExempt verifies webhook endpoint is exempt from CSRF
func TestCSRFProtection_WebhookExempt(t *testing.T) {
	srv := newTestServer(t, &mockAppService{},
		withWebhookHandler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		})),
	)

	t.Run("webhook accepts POST without CSRF token", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/webhooks/eventsub", nil)
		rec := httptest.NewRecorder()

		srv.echo.ServeHTTP(rec, req)

		// Should NOT be rejected with 403 - webhook handler should be called
		// (will return 200 from our mock)
		assert.Equal(t, http.StatusOK, rec.Code)
	})
}
