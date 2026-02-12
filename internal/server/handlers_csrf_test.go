package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/pscheid92/chatpulse/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCSRFProtection_ConfigSave verifies CSRF protection on config save endpoint
func TestCSRFProtection_ConfigSave(t *testing.T) {
	userID := uuid.New()
	overlayUUID := uuid.New()

	app := &mockAppService{
		getUserByIDFn: func(ctx context.Context, id uuid.UUID) (*domain.User, error) {
			return &domain.User{
				ID:          userID,
				OverlayUUID: overlayUUID,
			}, nil
		},
		saveConfigFn: func(ctx context.Context, id uuid.UUID, forTrigger, againstTrigger, leftLabel, rightLabel string, decaySpeed float64, overlayUUID uuid.UUID) error {
			return nil
		},
	}

	srv := newTestServer(t, app)
	srv.csrfMiddleware = middleware.CSRFWithConfig(middleware.CSRFConfig{
		TokenLookup: "form:csrf_token,header:X-CSRF-Token",
		CookieName:  "csrf_token",
	})
	srv.registerRoutes()

	t.Run("rejects POST without CSRF token", func(t *testing.T) {
		formData := url.Values{}
		formData.Set("for_trigger", "yes")
		formData.Set("against_trigger", "no")
		formData.Set("left_label", "Against")
		formData.Set("right_label", "For")
		formData.Set("decay_speed", "1.0")

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
			if c.Name == "csrf_token" {
				csrfCookie = c
				break
			}
		}
		require.NotNil(t, csrfCookie, "CSRF cookie should be set")

		// Now POST with CSRF token
		formData := url.Values{}
		formData.Set("for_trigger", "yes")
		formData.Set("against_trigger", "no")
		formData.Set("left_label", "Against")
		formData.Set("right_label", "For")
		formData.Set("decay_speed", "1.0")
		formData.Set("csrf_token", csrfCookie.Value)

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
	userID := uuid.New()
	overlayUUID := uuid.New()

	app := &mockAppService{
		getUserByIDFn: func(ctx context.Context, id uuid.UUID) (*domain.User, error) {
			return &domain.User{
				ID:          userID,
				OverlayUUID: overlayUUID,
			}, nil
		},
		getConfigFn: func(ctx context.Context, userID uuid.UUID) (*domain.Config, error) {
			return &domain.Config{
				ForTrigger: "yes", AgainstTrigger: "no",
				LeftLabel: "Against", RightLabel: "For", DecaySpeed: 1.0,
			}, nil
		},
		resetSentimentFn: func(ctx context.Context, overlayUUID uuid.UUID) error {
			return nil
		},
	}

	srv := newTestServer(t, app)
	srv.csrfMiddleware = middleware.CSRFWithConfig(middleware.CSRFConfig{
		TokenLookup: "form:csrf_token,header:X-CSRF-Token",
		CookieName:  "csrf_token",
	})
	srv.registerRoutes()

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
			if c.Name == "csrf_token" {
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
	userID := uuid.New()
	overlayUUID := uuid.New()

	app := &mockAppService{
		getUserByIDFn: func(ctx context.Context, id uuid.UUID) (*domain.User, error) {
			return &domain.User{
				ID:          userID,
				OverlayUUID: overlayUUID,
			}, nil
		},
		getConfigFn: func(ctx context.Context, userID uuid.UUID) (*domain.Config, error) {
			return &domain.Config{
				ForTrigger: "yes", AgainstTrigger: "no",
				LeftLabel: "Against", RightLabel: "For", DecaySpeed: 1.0,
			}, nil
		},
		rotateOverlayUUIDFn: func(ctx context.Context, userID uuid.UUID) (uuid.UUID, error) {
			return uuid.New(), nil
		},
	}

	srv := newTestServer(t, app)
	srv.csrfMiddleware = middleware.CSRFWithConfig(middleware.CSRFConfig{
		TokenLookup: "form:csrf_token,header:X-CSRF-Token",
		CookieName:  "csrf_token",
	})
	srv.registerRoutes()

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
			if c.Name == "csrf_token" {
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
	userID := uuid.New()
	overlayUUID := uuid.New()

	app := &mockAppService{
		getUserByIDFn: func(ctx context.Context, id uuid.UUID) (*domain.User, error) {
			return &domain.User{
				ID:          userID,
				OverlayUUID: overlayUUID,
			}, nil
		},
		getConfigFn: func(ctx context.Context, userID uuid.UUID) (*domain.Config, error) {
			return &domain.Config{
				ForTrigger: "yes", AgainstTrigger: "no",
				LeftLabel: "Against", RightLabel: "For", DecaySpeed: 1.0,
			}, nil
		},
	}

	srv := newTestServer(t, app)
	srv.csrfMiddleware = middleware.CSRFWithConfig(middleware.CSRFConfig{
		TokenLookup: "form:csrf_token,header:X-CSRF-Token",
		CookieName:  "csrf_token",
	})
	srv.registerRoutes()

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
			if c.Name == "csrf_token" {
				csrfCookie = c
				break
			}
		}
		require.NotNil(t, csrfCookie, "CSRF cookie should be set")

		// Now POST logout with CSRF token
		formData := url.Values{}
		formData.Set("csrf_token", csrfCookie.Value)

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
	app := &mockAppService{}

	mockWebhook := &mockWebhookHandler{}

	srv := newTestServer(t, app)
	srv.webhook = mockWebhook
	srv.csrfMiddleware = middleware.CSRFWithConfig(middleware.CSRFConfig{
		TokenLookup: "form:csrf_token,header:X-CSRF-Token",
		CookieName:  "csrf_token",
	})
	srv.registerRoutes()

	t.Run("webhook accepts POST without CSRF token", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/webhooks/eventsub", nil)
		rec := httptest.NewRecorder()

		srv.echo.ServeHTTP(rec, req)

		// Should NOT be rejected with 403 - webhook handler should be called
		// (will return 200 from our mock)
		assert.Equal(t, http.StatusOK, rec.Code)
	})
}

// mockWebhookHandler for testing
type mockWebhookHandler struct{}

func (m *mockWebhookHandler) HandleEventSub(c echo.Context) error {
	return c.NoContent(http.StatusOK)
}
