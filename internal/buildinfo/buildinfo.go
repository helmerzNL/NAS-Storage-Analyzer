// Package buildinfo says which build is running and on which release channel.
//
// The channel comes from NASA_TAG, the one variable that picks the image in docker-compose.yml
// (`latest`, `beta` or a version number) and is passed on into the container (baseline section 20).
// A build whose tag contains "beta" is a beta build; everything else is stable.
package buildinfo

import "strings"

const (
	Stable = "stable"
	Beta   = "beta"
)

// Info is what /api/v1/version reports.
type Info struct {
	// Version is the MAJOR.MINOR.PATCH the image was built for ("dev" for a build from a checkout).
	Version string `json:"version"`
	// Commit is the git commit the image was built from, empty when unknown.
	Commit string `json:"commit"`
	// Channel is "stable" or "beta".
	Channel string `json:"channel"`
}

// ChannelOf derives the channel from the image tag (NASA_TAG): "beta", "beta-<sha>" and
// "1.2.0-beta.3" are beta, "latest", "" and "1.2.0" are stable.
func ChannelOf(tag string) string {
	if strings.Contains(strings.ToLower(tag), "beta") {
		return Beta
	}
	return Stable
}

// New combines the version and commit baked into the binary with the channel of the tag the
// container was started with. An empty version reads as "dev".
func New(version, commit, tag string) Info {
	if version == "" {
		version = "dev"
	}
	return Info{Version: version, Commit: commit, Channel: ChannelOf(tag)}
}
