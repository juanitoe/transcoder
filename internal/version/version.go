package version

import "time"

// Build information - set via ldflags at build time
var (
	BuildTime = "" // Set via: -ldflags "-X transcoder/internal/version.BuildTime=$(date -u +%Y%m%d.%H%M%S)"
	GitCommit = "" // Set via: -ldflags "-X transcoder/internal/version.GitCommit=$(git rev-parse --short HEAD)"
)

// GetVersion returns the version string (build time or git commit)
func GetVersion() string {
	if BuildTime != "" {
		return BuildTime
	}
	if GitCommit != "" {
		return GitCommit
	}
	// Fallback for development builds without ldflags
	return time.Now().Format("20060102.150405")
}
