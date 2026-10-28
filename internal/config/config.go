// Package config loads config.yaml. Every key can be overridden via
// IPERF3_<SECTION>_<KEY> (e.g. IPERF3_WEB_LISTEN); the old unprefixed names
// remain as deprecated aliases (see env.go).
package config

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"i-tracker/internal/selfupdate"
)

// DefaultListen applies if web.listen is unset (local only); the generated
// config.yaml and the Docker image set 0.0.0.0:8000 explicitly.
const DefaultListen = "127.0.0.1:8000"

// Config holds the whole application configuration.
type Config struct {
	Web       WebConfig       `yaml:"web"`
	Storage   StorageConfig   `yaml:"storage"`
	Auth      AuthConfig      `yaml:"auth"`
	Scheduler SchedulerConfig `yaml:"scheduler"`
	Iperf     IperfConfig     `yaml:"iperf"`
	GeoIP     GeoIPConfig     `yaml:"geoip"`
	Map       MapConfig       `yaml:"map"`
	Update    UpdateConfig    `yaml:"update"`
	Log       LogConfig       `yaml:"log"`

	// Dir is the config file's directory; relative paths resolve against it.
	Dir string `yaml:"-"`
	// Warnings collects load notices (e.g. deprecated env vars) to log once the logger exists.
	Warnings []string `yaml:"-"`
}

// WebConfig configures the HTTP server.
type WebConfig struct {
	Listen string `yaml:"listen"`
}

// StorageConfig configures the SQLite database.
type StorageConfig struct {
	Path string `yaml:"path"`
}

// AuthConfig configures sign-in.
type AuthConfig struct {
	// SecretKey signs sessions (JWT, HS256) and derives CSRF tokens.
	SecretKey string `yaml:"secret_key"`
	// SessionTTL is the sliding idle session lifetime (e.g. 12h).
	SessionTTL string `yaml:"session_ttl"`
	// PublicRead gives guests a read-only view of dashboard, tests, traces and
	// servers (default on); off = every request requires sign-in.
	PublicRead bool `yaml:"public_read"`
	// SecretGenerated is true if no key was configured and a random one is used for this run.
	SecretGenerated bool `yaml:"-"`
}

// SessionDuration returns the session lifetime (empty = 12h).
func (a AuthConfig) SessionDuration() (time.Duration, error) {
	if a.SessionTTL == "" {
		return 12 * time.Hour, nil
	}
	return time.ParseDuration(a.SessionTTL)
}

// SchedulerConfig controls scheduled tests.
type SchedulerConfig struct {
	Enabled bool `yaml:"enabled"`
}

// IperfConfig configures the iperf3 binary.
type IperfConfig struct {
	// Path to iperf3; empty = auto-detect (PATH, on Windows also common install dirs).
	Path string `yaml:"path"`
	// AutoInstall installs a missing iperf3 at startup via the package manager
	// without asking (for headless setups, e.g. Docker); off = the UI asks an admin.
	AutoInstall bool `yaml:"auto_install"`
}

// GeoIPConfig configures the city database for trace locations.
type GeoIPConfig struct {
	// Path is the database location (MMDB).
	Path string `yaml:"path"`
	// AutoUpdate downloads and refreshes the database: DB-IP City Lite (no
	// account, monthly) or, with MaxMind credentials, GeoLite2 City.
	AutoUpdate bool `yaml:"auto_update"`
	// MaxMindAccountID and MaxMindLicenseKey are optional (free maxmind.com
	// account); if set, GeoLite2 is used instead of DB-IP.
	MaxMindAccountID  string `yaml:"maxmind_account_id"`
	MaxMindLicenseKey string `yaml:"maxmind_license_key"`
}

// MapConfig configures the peering map background.
type MapConfig struct {
	// TileURL is a tile URL with {z}, {x} and {y}; empty = embedded world map
	// (Natural Earth), no external requests.
	TileURL string `yaml:"tile_url"`
	// TileAttribution is the tile credit (HTML allowed); empty = OpenStreetMap notice.
	TileAttribution string `yaml:"tile_attribution"`
}

