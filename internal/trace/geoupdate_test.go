package trace

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func gz(t *testing.T, data []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	zw.Write(data)
	zw.Close()
	return buf.Bytes()
}

func tarGz(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	for _, name := range []string{"GeoLite2-City_20261010/COPYRIGHT.txt", "GeoLite2-City_20261010/GeoLite2-City.mmdb"} {
		body, ok := files[name]
		if !ok {
			continue
		}
		tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg})
		tw.Write([]byte(body))
	}
	tw.Close()
	zw.Close()
	return buf.Bytes()
}

// testUpdater returns an Updater that just stores the file instead of
// validating and reloading it; loaded is the simulated loaded state.
func testUpdater(t *testing.T, srv *httptest.Server, loaded *[2]string) *GeoUpdater {
	t.Helper()
	dir := t.TempDir()
	u := &GeoUpdater{
		Path:          filepath.Join(dir, "geoip", "city.mmdb"),
		Client:        srv.Client(),
		DBIPURLFormat: srv.URL + "/dbip-city-lite-%s.mmdb.gz",
		MaxMindURL:    srv.URL + "/maxmind?suffix=tar.gz",
		Now:           func() time.Time { return time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC) },
	}
	u.validate = func(string) error { return nil }
	u.install = func(tmp, dst string) error { return os.Rename(tmp, dst) }
	u.loaded = func() (string, string) { return loaded[0], loaded[1] }
	return u
}

func TestGeoUpdaterDBIP(t *testing.T) {
	sept := time.Date(2026, 9, 2, 8, 0, 0, 0, time.UTC)
	oct := time.Date(2026, 10, 5, 8, 0, 0, 0, time.UTC)
	var octAvailable atomic.Bool
	var gets atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var lastMod time.Time
		var body []byte
		switch {
		case r.URL.Path == "/dbip-city-lite-2026-09.mmdb.gz":
			lastMod, body = sept, gz(t, []byte("september"))
		case r.URL.Path == "/dbip-city-lite-2026-10.mmdb.gz" && octAvailable.Load():
			lastMod, body = oct, gz(t, []byte("october"))
		default:
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "" {
			t.Error("DB-IP request carries credentials")
		}
		w.Header().Set("Last-Modified", lastMod.Format(http.TimeFormat))
		if r.Method == http.MethodGet {
			gets.Add(1)
			w.Write(body)
		}
	}))
	defer srv.Close()

	state := [2]string{}
	u := testUpdater(t, srv, &state)
	if u.Source() != SourceDBIP {
		t.Fatalf("source without credentials: %s", u.Source())
	}

	// No database: last month's release is downloaded.
	if err := u.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(u.Path); string(b) != "september" {
		t.Fatalf("content %q", b)
	}
	if fi, _ := os.Stat(u.Path); !fi.ModTime().Equal(sept) {
		t.Errorf("mtime %v, want %v", fi.ModTime(), sept)
	}

	// Loaded and current: no further download.
	state = [2]string{SourceDBIP, u.Path}
	if err := u.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if gets.Load() != 1 {
		t.Fatalf("%d downloads, want 1", gets.Load())
	}

	// New monthly release: downloaded.
	octAvailable.Store(true)
	if err := u.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(u.Path); string(b) != "october" || gets.Load() != 2 {
		t.Fatalf("content %q after %d downloads", b, gets.Load())
	}
}

func TestGeoUpdaterMaxMind(t *testing.T) {
	archive := tarGz(t, map[string]string{
		"GeoLite2-City_20261010/COPYRIGHT.txt":      "c",
		"GeoLite2-City_20261010/GeoLite2-City.mmdb": "maxmind-db",
	})
	sum := sha256.Sum256(archive)
	checksum := hex.EncodeToString(sum[:])
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if id, key, ok := r.BasicAuth(); !ok || id != "4711" || key != "secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Query().Get("suffix") {
		case "tar.gz":
			w.Header().Set("Last-Modified", "Sat, 10 Oct 2026 06:00:00 GMT")
			if r.Method == http.MethodGet {
				w.Write(archive)
			}
		case "tar.gz.sha256":
			w.Write([]byte(checksum + "  GeoLite2-City_20261010.tar.gz\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	// With credentials, a DB-IP database is replaced by GeoLite2.
	state := [2]string{SourceDBIP, "elsewhere/city.mmdb"}
	u := testUpdater(t, srv, &state)
	u.AccountID, u.LicenseKey = "4711", "secret"
	if err := u.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(u.Path); string(b) != "maxmind-db" {
		t.Fatalf("content %q", b)
	}

	// Wrong checksum: file stays unchanged.
	checksum = strings.Repeat("0", 64)
	os.Chtimes(u.Path, time.Unix(0, 0), time.Unix(0, 0))
	state = [2]string{SourceMaxMind, u.Path}
	if err := u.Check(context.Background()); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("want checksum error: %v", err)
	}
	if b, _ := os.ReadFile(u.Path); string(b) != "maxmind-db" {
		t.Fatalf("file changed: %q", b)
	}

	// Wrong credentials are reported clearly.
	u.LicenseKey = "wrong"
	if err := u.Check(context.Background()); err == nil || !strings.Contains(err.Error(), "license key") {
		t.Fatalf("want credentials error: %v", err)
	}
}

func TestGeoUpdaterKeepsManualMaxMind(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.NotFound(w, r)
	}))
	defer srv.Close()

	// Manually placed GeoLite2 database without MaxMind credentials: leave it alone.
	state := [2]string{SourceMaxMind, "/var/lib/GeoIP/GeoLite2-City.mmdb"}
	u := testUpdater(t, srv, &state)
	if err := u.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 0 {
		t.Fatalf("%d requests, want none", requests.Load())
	}
}

func TestGeoUpdaterRejectsInvalid(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Write(gz(t, []byte("broken")))
		}
	}))
	defer srv.Close()

	state := [2]string{}
	u := testUpdater(t, srv, &state)
	u.validate = func(string) error { return errors.New("not an MMDB") }
	installed := false
	u.install = func(string, string) error { installed = true; return nil }
	if err := u.Check(context.Background()); err == nil || !strings.Contains(err.Error(), "invalid") {
		t.Fatalf("want error: %v", err)
	}
	if installed {
		t.Error("invalid database was installed")
	}
	entries, _ := os.ReadDir(filepath.Dir(u.Path))
	if len(entries) != 0 {
		t.Errorf("temp files left: %v", entries)
	}
	if u.Updating() {
		t.Error("Updating still true after completion")
	}
}
