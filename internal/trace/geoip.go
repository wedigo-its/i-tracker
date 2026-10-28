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

// Source of the loaded database.
const (
	SourceDBIP    = "dbip"    // DB-IP City Lite (CC BY 4.0, no account)
	SourceMaxMind = "maxmind" // MaxMind GeoLite2/GeoIP2 City (account required)
)

// GeoIP looks up locations in an MMDB city database (DB-IP City Lite or MaxMind
// GeoLite2 City), swappable at runtime (see GeoUpdater). A nil or empty GeoIP
// is valid and returns no locations.
type GeoIP struct {
	mu     sync.RWMutex
	reader *geoip2.Reader
	path   string
}

// Location is the location of an IP address.
type Location struct {
	Latitude, Longitude float64
	City, Country       string
	CountryCode         string
}

// GeoInfo describes the loaded database for the UI.
type GeoInfo struct {
	// Source is SourceDBIP, SourceMaxMind or empty (no database).
	Source string `json:"source"`
	// BuildDate is the database build date (YYYY-MM-DD).
	BuildDate string `json:"build_date,omitempty"`
	// Updating is true while a download runs.
	Updating bool `json:"updating"`
}

// NewGeoIP returns an empty GeoIP to be loaded later.
func NewGeoIP() *GeoIP { return &GeoIP{} }

// OpenGeoIP opens the first existing database in paths.
func OpenGeoIP(paths ...string) (*GeoIP, string, error) {
	g := NewGeoIP()
	p, err := g.Load(paths...)
	if err != nil {
		return nil, p, err
	}
	return g, p, nil
}

// Load opens the first existing database in paths, replacing the loaded one.
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

// replace moves the verified file tmp over dst and loads it. The old database
// is closed first because Windows cannot overwrite a mapped (mmap) file;
// lookups wait on the lock meanwhile.
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

// Close closes the database.
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

// Loaded returns source and path of the loaded database (empty = none).
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

// Info returns source and build date of the loaded database.
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

// Lookup returns ip's location, or nil if unknown (e.g. private addresses).
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

// errNotCity reports a database without city-level locations.
var errNotCity = errors.New("not a city database")

// openCity opens an MMDB file and checks that it is a city database.
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

// sourceOf maps the MMDB metadata database type to a source.
func sourceOf(dbType string) string {
	if strings.HasPrefix(dbType, "DBIP") {
		return SourceDBIP
	}
	return SourceMaxMind
}
