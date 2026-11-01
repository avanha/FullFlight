// Package version names the running build, for the User-Agent we send to the services we call.
package version

import (
	"runtime/debug"
	"strings"
)

// Version overrides the version the build records, for a build that wants a name of its own
// (go build -ldflags "-X github.com/avanha/fullflight/internal/version.Version=v0.5.0"). Normally it is empty.
// Go records the main module's version from the git tag when it builds (v0.6.0, or a longer pseudo-version after
// the tag, ending +dirty if the tree had changes), so a plain `go build` or `go install` already names the release.
var Version string

// Repo is where the project lives, so the operator of a service we call can see who and what we are.
const Repo = "https://github.com/avanha/FullFlight"

// String is the version of this build.
func String() string {
	if Version != "" {
		return Version
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		return fromBuildInfo(info)
	}
	return "dev"
}

// fromBuildInfo names a build: the module version Go recorded, or, when it recorded none ("(devel)", as for
// a build outside git), the commit it was built from, or "dev".
func fromBuildInfo(info *debug.BuildInfo) string {
	if v := info.Main.Version; v != "" && v != "(devel)" {
		return v
	}
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
	return "dev"
}

// UserAgent is what we call ourselves in a request: "FullFlight/v0.5.0 (personal flight tracker; +https://github.com/avanha/FullFlight)".
func UserAgent() string {
	return "FullFlight/" + strings.TrimSpace(String()) + " (personal flight tracker; +" + Repo + ")"
}
