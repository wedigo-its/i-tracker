package web

import (
	"net/http"
	"strings"
	"testing"

	"i-tracker/internal/auth"
)

func TestAuthFlow(t *testing.T) {
	srv := newTestServer(t)

	expectStatus(t, srv, "POST", "/api/auth/login", "", `{"username":"admin","password":"falsch"}`, http.StatusUnauthorized)
	expectStatus(t, srv, "GET", "/api/auth/me", "", "", http.StatusUnauthorized)

	admin := login(t, srv, "admin", "admin123")
	var me map[string]any
	if code := call(t, srv, "GET", "/api/auth/me", admin, "", &me); code != http.StatusOK ||
		me["username"] != "admin" || me["is_admin"] != true || me["last_login"] == nil {
		t.Fatalf("/me: status %d, %v", code, me)
	}
	if _, ok := me["token_version"]; ok {
		t.Errorf("/me leaks the token version: %v", me)
	}

	var bob map[string]any
	if code := call(t, srv, "POST", "/api/auth/register", admin, `{"username":"bob","email":"bob@example.org","password":"geheimPasswort1"}`, &bob); code != http.StatusOK ||
		bob["is_admin"] != false || bob["last_login"] != nil {
		t.Fatalf("register: status %d, %v", code, bob)
	}
	expectStatus(t, srv, "POST", "/api/auth/register", admin, `{"username":"bob","email":"x@example.org","password":"geheimPasswort1"}`, http.StatusBadRequest)
	expectStatus(t, srv, "POST", "/api/auth/register", admin, `{"username":"al","email":"al@example.org","password":"geheimPasswort1"}`, http.StatusUnprocessableEntity)

	user := login(t, srv, "bob", "geheimPasswort1")
	expectStatus(t, srv, "GET", "/api/auth/users", user, "", http.StatusForbidden)

	var users []map[string]any
	if code := call(t, srv, "GET", "/api/auth/users", admin, "", &users); code != http.StatusOK || len(users) != 2 {
		t.Fatalf("user list: status %d, %d entries", code, len(users))
	}

	expectStatus(t, srv, "DELETE", "/api/auth/users/1", admin, "", http.StatusBadRequest)
	expectStatus(t, srv, "DELETE", "/api/auth/users/99", admin, "", http.StatusNotFound)
	expectStatus(t, srv, "DELETE", "/api/auth/users/2", admin, "", http.StatusOK)
	expectStatus(t, srv, "GET", "/api/auth/me", user, "", http.StatusUnauthorized)
}

// TestInitAdminRemoved: EnsureAdmin creates the first admin at startup; the old public endpoint is gone.
func TestInitAdminRemoved(t *testing.T) {
	srv, _ := newServerWithStore(t)
	expectStatus(t, srv, "POST", "/api/auth/init-admin", "", "", http.StatusNotFound)
}

func TestForeignTokenRejected(t *testing.T) {
	srv := newTestServer(t)
	foreign, _, err := auth.NewTokens("anderer-schluessel", 0).Create("admin", 0)
	if err != nil {
		t.Fatal(err)
	}
	expectStatus(t, srv, "GET", "/api/auth/me", foreign, "", http.StatusUnauthorized)
}

// TestPasswordMinLength: new passwords need 8 characters (not bytes; ten until
// 2026-10-21); existing passwords stay valid.
func TestPasswordMinLength(t *testing.T) {
	srv := newTestServer(t)
	admin := login(t, srv, "admin", "admin123")

	var out map[string]any
	if code := call(t, srv, "POST", "/api/auth/register", admin, `{"username":"sieben","email":"s@example.org","password":"1234567"}`, &out); code != http.StatusUnprocessableEntity ||
		out["detail"] != "Passwort muss mindestens 8 Zeichen lang sein" {
		t.Errorf("7 chars: status %d, %v", code, out)
	}
	expectStatus(t, srv, "POST", "/api/auth/register", admin, `{"username":"acht","email":"a@example.org","password":"12345678"}`, http.StatusOK)
	// 8 umlauts exceed 8 bytes but are valid; 7 are not.
	expectStatus(t, srv, "POST", "/api/auth/register", admin, `{"username":"umlaut7","email":"u7@example.org","password":"äöüäöüä"}`, http.StatusUnprocessableEntity)
	expectStatus(t, srv, "POST", "/api/auth/register", admin, `{"username":"umlaut8","email":"u8@example.org","password":"äöüäöüäö"}`, http.StatusOK)

	expectStatus(t, srv, "POST", "/api/auth/change-password", admin, `{"current_password":"admin123","new_password":"1234567"}`, http.StatusUnprocessableEntity)
	expectStatus(t, srv, "POST", "/api/auth/change-password", admin, `{"current_password":"admin123","new_password":"`+strings.Repeat("x", 257)+`"}`, http.StatusUnprocessableEntity)
	expectStatus(t, srv, "POST", "/api/auth/change-password", admin, `{"current_password":"admin123","new_password":"12345678"}`, http.StatusOK)
}

