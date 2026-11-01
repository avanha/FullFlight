package history

import (
	"archive/tar"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Cache is the directory the downloaded days are kept in. adsb.lol publishes each day as a tar split into parts
// (v2026.10.31-planes-readsb-prod-0.tar.aa, .ab, ...); they are kept exactly as downloaded, in Dir/adsblol.
type Cache struct {
	Dir string
}

// ErrNotCached says the parts of a day are not in the cache.
var ErrNotCached = errors.New("that day is not in the cache")

// Parts returns the cached parts of day (YYYY-MM-DD), in order. They must run .aa, .ab, ... without a gap.
func (c Cache) Parts(day string) ([]string, error) {
	name := "v" + strings.ReplaceAll(day, "-", ".") + "-planes-readsb-prod-0.tar."
	parts, err := filepath.Glob(filepath.Join(c.Dir, "adsblol", name+"??"))
	if err != nil {
		return nil, err
	}
	if len(parts) == 0 {
		return nil, fmt.Errorf("%s: %w", day, ErrNotCached)
	}
	sort.Strings(parts)
	for i, p := range parts {
		want := string(rune('a'+i/26)) + string(rune('a'+i%26))
		if filepath.Base(p) != name+want {
			return nil, fmt.Errorf("%s: part %s is missing from %s", day, name+want, filepath.Dir(p))
		}
	}
	return parts, nil
}

var traceName = regexp.MustCompile(`^traces/[0-9a-f]{2}/trace_full_([0-9a-f]{6})\.json$`)

// maxTrace bounds one trace file, so a damaged archive cannot fill memory.
const maxTrace = 256 << 20

// Scan reads the parts as one tar, once through, and calls fn with the trace file of every aircraft in want (lower-case
// hex). It returns nil only if the archive ended cleanly: a part cut short is an error, so that a day is never taken as
// searched when it was not.
func Scan(parts []string, want map[string]bool, fn func(hex string, trace io.Reader) error) error {
	var readers []io.Reader
	for _, p := range parts {
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()
		readers = append(readers, f)
	}
	cr := &counter{r: io.MultiReader(readers...)}
	tr := tar.NewReader(cr)
	var end int64 // where the last member, and its padding to a whole block, ended
	for {
		h, err := tr.Next()
		if err == io.EOF {
			// the reader takes the end of the stream for the end of the archive; a real one has two empty blocks after the last member
			if cr.n-end < 2*512 {
				return fmt.Errorf("the archive stops after %d bytes without its end: is the last part missing?", cr.n)
			}
			return nil
		}
		if err != nil {
			return fmt.Errorf("reading the archive: %w", err)
		}
		end = cr.n + (h.Size+511)/512*512
		m := traceName.FindStringSubmatch(path.Clean(h.Name))
		if m == nil || h.Typeflag != tar.TypeReg || !want[m[1]] {
			continue
		}
		if h.Size > maxTrace {
			return fmt.Errorf("%s: %d bytes is not a trace file", h.Name, h.Size)
		}
		if err := fn(m[1], io.LimitReader(tr, h.Size)); err != nil {
			return fmt.Errorf("%s: %w", h.Name, err)
		}
	}
}

// counter counts the bytes read through it.
type counter struct {
	r io.Reader
	n int64
}

func (c *counter) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}
