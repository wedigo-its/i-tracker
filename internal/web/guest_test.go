package web

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"i-tracker/internal/auth"
	"i-tracker/internal/db"
	"i-tracker/internal/model"
	"i-tracker/internal/store"
)

// Einordnung der API-Routen für die Gastansicht (auth.public_read). Jede
// GET-Route aus server.go muss in genau einer Liste stehen, damit neue
// Routen bewusst eingeordnet werden.
var (
	// alwaysPublic ist auch ohne public_read ohne Anmeldung erreichbar.
	alwaysPublic = map[string]bool{
		"/api/info":                  true,
		"/api/auth/status":           true,
		"/api/public-servers":        true,
		"/api/public-servers/search": true,
	}
	// guestReadable sind die lesenden Endpunkte der Gastansicht.
	guestReadable = map[string]bool{
		"/api/status":                          true,
		"/api/servers":                         true,
		"/api/servers/{server_id}":             true,
		"/api/tests":                           true,
		"/api/tests/{test_id}":                 true,
		"/api/tests/{test_id}/live":            true,
		"/api/tests/live/stream":               true,
		"/api/tests/server/{server_id}/latest": true,
		"/api/stats/dashboard":                 true,
		"/api/stats/servers":                   true,
		"/api/stats/servers/{server_id}":       true,
		"/api/tests/{test_id}/trace":           true,
		"/api/traces":                          true,
		"/api/traces/{trace_id}":               true,
		"/api/traces/test/{test_id}":           true,
	}
	// userOnlyGet verlangt auch mit public_read eine Anmeldung.
	userOnlyGet = map[string]bool{
		"/api/auth/me":              true,
		"/api/auth/users":           true,
		"/api/admin/stats/database": true,
		// Startet eine Traceroute (eigener Test, Fehler als SSE-Event).
		"/api/live-trace/stream/{destination}": true,
	}
	// publicWrite sind ändernde Routen, die ohne Anmeldung erreichbar sein müssen.
	publicWrite = map[string]bool{
		"POST /api/auth/login":  true,
		"POST /api/auth/logout": true,
	}
)

type route struct{ method, pattern string }

// apiRoutes liefert alle API-Routen aus der Registrierung in server.go (Routes).
func apiRoutes(t *testing.T) []route {
	t.Helper()
	var out []route
	for _, r := range Routes() {
		method, pattern, ok := strings.Cut(r.Pattern, " ")
		if ok && strings.HasPrefix(pattern, "/api/") {
			out = append(out, route{method, pattern})
		}
	}
	if len(out) < 30 {
		t.Fatalf("nur %d Routen registriert", len(out))
	}
	return out
}

// TestRouteAccessMatchesGuestLists gleicht die Zugriffsstufe aus der Registrierung
// (Grundlage der Produktdokumentation) mit den Listen dieses Tests ab.
func TestRouteAccessMatchesGuestLists(t *testing.T) {
	for _, r := range Routes() {
		method, pattern, _ := strings.Cut(r.Pattern, " ")
		if method != "GET" || !strings.HasPrefix(pattern, "/api/") {
			continue
		}
		var want string
		switch {
		case alwaysPublic[pattern]:
			want = "public"
		case guestReadable[pattern]:
			want = "read"
		case userOnlyGet[pattern]:
			want = "user|admin"
		}
		if want == "" || !strings.Contains(want, r.Access) {
			t.Errorf("%s: Zugriffsstufe %q passt nicht zur Einordnung %q", r.Pattern, r.Access, want)
		}
	}
}

// guestFixture legt Server, Test (mit Rohausgabe) und Trace an.
type guestFixture struct {
	srv                     *httptest.Server
	st                      *store.Store
	admin                   string
	serverID, testID, trace int64
}

// rawMarker steht in der Rohausgabe und darf bei Gästen nie auftauchen.
const rawMarker = "rohausgabe-nur-fuer-angemeldete"

func newGuestFixture(t *testing.T, publicRead bool) guestFixture {
	t.Helper()
	srv, st := newTestServerOpts(t, nil, publicRead)
	f := guestFixture{srv: srv, st: st, admin: login(t, srv, "admin", "admin123")}
	ctx := context.Background()

	var sv model.Server
	if code := call(t, srv, "POST", "/api/servers", f.admin, `{"name":"Gast-Server","host":"iperf.example.org"}`, &sv); code != http.StatusCreated {
		t.Fatalf("Server anlegen: %d", code)
	}
	f.serverID = sv.ID

	test := &model.Test{ServerID: sv.ID, Protocol: model.ProtocolTCP, Direction: model.DirectionDownload, Duration: 10, ParallelStreams: 1}
	if err := st.CreateTest(ctx, test); err != nil {
		t.Fatal(err)
	}
	down, raw := 512.5, `{"start":{"system_info":"`+rawMarker+`"}}`
	if err := st.FinishTest(ctx, test.ID, model.StatusCompleted, db.Now(), model.TestResult{DownloadBandwidthMbps: &down}, nil, &raw); err != nil {
		t.Fatal(err)
	}
	f.testID = test.ID

	ip := "192.0.2.1"
	tr := &model.Trace{TestID: &test.ID, DestinationHost: "iperf.example.org", Completed: true,
		Hops: []model.TraceHop{{HopNumber: 1, IPAddress: &ip, Responded: true}}}
	if err := st.CreateTrace(ctx, tr); err != nil {
		t.Fatal(err)
	}
	f.trace = tr.ID
	return f
}

