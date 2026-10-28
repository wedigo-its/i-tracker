// Package web serves the embedded UI and the HTTP API.
package web

import (
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

// Deps are the HTTP server's dependencies.
type Deps struct {
	Store     *store.Store
	Tokens    *auth.Tokens
	Runner    *iperf.Runner
	Tracer    *trace.Tracer
	Scheduler *scheduler.Scheduler
	// Map configures the peering map (empty = embedded world map).
	Map MapSettings
	// GeoIP reports the location database state (optional).
	GeoIP func() trace.GeoInfo
	// PublicRead opens read-only endpoints to guests (auth.public_read).
	PublicRead bool
}

// MapSettings describes the UI map background.
type MapSettings struct {
	TileURL         string `json:"tile_url"`
	TileAttribution string `json:"tile_attribution"`
	// TileOrigin is the tile server's scheme and host for the CSP img-src.
	TileOrigin string `json:"-"`
}

// Server holds the HTTP handlers.
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
	// publicRead: guests may use read-only endpoints (see optionalUser).
	publicRead bool

	testTraces testTraces
	// legacyAdmin detects the legacy default password for /api/auth/status.
	legacyAdmin legacyPasswordCache
	// limiter throttles failed sign-ins and wrong current passwords per client address.
	limiter *auth.Limiter
	// revoked holds signed-out sessions until they expire.
	revoked *auth.Revocations

	// routeList holds the API routes for the product docs (Routes).
	routeList []Route
}

// Route describes an API route for the product docs (i-tracker reference).
// Access: public, read (guests too with auth.public_read), user or admin.
type Route struct {
	Pattern string `json:"route"`
	Access  string `json:"access"`
}

// Routes returns all API routes in registration order, excluding the UI (/, /static/).
func Routes() []Route {
	return NewServer(Deps{}).routeList
}

// NewServer creates the HTTP server.
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

// Handler returns the HTTP handler.
func (s *Server) Handler() http.Handler { return s.handler }

func (s *Server) routes() {
	// add registers an API route and records its access level for Routes.
	add := func(pattern, access string, h http.Handler) {
		s.mux.Handle(pattern, h)
		s.routeList = append(s.routeList, Route{Pattern: pattern, Access: access})
	}
	s.mux.HandleFunc("GET /{$}", handleIndex)
	s.mux.Handle("GET /static/", noCache(http.FileServerFS(static)))
	// Vendor libraries change only with new releases: cache for a day.
	s.mux.Handle("GET /static/vendor/", cacheFor(24*time.Hour, http.FileServerFS(static)))
	add("GET /api/info", "public", http.HandlerFunc(s.handleInfo))
	add("GET /health", "public", http.HandlerFunc(s.handleHealth))
	// Read-only UI endpoints use optionalUser (guests allowed with auth.public_read).
	add("GET /api/status", "read", s.optionalUser(s.handleStatus))

	add("POST /api/auth/login", "public", http.HandlerFunc(s.handleLogin))
	add("POST /api/auth/logout", "public", http.HandlerFunc(s.handleLogout))
	add("POST /api/auth/logout-all", "user", s.requireUserPending(s.handleLogoutAll))
	add("GET /api/auth/status", "public", http.HandlerFunc(s.handleAuthStatus))
	add("GET /api/auth/me", "user", s.requireUserPending(s.handleMe))
	add("POST /api/auth/change-password", "user", s.requireUserPending(s.handleChangePassword))
	add("POST /api/auth/register", "admin", s.requireAdmin(s.handleRegister))
	add("GET /api/auth/users", "admin", s.requireAdmin(s.handleListUsers))
	add("DELETE /api/auth/users/{user_id}", "admin", s.requireAdmin(s.handleDeleteUser))

	add("GET /api/servers", "read", s.optionalUser(s.handleListServers))
	add("POST /api/servers", "user", s.requireUser(s.handleCreateServer))
	add("GET /api/servers/{server_id}", "read", s.optionalUser(s.handleGetServer))
	add("PUT /api/servers/{server_id}", "user", s.requireUser(s.handleUpdateServer))
	add("DELETE /api/servers/{server_id}", "user", s.requireUser(s.handleDeleteServer))

	add("GET /api/tests", "read", s.optionalUser(s.handleListTests))
	add("POST /api/tests/run", "user", s.requireUser(s.handleRunTest))
	add("GET /api/tests/{test_id}", "read", s.optionalUser(s.handleGetTest))
	add("DELETE /api/tests/{test_id}", "user", s.requireUser(s.handleDeleteTest))
	add("GET /api/tests/{test_id}/live", "read", s.optionalUser(s.handleTestLive))
	add("GET /api/tests/live/stream", "read", s.optionalUser(s.handleLiveTestsStream))
	add("GET /api/tests/server/{server_id}/latest", "read", s.optionalUser(s.handleLatestTest))

	add("GET /api/stats/dashboard", "read", s.optionalUser(s.handleDashboardStats))
	add("GET /api/stats/servers", "read", s.optionalUser(s.handleServerStatsList))
	add("GET /api/stats/servers/{server_id}", "read", s.optionalUser(s.handleServerStats))

	add("POST /api/tests/{test_id}/trace", "user", s.requireUser(s.handleStartTestTrace))
	add("GET /api/tests/{test_id}/trace", "read", s.optionalUser(s.handleGetTestTrace))
	add("DELETE /api/tests/{test_id}/trace", "user", s.requireUser(s.handleDeleteTestTrace))
	add("POST /api/traces", "user", s.requireUser(s.handleCreateTrace))
	add("GET /api/traces", "read", s.optionalUser(s.handleListTraces))
	add("GET /api/traces/{trace_id}", "read", s.optionalUser(s.handleGetTrace))
	add("GET /api/traces/test/{test_id}", "read", s.optionalUser(s.handleTracesByTest))
	add("DELETE /api/traces/{trace_id}", "user", s.requireUser(s.handleDeleteTrace))

	// Runs and saves a trace to any target, so signed-in users only even with
	// auth.public_read; auth errors arrive as SSE events.
	add("GET /api/live-trace/stream/{destination}", "user", http.HandlerFunc(s.handleLiveTrace))

	add("DELETE /api/admin/cleanup/tests", "admin", s.requireAdmin(s.handleCleanupTests))
	add("DELETE /api/admin/cleanup/traces", "admin", s.requireAdmin(s.handleCleanupTraces))
	add("GET /api/admin/stats/database", "admin", s.requireAdmin(s.handleDatabaseStats))
	add("POST /api/iperf3/install", "admin", s.requireAdmin(s.handleInstallIperf))

	// Public: static list without user data.
	add("GET /api/public-servers", "public", http.HandlerFunc(s.handlePublicServers))
	add("GET /api/public-servers/search", "public", http.HandlerFunc(s.handleSearchPublicServers))
}