// tilePlaceholders replaces Leaflet placeholders for URL validation.
var tilePlaceholders = strings.NewReplacer("{s}", "a", "{z}", "0", "{x}", "0", "{y}", "0", "{r}", "")

// TileOrigin returns the tile URL's scheme and host (for the CSP) or "".
func (m MapConfig) TileOrigin() string {
	if m.TileURL == "" {
		return ""
	}
	u, err := url.Parse(tilePlaceholders.Replace(m.TileURL))
	if err != nil || u.Host == "" {
		return ""
	}
	// Allow the subdomain placeholder ({s}.tile.example.org) as a wildcard.
	host := u.Host
	if strings.HasPrefix(m.TileURL, u.Scheme+"://{s}.") {
		host = "*." + strings.TrimPrefix(host, "a.")
	}
	return u.Scheme + "://" + host
}

// UpdateConfig controls the automatic self-update (see internal/selfupdate).
type UpdateConfig struct {
	// Mode is auto (install new releases and restart the service), notify
	// (only report a newer release) or off.
	Mode string `yaml:"mode"`
	// Interval is how often the feed is checked (default 6h).
	Interval string `yaml:"interval"`
	// Window restricts automatic installs to a daily local time window such
	// as "02:00-05:00"; empty allows any time.
	Window string `yaml:"window"`
}

// IntervalDuration returns the check interval (empty = 6h).
func (u UpdateConfig) IntervalDuration() (time.Duration, error) {
	if u.Interval == "" {
		return 6 * time.Hour, nil
	}
	return time.ParseDuration(u.Interval)
}

// LogConfig controls logging.
type LogConfig struct {
	// Level: debug, info, warn or error.
	Level string `yaml:"level"`
	// Format: text (default) or json.
	Format string `yaml:"format"`
}

// SlogLevel returns the configured log level (default info).
func (l LogConfig) SlogLevel() slog.Level {
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(l.Level)); err != nil {
		return slog.LevelInfo
	}
	return lvl
}

// defaultYAML is a new config.yaml; %s is the random token key.
const defaultYAML = `# i-Tracker – configuration
# Every key can be overridden with an environment variable
# IPERF3_<SECTION>_<KEY>, e.g. IPERF3_WEB_LISTEN, IPERF3_STORAGE_PATH,
# IPERF3_AUTH_SECRET_KEY, IPERF3_SCHEDULER_ENABLED, IPERF3_LOG_LEVEL.

web:
  # 0.0.0.0 = reachable from the network. Without this key the service only
  # listens on 127.0.0.1:8000.
  listen: "0.0.0.0:8000"

storage:
  path: "data/i-tracker.db"

auth:
  # Key used to sign login sessions. Generated randomly on first start;
  # changing it invalidates all existing logins.
  secret_key: "%s"
  # Session lifetime without activity; activity extends it (sliding).
  session_ttl: "12h"
  # Guest view: visitors who are not signed in see the dashboard, test
  # results, traces and servers read-only. false = sign-in required for everything.
  public_read: true

scheduler:
  enabled: true

iperf:
  # Path to the iperf3 binary (version 3.17 or newer); empty = auto-detect.
  path: ""
  # Install iperf3 on startup without asking if it is missing (winget or package manager).
  # Default: off – the web interface asks an administrator before installing.
  auto_install: false

geoip:
  # City database for the locations on the map (relative to this file).
  path: "geoip/city.mmdb"
  # Download the database automatically and keep it up to date. Without
  # MaxMind credentials: DB-IP City Lite (no account needed, monthly updates).
  auto_update: true
  # Optional: free MaxMind account (https://www.maxmind.com/en/geolite2/signup)
  # to use GeoLite2 City instead. The license key can also be set via
  # IPERF3_GEOIP_MAXMIND_LICENSE_KEY.
  maxmind_account_id: ""
  maxmind_license_key: ""

map:
  # Empty = embedded offline world map (Natural Earth), no external requests.
  # Optional own tile server, e.g. "https://tiles.example.org/{z}/{x}/{y}.png"
  tile_url: ""
  # Attribution shown for the tiles (HTML allowed); empty = OpenStreetMap notice.
  tile_attribution: ""

update:
  # auto = install new releases and restart the service, notify = only report, off
  mode: "auto"
  interval: "6h"
  # Optional daily window for automatic installs, e.g. "02:00-05:00".
  window: ""

log:
  # debug | info | warn | error
  level: "info"
  # text | json
  format: "text"
`

