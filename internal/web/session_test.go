package web

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"

	"i-tracker/internal/auth"
	"i-tracker/internal/db"
	"i-tracker/internal/iperf"
	"i-tracker/internal/model"
	"i-tracker/internal/scheduler"
	"i-tracker/internal/store"
	"i-tracker/internal/trace"
)

// browser is an HTTP client with a cookie jar, like the UI.
type browser struct {
	t    *testing.T
	srv  *httptest.Server
	c    *http.Client
	csrf string
}

func newBrowser(t *testing.T, srv *httptest.Server) *browser {
	jar, _ := cookiejar.New(nil)
	return &browser{t: t, srv: srv, c: &http.Client{Jar: jar}}
}

func (b *browser) do(method, path, body string, header map[string]string, out any) (int, http.Header) {
	b.t.Helper()
	req, err := http.NewRequest(method, b.srv.URL+path, strings.NewReader(body))
	if err != nil {
		b.t.Fatal(err)
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := b.c.Do(req)
	if err != nil {
		b.t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil && resp.StatusCode != http.StatusNoContent {
		json.NewDecoder(resp.Body).Decode(out)
	}
	return resp.StatusCode, resp.Header
}

func (b *browser) login(user, pass string) map[string]any {
	b.t.Helper()
	var out map[string]any
	code, hdr := b.do("POST", "/api/auth/login", `{"username":"`+user+`","password":"`+pass+`"}`, nil, &out)
	if code != http.StatusOK {
		b.t.Fatalf("login: %d %v", code, out)
	}
	sc := hdr.Get("Set-Cookie")
	if !strings.Contains(sc, sessionCookie+"=") || !strings.Contains(sc, "HttpOnly") || !strings.Contains(sc, "SameSite=Lax") || strings.Contains(sc, "Secure") {
		b.t.Errorf("Set-Cookie: %s", sc)
	}
	b.csrf, _ = out["csrf_token"].(string)
	if b.csrf == "" {
		b.t.Fatal("csrf_token missing")
	}
	return out
}

func TestCookieSessionAndCSRF(t *testing.T) {
	srv := newTestServer(t)
	b := newBrowser(t, srv)
	b.login("admin", "admin123")

	// Reading needs only the cookie; /auth/me returns the CSRF token in a header.
	var me map[string]any
	code, hdr := b.do("GET", "/api/auth/me", "", nil, &me)
	if code != http.StatusOK || me["username"] != "admin" || hdr.Get(csrfHeader) != b.csrf {
		t.Fatalf("/me: %d %v csrf=%q", code, me, hdr.Get(csrfHeader))
	}
	// Mutation without or with a wrong CSRF token: 403.
	body := `{"name":"S1","host":"h1"}`
	if code, _ := b.do("POST", "/api/servers", body, nil, nil); code != http.StatusForbidden {
		t.Errorf("without CSRF: %d", code)
	}
	if code, _ := b.do("POST", "/api/servers", body, map[string]string{csrfHeader: "falsch"}, nil); code != http.StatusForbidden {
		t.Errorf("wrong CSRF: %d", code)
	}
	if code, _ := b.do("POST", "/api/servers", body, map[string]string{csrfHeader: b.csrf}, nil); code != http.StatusCreated && code != http.StatusOK {
		t.Errorf("with CSRF: %d", code)
	}
	// Cross-origin requests are rejected, login included.
	if code, _ := b.do("POST", "/api/servers", `{"name":"S2","host":"h2"}`, map[string]string{csrfHeader: b.csrf, "Sec-Fetch-Site": "cross-site"}, nil); code != http.StatusForbidden {
		t.Errorf("cross-site: %d", code)
	}
	if code, _ := b.do("POST", "/api/auth/login", `{"username":"admin","password":"admin123"}`, map[string]string{"Origin": "https://evil.example"}, nil); code != http.StatusForbidden {
		t.Errorf("login from foreign origin: %d", code)
	}

	// Logout clears the cookie and revokes the token, so a copied cookie dies too.
	stolen := b.cookie()
	if code, _ := b.do("POST", "/api/auth/logout", "", nil, nil); code != http.StatusNoContent {
		t.Errorf("logout: %d", code)
	}
	if code, _ := b.do("GET", "/api/auth/me", "", nil, nil); code != http.StatusUnauthorized {
		t.Errorf("after logout: %d", code)
	}
	if code, _ := cookieRequest(t, srv, "GET", "/api/auth/me", stolen); code != http.StatusUnauthorized {
		t.Errorf("copied cookie after logout: %d", code)
	}
}

// cookie returns the session cookie value from the jar.
func (b *browser) cookie() string {
	b.t.Helper()
	u, _ := url.Parse(b.srv.URL)
	for _, c := range b.c.Jar.Cookies(u) {
		if c.Name == sessionCookie {
			return c.Value
		}
	}
	b.t.Fatal("no session cookie")
	return ""
}

// cookieRequest sends a request with token as session cookie (no jar) and returns status and headers.
func cookieRequest(t *testing.T, srv *httptest.Server, method, path, token string) (int, http.Header) {
	t.Helper()
	req, _ := http.NewRequest(method, srv.URL+path, nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode, resp.Header
}

// signToken signs a session token like the server, with custom times (renewal, expiry).
func signToken(t *testing.T, user, sessionID string, iat, exp time.Time) string {
	t.Helper()
	tok, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub": user, "ver": 0, "jti": sessionID, "iat": iat.Unix(), "exp": exp.Unix(),
	}).SignedString([]byte(testSecret))
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

// TestSlidingSession: past half its lifetime an active cookie is renewed in
// the same session (same CSRF token); fresh cookies and Bearer tokens are not,
// expired ones are rejected.
func TestSlidingSession(t *testing.T) {
	srv := newTestServer(t)
	ttl := auth.DefaultSessionTTL
	now := time.Now()
	old := signToken(t, "admin", "sitzung-alt", now.Add(-ttl/2-time.Minute), now.Add(ttl/2-time.Minute))

	code, hdr := cookieRequest(t, srv, "GET", "/api/auth/me", old)
	if code != http.StatusOK {
		t.Fatalf("/me with older cookie: %d", code)
	}
	var renewed *http.Cookie
	for _, c := range (&http.Response{Header: hdr}).Cookies() {
		if c.Name == sessionCookie {
			renewed = c
		}
	}
	if renewed == nil || renewed.Value == old || renewed.MaxAge != int(ttl/time.Second) || !renewed.HttpOnly {
		t.Fatalf("not renewed: %v", hdr.Values("Set-Cookie"))
	}
	claims, err := auth.NewTokens(testSecret, 0).Parse(renewed.Value)
	if err != nil || claims.SessionID != "sitzung-alt" || claims.ExpiresAt.Before(now.Add(ttl-time.Minute)) {
		t.Fatalf("renewed token: %+v %v", claims, err)
	}
	csrf := auth.NewTokens(testSecret, 0).CSRF("sitzung-alt")
	if hdr.Get(csrfHeader) != csrf {
		t.Errorf("CSRF token changed on renewal: %q", hdr.Get(csrfHeader))
	}
	// The renewed cookie works and, being fresh, is not renewed again.
	if code, hdr := cookieRequest(t, srv, "GET", "/api/auth/me", renewed.Value); code != http.StatusOK || hdr.Get("Set-Cookie") != "" {
		t.Errorf("fresh cookie: %d, Set-Cookie %q", code, hdr.Get("Set-Cookie"))
	}
	// Bearer tokens keep their fixed lifetime.
	req, _ := http.NewRequest("GET", srv.URL+"/api/auth/me", nil)
	req.Header.Set("Authorization", "Bearer "+old)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Set-Cookie") != "" {
		t.Errorf("Bearer: %d, Set-Cookie %q", resp.StatusCode, resp.Header.Get("Set-Cookie"))
	}
	// No activity within the lifetime ends the session.
	expired := signToken(t, "admin", "sitzung-abgelaufen", now.Add(-ttl-time.Minute), now.Add(-time.Minute))
	if code, _ := cookieRequest(t, srv, "GET", "/api/auth/me", expired); code != http.StatusUnauthorized {
		t.Errorf("expired cookie: %d", code)
	}
	// Old-version tokens without a session ID are invalid.
	if code, _ := cookieRequest(t, srv, "GET", "/api/auth/me", signToken(t, "admin", "", now, now.Add(time.Hour))); code != http.StatusUnauthorized {
		t.Errorf("token without jti: %d", code)
	}
}

// TestCookiePasswordChangeKeepsSession: after a password change the browser
// session continues with a new cookie and the same CSRF token; a copied cookie dies.
func TestCookiePasswordChangeKeepsSession(t *testing.T) {
	srv := newTestServer(t)
	b := newBrowser(t, srv)
	b.login("admin", "admin123")
	before := b.cookie()
	code, _ := b.do("POST", "/api/auth/change-password", `{"current_password":"admin123","new_password":"neuesPasswort"}`, map[string]string{csrfHeader: b.csrf}, nil)
	if code != http.StatusOK {
		t.Fatalf("change-password: %d", code)
	}
	if b.cookie() == before {
		t.Fatal("cookie not renewed")
	}
	if code, _ := b.do("POST", "/api/servers", `{"name":"S1","host":"h1"}`, map[string]string{csrfHeader: b.csrf}, nil); code != http.StatusCreated && code != http.StatusOK {
		t.Errorf("after password change with previous CSRF token: %d", code)
	}
	if code, _ := cookieRequest(t, srv, "GET", "/api/auth/me", before); code != http.StatusUnauthorized {
		t.Errorf("old cookie after password change: %d", code)
	}
}

func TestSecureCookieBehindTLSProxy(t *testing.T) {
	srv := newTestServer(t)
	b := newBrowser(t, srv)
	_, hdr := b.do("POST", "/api/auth/login", `{"username":"admin","password":"admin123"}`, map[string]string{"X-Forwarded-Proto": "https"}, nil)
	if sc := hdr.Get("Set-Cookie"); !strings.HasPrefix(sc, sessionCookieSecure+"=") || !strings.Contains(sc, "Secure") {
		t.Errorf("Set-Cookie under TLS: %s", sc)
	}
}

func TestSecurityHeaders(t *testing.T) {
	srv := newTestServer(t)
	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	csp := resp.Header.Get("Content-Security-Policy")
	for _, want := range []string{"default-src 'self'", "script-src 'self'", "style-src 'self'", "frame-ancestors 'none'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP without %q: %s", want, csp)
		}
	}
	if strings.Contains(csp, "unsafe-inline") || strings.Contains(csp, "http") {
		t.Errorf("CSP too open: %s", csp)
	}
	if resp.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Error("nosniff missing")
	}
}

