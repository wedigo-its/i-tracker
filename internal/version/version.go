// Package version holds the version and the build time injected via -ldflags
// "-X i-tracker/internal/version.Version=... -X i-tracker/internal/version.BuildDate=..."
// (see build.cmd), which shows exactly which binary is running.
package version

// Name is the application's display name.
const Name = "i-Tracker"

// Version is the release from "git describe --tags --always --dirty" (e.g.
// "v0.0.1"); "dev" for unstamped builds. Only a clean release version
// updates itself (selfupdate.IsRelease).
var Version = "dev"

// BuildDate is the build timestamp (2006-01-02 15:04); "dev" for unstamped builds (e.g. go run).
var BuildDate = "dev"
