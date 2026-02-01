package version

import "fmt"

// Version information
const (
	Major = 1
	Minor = 2
	Patch = 1
)

// Build information - set via ldflags at build time
var (
	BuildTime = "" // Set via: -ldflags "-X transcoder/internal/version.BuildTime=$(date -u +%Y%m%d.%H%M%S)"
	GitCommit = "" // Set via: -ldflags "-X transcoder/internal/version.GitCommit=$(git rev-parse --short HEAD)"
)

// String returns the semantic version string
func String() string {
	return fmt.Sprintf("%d.%d.%d", Major, Minor, Patch)
}

// GetVersion returns the full version string with build info
func GetVersion() string {
	ver := String()
	if GitCommit != "" {
		ver += "-" + GitCommit
	}
	if BuildTime != "" {
		ver += " (" + BuildTime + ")"
	}
	return ver
}
