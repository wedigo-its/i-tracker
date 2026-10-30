// Package version holds the version and the build time injected via -ldflags
// "-X i-tracker/internal/version.Version=... -X i-tracker/internal/version.BuildDate=..."
// (see build.cmd), which shows exactly which binary is running.
package version

// Name is the application's display name.
const Name = "i-Tracker"

// Version is the latest release tag, the build number (the count of commits
// up to the build) and the commit as build metadata ("v1.0.0.13+0b1c2d3";
// build.cmd sets it). Several builds of one release differ in the build
// number, so the self-update sees a later build as newer, also installations
// from before build numbers ("v1.0.0"); the commit does not order. A build
// from a working copy with changes ends in "-dirty"; "dev" for unstamped
// builds and without a tag. Only a clean release version updates itself
// (selfupdate.IsRelease). The interface shows "v1.0.0-0b1c2d3" (showVersion
// in app.js).
var Version = "dev"

// BuildDate is the build timestamp (2006-01-02 15:04); "dev" for unstamped builds (e.g. go run).
var BuildDate = "dev"
