// Package version holds build-time metadata injected via ldflags.
package version

var (
	// Version is the release version, injected at build time.
	Version = "dev"
	// Revision is the git commit hash, injected at build time.
	Revision = "unknown"
	// BuiltAt is the build timestamp, injected at build time.
	BuiltAt = "unknown"
)
