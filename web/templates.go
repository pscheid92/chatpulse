package web

import "embed"

//go:embed templates/*.html
var TemplateFiles embed.FS
