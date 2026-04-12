// Package ui embeds the web UI static assets.
package ui

import "embed"

// FS contains the embedded web UI files.
//
//go:embed index.html app.js style.css
var FS embed.FS
