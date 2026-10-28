package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestListenDefaults(t *testing.T) {
	// Without web.listen: local only.
	cfg, err := Load(writeConfig(t, "log:\n  level: info\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Web.Listen != DefaultListen || DefaultListen != "127.0.0.1:8000" {
		t.Errorf("Listen = %q", cfg.Web.Listen)
	}
	// The file generated on first start keeps the previous behaviour.
	created, err := CreateDefault(filepath.Join(t.TempDir(), "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if created.Web.Listen != "0.0.0.0:8000" || created.Auth.SecretKey == "" || created.Auth.SecretGenerated {
		t.Errorf("CreateDefault: %+v", created.Web)
	}
	if created.Log.Format != "text" || created.Map.TileURL != "" {
		t.Errorf("CreateDefault: log %+v, map %+v", created.Log, created.Map)
	}
}

func TestEnvOverrides(t *testing.T) {
	env := map[string]string{
		"IPERF3_WEB_LISTEN":        "0.0.0.0:9000",
		"IPERF3_SCHEDULER_ENABLED": "false",
		"IPERF3_LOG_FORMAT":        "json",
		"IPERF3_MAP_TILE_URL":      "https://tiles.example.org/{z}/{x}/{y}.png",
		// Deprecated: applies while the new name is unset …
		"DB_PATH": "/data/x.db",
		// … and is ignored once it is set.
		"LOG_LEVEL":        "debug",
		"IPERF3_LOG_LEVEL": "warn",
	}
	c := defaults()
	if err := c.applyEnv(func(k string) (string, bool) { v, ok := env[k]; return v, ok }); err != nil {
		t.Fatal(err)
	}
	if c.Web.Listen != "0.0.0.0:9000" || c.Scheduler.Enabled || c.Log.Format != "json" || c.Storage.Path != "/data/x.db" || c.Log.Level != "warn" {
		t.Errorf("result: %+v", c)
	}
	if c.Map.TileURL == "" || c.Map.TileOrigin() != "https://tiles.example.org" {
		t.Errorf("TileOrigin = %q", c.Map.TileOrigin())
	}
	if len(c.Warnings) != 2 || !strings.Contains(strings.Join(c.Warnings, "\n"), "IPERF3_STORAGE_PATH") {
		t.Errorf("warnings: %q", c.Warnings)
	}

	bad := defaults()
	if err := bad.applyEnv(func(k string) (string, bool) {
		return "vielleicht", k == "IPERF3_SCHEDULER_ENABLED"
	}); err == nil {
		t.Error("invalid bool accepted")
	}
}

func TestEnvKeys(t *testing.T) {
	keys := strings.Join(EnvKeys(), ",")
	for _, want := range []string{"IPERF3_WEB_LISTEN", "IPERF3_STORAGE_PATH", "IPERF3_AUTH_SECRET_KEY", "IPERF3_IPERF_AUTO_INSTALL", "IPERF3_MAP_TILE_URL", "IPERF3_LOG_FORMAT", "IPERF3_AUTH_SESSION_TTL", "IPERF3_AUTH_PUBLIC_READ", "IPERF3_GEOIP_AUTO_UPDATE", "IPERF3_GEOIP_MAXMIND_ACCOUNT_ID", "IPERF3_GEOIP_MAXMIND_LICENSE_KEY"} {
		if !strings.Contains(keys, want) {
			t.Errorf("%s missing in %s", want, keys)
		}
	}
}

func TestValidate(t *testing.T) {
	for _, content := range []string{
		"log:\n  format: xml\n",
		"map:\n  tile_url: \"ftp://x/{z}/{x}/{y}\"\n",
		"map:\n  tile_url: \"https://x/tiles.png\"\n",
		"geoip:\n  maxmind_account_id: \"4711\"\n",
	} {
		if _, err := Load(writeConfig(t, content)); err == nil {
			t.Errorf("%q: no error", content)
		}
	}
	cfg, err := Load(writeConfig(t, "map:\n  tile_url: \"https://{s}.tile.example.org/{z}/{x}/{y}.png\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Map.TileOrigin(); got != "https://*.tile.example.org" {
		t.Errorf("TileOrigin = %q", got)
	}
}

func TestSessionTTL(t *testing.T) {
	cfg, err := Load(writeConfig(t, "log:\n  level: info\n"))
	if err != nil {
		t.Fatal(err)
	}
	if d, err := cfg.Auth.SessionDuration(); err != nil || d != 12*time.Hour {
		t.Errorf("default: %v %v", d, err)
	}
	created, err := CreateDefault(filepath.Join(t.TempDir(), "config.yaml"))
	if err != nil || created.Auth.SessionTTL != "12h" {
		t.Errorf("CreateDefault: %q %v", created.Auth.SessionTTL, err)
	}
	cfg, err = Load(writeConfig(t, "auth:\n  session_ttl: \"30m\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if d, _ := cfg.Auth.SessionDuration(); d != 30*time.Minute {
		t.Errorf("from file: %v", d)
	}
	c := defaults()
	if err := c.applyEnv(func(k string) (string, bool) { return "8h", k == "IPERF3_AUTH_SESSION_TTL" }); err != nil {
		t.Fatal(err)
	}
	if d, _ := c.Auth.SessionDuration(); d != 8*time.Hour {
		t.Errorf("from env: %v", d)
	}
	for _, bad := range []string{"zwölf", "30s", "-1h"} {
		if _, err := Load(writeConfig(t, "auth:\n  session_ttl: \""+bad+"\"\n")); err == nil {
			t.Errorf("session_ttl %q accepted", bad)
		}
	}
}

// TestPublicRead: guest view defaults to on and can be disabled via file or env.
func TestPublicRead(t *testing.T) {
	cfg, err := Load(writeConfig(t, "log:\n  level: info\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Auth.PublicRead {
		t.Error("default: public_read off")
	}
	created, err := CreateDefault(filepath.Join(t.TempDir(), "config.yaml"))
	if err != nil || !created.Auth.PublicRead {
		t.Errorf("CreateDefault: public_read %v, %v", created.Auth.PublicRead, err)
	}
	cfg, err = Load(writeConfig(t, "auth:\n  public_read: false\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Auth.PublicRead {
		t.Error("from file: public_read on")
	}
	c := defaults()
	if err := c.applyEnv(func(k string) (string, bool) { return "false", k == "IPERF3_AUTH_PUBLIC_READ" }); err != nil {
		t.Fatal(err)
	}
	if c.Auth.PublicRead {
		t.Error("from env: public_read on")
	}
}

// TestUpdate: self-update defaults to auto every 6h; mode, interval and window are validated.
func TestUpdate(t *testing.T) {
	cfg, err := Load(writeConfig(t, "log:\n  level: info\n"))
	if err != nil {
		t.Fatal(err)
	}
	if d, _ := cfg.Update.IntervalDuration(); cfg.Update.Mode != "auto" || d != 6*time.Hour || cfg.Update.Window != "" {
		t.Errorf("defaults: %+v", cfg.Update)
	}
	created, err := CreateDefault(filepath.Join(t.TempDir(), "config.yaml"))
	if err != nil || created.Update.Mode != "auto" || created.Update.Interval != "6h" {
		t.Errorf("CreateDefault: %+v, %v", created.Update, err)
	}
	cfg, err = Load(writeConfig(t, "update:\n  mode: notify\n  interval: 12h\n  window: \"02:00-05:00\"\n"))
	if err != nil || cfg.Update.Mode != "notify" || cfg.Update.Window != "02:00-05:00" {
		t.Errorf("from file: %+v, %v", cfg.Update, err)
	}
	c := defaults()
	if err := c.applyEnv(func(k string) (string, bool) { return "off", k == "IPERF3_UPDATE_MODE" }); err != nil || c.Update.Mode != "off" {
		t.Errorf("from env: %+v, %v", c.Update, err)
	}
	for _, bad := range []string{"update:\n  mode: sometimes\n", "update:\n  interval: 30s\n", "update:\n  window: \"2-5\"\n"} {
		if _, err := Load(writeConfig(t, bad)); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
