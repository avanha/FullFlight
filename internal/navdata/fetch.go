package navdata

import (
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
)

// DefaultBase is where FlightGear keeps the files. The data is GPL-licensed (Robin A. Peel's X-Plane
// navigation data) and from 2013, so FullFlight keeps it out of git and out of the binary.
const DefaultBase = "https://gitlab.com/flightgear/fgdata/-/raw/next/Navaids"

// Files are the files Load reads, as they are fetched.
var Files = []string{"fix.dat.gz", "nav.dat.gz"}

// maxDownload bounds one download; the real files are 1 to 2 MB.
const maxDownload = 32 << 20

// Fetch downloads Files from base into dir, replacing what is there. A download that is not complete
// gzip (cut short, or an error page) never replaces a good file: each is checked in a temporary file
// first. It reports each file on progress, if that is not nil.
func Fetch(ctx context.Context, client *http.Client, base, userAgent, dir string, progress io.Writer) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, name := range Files {
		if progress != nil {
			fmt.Fprintf(progress, "fetching %s\n", name)
		}
		if err := fetchOne(ctx, client, base+"/"+name, userAgent, filepath.Join(dir, name)); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	return nil
}

func fetchOne(ctx context.Context, client *http.Client, url, userAgent, dest string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %s", resp.Status)
	}
	tmp, err := os.CreateTemp(filepath.Dir(dest), filepath.Base(dest)+".*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // a no-op once it has been renamed
	n, err := io.Copy(tmp, io.LimitReader(resp.Body, maxDownload+1))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if n > maxDownload {
		return fmt.Errorf("more than %d bytes", maxDownload)
	}
	if err := checkGzip(tmp.Name()); err != nil {
		return fmt.Errorf("not a complete gzip file: %w", err)
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil { // CreateTemp makes it private; these are not secret
		return err
	}
	return os.Rename(tmp.Name(), dest)
}

// checkGzip reads the whole file, which also checks its CRC and length, and bounds what it may expand to.
func checkGzip(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	n, err := io.Copy(io.Discard, io.LimitReader(gz, MaxFileBytes+1))
	if err != nil {
		return err
	}
	if n > MaxFileBytes {
		return fmt.Errorf("expands to more than %d bytes", MaxFileBytes)
	}
	return nil
}
