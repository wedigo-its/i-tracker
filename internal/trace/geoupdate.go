package trace

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"time"
)

// Database download URLs.
const (
	// DB-IP City Lite is released monthly; %s is the month (YYYY-MM).
	DBIPURLFormat = "https://download.db-ip.com/free/dbip-city-lite-%s.mmdb.gz"
	// MaxMind GeoLite2 City as tar.gz; checksum at suffix=tar.gz.sha256.
	MaxMindURL = "https://download.maxmind.com/geoip/databases/GeoLite2-City/download?suffix=tar.gz"
)

// maxDatabaseSize caps the unpacked database so a bad response cannot fill the
// disk (DB-IP City Lite is about 130 MB).
const maxDatabaseSize = 1 << 30

// staleAfter is the age after which a MaxMind database without Last-Modified is
// re-downloaded (MaxMind updates twice a week).
const staleAfter = 7 * 24 * time.Hour

// GeoUpdater downloads the GeoIP database and keeps it current: DB-IP City Lite
// without MaxMind credentials (no account needed), MaxMind GeoLite2 City with.
type GeoUpdater struct {
	Geo  *GeoIP
	Path string // database file location

	// MaxMind credentials (optional, both or neither).
	AccountID, LicenseKey string

	Client    *http.Client
	UserAgent string

	// Replaceable in tests.
	DBIPURLFormat string
	MaxMindURL    string
	Now           func() time.Time
	validate      func(path string) error
	install       func(tmp, dst string) error
	loaded        func() (source, path string)

	updating atomic.Bool
}

// Source returns the wanted database source.
func (u *GeoUpdater) Source() string {
	if u.AccountID != "" && u.LicenseKey != "" {
		return SourceMaxMind
	}
	return SourceDBIP
}

// Updating reports whether a download is running.
func (u *GeoUpdater) Updating() bool { return u != nil && u.updating.Load() }

