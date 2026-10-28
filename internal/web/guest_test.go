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

// Guest view (auth.public_read) classes: every GET route must be in exactly one
// list so new routes get classified deliberately.
var (
	// alwaysPublic needs no sign-in even without public_read.
	alwaysPublic = map[string]bool{
		"/api/info":                  true,
		"/api/auth/status":           true,
		"/api/public-servers":        true,
		"/api/public-servers/search": true,
	}
	// guestReadable are the guest view's read endpoints.
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
	// userOnlyGet needs sign-in even with public_read.
	userOnlyGet = map[string]bool{
		"/api/auth/me":              true,
		"/api/auth/users":           true,
		"/api/admin/stats/database": true,
		"/api/update":               true,
		// Starts a trace (own test, errors as SSE events).
		"/api/live-trace/stream/{destination}": true,
	}
	// publicWrite are mutating routes that must work without sign-in.
	publicWrite = map[string]bool{
		"POST /api/auth/login":  true,
		"POST /api/auth/logout": true,
	}
)

type route struct{ method, pattern string }

// apiRoutes returns all registered API routes (Routes).
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
		t.Fatalf("only %d routes registered", len(out))
	}
	return out
}

// TestRouteAccessMatchesGuestLists checks the registered access levels (used in the product docs) against these lists.
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
			t.Errorf("%s: access %q does not match class %q", r.Pattern, r.Access, want)
		}
	}
}

// guestFixture holds a server, a test with raw output and a trace.
type guestFixture struct {
	srv                     *httptest.Server
	st                      *store.Store
	admin                   string
	serverID, testID, trace int64
}

// rawMarker is in the raw output and must never reach guests.
const rawMarker = "rohausgabe-nur-fuer-angemeldete"

func newGuestFixture(t *testing.T, publicRead bool) guestFixture {
	t.Helper()
	srv, st := newTestServerOpts(t, nil, publicRead)
	f := guestFixture{srv: srv, st: st, admin: login(t, srv, "admin", "admin123")}
	ctx := context.Background()

	var sv model.Server
	if code := call(t, srv, "POST", "/api/servers", f.admin, `{"name":"Gast-Server","host":"iperf.example.org"}`, &sv); code != http.StatusCreated {
		t.Fatalf("create server: %d", code)
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

// path fills in a route's path parameters.
func (f guestFixture) path(pattern string) string {
	return strings.NewReplacer(
		"{server_id}", itoa(f.serverID), "{test_id}", itoa(f.testID), "{trace_id}", itoa(f.trace),
		"{user_id}", "1", "{destination}", "127.0.0.1",
	).Replace(pattern)
}

// guestGet GETs path (token optional) and returns status and body; SSE streams return the status only.
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

// TestGuestRoutesClassified: every GET route is classified for the guest view.
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
			t.Errorf("GET %s classified %d times (alwaysPublic, guestReadable or userOnlyGet)", r.pattern, n)
		}
	}
}

// TestGuestReadOnly: with public_read guests read data without secrets; every mutation needs sign-in.
func TestGuestReadOnly(t *testing.T) {
	f := newGuestFixture(t, true)

	var bodies strings.Builder
	for _, r := range apiRoutes(t) {
		if r.method != "GET" || !guestReadable[r.pattern] {
			continue
		}
		code, body := guestGet(t, f.srv, f.path(r.pattern), "")
		if code != http.StatusOK {
			t.Errorf("guest GET %s: status %d, %s", r.pattern, code, body)
		}
		bodies.WriteString(body)
	}
	// The data really arrives.
	all := bodies.String()
	for _, want := range []string{"Gast-Server", "iperf.example.org", "512.5", "192.0.2.1", `"available"`} {
		if !strings.Contains(all, want) {
			t.Errorf("guest responses lack %q", want)
		}
	}
	// No raw output, local paths, user or session data.
	for _, secret := range []string{rawMarker, "kein-iperf3", `"path"`, "install_command", auth.DefaultAdminEmail, "access_token", "csrf", "hashed"} {
		if strings.Contains(all, secret) {
			t.Errorf("guest responses contain %q", secret)
		}
	}

	// Signed-in users still see everything.
	if _, body := guestGet(t, f.srv, "/api/tests/"+itoa(f.testID), f.admin); !strings.Contains(body, rawMarker) {
		t.Errorf("admin: raw output missing: %s", body)
	}
	if _, body := guestGet(t, f.srv, "/api/status", f.admin); !strings.Contains(body, "kein-iperf3") {
		t.Errorf("admin: iperf3 path missing: %s", body)
	}

	// Sign-in only: users, database stats, own account.
	for p := range userOnlyGet {
		if strings.Contains(p, "live-trace") {
			continue
		}
		expectStatus(t, f.srv, "GET", f.path(p), "", "", http.StatusUnauthorized)
	}

	// Every mutating route rejects guests: no token, invalid token, invalid cookie.
	for _, r := range apiRoutes(t) {
		if r.method == "GET" || publicWrite[r.method+" "+r.pattern] {
			continue
		}
		p := f.path(r.pattern)
		expectStatus(t, f.srv, r.method, p, "", `{}`, http.StatusUnauthorized)
		expectStatus(t, f.srv, r.method, p, "kaputt", `{}`, http.StatusUnauthorized)
		if code, _ := cookieRequest(t, f.srv, r.method, p, "kaputt"); code != http.StatusUnauthorized {
			t.Errorf("%s %s with invalid cookie: status %d", r.method, p, code)
		}
	}

	// Nothing changed.
	var servers []model.Server
	var tests []model.Test
	call(t, f.srv, "GET", "/api/servers", f.admin, "", &servers)
	call(t, f.srv, "GET", "/api/tests", f.admin, "", &tests)
	if len(servers) != 1 || len(tests) != 1 {
		t.Errorf("after guest access: %d servers, %d tests", len(servers), len(tests))
	}
	var traces []model.Trace
	if call(t, f.srv, "GET", "/api/traces", f.admin, "", &traces); len(traces) != 1 {
		t.Errorf("after guest access: %d traces", len(traces))
	}
}

