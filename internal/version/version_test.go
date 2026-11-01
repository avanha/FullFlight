package version

import (
	"runtime/debug"
	"strings"
	"testing"
)

func TestUserAgent(t *testing.T) {
	old := Version
	defer func() { Version = old }()
	Version = "v1.2.3"
	if got, want := UserAgent(), "FullFlight/v1.2.3 (personal flight tracker; +https://github.com/avanha/FullFlight)"; got != want {
		t.Errorf("UserAgent = %q, want %q", got, want)
	}
	Version = ""
	if v := String(); v == "" || strings.ContainsAny(v, " \t\n") {
		t.Errorf("unset version = %q: want a commit or dev, one word", v)
	}
	if !strings.HasPrefix(UserAgent(), "FullFlight/") {
		t.Errorf("UserAgent = %q", UserAgent())
	}
}

func TestFromBuildInfo(t *testing.T) {
	setting := func(k, v string) debug.BuildSetting { return debug.BuildSetting{Key: k, Value: v} }
	for _, c := range []struct {
		name string
		info debug.BuildInfo
		want string
	}{
		{"at a tag", debug.BuildInfo{Main: debug.Module{Version: "v0.6.0"}}, "v0.6.0"},
		{"after a tag", debug.BuildInfo{Main: debug.Module{Version: "v0.6.1-0.20261101123456-abcdef123456+dirty"}}, "v0.6.1-0.20261101123456-abcdef123456+dirty"},
		{"devel with a commit", debug.BuildInfo{Main: debug.Module{Version: "(devel)"},
			Settings: []debug.BuildSetting{setting("vcs.revision", "3a1895636b447e2afd9e86dc0ffc14f12d885a9b")}}, "3a18956"},
		{"devel, modified", debug.BuildInfo{Main: debug.Module{Version: "(devel)"},
			Settings: []debug.BuildSetting{setting("vcs.revision", "3a1895636b447e2afd9e86dc0ffc14f12d885a9b"), setting("vcs.modified", "true")}}, "3a18956-dirty"},
		{"nothing known", debug.BuildInfo{}, "dev"},
	} {
		if got := fromBuildInfo(&c.info); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}