// CreateDefault writes a default config.yaml with a fresh token key to path and loads it.
func CreateDefault(path string) (*Config, error) {
	content := fmt.Sprintf(defaultYAML, newSecret())
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("default configuration could not be created (%s): %w", path, err)
		}
	}
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		return nil, fmt.Errorf("default configuration could not be created (%s): %w", path, err)
	}
	return Load(path)
}

// Load reads the YAML config, then applies environment variables.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("configuration could not be read: %w", err)
	}

	cfg := defaults()
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("configuration is invalid: %w", err)
	}
	if err := cfg.applyEnv(os.LookupEnv); err != nil {
		return nil, err
	}

	// Resolve relative paths against the config file's directory.
	cfg.Dir = filepath.Dir(filepath.Clean(path))
	for _, p := range []*string{&cfg.Storage.Path, &cfg.GeoIP.Path} {
		if !filepath.IsAbs(*p) {
			*p = filepath.Join(cfg.Dir, *p)
		}
	}

	if cfg.Auth.SecretKey == "" {
		cfg.Auth.SecretKey = newSecret()
		cfg.Auth.SecretGenerated = true
	}

	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func defaults() *Config {
	return &Config{
		Web:       WebConfig{Listen: DefaultListen},
		Storage:   StorageConfig{Path: "data/i-tracker.db"},
		Auth:      AuthConfig{SessionTTL: "12h", PublicRead: true},
		GeoIP:     GeoIPConfig{Path: "geoip/city.mmdb", AutoUpdate: true},
		Scheduler: SchedulerConfig{Enabled: true},
		Update:    UpdateConfig{Mode: string(selfupdate.ModeAuto), Interval: "6h"},
		Log:       LogConfig{Level: "info", Format: "text"},
	}
}

func (c *Config) validate() error {
	if c.Web.Listen == "" {
		return errors.New("web.listen must not be empty")
	}
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(c.Log.Level)); err != nil {
		return fmt.Errorf("log.level is invalid: %q", c.Log.Level)
	}
	if d, err := c.Auth.SessionDuration(); err != nil || d < time.Minute {
		return fmt.Errorf("auth.session_ttl must be a duration of at least 1m (e.g. 12h): %q", c.Auth.SessionTTL)
	}
	switch c.Log.Format {
	case "", "text", "json":
	default:
		return fmt.Errorf("log.format must be text or json: %q", c.Log.Format)
	}
	if (c.GeoIP.MaxMindAccountID == "") != (c.GeoIP.MaxMindLicenseKey == "") {
		return errors.New("geoip.maxmind_account_id and geoip.maxmind_license_key must be set together")
	}
	if c.Map.TileURL != "" {
		u, err := url.Parse(tilePlaceholders.Replace(c.Map.TileURL))
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") ||
			!strings.Contains(c.Map.TileURL, "{z}") || !strings.Contains(c.Map.TileURL, "{x}") || !strings.Contains(c.Map.TileURL, "{y}") {
			return fmt.Errorf("map.tile_url must be an http(s) URL with {z}, {x} and {y}: %q", c.Map.TileURL)
		}
	}
	if _, err := selfupdate.ParseMode(c.Update.Mode); err != nil {
		return fmt.Errorf("update.mode: %w", err)
	}
	if d, err := c.Update.IntervalDuration(); err != nil || d < time.Minute {
		return fmt.Errorf("update.interval must be a duration of at least 1m (e.g. 6h): %q", c.Update.Interval)
	}
	if _, err := selfupdate.ParseWindow(c.Update.Window); err != nil {
		return fmt.Errorf("update.window: %w", err)
	}
	return nil
}

func newSecret() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("random number generator not available: " + err.Error())
	}
	return hex.EncodeToString(b)
}
