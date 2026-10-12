// Package web stellt die Oberfläche (eingebettet) und die HTTP-API bereit.
package web

import (
	"bytes"
	"embed"
	"encoding/json"
	"io"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"strconv"
	"time"
	"unicode/utf8"

	"i-tracker/internal/auth"
	"i-tracker/internal/i18n"
	"i-tracker/internal/iperf"
	"i-tracker/internal/model"
	"i-tracker/internal/scheduler"
	"i-tracker/internal/store"
	"i-tracker/internal/trace"
	"i-tracker/internal/version"
)

//go:embed static
var static embed.FS

// Deps sind die Abhängigkeiten des HTTP-Servers.
type Deps struct {
	Store     *store.Store
	Tokens    *auth.Tokens
	Runner    *iperf.Runner
	Tracer    *trace.Tracer
	Scheduler *scheduler.Scheduler
	// Map ist die Kartenkonfiguration der Peering-Map (leer = eingebettete Weltkarte).
	Map MapSettings
	// GeoIP liefert den Zustand der Standort-Datenbank (optional).
	GeoIP func() trace.GeoInfo
	// PublicRead erlaubt Gästen ohne Anmeldung die lesenden Endpunkte
	// (auth.public_read); ändernde verlangen weiter eine Anmeldung.
	PublicRead bool
}

// MapSettings beschreibt den Kartenhintergrund für die Oberfläche.
type MapSettings struct {
	TileURL         string `json:"tile_url"`
	TileAttribution string `json:"tile_attribution"`
	// TileOrigin ist Schema und Host des Kachelservers für die CSP (img-src).
	TileOrigin string `json:"-"`
}

// Server bündelt die HTTP-Handler.
type Server struct {
	store     *store.Store
	tokens    *auth.Tokens
	runner    *iperf.Runner
	tracer    *trace.Tracer
	scheduler *scheduler.Scheduler
	mapCfg    MapSettings
	geoInfo   func() trace.GeoInfo
	mux       *http.ServeMux
	handler   http.Handler
	// publicRead: Gäste dürfen die lesenden Endpunkte nutzen (siehe optionalUser).
	publicRead bool

	testTraces testTraces
	// legacyAdmin erkennt das frühere Standardpasswort für /api/auth/status.
	legacyAdmin legacyPasswordCache
	// limiter bremst Fehlanmeldungen (Login und falsches aktuelles Passwort
	// beim Passwortwechsel) je Client-Adresse.
	limiter *auth.Limiter
	// revoked enthält die abgemeldeten Sitzungen bis zu ihrem Ablauf.
	revoked *auth.Revocations
}

// NewServer erstellt den HTTP-Server.
func NewServer(d Deps) *Server {
	s := &Server{
		store:      d.Store,
		tokens:     d.Tokens,
		runner:     d.Runner,
		tracer:     d.Tracer,
		scheduler:  d.Scheduler,
		mapCfg:     d.Map,
		geoInfo:    d.GeoIP,
		publicRead: d.PublicRead,
		mux:        http.NewServeMux(),
		testTraces: testTraces{running: map[int64]bool{}},
		limiter:    auth.NewLimiter(auth.LoginMaxFailures, auth.LoginWindow),
		revoked:    auth.NewRevocations(),
	}
	s.routes()
	imgSrc := ""
	if d.Map.TileOrigin != "" {
		imgSrc = " " + d.Map.TileOrigin
	}
	s.handler = securityHeaders(crossOriginGuard(s.mux), imgSrc)
	return s
}

// Handler liefert den HTTP-Handler.
func (s *Server) Handler() http.Handler { return s.handler }