func (s *Server) handleInfo(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, r, http.StatusOK, map[string]any{
		"name":       version.Name,
		"version":    version.Version,
		"build_date": version.BuildDate,
	})
}

// handleStatus feeds the UI status dot; guests (u == nil) get only iperf3
// availability and version, not local path, error or install command.
func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request, u *model.User) {
	geo := trace.GeoInfo{}
	if s.geoInfo != nil {
		geo = s.geoInfo()
	}
	var ip any = s.iperfStatus(r)
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

// iperfStatus returns the iperf3 status with its error in the request language.
func (s *Server) iperfStatus(r *http.Request) iperf.Status {
	st := s.runner.Status()
	if st.Err != nil {
		st.Error = i18n.ErrorIn(i18n.FromRequest(r), st.Err)
	}
	return st
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, r, http.StatusOK, map[string]any{
		"status":            "healthy",
		"scheduler_running": s.scheduler.Running(),
	})
}

// writeJSON writes v as JSON. Texts in v must already be rendered in the
// request language (writeErr, message, localizeTests, localizeTraces).
func writeJSON(w http.ResponseWriter, r *http.Request, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Language", i18n.FromRequest(r))
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// errorBody is the JSON of an error response: detail is the message in the
// request language (as in every earlier version), code its catalog key.
type errorBody struct {
	Detail string `json:"detail"`
	Code   string `json:"code,omitempty"`
}

// writeErr answers with err rendered in the request language. Errors that
// reach the UI are *i18n.Error values (i18n.E, i18n.Wrap); any other error
// is shown with its own text.
func writeErr(w http.ResponseWriter, r *http.Request, status int, err error) {
	writeJSON(w, r, status, errorBody{Detail: i18n.ErrorIn(i18n.FromRequest(r), err), Code: i18n.KeyOf(err)})
}

// writeError answers with the catalog message key and its arguments.
func writeError(w http.ResponseWriter, r *http.Request, status int, key string, args ...any) {
	writeErr(w, r, status, i18n.E(key, args...))
}

// message renders the catalog message key in the request language, for the
// "message" field of a response.
func message(r *http.Request, key string, args ...any) string {
	return i18n.M(key, args...).In(i18n.FromRequest(r))
}

// writeInternal logs err and answers 500 without revealing internals.
func writeInternal(w http.ResponseWriter, r *http.Request, err error) {
	slog.Error("Internal error", "method", r.Method, "path", r.URL.Path, "error", err)
	writeError(w, r, http.StatusInternalServerError, "err.internal")
}

// decodeJSON reads the request body into dst; on errors it answers 422 and
// returns false.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		writeErr(w, r, http.StatusUnprocessableEntity, i18n.Wrap(err, "err.invalid_body"))
		return false
	}
	// encoding/json silently replaces invalid bytes with U+FFFD; wrongly
	// encoded umlauts would be stored corrupted without notice.
	if !utf8.Valid(body) {
		writeError(w, r, http.StatusUnprocessableEntity, "err.invalid_encoding")
		return false
	}
	if err := json.Unmarshal(body, dst); err != nil {
		writeErr(w, r, http.StatusUnprocessableEntity, i18n.Wrap(err, "err.invalid_body"))
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

// cacheFor lets the browser cache the response for d.
func cacheFor(d time.Duration, next http.Handler) http.Handler {
	v := "public, max-age=" + strconv.Itoa(int(d/time.Second))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", v)
		next.ServeHTTP(w, r)
	})
}

func init() {
	// Serve the embedded world map with the right type.
	_ = mime.AddExtensionType(".geojson", "application/geo+json")
}

// noCache forces revalidation so a new build shows the current UI at once.
func noCache(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		next.ServeHTTP(w, r)
	})
}
