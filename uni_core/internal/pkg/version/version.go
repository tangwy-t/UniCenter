// Package version provides build-time version information injected via -ldflags.
package version

var (
	// Version is the application version (e.g., "1.0.0"). Injected at build time.
	Version = "dev"

	// BuildTime is the ISO 8601 timestamp of the build. Injected at build time.
	BuildTime = "unknown"

	// CommitHash is the git commit SHA of the build. Injected at build time.
	CommitHash = "unknown"
)