// newServerWithStore starts a server on an empty database.
func newServerWithStore(t *testing.T) (*httptest.Server, *store.Store) {
	t.Helper()
	conn, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	st := store.New(conn)
	runner := iperf.NewRunner(st, filepath.Join(t.TempDir(), "kein-iperf3"))
	t.Cleanup(runner.Stop)
	tracer := trace.NewTracer(nil, filepath.Join(t.TempDir(), "kein-traceroute"))
	srv := httptest.NewServer(NewServer(Deps{
		Store: st, Tokens: auth.NewTokens(testSecret, 0), Runner: runner, Tracer: tracer,
		Scheduler: scheduler.New(st, runner, tracer),
	}).Handler())
	t.Cleanup(srv.Close)
	return srv, st
}

func TestFirstAdminMustChangePassword(t *testing.T) {
	srv, st := newServerWithStore(t)
	var password string
	InitialPasswordHook = func(_, p string) { password = p }
	t.Cleanup(func() { InitialPasswordHook = nil })

	created, err := EnsureAdmin(context.Background(), st)
	if err != nil || !created || len(password) < 16 {
		t.Fatalf("EnsureAdmin: %v %v %q", created, err, password)
	}
	if again, _ := EnsureAdmin(context.Background(), st); again {
		t.Error("second admin created")
	}
	expectStatus(t, srv, "POST", "/api/auth/login", "", `{"username":"admin","password":"admin123"}`, http.StatusUnauthorized)

	tok := login(t, srv, "admin", password)
	var me map[string]any
	if call(t, srv, "GET", "/api/auth/me", tok, "", &me); me["must_change_password"] != true {
		t.Fatalf("/me: %v", me)
	}
	// Everything but /me and password change is blocked.
	expectStatus(t, srv, "GET", "/api/servers", tok, "", http.StatusForbidden)
	expectStatus(t, srv, "POST", "/api/auth/change-password", tok, `{"current_password":"`+password+`","new_password":"`+password+`"}`, http.StatusUnprocessableEntity)
	var changed map[string]any
	if code := call(t, srv, "POST", "/api/auth/change-password", tok, `{"current_password":"`+password+`","new_password":"neuesPasswort"}`, &changed); code != http.StatusOK {
		t.Fatalf("change-password: %d %v", code, changed)
	}
	// The initial-password token is dead; the session continues with the new one.
	expectStatus(t, srv, "GET", "/api/servers", tok, "", http.StatusUnauthorized)
	expectStatus(t, srv, "GET", "/api/servers", changed["access_token"].(string), "", http.StatusOK)
}