// TestGuestInvalidSession: invalid or expired sessions read as guests; writes stay blocked, CSRF still applies.
func TestGuestInvalidSession(t *testing.T) {
	f := newGuestFixture(t, true)
	now := time.Now()
	expired := signToken(t, "admin", "abgelaufen", now.Add(-2*time.Hour), now.Add(-time.Hour))

	expectStatus(t, f.srv, "GET", "/api/servers", "kaputt", "", http.StatusOK)
	if code, _ := cookieRequest(t, f.srv, "GET", "/api/servers", expired); code != http.StatusOK {
		t.Errorf("expired cookie on read: status %d", code)
	}
	if code, _ := cookieRequest(t, f.srv, "POST", "/api/servers", expired); code != http.StatusUnauthorized {
		t.Errorf("expired cookie on write: status %d", code)
	}
	// No raw output with an invalid token either.
	if _, body := guestGet(t, f.srv, "/api/tests/"+itoa(f.testID), "kaputt"); strings.Contains(body, rawMarker) {
		t.Error("invalid token gets the raw output")
	}
	// /auth/me still answers 401 to guests (the UI detects guests by it).
	expectStatus(t, f.srv, "GET", "/api/auth/me", "kaputt", "", http.StatusUnauthorized)

	// Cookie session without CSRF token: writes 403, reads as the user.
	b := newBrowser(t, f.srv)
	b.login("admin", "admin123")
	if code, _ := b.do("POST", "/api/servers", `{"name":"B","host":"b"}`, nil, nil); code != http.StatusForbidden {
		t.Errorf("without CSRF token: status %d", code)
	}
	var detail map[string]any
	if code, _ := b.do("GET", "/api/tests/"+itoa(f.testID), "", nil, &detail); code != http.StatusOK || detail["raw_output"] == nil {
		t.Errorf("cookie session: %d, raw_output %v", code, detail["raw_output"])
	}

	// /auth/status reports the guest view.
	var status map[string]any
	if call(t, f.srv, "GET", "/api/auth/status", "", "", &status); status["public_read"] != true {
		t.Errorf("/auth/status: %v", status)
	}
}

// TestGuestPublicReadOff: without public_read every read route stays closed.
func TestGuestPublicReadOff(t *testing.T) {
	f := newGuestFixture(t, false)
	for _, r := range apiRoutes(t) {
		if r.method != "GET" || alwaysPublic[r.pattern] || strings.Contains(r.pattern, "live-trace") {
			continue
		}
		if code, body := guestGet(t, f.srv, f.path(r.pattern), ""); code != http.StatusUnauthorized {
			t.Errorf("without public_read GET %s: status %d, %s", r.pattern, code, body)
		}
		if code, _ := guestGet(t, f.srv, f.path(r.pattern), "kaputt"); code != http.StatusUnauthorized {
			t.Errorf("without public_read GET %s with invalid token: status %d", r.pattern, code)
		}
	}
}

// TestGuestLiveTrace: the live trace is an action, so it needs sign-in even with public_read; nothing is saved.
func TestGuestLiveTrace(t *testing.T) {
	for _, publicRead := range []bool{true, false} {
		f := newGuestFixture(t, publicRead)
		events := readSSE(t, f.srv, "127.0.0.1", "")
		if eventTypes(events) != "error" || events[0].Message == "" {
			t.Errorf("public_read=%v: guest live trace: %+v", publicRead, events)
		}
		var traces []model.Trace
		if call(t, f.srv, "GET", "/api/traces", f.admin, "", &traces); len(traces) != 1 {
			t.Errorf("public_read=%v: %d traces after guest live trace", publicRead, len(traces))
		}
	}
}