func (s *Server) routes() {
	// Oberfläche: index.html unter "/", Skripte und Styles unter /static/.
	s.mux.HandleFunc("GET /{$}", handleIndex)
	s.mux.Handle("GET /static/", noCache(http.FileServerFS(static)))
	// Fremdbibliotheken ändern sich nur mit neuen Versionen: einen Tag zwischenspeichern.
	s.mux.Handle("GET /static/vendor/", cacheFor(24*time.Hour, http.FileServerFS(static)))
	s.mux.HandleFunc("GET /api/info", s.handleInfo)
	s.mux.HandleFunc("GET /health", s.handleHealth)
	// Lesende Endpunkte der Oberfläche nutzen optionalUser: Mit
	// auth.public_read dürfen auch Gäste sie aufrufen (u == nil), sonst
	// verlangen sie wie alle ändernden Endpunkte eine Anmeldung.
	s.mux.Handle("GET /api/status", s.optionalUser(s.handleStatus))

	s.mux.HandleFunc("POST /api/auth/login", s.handleLogin)
	s.mux.HandleFunc("POST /api/auth/logout", s.handleLogout)
	s.mux.Handle("POST /api/auth/logout-all", s.requireUserPending(s.handleLogoutAll))
	s.mux.HandleFunc("GET /api/auth/status", s.handleAuthStatus)
	s.mux.Handle("GET /api/auth/me", s.requireUserPending(s.handleMe))
	s.mux.Handle("POST /api/auth/change-password", s.requireUserPending(s.handleChangePassword))
	s.mux.Handle("POST /api/auth/register", s.requireAdmin(s.handleRegister))
	s.mux.Handle("GET /api/auth/users", s.requireAdmin(s.handleListUsers))
	s.mux.Handle("DELETE /api/auth/users/{user_id}", s.requireAdmin(s.handleDeleteUser))

	s.mux.Handle("GET /api/servers", s.optionalUser(s.handleListServers))
	s.mux.Handle("POST /api/servers", s.requireUser(s.handleCreateServer))
	s.mux.Handle("GET /api/servers/{server_id}", s.optionalUser(s.handleGetServer))
	s.mux.Handle("PUT /api/servers/{server_id}", s.requireUser(s.handleUpdateServer))
	s.mux.Handle("DELETE /api/servers/{server_id}", s.requireUser(s.handleDeleteServer))

	s.mux.Handle("GET /api/tests", s.optionalUser(s.handleListTests))
	s.mux.Handle("POST /api/tests/run", s.requireUser(s.handleRunTest))
	s.mux.Handle("GET /api/tests/{test_id}", s.optionalUser(s.handleGetTest))
	s.mux.Handle("DELETE /api/tests/{test_id}", s.requireUser(s.handleDeleteTest))
	s.mux.Handle("GET /api/tests/{test_id}/live", s.optionalUser(s.handleTestLive))
	s.mux.Handle("GET /api/tests/live/stream", s.optionalUser(s.handleLiveTestsStream))
	s.mux.Handle("GET /api/tests/server/{server_id}/latest", s.optionalUser(s.handleLatestTest))

	s.mux.Handle("GET /api/stats/dashboard", s.optionalUser(s.handleDashboardStats))
	s.mux.Handle("GET /api/stats/servers", s.optionalUser(s.handleServerStatsList))
	s.mux.Handle("GET /api/stats/servers/{server_id}", s.optionalUser(s.handleServerStats))

	s.mux.Handle("POST /api/tests/{test_id}/trace", s.requireUser(s.handleStartTestTrace))
	s.mux.Handle("GET /api/tests/{test_id}/trace", s.optionalUser(s.handleGetTestTrace))
	s.mux.Handle("DELETE /api/tests/{test_id}/trace", s.requireUser(s.handleDeleteTestTrace))
	s.mux.Handle("POST /api/traces", s.requireUser(s.handleCreateTrace))
	s.mux.Handle("GET /api/traces", s.optionalUser(s.handleListTraces))
	s.mux.Handle("GET /api/traces/{trace_id}", s.optionalUser(s.handleGetTrace))
	s.mux.Handle("GET /api/traces/test/{test_id}", s.optionalUser(s.handleTracesByTest))
	s.mux.Handle("DELETE /api/traces/{trace_id}", s.requireUser(s.handleDeleteTrace))

	// Startet eine Traceroute zu einem beliebigen Ziel und speichert sie – eine
	// Aktion, daher auch mit auth.public_read nur für angemeldete Benutzer.
	// Anmeldung per Sitzungs-Cookie (EventSource) oder Bearer-Token; Fehler als SSE-Event.
	s.mux.HandleFunc("GET /api/live-trace/stream/{destination}", s.handleLiveTrace)

	s.mux.Handle("DELETE /api/admin/cleanup/tests", s.requireAdmin(s.handleCleanupTests))
	s.mux.Handle("DELETE /api/admin/cleanup/traces", s.requireAdmin(s.handleCleanupTraces))
	s.mux.Handle("GET /api/admin/stats/database", s.requireAdmin(s.handleDatabaseStats))
	s.mux.Handle("POST /api/iperf3/install", s.requireAdmin(s.handleInstallIperf))

	// Öffentlich: statische Liste ohne Benutzerdaten.
	s.mux.HandleFunc("GET /api/public-servers", s.handlePublicServers)
	s.mux.HandleFunc("GET /api/public-servers/search", s.handleSearchPublicServers)
}

func (s *Server) handleInfo(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, r, http.StatusOK, map[string]any{
		"name":       version.Name,
		"version":    version.Version,
		"build_date": version.BuildDate,
	})
}

