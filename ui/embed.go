// Package ui embeds the frontend static files.
package ui

import "embed"

//go:embed index.html app.js style.css
var FS embed.FS
