// Package version names the running build, for the User-Agent we send to the services we call.
package version

import (
	"runtime/debug"
	"strings"
)

// Version is set at build time (make build does: -ldflags "-X github.com/avanha/fullflight/internal/version.Version=v0.5.0").
// Left empty, the commit the binary was built from is used, and "dev" when even that is unknown (go run, go test).
var Version string

// Repo is where the project lives, so the operator of a service we call can see who and what we are.
const Repo = "https://github.com/avanha/FullFlight"

// String is the version of this build.
func String() string {
	if Version != "" {
		return Version
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		var rev, dirty string
		for _, s := range info.Settings {
			switch s.Key {
			case "vcs.revision":
				rev = s.Value
			case "vcs.modified":
				if s.Value == "true" {
					dirty = "-dirty"
				}
			}
		}
		if len(rev) >= 7 {
			return rev[:7] + dirty
		}
	}
	return "dev"
}

// UserAgent is what we call ourselves in a request: "FullFlight/v0.5.0 (personal flight tracker; +https://github.com/avanha/FullFlight)".
func UserAgent() string {
	return "FullFlight/" + strings.TrimSpace(String()) + " (personal flight tracker; +" + Repo + ")"
}