// TestChangePasswordEndsOtherSessions: old tokens of the user die, the new token
// from the response works, other users stay signed in.
func TestChangePasswordEndsOtherSessions(t *testing.T) {
	srv := newTestServer(t)
	tok := login(t, srv, "admin", "admin123")
	other := login(t, srv, "admin", "admin123")
	expectStatus(t, srv, "POST", "/api/auth/register", tok, `{"username":"bob","email":"bob@example.org","password":"geheimPasswort1"}`, http.StatusOK)
	bob := login(t, srv, "bob", "geheimPasswort1")

	expectStatus(t, srv, "POST", "/api/auth/change-password", "", `{}`, http.StatusUnauthorized)
	expectStatus(t, srv, "POST", "/api/auth/change-password", tok, `{"current_password":"falsch","new_password":"neuesPasswort"}`, http.StatusBadRequest)
	expectStatus(t, srv, "POST", "/api/auth/change-password", tok, `{"current_password":"admin123","new_password":"kurz"}`, http.StatusUnprocessableEntity)
	// Failed attempts end no session.
	expectStatus(t, srv, "GET", "/api/auth/me", other, "", http.StatusOK)

	var out map[string]any
	if code := call(t, srv, "POST", "/api/auth/change-password", tok, `{"current_password":"admin123","new_password":"neuesPasswort"}`, &out); code != http.StatusOK {
		t.Fatalf("change-password: %d %v", code, out)
	}
	fresh, _ := out["access_token"].(string)
	if fresh == "" || fresh == tok || out["csrf_token"] == "" {
		t.Fatalf("response without new token: %v", out)
	}

	expectStatus(t, srv, "GET", "/api/auth/me", tok, "", http.StatusUnauthorized)
	expectStatus(t, srv, "GET", "/api/auth/me", other, "", http.StatusUnauthorized)
	expectStatus(t, srv, "GET", "/api/auth/me", fresh, "", http.StatusOK)
	expectStatus(t, srv, "GET", "/api/auth/me", bob, "", http.StatusOK)

	expectStatus(t, srv, "POST", "/api/auth/login", "", `{"username":"admin","password":"admin123"}`, http.StatusUnauthorized)
	login(t, srv, "admin", "neuesPasswort")
}

// TestLogoutRevokesBearer: logout revokes Bearer tokens too; the user's other sessions survive.
func TestLogoutRevokesBearer(t *testing.T) {
	srv := newTestServer(t)
	tok := login(t, srv, "admin", "admin123")
	other := login(t, srv, "admin", "admin123")

	expectStatus(t, srv, "POST", "/api/auth/logout", tok, "", http.StatusNoContent)
	expectStatus(t, srv, "GET", "/api/auth/me", tok, "", http.StatusUnauthorized)
	expectStatus(t, srv, "GET", "/api/servers", tok, "", http.StatusUnauthorized)
	expectStatus(t, srv, "GET", "/api/auth/me", other, "", http.StatusOK)
	// Logout without or with an invalid token is harmless.
	expectStatus(t, srv, "POST", "/api/auth/logout", "", "", http.StatusNoContent)
	expectStatus(t, srv, "POST", "/api/auth/logout", "kein-token", "", http.StatusNoContent)
}

