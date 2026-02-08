package server

import (
	"bytes"
	"fmt"
	"html/template"
	"log"

	"github.com/labstack/echo/v4"
)

// Session keys
const (
	sessionName          = "chatpulse-session"
	sessionKeyToken      = "token"
	sessionKeyOAuthState = "oauth_state"
)

func renderTemplate(c echo.Context, tmpl *template.Template, data interface{}) error {
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		log.Printf("Template execution failed for %s: %v", c.Request().URL.Path, err)
		return c.String(500, "Failed to render page")
	}
	return c.HTMLBlob(200, buf.Bytes())
}

func (s *Server) getBaseURL(c echo.Context) string {
	scheme := "http"
	if c.Request().TLS != nil {
		scheme = "https"
	}
	if fwdProto := c.Request().Header.Get("X-Forwarded-Proto"); fwdProto != "" {
		scheme = fwdProto
	}
	return fmt.Sprintf("%s://%s", scheme, c.Request().Host)
}
