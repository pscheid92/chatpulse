package version

import "runtime"

// Build information, injected via ldflags at build time
var (
	// Version is the git tag or semantic version
	Version = "dev"
	// Commit is the git commit SHA
	Commit = "unknown"
	// BuildTime is the ISO 8601 build timestamp
	BuildTime = "unknown"
)

// Info holds complete build information
type Info struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuildTime string `json:"build_time"`
	GoVersion string `json:"go_version"`
}

// Get returns the current build information
func Get() Info {
	return Info{
		Version:   Version,
		Commit:    Commit,
		BuildTime: BuildTime,
		GoVersion: runtime.Version(),
	}
}