func TestLegacyBcryptRehashAndDefaultPassword(t *testing.T) {
	srv, st := newServerWithStore(t)
	ctx := context.Background()
	h, _ := bcrypt.GenerateFromPassword([]byte("admin123"), bcrypt.MinCost)
	if err := st.CreateUser(ctx, &model.User{Username: "admin", Email: "a@x", HashedPassword: string(h), IsActive: true, IsAdmin: true}); err != nil {
		t.Fatal(err)
	}
	if err := FlagLegacyDefaultPassword(ctx, st); err != nil {
		t.Fatal(err)
	}
	tok := login(t, srv, "admin", "admin123")
	u, _ := st.UserByUsername(ctx, "admin")
	if !strings.HasPrefix(u.HashedPassword, "$argon2id$") || !u.MustChangePassword {
		t.Errorf("after login: hash=%.12s must=%v", u.HashedPassword, u.MustChangePassword)
	}
	expectStatus(t, srv, "GET", "/api/tests", tok, "", http.StatusForbidden)
	// The rehashed password still works.
	login(t, srv, "admin", "admin123")
}

// TestAuthStatusInitialPending: public /api/auth/status reports initial_pending
// only until the generated first-start password is changed, never again after.
func TestAuthStatusInitialPending(t *testing.T) {
	status := func(srv *httptest.Server) map[string]any {
		t.Helper()
		var out map[string]any
		if code := call(t, srv, "GET", "/api/auth/status", "", "", &out); code != http.StatusOK {
			t.Fatalf("/api/auth/status: status %d, %v", code, out)
		}
		if out["auth_enabled"] != true || out["public_read"] != false || len(out) != 3 {
			t.Fatalf("/api/auth/status: %v", out)
		}
		return out
	}
	ctx := context.Background()

	srv, st := newServerWithStore(t)
	if got := status(srv)["initial_pending"]; got != false {
		t.Errorf("no users: initial_pending = %v", got)
	}
	var password string
	InitialPasswordHook = func(_, p string) { password = p }
	t.Cleanup(func() { InitialPasswordHook = nil })
	if created, err := EnsureAdmin(ctx, st); err != nil || !created {
		t.Fatalf("EnsureAdmin: %v %v", created, err)
	}
	if got := status(srv)["initial_pending"]; got != true {
		t.Errorf("after first start: initial_pending = %v", got)
	}
	// Signing in changes nothing; only the password change does.
	tok := login(t, srv, "admin", password)
	if got := status(srv)["initial_pending"]; got != true {
		t.Errorf("after sign-in: initial_pending = %v", got)
	}
	expectStatus(t, srv, "POST", "/api/auth/change-password", tok, `{"current_password":"`+password+`","new_password":"neuesPasswort"}`, http.StatusOK)
	if got := status(srv)["initial_pending"]; got != false {
		t.Errorf("after password change: initial_pending = %v", got)
	}
	// Stays off after a restart (FlagLegacyDefaultPassword) too.
	if err := FlagLegacyDefaultPassword(ctx, st); err != nil {
		t.Fatal(err)
	}
	if got := status(srv)["initial_pending"]; got != false {
		t.Errorf("after restart: initial_pending = %v", got)
	}

	// Legacy default password: change forced, but no generated password.
	legacySrv, legacySt := newServerWithStore(t)
	h, _ := bcrypt.GenerateFromPassword([]byte(auth.LegacyDefaultPassword), bcrypt.MinCost)
	if err := legacySt.CreateUser(ctx, &model.User{Username: "admin", Email: "a@x", HashedPassword: string(h), IsActive: true, IsAdmin: true}); err != nil {
		t.Fatal(err)
	}
	if err := FlagLegacyDefaultPassword(ctx, legacySt); err != nil {
		t.Fatal(err)
	}
	if got := status(legacySrv)["initial_pending"]; got != false {
		t.Errorf("legacy default password: initial_pending = %v", got)
	}
	// Unchanged after rehashing to Argon2id.
	login(t, legacySrv, "admin", auth.LegacyDefaultPassword)
	if got := status(legacySrv)["initial_pending"]; got != false {
		t.Errorf("legacy default password after rehash: initial_pending = %v", got)
	}

	// Existing admin without forced change.
	if got := status(newTestServer(t))["initial_pending"]; got != false {
		t.Errorf("existing admin: initial_pending = %v", got)
	}
}

