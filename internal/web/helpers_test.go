package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"i-tracker/internal/auth"
	"i-tracker/internal/db"
	"i-tracker/internal/iperf"
	"i-tracker/internal/scheduler"
	"i-tracker/internal/store"
	"i-tracker/internal/trace"
)

// testSecret is the test servers' signing key.
const testSecret = "test-secret"

// newTestServer starts the API on a fresh database with the default admin.
func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	return newTestServerWithTracer(t, nil)
}

// newTestServerWithTracer takes a custom tracer; nil = no traceroute binary.
func newTestServerWithTracer(t *testing.T, tracer *trace.Tracer) *httptest.Server {
	t.Helper()
	srv, _ := newTestServerOpts(t, tracer, false)
	return srv
}

// newTestServerOpts also sets auth.public_read and returns the store.
func newTestServerOpts(t *testing.T, tracer *trace.Tracer, publicRead bool) (*httptest.Server, *store.Store) {
	t.Helper()
	conn, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })

	st := store.New(conn)
	admin, err := auth.NewAdmin("admin", auth.DefaultAdminEmail, "admin123", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateUser(context.Background(), admin); err != nil {
		t.Fatal(err)
	}

	// Missing binaries make tests and traces fail fast and deterministically;
	// the iperf and trace packages test real execution.
	runner := iperf.NewRunner(st, filepath.Join(t.TempDir(), "kein-iperf3"))
	t.Cleanup(runner.Stop)

	if tracer == nil {
		tracer = trace.NewTracer(nil, filepath.Join(t.TempDir(), "kein-traceroute"))
	}
	// Scheduler not started (as with scheduler.enabled: false).
	srv := httptest.NewServer(NewServer(Deps{
		Store:      st,
		Tokens:     auth.NewTokens(testSecret, 0),
		Runner:     runner,
		Tracer:     tracer,
		Scheduler:  scheduler.New(st, runner, tracer),
		PublicRead: publicRead,
	}).Handler())
	t.Cleanup(srv.Close)
	return srv, st
}

// call sends a request, decodes the body into out (if non-nil) and returns the status.
func call(t *testing.T, srv *httptest.Server, method, path, token, body string, out any) int {
	t.Helper()
	req, err := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil && resp.StatusCode != http.StatusNoContent {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			t.Fatalf("%s %s: unreadable response: %v", method, path, err)
		}
	}
	return resp.StatusCode
}

func login(t *testing.T, srv *httptest.Server, username, password string) string {
	t.Helper()
	var out map[string]any
	code := call(t, srv, "POST", "/api/auth/login", "", `{"username":"`+username+`","password":"`+password+`"}`, &out)
	if code != http.StatusOK {
		t.Fatalf("login %s: status %d, %v", username, code, out)
	}
	if out["token_type"] != "bearer" {
		t.Fatalf("token_type = %v", out["token_type"])
	}
	return out["access_token"].(string)
}

// expectStatus checks only the status code.
func expectStatus(t *testing.T, srv *httptest.Server, method, path, token, body string, want int) {
	t.Helper()
	if got := call(t, srv, method, path, token, body, nil); got != want {
		t.Errorf("%s %s: status %d, want %d", method, path, got, want)
	}
}
