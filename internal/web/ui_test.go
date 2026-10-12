package web

import (
	"io"
	"io/fs"
	"net/http"
	"regexp"
	"strings"
	"testing"
)

func TestServesUI(t *testing.T) {
	srv := newTestServer(t)
	for path, want := range map[string]string{
		"/":              "<title>i-Tracker</title>",
		"/static/app.js": "async function api(",
	} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), want) {
			t.Errorf("%s: Status %d, enthält %q nicht", path, resp.StatusCode, want)
		}
		if cc := resp.Header.Get("Cache-Control"); cc != "no-cache" {
			t.Errorf("%s: Cache-Control = %q", path, cc)
		}
	}
	expectStatus(t, srv, "GET", "/static/gibt-es-nicht.js", "", "", http.StatusNotFound)
}

// TestAssetsEmbedded prüft, dass alle Bibliotheken, Styles und die Weltkarte
// eingebettet ausgeliefert werden und die Oberfläche keine fremden Server
// anspricht (CSP default-src 'self').
func TestAssetsEmbedded(t *testing.T) {
	srv := newTestServer(t)
	for path, want := range map[string]string{
		"/static/wedigo-tokens.css":                      "--accent-rgb",
		"/static/app.css":                                ".map-land",
		"/static/theme-init.js":                          "wedigo-theme",
		"/static/i18n.js":                                "wedigo-lang",
		"/static/vendor/chartjs/chart.umd.js":            "Chart.js v4.4.1",
		"/static/vendor/chartjs/LICENSE.md":              "MIT",
		"/static/vendor/leaflet/leaflet.js":              "1.9.4",
		"/static/vendor/leaflet/leaflet.css":             ".leaflet-pane",
		"/static/vendor/leaflet/LICENSE":                 "BSD",
		"/static/vendor/leaflet/images/marker-icon.png":  "PNG",
		"/static/vendor/naturalearth/world-110m.geojson": `"FeatureCollection"`,
		"/static/vendor/naturalearth/LICENSE.txt":        "public domain",
	} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), want) {
			t.Errorf("%s: Status %d, enthält %q nicht", path, resp.StatusCode, want)
		}
	}

	// index.html und die eigenen Skripte laden nichts von außen (Links wie die
	// Kartenquelle in der Attribution sind erlaubt) und enthalten
	// weder Inline-Skripte noch Inline-Handler oder -Styles.
	external := regexp.MustCompile(`(?i)src\s*=\s*["']https?://|<link[^>]+href\s*=\s*["']https?://|url\(\s*["']?https?://|fetch\(\s*["']https?://|EventSource\(\s*["']https?://|tile\.openstreetmap|cdn\.jsdelivr|unpkg\.com`)
	inline := regexp.MustCompile(`(?i)\son[a-z]+\s*=\s*"|\sstyle\s*=\s*"|<script>`)
	for _, name := range []string{"static/index.html", "static/app.js", "static/i18n.js", "static/theme-init.js", "static/app.css"} {
		data, err := fs.ReadFile(static, name)
		if err != nil {
			t.Fatal(err)
		}
		if m := external.Find(data); m != nil {
			t.Errorf("%s lädt eine externe Ressource: %s", name, m)
		}
		if strings.HasSuffix(name, ".css") {
			continue
		}
		for _, m := range inline.FindAll(data, -1) {
			t.Errorf("%s enthält Inline-Code: %s", name, m)
		}
	}
}

// TestNoHexOutsideTokens: Hex-Farben stehen nur in wedigo-tokens.css, im
// Produktfarben-Block von app.css und in der Serienpalette (SERIES_COLORS).
func TestNoHexOutsideTokens(t *testing.T) {
	hex := regexp.MustCompile(`#[0-9a-fA-F]{6}\b|#[0-9a-fA-F]{3}\b`)
	css, _ := fs.ReadFile(static, "static/app.css")
	_, rest, _ := strings.Cut(string(css), "/* ---------- Abgeleitete Werte")
	if m := hex.FindString(rest); m != "" {
		t.Errorf("app.css: Hex-Farbe %s außerhalb des Produktfarben-Blocks", m)
	}
	js, _ := fs.ReadFile(static, "static/app.js")
	for _, line := range strings.Split(string(js), "\n") {
		if strings.Contains(line, "SERIES_COLORS = [") {
			continue
		}
		if m := hex.FindString(line); m != "" && !strings.Contains(line, "&#") {
			t.Errorf("app.js: Hex-Farbe %s in %q", m, strings.TrimSpace(line))
		}
	}
}

