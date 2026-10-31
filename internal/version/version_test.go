package version

import (
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
