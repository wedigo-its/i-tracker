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

// testUpdater liefert einen Updater, der statt einer echten MMDB-Prüfung und
// eines Neuladens nur die Datei ablegt; loaded gibt den simulierten Stand an.
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
			t.Error("DB-IP-Anfrage trägt Zugangsdaten")
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
		t.Fatalf("Quelle ohne Zugangsdaten: %s", u.Source())
	}

	// Keine Datenbank: die Ausgabe des Vormonats wird geladen.
	if err := u.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(u.Path); string(b) != "september" {
		t.Fatalf("Inhalt %q", b)
	}
	if fi, _ := os.Stat(u.Path); !fi.ModTime().Equal(sept) {
		t.Errorf("Änderungszeit %v, erwartet %v", fi.ModTime(), sept)
	}

	// Geladen und aktuell: kein weiterer Download.
	state = [2]string{SourceDBIP, u.Path}
	if err := u.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if gets.Load() != 1 {
		t.Fatalf("%d Downloads, erwartet 1", gets.Load())
	}

	// Neue Monatsausgabe: wird geladen.
	octAvailable.Store(true)
	if err := u.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(u.Path); string(b) != "october" || gets.Load() != 2 {
		t.Fatalf("Inhalt %q nach %d Downloads", b, gets.Load())
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

	// Mit Zugangsdaten wird eine DB-IP-Datenbank durch GeoLite2 ersetzt.
	state := [2]string{SourceDBIP, "irgendwo/city.mmdb"}
	u := testUpdater(t, srv, &state)
	u.AccountID, u.LicenseKey = "4711", "secret"
	if err := u.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(u.Path); string(b) != "maxmind-db" {
		t.Fatalf("Inhalt %q", b)
	}

	// Falsche Prüfsumme: Datei bleibt unverändert.
	checksum = strings.Repeat("0", 64)
	os.Chtimes(u.Path, time.Unix(0, 0), time.Unix(0, 0))
	state = [2]string{SourceMaxMind, u.Path}
	if err := u.Check(context.Background()); err == nil || !strings.Contains(err.Error(), "Prüfsumme") {
		t.Fatalf("Prüfsummenfehler erwartet: %v", err)
	}
	if b, _ := os.ReadFile(u.Path); string(b) != "maxmind-db" {
		t.Fatalf("Datei verändert: %q", b)
	}

	// Falsche Zugangsdaten werden verständlich gemeldet.
	u.LicenseKey = "falsch"
	if err := u.Check(context.Background()); err == nil || !strings.Contains(err.Error(), "License-Key") {
		t.Fatalf("Fehler zu Zugangsdaten erwartet: %v", err)
	}
}

func TestGeoUpdaterKeepsManualMaxMind(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.NotFound(w, r)
	}))
	defer srv.Close()

	// Von Hand abgelegte GeoLite2-Datenbank ohne MaxMind-Zugang: nicht anfassen.
	state := [2]string{SourceMaxMind, "/var/lib/GeoIP/GeoLite2-City.mmdb"}
	u := testUpdater(t, srv, &state)
	if err := u.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 0 {
		t.Fatalf("%d Anfragen, erwartet keine", requests.Load())
	}
}

func TestGeoUpdaterRejectsInvalid(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Write(gz(t, []byte("kaputt")))
		}
	}))
	defer srv.Close()

	state := [2]string{}
	u := testUpdater(t, srv, &state)
	u.validate = func(string) error { return errors.New("keine MMDB") }
	installed := false
	u.install = func(string, string) error { installed = true; return nil }
	if err := u.Check(context.Background()); err == nil || !strings.Contains(err.Error(), "ungültig") {
		t.Fatalf("Fehler erwartet: %v", err)
	}
	if installed {
		t.Error("ungültige Datenbank wurde übernommen")
	}
	entries, _ := os.ReadDir(filepath.Dir(u.Path))
	if len(entries) != 0 {
		t.Errorf("temporäre Dateien übrig: %v", entries)
	}
	if u.Updating() {
		t.Error("Updating nach Abschluss noch true")
	}
}