// handleStatus liefert den Betriebszustand für den Statuspunkt der Oberfläche.
// Angemeldete Benutzer erhalten den vollständigen iperf3-Status mit lokalem
// Pfad, Fehlermeldung und Installationsbefehl; Gäste (u == nil) nur, ob
// iperf3 verfügbar ist, und die Version.
func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request, u *model.User) {
	geo := trace.GeoInfo{}
	if s.geoInfo != nil {
		geo = s.geoInfo()
	}
	var ip any = s.runner.Status()
	if u == nil {
		st := s.runner.Status()
		ip = map[string]any{"available": st.Available, "installing": st.Installing, "version": st.Version}
	}
	writeJSON(w, r, http.StatusOK, map[string]any{
		"scheduler_running": s.scheduler.Running(),
		"iperf3":            ip,
		"map":               s.mapCfg,
		"geoip":             geo,
	})
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, r, http.StatusOK, map[string]any{
		"status":            "healthy",
		"scheduler_running": s.scheduler.Running(),
	})
}

// writeJSON schreibt v als JSON. Meldungstexte werden dabei in die Sprache
// der Anfrage übersetzt (siehe localizeJSON).
func writeJSON(w http.ResponseWriter, r *http.Request, status int, v any) {
	lang := i18n.FromRequest(r)
	if lang != i18n.DE {
		v = localizeJSON(v, lang)
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Language", lang)
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// messageKeys sind die JSON-Felder mit Meldungstexten. Nutzerdaten wie Namen
// oder Beschreibungen werden nie übersetzt.
var messageKeys = map[string]bool{"detail": true, "message": true, "warning": true, "error": true, "error_message": true}

// localizeJSON übersetzt die Meldungsfelder in v (auch verschachtelt). Dazu
// wird v einmal in eine generische Struktur umgewandelt; scheitert das,
// bleibt v unverändert.
func localizeJSON(v any, lang string) any {
	data, err := json.Marshal(v)
	if err != nil {
		return v
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var generic any
	if err := dec.Decode(&generic); err != nil {
		return v
	}
	var walk func(any)
	walk = func(node any) {
		switch n := node.(type) {
		case map[string]any:
			for k, val := range n {
				if s, ok := val.(string); ok && messageKeys[k] {
					n[k] = i18n.Translate(s, lang)
				} else {
					walk(val)
				}
			}
		case []any:
			for _, e := range n {
				walk(e)
			}
		}
	}
	walk(generic)
	return generic
}

// writeError schreibt eine Fehlerantwort als {"detail": "..."}.
func writeError(w http.ResponseWriter, r *http.Request, status int, detail string) {
	writeJSON(w, r, status, map[string]string{"detail": detail})
}

// writeInternal protokolliert err und antwortet mit 500, ohne Interna preiszugeben.
func writeInternal(w http.ResponseWriter, r *http.Request, err error) {
	slog.Error("Interner Fehler", "method", r.Method, "path", r.URL.Path, "error", err)
	writeError(w, r, http.StatusInternalServerError, "Interner Serverfehler")
}

// decodeJSON liest den Request-Body nach dst; bei Fehlern wird mit 422
// geantwortet und false geliefert.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		writeError(w, r, http.StatusUnprocessableEntity, "Ungültiger Request-Body: "+err.Error())
		return false
	}
	// encoding/json ersetzt ungültige Bytes stillschweigend durch U+FFFD; so
	// würden falsch kodierte Umlaute unbemerkt verfälscht gespeichert.
	if !utf8.Valid(body) {
		writeError(w, r, http.StatusUnprocessableEntity, "Ungültige Zeichenkodierung – erwartet wird UTF-8")
		return false
	}
	if err := json.Unmarshal(body, dst); err != nil {
		writeError(w, r, http.StatusUnprocessableEntity, "Ungültiger Request-Body: "+err.Error())
		return false
	}
	return true
}

func handleIndex(w http.ResponseWriter, r *http.Request) {
	data, err := fs.ReadFile(static, "static/index.html")
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Write(data)
}

// cacheFor erlaubt dem Browser, die Antwort für d zwischenzuspeichern.
func cacheFor(d time.Duration, next http.Handler) http.Handler {
	v := "public, max-age=" + strconv.Itoa(int(d/time.Second))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", v)
		next.ServeHTTP(w, r)
	})
}

func init() {
	// Die eingebettete Weltkarte (GeoJSON) mit passendem Typ ausliefern.
	_ = mime.AddExtensionType(".geojson", "application/geo+json")
}

// noCache lässt den Browser eingebettete Dateien bei jedem Laden prüfen, damit
// nach einem neuen Build sofort die aktuelle Oberfläche erscheint.
func noCache(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		next.ServeHTTP(w, r)
	})
}
