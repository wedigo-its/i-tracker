package trace

import (
	"errors"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/oschwald/geoip2-golang"
)

// Herkunft der geladenen Datenbank.
const (
	SourceDBIP    = "dbip"    // DB-IP City Lite (CC BY 4.0, ohne Account)
	SourceMaxMind = "maxmind" // MaxMind GeoLite2/GeoIP2 City (Account nötig)
)

// GeoIP schlägt Standorte in einer City-Datenbank im MMDB-Format nach (DB-IP
// City Lite oder MaxMind GeoLite2 City). Die Datenbank lässt sich zur Laufzeit
// austauschen (siehe GeoUpdater). Ein nil-*GeoIP und eine leere GeoIP sind
// gültig und liefern keine Standorte.
type GeoIP struct {
	mu     sync.RWMutex
	reader *geoip2.Reader
	path   string
}

// Location ist der Standort einer IP-Adresse.
type Location struct {
	Latitude, Longitude float64
	City, Country       string
	CountryCode         string
}

// GeoInfo beschreibt die geladene Datenbank für die Oberfläche.
type GeoInfo struct {
	// Source ist SourceDBIP, SourceMaxMind oder leer (keine Datenbank).
	Source string `json:"source"`
	// BuildDate ist das Erstellungsdatum der Datenbank (YYYY-MM-DD).
	BuildDate string `json:"build_date,omitempty"`
	// Updating ist true, solange ein Download läuft.
	Updating bool `json:"updating"`
}

// NewGeoIP liefert eine leere, später befüllbare GeoIP.
func NewGeoIP() *GeoIP { return &GeoIP{} }

// OpenGeoIP öffnet die erste vorhandene Datenbank aus paths.
func OpenGeoIP(paths ...string) (*GeoIP, string, error) {
	g := NewGeoIP()
	p, err := g.Load(paths...)
	if err != nil {
		return nil, p, err
	}
	return g, p, nil
}

// Load öffnet die erste vorhandene Datenbank aus paths und ersetzt die
// bisher geladene.
func (g *GeoIP) Load(paths ...string) (string, error) {
	for _, p := range paths {
		if _, err := os.Stat(p); err != nil {
			continue
		}
		r, err := openCity(p)
		if err != nil {
			return p, err
		}
		g.mu.Lock()
		old := g.reader
		g.reader, g.path = r, p
		g.mu.Unlock()
		if old != nil {
			old.Close()
		}
		return p, nil
	}
	return "", os.ErrNotExist
}

// replace ersetzt die Datei dst durch die bereits geprüfte Datei tmp und lädt
// sie. Die alte Datenbank wird vorher geschlossen, weil Windows eine
// eingeblendete (mmap) Datei nicht überschreiben lässt; Abfragen warten so
// lange auf die Sperre.
func (g *GeoIP) replace(tmp, dst string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.reader != nil {
		g.reader.Close()
		g.reader, g.path = nil, ""
	}
	if err := os.Rename(tmp, dst); err != nil {
		return err
	}
	r, err := openCity(dst)
	if err != nil {
		return err
	}
	g.reader, g.path = r, dst
	return nil
}

// Close schließt die Datenbank.
func (g *GeoIP) Close() error {
	if g == nil {
		return nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.reader == nil {
		return nil
	}
	err := g.reader.Close()
	g.reader, g.path = nil, ""
	return err
}

// Loaded liefert Herkunft und Pfad der geladenen Datenbank (leer = keine).
func (g *GeoIP) Loaded() (source, path string) {
	if g == nil {
		return "", ""
	}
	g.mu.RLock()
	defer g.mu.RUnlock()
	if g.reader == nil {
		return "", ""
	}
	return sourceOf(g.reader.Metadata().DatabaseType), g.path
}

// Info liefert Herkunft und Erstellungsdatum der geladenen Datenbank.
func (g *GeoIP) Info() GeoInfo {
	if g == nil {
		return GeoInfo{}
	}
	g.mu.RLock()
	defer g.mu.RUnlock()
	if g.reader == nil {
		return GeoInfo{}
	}
	md := g.reader.Metadata()
	return GeoInfo{
		Source:    sourceOf(md.DatabaseType),
		BuildDate: time.Unix(int64(md.BuildEpoch), 0).UTC().Format("2006-01-02"),
	}
}

// Lookup liefert den Standort von ip oder nil, wenn er unbekannt ist (z. B.
// bei privaten Adressen).
func (g *GeoIP) Lookup(ip string) *Location {
	parsed := net.ParseIP(ip)
	if g == nil || parsed == nil {
		return nil
	}
	g.mu.RLock()
	defer g.mu.RUnlock()
	if g.reader == nil {
		return nil
	}
	rec, err := g.reader.City(parsed)
	if err != nil || (rec.Location.Latitude == 0 && rec.Location.Longitude == 0) {
		return nil
	}
	return &Location{
		Latitude:    rec.Location.Latitude,
		Longitude:   rec.Location.Longitude,
		City:        rec.City.Names["en"],
		Country:     rec.Country.Names["en"],
		CountryCode: rec.Country.IsoCode,
	}
}

// errNotCity meldet eine Datenbank, die keine Standorte auf Stadtebene enthält.
var errNotCity = errors.New("keine City-Datenbank")

// openCity öffnet eine MMDB-Datei und prüft, dass sie Stadt-Standorte liefert.
func openCity(path string) (*geoip2.Reader, error) {
	r, err := geoip2.Open(path)
	if err != nil {
		return nil, err
	}
	if !strings.Contains(r.Metadata().DatabaseType, "City") {
		r.Close()
		return nil, errNotCity
	}
	return r, nil
}

// sourceOf ordnet den Datenbanktyp aus den MMDB-Metadaten einer Herkunft zu.
func sourceOf(dbType string) string {
	if strings.HasPrefix(dbType, "DBIP") {
		return SourceDBIP
	}
	return SourceMaxMind
}
