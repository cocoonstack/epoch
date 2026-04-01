// Package version holds build-time version metadata, injected via ldflags.
package version

// Set at build time via -ldflags.
var (
	VERSION  = "dev"
	REVISION = "unknown"
	BUILTAT  = "unknown"
)