func TestLiveTestsStream(t *testing.T) {
	srv := newTestServer(t)
	tok := login(t, srv, "admin", "admin123")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/api/tests/live/stream", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("Content-Type %q", resp.Header.Get("Content-Type"))
	}
	sc := bufio.NewScanner(resp.Body)
	next := func() map[string]any {
		t.Helper()
		for sc.Scan() {
			if data, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
				var ev map[string]any
				if err := json.Unmarshal([]byte(data), &ev); err != nil {
					t.Fatal(err)
				}
				return ev
			}
		}
		t.Fatalf("stream ended: %v", sc.Err())
		return nil
	}
	if ev := next(); ev["type"] != "live" || len(ev["tests"].([]any)) != 0 {
		t.Fatalf("first event: %v", ev)
	}

	// A test shows up in the stream (fails at once without iperf3, stays
	// briefly visible as failed).
	var sv map[string]any
	call(t, srv, "POST", "/api/servers", tok, `{"name":"S","host":"127.0.0.1"}`, &sv)
	var test map[string]any
	if code := call(t, srv, "POST", "/api/tests/run", tok, `{"server_id":`+itoa(int64(sv["id"].(float64)))+`}`, &test); code != http.StatusCreated {
		t.Fatalf("run: %d %v", code, test)
	}
	for {
		ev := next()
		tests := ev["tests"].([]any)
		if len(tests) == 0 {
			continue
		}
		lt := tests[0].(map[string]any)
		if lt["test_id"] != test["id"] || lt["server_name"] != "S" {
			t.Fatalf("entry: %v", lt)
		}
		if lt["status"] == "failed" {
			break
		}
	}
}