// Run checks now and then every interval until ctx ends.
func (u *GeoUpdater) Run(ctx context.Context, interval time.Duration) {
	for {
		if err := u.Check(ctx); err != nil && ctx.Err() == nil {
			slog.Warn("GeoIP database could not be updated", "source", u.Source(), "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}

// Check downloads the database if missing, outdated or the source changes.
// A manually placed MaxMind database is not replaced by DB-IP without credentials.
func (u *GeoUpdater) Check(ctx context.Context) error {
	u.defaults()
	want := u.Source()
	have, loadedPath := u.loaded()
	managed := have == "" || (have == want && sameFile(loadedPath, u.Path)) ||
		(have == SourceDBIP && want == SourceMaxMind)
	if !managed {
		return nil
	}

	url, lastMod, err := u.latest(ctx, want)
	if err != nil {
		return err
	}
	if have != "" && have == want && !u.newer(want, lastMod) {
		return nil
	}

	u.updating.Store(true)
	defer u.updating.Store(false)
	slog.Info("Downloading GeoIP database", "source", want)
	if err := u.download(ctx, want, url); err != nil {
		return err
	}
	if lastMod.IsZero() {
		lastMod = u.Now()
	}
	// The mtime records the source's release for the next check.
	os.Chtimes(u.Path, lastMod, lastMod)
	slog.Info("GeoIP database updated", "source", want, "path", u.Path)
	return nil
}

func (u *GeoUpdater) defaults() {
	if u.Client == nil {
		u.Client = &http.Client{Timeout: 10 * time.Minute}
	}
	if u.DBIPURLFormat == "" {
		u.DBIPURLFormat = DBIPURLFormat
	}
	if u.MaxMindURL == "" {
		u.MaxMindURL = MaxMindURL
	}
	if u.Now == nil {
		u.Now = time.Now
	}
	if u.validate == nil {
		u.validate = func(p string) error {
			r, err := openCity(p)
			if err != nil {
				return err
			}
			return r.Close()
		}
	}
	if u.install == nil {
		u.install = u.Geo.replace
	}
	if u.loaded == nil {
		u.loaded = u.Geo.Loaded
	}
}

// latest returns the current download URL and, if known, its Last-Modified.
// MaxMind does not count HEAD requests against the daily download limit.
func (u *GeoUpdater) latest(ctx context.Context, source string) (string, time.Time, error) {
	if source == SourceMaxMind {
		res, err := u.request(ctx, http.MethodHead, u.MaxMindURL, false)
		if err != nil {
			return "", time.Time{}, err
		}
		res.Body.Close()
		if res.StatusCode == http.StatusUnauthorized {
			return "", time.Time{}, errors.New("MaxMind rejects the account ID or license key")
		}
		lastMod, _ := http.ParseTime(res.Header.Get("Last-Modified"))
		return u.MaxMindURL, lastMod, nil
	}
	// The current month's release appears mid-month; until then use last month's.
	now := u.Now().UTC()
	for _, month := range []time.Time{now, now.AddDate(0, 0, -now.Day())} {
		url := fmt.Sprintf(u.DBIPURLFormat, month.Format("2006-01"))
		res, err := u.request(ctx, http.MethodHead, url, true)
		if err != nil {
			return "", time.Time{}, err
		}
		res.Body.Close()
		if res.StatusCode == http.StatusOK {
			lastMod, _ := http.ParseTime(res.Header.Get("Last-Modified"))
			if lastMod.IsZero() {
				lastMod = time.Date(month.Year(), month.Month(), 1, 0, 0, 0, 0, time.UTC)
			}
			return url, lastMod, nil
		}
	}
	return "", time.Time{}, errors.New("no DB-IP release found for this or the previous month")
}

// newer reports whether the source is newer than the stored file.
func (u *GeoUpdater) newer(source string, lastMod time.Time) bool {
	fi, err := os.Stat(u.Path)
	if err != nil {
		return true
	}
	if lastMod.IsZero() {
		return source == SourceMaxMind && u.Now().Sub(fi.ModTime()) > staleAfter
	}
	return lastMod.After(fi.ModTime().Add(time.Second))
}

// download fetches the database into a temp file next to Path, verifies it and
// then replaces the current file.
func (u *GeoUpdater) download(ctx context.Context, source, url string) error {
	if err := os.MkdirAll(filepath.Dir(u.Path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(u.Path), ".geoip-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // gone after a successful replace

	res, err := u.request(ctx, http.MethodGet, url, source == SourceDBIP)
	if err != nil {
		tmp.Close()
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		tmp.Close()
		return fmt.Errorf("download failed: HTTP %d", res.StatusCode)
	}

	sum := sha256.New()
	body := io.TeeReader(res.Body, sum)
	if source == SourceMaxMind {
		err = extractTarMMDB(body, tmp)
	} else {
		err = gunzipTo(body, tmp)
	}
	if err == nil {
		// Drain the rest so the checksum covers the whole file.
		_, err = io.Copy(io.Discard, body)
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if source == SourceMaxMind {
		if err := u.verifyMaxMind(ctx, sum); err != nil {
			return err
		}
	}
	if err := u.validate(tmpName); err != nil {
		return fmt.Errorf("downloaded database is invalid: %w", err)
	}
	return u.install(tmpName, u.Path)
}

// verifyMaxMind compares the archive's SHA-256 with MaxMind's.
func (u *GeoUpdater) verifyMaxMind(ctx context.Context, sum hash.Hash) error {
	res, err := u.request(ctx, http.MethodGet, strings.Replace(u.MaxMindURL, "suffix=tar.gz", "suffix=tar.gz.sha256", 1), false)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("checksum not available: HTTP %d", res.StatusCode)
	}
	line, err := io.ReadAll(io.LimitReader(res.Body, 1024))
	if err != nil {
		return err
	}
	want, _, _ := strings.Cut(strings.TrimSpace(string(line)), " ")
	if !strings.EqualFold(want, hex.EncodeToString(sum.Sum(nil))) {
		return errors.New("checksum of the MaxMind database does not match")
	}
	return nil
}

// request sends a request, with credentials for MaxMind. Redirects to another
// host (MaxMind redirects to storage) drop the Authorization header automatically.
func (u *GeoUpdater) request(ctx context.Context, method, url string, anonymous bool) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, nil)
	if err != nil {
		return nil, err
	}
	if u.UserAgent != "" {
		req.Header.Set("User-Agent", u.UserAgent)
	}
	if !anonymous {
		req.SetBasicAuth(u.AccountID, u.LicenseKey)
	}
	return u.Client.Do(req)
}

// gunzipTo unpacks a gzip stream into w.
func gunzipTo(r io.Reader, w io.Writer) error {
	zr, err := gzip.NewReader(r)
	if err != nil {
		return err
	}
	if err := copyLimited(w, zr); err != nil {
		return err
	}
	return zr.Close()
}

// extractTarMMDB writes the .mmdb file from a tar.gz archive to w.
func extractTarMMDB(r io.Reader, w io.Writer) error {
	zr, err := gzip.NewReader(r)
	if err != nil {
		return err
	}
	tr := tar.NewReader(zr)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return errors.New("archive contains no .mmdb file")
		}
		if err != nil {
			return err
		}
		if h.Typeflag == tar.TypeReg && strings.HasSuffix(path.Base(h.Name), ".mmdb") {
			return copyLimited(w, tr)
		}
	}
}

func copyLimited(w io.Writer, r io.Reader) error {
	n, err := io.Copy(w, io.LimitReader(r, maxDatabaseSize+1))
	if err != nil {
		return err
	}
	if n > maxDatabaseSize {
		return errors.New("database is larger than allowed")
	}
	return nil
}

// sameFile compares two paths (case-insensitive on Windows).
func sameFile(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	aa, err1 := filepath.Abs(a)
	bb, err2 := filepath.Abs(b)
	if err1 != nil || err2 != nil {
		return false
	}
	if runtime.GOOS == "windows" {
		return strings.EqualFold(aa, bb)
	}
	return aa == bb
}
