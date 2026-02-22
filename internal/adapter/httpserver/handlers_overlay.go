package httpserver

import (
	"errors"
	"net/http"

	"github.com/centrifugal/centrifuge"
	"github.com/labstack/echo/v4"
	"github.com/pscheid92/chatpulse/internal/domain"
	apperrors "github.com/pscheid92/chatpulse/internal/platform/errors"
	"github.com/pscheid92/uuid"
)

func (s *Server) registerOverlayRoutes() {
	s.echo.GET("/overlay/:uuid", s.handleOverlay)
	s.echo.GET("/connection/websocket", echo.WrapHandler(s.centrifugeAuthMiddleware(s.websocketHandler)))
}

func (s *Server) handleOverlay(c echo.Context) error {
	ctx := c.Request().Context()

	overlayUUIDStr := c.Param("uuid")
	overlayUUID, err := uuid.Parse(overlayUUIDStr)
	if err != nil {
		return apperrors.ValidationError("invalid UUID format").WithField("uuid", overlayUUIDStr)
	}

	user, err := s.app.GetStreamerByOverlayUUID(ctx, overlayUUID)
	if errors.Is(err, domain.ErrStreamerNotFound) {
		return apperrors.NotFoundError("overlay not found").WithField("overlay_uuid", overlayUUID.String())
	}
	if err != nil {
		return apperrors.InternalError("failed to load overlay", err).WithField("overlay_uuid", overlayUUID.String())
	}

	config, err := s.app.GetConfig(ctx, user.ID)
	if errors.Is(err, domain.ErrConfigNotFound) {
		return apperrors.NotFoundError("config not found").
			WithField("user_id", user.ID.String()).
			WithField("overlay_uuid", overlayUUID.String())
	}
	if err != nil {
		return apperrors.InternalError("failed to load config", err).
			WithField("user_id", user.ID.String()).
			WithField("overlay_uuid", overlayUUID.String())
	}

	data := map[string]any{
		"ForLabel":     config.ForLabel,
		"AgainstLabel": config.AgainstLabel,
		"DisplayMode":  string(config.DisplayMode),
		"WSHost":       c.Request().Host,
		"SessionUUID":  overlayUUIDStr,
	}
	return s.renderTemplate(c, "overlay.html", data)
}

func (s *Server) centrifugeAuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		overlayUUIDStr := r.URL.Query().Get("overlay")
		if overlayUUIDStr == "" {
			http.Error(w, "missing overlay parameter", http.StatusBadRequest)
			return
		}

		if _, err := uuid.Parse(overlayUUIDStr); err != nil {
			http.Error(w, "invalid overlay UUID", http.StatusBadRequest)
			return
		}

		// Set Centrifuge credentials using the overlay UUID as the user ID.
		// The OnConnecting handler resolves this to the actual Twitch user ID.
		cred := &centrifuge.Credentials{UserID: overlayUUIDStr}
		newCtx := centrifuge.SetCredentials(r.Context(), cred)
		r = r.WithContext(newCtx)

		next.ServeHTTP(w, r)
	})
}