// path setzt die Pfadparameter einer Route ein.
func (f guestFixture) path(pattern string) string {
	return strings.NewReplacer(
		"{server_id}", itoa(f.serverID), "{test_id}", itoa(f.testID), "{trace_id}", itoa(f.trace),
		"{user_id}", "1", "{destination}", "127.0.0.1",
	).Replace(pattern)
}

// guestGet ruft path ohne Anmeldung auf (token optional) und liefert Status
// und Body. Bei SSE-Strömen wird nur der Status gelesen.
func guestGet(t *testing.T, srv *httptest.Server, path, token string) (int, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.Header.Get("Content-Type") == "text/event-stream" {
		return resp.StatusCode, ""
	}
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

// TestGuestRoutesClassified: Jede GET-Route ist für die Gastansicht eingeordnet.
func TestGuestRoutesClassified(t *testing.T) {
	for _, r := range apiRoutes(t) {
		if r.method != "GET" {
			continue
		}
		n := 0
		for _, set := range []map[string]bool{alwaysPublic, guestReadable, userOnlyGet} {
			if set[r.pattern] {
				n++
			}
		}
		if n != 1 {
			t.Errorf("GET %s ist %d-mal eingeordnet (alwaysPublic, guestReadable oder userOnlyGet)", r.pattern, n)
		}
	}
}

// TestGuestReadOnly: Mit public_read lesen Gäste Dashboard, Tests, Traces und
// Server, ohne Geheimnisse zu sehen; alles Ändernde verlangt eine Anmeldung.
func TestGuestReadOnly(t *testing.T) {
	f := newGuestFixture(t, true)

	var bodies strings.Builder
	for _, r := range apiRoutes(t) {
		if r.method != "GET" || !guestReadable[r.pattern] {
			continue
		}
		code, body := guestGet(t, f.srv, f.path(r.pattern), "")
		if code != http.StatusOK {
			t.Errorf("Gast GET %s: Status %d, %s", r.pattern, code, body)
		}
		bodies.WriteString(body)
	}
	// Die Daten kommen wirklich an.
	all := bodies.String()
	for _, want := range []string{"Gast-Server", "iperf.example.org", "512.5", "192.0.2.1", `"available"`} {
		if !strings.Contains(all, want) {
			t.Errorf("Gastantworten enthalten %q nicht", want)
		}
	}
	// Keine Rohausgabe, keine lokalen Pfade, keine Benutzer- oder Sitzungsdaten.
	for _, secret := range []string{rawMarker, "kein-iperf3", `"path"`, "install_command", auth.DefaultAdminEmail, "access_token", "csrf", "hashed"} {
		if strings.Contains(all, secret) {
			t.Errorf("Gastantworten enthalten %q", secret)
		}
	}

	// Angemeldete Benutzer sehen weiter alles.
	if _, body := guestGet(t, f.srv, "/api/tests/"+itoa(f.testID), f.admin); !strings.Contains(body, rawMarker) {
		t.Errorf("Admin: Rohausgabe fehlt: %s", body)
	}
	if _, body := guestGet(t, f.srv, "/api/status", f.admin); !strings.Contains(body, "kein-iperf3") {
		t.Errorf("Admin: iperf3-Pfad fehlt: %s", body)
	}

	// Nur angemeldet: Benutzer, Datenbank-Statistik, eigenes Konto.
	for p := range userOnlyGet {
		if strings.Contains(p, "live-trace") {
			continue
		}
		expectStatus(t, f.srv, "GET", f.path(p), "", "", http.StatusUnauthorized)
	}

	// Jede ändernde Route weist Gäste ab – ohne Token, mit ungültigem Token
	// und mit ungültigem Sitzungs-Cookie.
	for _, r := range apiRoutes(t) {
		if r.method == "GET" || publicWrite[r.method+" "+r.pattern] {
			continue
		}
		p := f.path(r.pattern)
		expectStatus(t, f.srv, r.method, p, "", `{}`, http.StatusUnauthorized)
		expectStatus(t, f.srv, r.method, p, "kaputt", `{}`, http.StatusUnauthorized)
		if code, _ := cookieRequest(t, f.srv, r.method, p, "kaputt"); code != http.StatusUnauthorized {
			t.Errorf("%s %s mit ungültigem Cookie: Status %d", r.method, p, code)
		}
	}

	// Nichts wurde verändert.
	var servers []model.Server
	var tests []model.Test
	call(t, f.srv, "GET", "/api/servers", f.admin, "", &servers)
	call(t, f.srv, "GET", "/api/tests", f.admin, "", &tests)
	if len(servers) != 1 || len(tests) != 1 {
		t.Errorf("nach Gastzugriffen: %d Server, %d Tests", len(servers), len(tests))
	}
	var traces []model.Trace
	if call(t, f.srv, "GET", "/api/traces", f.admin, "", &traces); len(traces) != 1 {
		t.Errorf("nach Gastzugriffen: %d Traces", len(traces))
	}
}

// TestGuestInvalidSession: Eine ungültige oder abgelaufene Sitzung gilt beim
// Lesen als Gast; Schreiben bleibt gesperrt, CSRF gilt weiter.
func TestGuestInvalidSession(t *testing.T) {
	f := newGuestFixture(t, true)
	now := time.Now()
	expired := signToken(t, "admin", "abgelaufen", now.Add(-2*time.Hour), now.Add(-time.Hour))

	expectStatus(t, f.srv, "GET", "/api/servers", "kaputt", "", http.StatusOK)
	if code, _ := cookieRequest(t, f.srv, "GET", "/api/servers", expired); code != http.StatusOK {
		t.Errorf("abgelaufenes Cookie beim Lesen: Status %d", code)
	}
	if code, _ := cookieRequest(t, f.srv, "POST", "/api/servers", expired); code != http.StatusUnauthorized {
		t.Errorf("abgelaufenes Cookie beim Schreiben: Status %d", code)
	}
	// Gast-Rohausgabe auch mit ungültigem Token nicht sichtbar.
	if _, body := guestGet(t, f.srv, "/api/tests/"+itoa(f.testID), "kaputt"); strings.Contains(body, rawMarker) {
		t.Error("ungültiges Token erhält die Rohausgabe")
	}
	// /auth/me meldet Gästen weiter 401 (daran erkennt die Oberfläche den Gast).
	expectStatus(t, f.srv, "GET", "/api/auth/me", "kaputt", "", http.StatusUnauthorized)

	// Cookie-Sitzung ohne CSRF-Token: Schreiben weiter 403, Lesen als Benutzer.
	b := newBrowser(t, f.srv)
	b.login("admin", "admin123")
	if code, _ := b.do("POST", "/api/servers", `{"name":"B","host":"b"}`, nil, nil); code != http.StatusForbidden {
		t.Errorf("ohne CSRF-Token: Status %d", code)
	}
	var detail map[string]any
	if code, _ := b.do("GET", "/api/tests/"+itoa(f.testID), "", nil, &detail); code != http.StatusOK || detail["raw_output"] == nil {
		t.Errorf("Cookie-Sitzung: %d, raw_output %v", code, detail["raw_output"])
	}

	// /auth/status meldet die Gastansicht.
	var status map[string]any
	if call(t, f.srv, "GET", "/api/auth/status", "", "", &status); status["public_read"] != true {
		t.Errorf("/auth/status: %v", status)
	}
}

// TestGuestPublicReadOff: Ohne public_read bleibt jede lesende Route gesperrt.
func TestGuestPublicReadOff(t *testing.T) {
	f := newGuestFixture(t, false)
	for _, r := range apiRoutes(t) {
		if r.method != "GET" || alwaysPublic[r.pattern] || strings.Contains(r.pattern, "live-trace") {
			continue
		}
		if code, body := guestGet(t, f.srv, f.path(r.pattern), ""); code != http.StatusUnauthorized {
			t.Errorf("ohne public_read GET %s: Status %d, %s", r.pattern, code, body)
		}
		if code, _ := guestGet(t, f.srv, f.path(r.pattern), "kaputt"); code != http.StatusUnauthorized {
			t.Errorf("ohne public_read GET %s mit ungültigem Token: Status %d", r.pattern, code)
		}
	}
}

// TestGuestLiveTrace: Die Live-Traceroute startet eine Aktion und verlangt
// auch mit public_read eine Anmeldung; es wird nichts gespeichert.
func TestGuestLiveTrace(t *testing.T) {
	for _, publicRead := range []bool{true, false} {
		f := newGuestFixture(t, publicRead)
		events := readSSE(t, f.srv, "127.0.0.1", "")
		if eventTypes(events) != "error" || events[0].Message == "" {
			t.Errorf("public_read=%v: Gast-Live-Trace: %+v", publicRead, events)
		}
		var traces []model.Trace
		if call(t, f.srv, "GET", "/api/traces", f.admin, "", &traces); len(traces) != 1 {
			t.Errorf("public_read=%v: %d Traces nach Gast-Live-Trace", publicRead, len(traces))
		}
	}
}
