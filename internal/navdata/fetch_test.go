package navdata

import (
	"bytes"
	"compress/gzip"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func gz(t *testing.T, s string) []byte {
	t.Helper()
	var b bytes.Buffer
	w := gzip.NewWriter(&b)
	w.Write([]byte(s))
	w.Close()
	return b.Bytes()
}

// serve answers each file with the bytes in files, and 404 for any other.
func serve(t *testing.T, files map[string][]byte, agent *string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if agent != nil {
			*agent = r.Header.Get("User-Agent")
		}
		b, ok := files[strings.TrimPrefix(r.URL.Path, "/")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write(b)
	}))
}

func TestFetch(t *testing.T) {
	var agent string
	srv := serve(t, map[string][]byte{"fix.dat.gz": gz(t, fixSample), "nav.dat.gz": gz(t, navSample)}, &agent)
	defer srv.Close()
	dir := filepath.Join(t.TempDir(), "navdata") // does not exist yet
	var progress bytes.Buffer
	if err := Fetch(context.Background(), srv.Client(), srv.URL, "FullFlight/test", dir, &progress); err != nil {
		t.Fatal(err)
	}
	db, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if db.Cycle != "2013.10" || db.Fixes == 0 || db.Navaid == 0 {
		t.Errorf("loaded %+v", db)
	}
	if fi, err := os.Stat(filepath.Join(dir, "fix.dat.gz")); err != nil || fi.Mode().Perm() != 0o644 {
		t.Errorf("file mode %v, %v", fi.Mode(), err)
	}
	if agent != "FullFlight/test" {
		t.Errorf("User-Agent %q", agent)
	}
	if !strings.Contains(progress.String(), "fetching fix.dat.gz") {
		t.Errorf("progress %q", progress.String())
	}
	if m, _ := filepath.Glob(filepath.Join(dir, "*.tmp")); len(m) != 0 {
		t.Errorf("temporary files left: %v", m)
	}
}

// A bad download must not replace a good file, and must not leave debris.
func TestFetchKeepsGoodFile(t *testing.T) {
	good := gz(t, fixSample)
	for name, bad := range map[string][]byte{
		"cut short": good[:len(good)/2],
		"not gzip":  []byte("<html>rate limited</html>"),
		"empty":     nil,
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "fix.dat.gz"), good, 0o644); err != nil {
				t.Fatal(err)
			}
			srv := serve(t, map[string][]byte{"fix.dat.gz": bad}, nil)
			defer srv.Close()
			if err := Fetch(context.Background(), srv.Client(), srv.URL, "x", dir, nil); err == nil {
				t.Fatal("no error")
			}
			if got, _ := os.ReadFile(filepath.Join(dir, "fix.dat.gz")); !bytes.Equal(got, good) {
				t.Error("the good file was replaced")
			}
			if m, _ := filepath.Glob(filepath.Join(dir, "*.tmp")); len(m) != 0 {
				t.Errorf("temporary files left: %v", m)
			}
		})
	}
}

func TestFetchNotFound(t *testing.T) {
	srv := serve(t, nil, nil)
	defer srv.Close()
	err := Fetch(context.Background(), srv.Client(), srv.URL, "x", t.TempDir(), nil)
	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Errorf("err = %v", err)
	}
}
