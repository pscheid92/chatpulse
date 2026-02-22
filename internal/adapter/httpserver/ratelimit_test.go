package httpserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testRemoteAddr = "1.2.3.4:1234"

func TestRateLimiterAllowsRequestsUnderLimit(t *testing.T) {
	e := echo.New()
	mw := newRateLimiter(10, 3) // 10 req/s, burst 3

	handler := mw(func(c echo.Context) error {
		return c.String(http.StatusOK, "ok")
	})

	for range 3 {
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		req.RemoteAddr = testRemoteAddr
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)

		err := handler(c)
		require.NoError(t, err)
		assert.Equal(t, http.StatusOK, rec.Code)
	}
}

func TestRateLimiterBlocksExcessiveRequests(t *testing.T) {
	e := echo.New()
	mw := newRateLimiter(0.01, 1) // very low rate, burst 1

	handler := mw(func(c echo.Context) error {
		return c.String(http.StatusOK, "ok")
	})

	// First request: allowed (burst)
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.RemoteAddr = testRemoteAddr
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	err := handler(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)

	// Second request: blocked
	req = httptest.NewRequest(http.MethodGet, "/test", nil)
	req.RemoteAddr = testRemoteAddr
	rec = httptest.NewRecorder()
	c = e.NewContext(req, rec)
	err = handler(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusTooManyRequests, rec.Code)

	var resp map[string]string
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "rate limit exceeded", resp["error"])
}

func TestRateLimiterDifferentIPsAreIndependent(t *testing.T) {
	e := echo.New()
	mw := newRateLimiter(0.01, 1) // very low rate, burst 1

	handler := mw(func(c echo.Context) error {
		return c.String(http.StatusOK, "ok")
	})

	// First IP uses its burst
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.RemoteAddr = testRemoteAddr
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	err := handler(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)

	// Second IP still has its own burst
	req = httptest.NewRequest(http.MethodGet, "/test", nil)
	req.RemoteAddr = "5.6.7.8:5678"
	rec = httptest.NewRecorder()
	c = e.NewContext(req, rec)
	err = handler(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)

	// First IP is now blocked
	req = httptest.NewRequest(http.MethodGet, "/test", nil)
	req.RemoteAddr = testRemoteAddr
	rec = httptest.NewRecorder()
	c = e.NewContext(req, rec)
	err = handler(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusTooManyRequests, rec.Code)
}
