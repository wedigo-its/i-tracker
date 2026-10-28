package auth

import (
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

func TestArgon2id(t *testing.T) {
	h, err := HashPassword("geheim")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$v=19$m=65536,t=3,p=4$") {
		t.Fatalf("hash format: %s", h)
	}
	if !CheckPassword(h, "geheim") || CheckPassword(h, "Geheim") || CheckPassword(h, "") {
		t.Error("check wrong")
	}
	if NeedsRehash(h) {
		t.Error("fresh hash must not need a rehash")
	}
	h2, _ := HashPassword("geheim")
	if h == h2 {
		t.Error("salt missing")
	}
	for _, broken := range []string{"$argon2id$", "$argon2id$v=19$m=1,t=1,p=1$!!$!!", "$argon2id$v=18$m=65536,t=3,p=4$AAAA$AAAA"} {
		if CheckPassword(broken, "geheim") {
			t.Errorf("%q accepted", broken)
		}
	}
}

func TestBcryptStillAccepted(t *testing.T) {
	b, err := bcrypt.GenerateFromPassword([]byte("admin123"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	if !CheckPassword(string(b), "admin123") || CheckPassword(string(b), "falsch") {
		t.Error("bcrypt check wrong")
	}
	if !NeedsRehash(string(b)) {
		t.Error("bcrypt hash must need a rehash")
	}
}

func TestGeneratePassword(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		p := GeneratePassword()
		if len(p) != 18 || strings.ContainsAny(p, "0O1lI") || seen[p] {
			t.Fatalf("password %q", p)
		}
		seen[p] = true
	}
}

func TestCSRF(t *testing.T) {
	tok := NewTokens("s", 0)
	a, b := tok.CSRF("sitzung-a"), tok.CSRF("sitzung-b")
	if a == b || !tok.CheckCSRF("sitzung-a", a) || tok.CheckCSRF("sitzung-a", b) || tok.CheckCSRF("sitzung-a", "") {
		t.Error("CSRF check wrong")
	}
	if NewTokens("anders", 0).CheckCSRF("sitzung-a", a) {
		t.Error("CSRF token independent of key")
	}
}

func TestDummyHash(t *testing.T) {
	h := DummyHash()
	if !strings.HasPrefix(h, "$argon2id$") || NeedsRehash(h) || h != DummyHash() {
		t.Fatalf("DummyHash: %q", h)
	}
	if CheckPassword(h, "") || CheckPassword(h, "admin123") {
		t.Error("DummyHash accepts a password")
	}
}

func TestTokensSliding(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	tok := NewTokens("s", 2*time.Hour)
	tok.now = func() time.Time { return now }
	if NewTokens("s", 0).TTL() != DefaultSessionTTL {
		t.Error("default TTL missing")
	}

	s, c, err := tok.Create("admin", 3)
	if err != nil || c.SessionID == "" || c.Version != 3 || !c.ExpiresAt.Equal(now.Add(2*time.Hour)) {
		t.Fatalf("Create: %+v %v", c, err)
	}
	_, c2, _ := tok.Create("admin", 3)
	if c2.SessionID == c.SessionID {
		t.Error("session ID not random")
	}
	got, err := tok.Parse(s)
	if err != nil || got.Username != c.Username || got.Version != c.Version || got.SessionID != c.SessionID ||
		!got.IssuedAt.Equal(c.IssuedAt) || !got.ExpiresAt.Equal(c.ExpiresAt) {
		t.Fatalf("Parse: %+v %v, want %+v", got, err, c)
	}
	if tok.NeedsRenewal(got) {
		t.Error("fresh token must not need renewal")
	}

	// Past half the lifetime: renew, same session.
	now = now.Add(time.Hour + time.Second)
	if !tok.NeedsRenewal(got) {
		t.Error("token past half lifetime not due for renewal")
	}
	s2, r, err := tok.Renew(got)
	if err != nil || r.SessionID != c.SessionID || r.Version != 3 || !r.ExpiresAt.After(c.ExpiresAt) {
		t.Fatalf("Renew: %+v %v", r, err)
	}
	if tok.CSRF(r.SessionID) != tok.CSRF(c.SessionID) {
		t.Error("CSRF token changes on renewal")
	}

	// The old token expires, the renewed one stays valid.
	now = c.ExpiresAt.Add(time.Second)
	if _, err := tok.Parse(s); err == nil {
		t.Error("expired token accepted")
	}
	if _, err := tok.Parse(s2); err != nil {
		t.Errorf("renewed token: %v", err)
	}
	if _, err := NewTokens("anders", 0).Parse(s2); err == nil {
		t.Error("foreign key accepted")
	}
}

func TestLimiter(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	l := NewLimiter(3, 5*time.Minute)
	l.now = func() time.Time { return now }
	for i := 0; i < 3; i++ {
		if l.Blocked("a") {
			t.Fatalf("blocked after %d failures", i)
		}
		l.Fail("a")
		now = now.Add(time.Minute)
	}
	if !l.Blocked("a") || l.Blocked("b") {
		t.Error("per-address block wrong")
	}
	// The first failure expires after the window.
	now = now.Add(2*time.Minute + time.Second)
	if l.Blocked("a") {
		t.Error("still blocked after the window")
	}
}

func TestRevocations(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	r := NewRevocations()
	r.now = func() time.Time { return now }
	if !r.Revoke("x", now.Add(time.Hour)) || !r.Revoked("x") || r.Revoked("y") {
		t.Fatal("revocation wrong")
	}
	// An earlier end does not shorten an existing revocation.
	r.Revoke("x", now.Add(time.Minute))
	now = now.Add(30 * time.Minute)
	if !r.Revoked("x") {
		t.Error("revocation shortened")
	}
	now = now.Add(31 * time.Minute)
	if r.Revoked("x") {
		t.Error("revocation still active after expiry")
	}
	r.Revoke("z", now.Add(time.Hour))
	if _, ok := r.until["x"]; ok {
		t.Error("expired entry not pruned")
	}
}