// TestI18nCatalogComplete: Jeder Textschlüssel aus index.html (data-i18n*)
// und app.js (t('…'), auch t(x ? '…' : '…')) steht im Katalog von i18n.js,
// jeweils mit deutscher und englischer Fassung.
func TestI18nCatalogComplete(t *testing.T) {
	read := func(name string) string {
		data, err := fs.ReadFile(static, "static/"+name)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	catalog := read("i18n.js")
	entry := regexp.MustCompile(`(?m)^\s*'([\w.]+)':\s*\[\s*'(?:[^'\\]|\\.)*',\s*'(?:[^'\\]|\\.)*'\s*\]`)
	keys := map[string]bool{}
	for _, m := range entry.FindAllStringSubmatch(catalog, -1) {
		keys[m[1]] = true
	}
	if len(keys) < 200 {
		t.Fatalf("nur %d Katalogeinträge mit zwei Sprachen erkannt", len(keys))
	}

	used := map[string]bool{}
	for _, m := range regexp.MustCompile(`data-i18n(?:-title|-aria|-placeholder)?="([\w.]+)"`).FindAllStringSubmatch(read("index.html"), -1) {
		used[m[1]] = true
	}
	app := read("app.js")
	for _, m := range regexp.MustCompile(`\bt\('([\w.]*\w)'`).FindAllStringSubmatch(app, -1) {
		used[m[1]] = true
	}
	for _, m := range regexp.MustCompile(`\bt\([\w.]+ \? '([\w.]+)' : '([\w.]+)'`).FindAllStringSubmatch(app, -1) {
		used[m[1]], used[m[2]] = true, true
	}
	for _, k := range []string{"guest.badge", "guest.back", "guest.noTestHint", "guest.signInTitle"} {
		if !used[k] {
			t.Errorf("%s wird nicht verwendet", k)
		}
	}
	for k := range used {
		if !keys[k] {
			t.Errorf("Textschlüssel %q fehlt im Katalog (de und en)", k)
		}
	}
}

// TestGuestUIMarkers: In der Gastansicht blendet body.read-only alle
// ändernden Bedienelemente aus; sie tragen dafür data-user-only.
func TestGuestUIMarkers(t *testing.T) {
	html, _ := fs.ReadFile(static, "static/index.html")
	for _, marker := range []string{
		`id="testForm" data-user-only`,
		`id="pmStart" data-user-only`,
		`id="testModalDelete" data-user-only`,
		`data-user-only data-click="serversPage.openForm"`,
		`data-user-only data-click="startTestShortcut"`,
		`data-user-only data-click="logout"`,
		`data-guest-only data-click="openLogin"`,
		`id="guestBack"`,
	} {
		if !strings.Contains(string(html), marker) {
			t.Errorf("index.html: %s fehlt", marker)
		}
	}
	js, _ := fs.ReadFile(static, "static/app.js")
	for _, marker := range []string{
		`data-user-only title="${t('dash.quickTest')}"`,
		`<div class="card-actions" data-user-only>`,
		`data-user-only data-click="peering.deleteTrace"`,
		`function showGuest()`,
	} {
		if !strings.Contains(string(js), marker) {
			t.Errorf("app.js: %s fehlt", marker)
		}
	}
	css, _ := fs.ReadFile(static, "static/app.css")
	if !strings.Contains(string(css), "body.read-only [data-user-only] { display: none !important; }") {
		t.Error("app.css: Regel für data-user-only fehlt")
	}
}
