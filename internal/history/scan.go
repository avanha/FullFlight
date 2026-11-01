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

// Kinds of release adsb.lol makes for a day: prod is the default, staging stands in when prod was down.
var kinds = []string{"prod", "staging"}

func partPrefix(day, kind string) string {
	return "v" + strings.ReplaceAll(day, "-", ".") + "-planes-readsb-" + kind + "-0.tar."
}

// partSuffix is "aa", "ab", ... for the i-th part.
func partSuffix(i int) string { return string(rune('a'+i/26)) + string(rune('a'+i%26)) }

// Parts returns the cached parts of day (YYYY-MM-DD), in order, the prod release if both are there. They must run .aa, .ab,
// ... without a gap.
func (c Cache) Parts(day string) ([]string, error) {
	for _, kind := range kinds {
		name := partPrefix(day, kind)
		parts, err := filepath.Glob(filepath.Join(c.Dir, "adsblol", name+"??"))
		if err != nil {
			return nil, err
		}
		if len(parts) == 0 {
			continue
		}
		sort.Strings(parts)
		for i, p := range parts {
			if filepath.Base(p) != name+partSuffix(i) {
				return nil, fmt.Errorf("%s: part %s is missing from %s", day, name+partSuffix(i), filepath.Dir(p))
			}
		}
		return parts, nil
	}
	return nil, fmt.Errorf("%s: %w", day, ErrNotCached)
}

// CachedDay is one day held in the cache.
type CachedDay struct {
	Day   string // YYYY-MM-DD
	Bytes int64
}

var cachedName = regexp.MustCompile(`^v(\d{4})\.(\d\d)\.(\d\d)-planes-readsb-(?:prod|staging)-0\.tar\.[a-z]{2}(?:\.part)?$`)

// Days lists the days in the cache, oldest first, with what each takes on disk (a download in progress counts).
func (c Cache) Days() ([]CachedDay, error) {
	ents, err := os.ReadDir(filepath.Join(c.Dir, "adsblol"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	by := map[string]int64{}
	for _, e := range ents {
		m := cachedName.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		by[m[1]+"-"+m[2]+"-"+m[3]] += info.Size()
	}
	var out []CachedDay
	for d, b := range by {
		out = append(out, CachedDay{d, b})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Day < out[j].Day })
	return out, nil
}

// Bytes is the size of the cache.
func (c Cache) Bytes() int64 {
	var n int64
	days, _ := c.Days()
	for _, d := range days {
		n += d.Bytes
	}
	return n
}

// Remove deletes everything held for day, and returns how much that was.
func (c Cache) Remove(day string) (int64, error) {
	var freed int64
	for _, kind := range kinds {
		matches, err := filepath.Glob(filepath.Join(c.Dir, "adsblol", partPrefix(day, kind)+"*"))
		if err != nil {
			return freed, err
		}
		for _, m := range matches {
			if info, err := os.Stat(m); err == nil {
				freed += info.Size()
			}
			if err := os.Remove(m); err != nil {
				return freed, err
			}
		}
	}
	return freed, nil
}

var traceName = regexp.MustCompile(`^traces/[0-9a-f]{2}/trace_full_([0-9a-f]{6})\.json$`)

// maxTrace bounds one trace file, so a damaged archive cannot fill memory.
const maxTrace = 256 << 20

// Scan reads the parts as one tar, once through, and calls fn with the trace file of every aircraft in want (lower-case
// hex). It returns nil only if the archive ended cleanly: a part cut short is an error, so that a day is never taken as
// searched when it was not.
func Scan(parts []string, want map[string]bool, fn func(hex string, trace io.Reader) error) (ScanStats, error) {
	var st ScanStats
	err := scan(parts, want, &st, fn)
	return st, err
}

// ScanStats says what an archive held: every member, and how many were aircraft trace files (of any aircraft).
type ScanStats struct {
	Members, Traces int
	Sample          []string // the first few names, to show what the layout looks like if no trace file was recognised
}

func scan(parts []string, want map[string]bool, st *ScanStats, fn func(hex string, trace io.Reader) error) error {
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
			if st.Traces == 0 {
				return fmt.Errorf("no aircraft trace files in %d archive members (expected traces/xx/trace_full_<hex>.json); adsb.lol may have changed the layout. First names: %v",
					st.Members, st.Sample)
			}
			return nil
		}
		if err != nil {
			return fmt.Errorf("reading the archive: %w", err)
		}
		end = cr.n + (h.Size+511)/512*512
		st.Members++
		if len(st.Sample) < 8 {
			st.Sample = append(st.Sample, h.Name)
		}
		m := traceName.FindStringSubmatch(path.Clean(h.Name))
		if m != nil && h.Typeflag == tar.TypeReg {
			st.Traces++
		}
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