// TestLogoutAll ends all of the user's sessions, not those of others.
func TestLogoutAll(t *testing.T) {
	srv := newTestServer(t)
	tok := login(t, srv, "admin", "admin123")
	other := login(t, srv, "admin", "admin123")
	expectStatus(t, srv, "POST", "/api/auth/register", tok, `{"username":"bob","email":"bob@example.org","password":"geheimPasswort1"}`, http.StatusOK)
	bob := login(t, srv, "bob", "geheimPasswort1")

	expectStatus(t, srv, "POST", "/api/auth/logout-all", "", "", http.StatusUnauthorized)
	expectStatus(t, srv, "POST", "/api/auth/logout-all", tok, "", http.StatusNoContent)
	expectStatus(t, srv, "GET", "/api/auth/me", tok, "", http.StatusUnauthorized)
	expectStatus(t, srv, "GET", "/api/auth/me", other, "", http.StatusUnauthorized)
	expectStatus(t, srv, "GET", "/api/auth/me", bob, "", http.StatusOK)
	// A new sign-in works again.
	expectStatus(t, srv, "GET", "/api/auth/me", login(t, srv, "admin", "admin123"), "", http.StatusOK)
}

// TestLoginRateLimit: after 10 failures per address every sign-in gets 429,
// even with the right password or a spoofed X-Forwarded-For.
func TestLoginRateLimit(t *testing.T) {
	srv := newTestServer(t)
	for i := 0; i < auth.LoginMaxFailures; i++ {
		expectStatus(t, srv, "POST", "/api/auth/login", "", `{"username":"admin","password":"falsch"}`, http.StatusUnauthorized)
	}
	req, _ := http.NewRequest("POST", srv.URL+"/api/auth/login", strings.NewReader(`{"username":"admin","password":"admin123"}`))
	req.Header.Set("X-Forwarded-For", "203.0.113.7")
	req.Header.Set("Accept-Language", "en")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests || resp.Header.Get("Retry-After") == "" {
		t.Fatalf("after %d failures: status %d, Retry-After %q", auth.LoginMaxFailures, resp.StatusCode, resp.Header.Get("Retry-After"))
	}
	var out map[string]any
	if code := call(t, srv, "POST", "/api/auth/login", "", `{"username":"unbekannt","password":"x"}`, &out); code != http.StatusTooManyRequests ||
		out["detail"] != "Zu viele Fehlversuche – bitte später erneut versuchen" {
		t.Errorf("blocked: status %d, %v", code, out)
	}
}

// TestChangePasswordRateLimit: wrong current passwords count as failures and then block change and login.
func TestChangePasswordRateLimit(t *testing.T) {
	srv := newTestServer(t)
	tok := login(t, srv, "admin", "admin123")
	for i := 0; i < auth.LoginMaxFailures; i++ {
		expectStatus(t, srv, "POST", "/api/auth/change-password", tok, `{"current_password":"falsch","new_password":"neuesPasswort"}`, http.StatusBadRequest)
	}
	expectStatus(t, srv, "POST", "/api/auth/change-password", tok, `{"current_password":"admin123","new_password":"neuesPasswort"}`, http.StatusTooManyRequests)
	expectStatus(t, srv, "POST", "/api/auth/login", "", `{"username":"admin","password":"admin123"}`, http.StatusTooManyRequests)
	// The existing session stays usable.
	expectStatus(t, srv, "GET", "/api/auth/me", tok, "", http.StatusOK)
}

// TestLoginUnknownUserChecksHash: unknown users still cost one current-parameter Argon2id check (equal timing).
func TestLoginUnknownUserChecksHash(t *testing.T) {
	srv := newTestServer(t)
	var checked []string
	orig := checkPassword
	checkPassword = func(hash, password string) bool {
		checked = append(checked, hash)
		return orig(hash, password)
	}
	t.Cleanup(func() { checkPassword = orig })

	expectStatus(t, srv, "POST", "/api/auth/login", "", `{"username":"niemand","password":"admin123"}`, http.StatusUnauthorized)
	if len(checked) != 1 || checked[0] != auth.DummyHash() ||
		!strings.HasPrefix(checked[0], "$argon2id$") || auth.NeedsRehash(checked[0]) {
		t.Fatalf("unknown user: checked hashes %q", checked)
	}
	// Cross-check: existing user, wrong password: exactly one check against its hash.
	checked = nil
	expectStatus(t, srv, "POST", "/api/auth/login", "", `{"username":"admin","password":"falsch"}`, http.StatusUnauthorized)
	if len(checked) != 1 || checked[0] == auth.DummyHash() || !strings.HasPrefix(checked[0], "$argon2id$") {
		t.Fatalf("existing user: checked hashes %q", checked)
	}
}
