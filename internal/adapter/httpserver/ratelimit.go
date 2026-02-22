package httpserver

import (
	"net/http"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"golang.org/x/time/rate"
)

const rateLimiterExpiry = 5 * time.Minute

func newRateLimiter(ratePerSecond float64, burst int) echo.MiddlewareFunc {
	store := middleware.NewRateLimiterMemoryStoreWithConfig(
		middleware.RateLimiterMemoryStoreConfig{
			Rate:      rate.Limit(ratePerSecond),
			Burst:     burst,
			ExpiresIn: rateLimiterExpiry,
		},
	)
	return middleware.RateLimiterWithConfig(middleware.RateLimiterConfig{
		IdentifierExtractor: func(c echo.Context) (string, error) {
			return c.RealIP(), nil
		},
		Store: store,
		DenyHandler: func(c echo.Context, identifier string, err error) error {
			return c.JSON(http.StatusTooManyRequests, map[string]string{
				"error": "rate limit exceeded",
			})
		},
	})
}
