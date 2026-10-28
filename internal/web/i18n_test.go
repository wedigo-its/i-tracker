// i18n_test.go
// Messages of the API follow the language of the request: errors, messages, stored errors, public servers
// Version: 2026.10.28

package web

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"i-tracker/internal/i18n"
	"i-tracker/internal/model"
)

// request sends a request with an optional token, JSON body and
// Accept-Language and decodes the JSON answer.
func request(t *testing.T, url, method, path, token, body, lang string, out any) (int, http.Header) {
	t.Helper()
	req, _ := http.NewRequest(method, url+path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if lang != "" {
		req.Header.Set("Accept-Language", lang)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if out != nil {
		json.NewDecoder(res.Body).Decode(out)
	}
	return res.StatusCode, res.Header
}

// TestLocalizedErrors: error responses carry the message in the request
// language (German without Accept-Language, as before) and its catalog key.
func TestLocalizedErrors(t *testing.T) {
	srv := newTestServer(t)
	tok := login(t, srv, "admin", "admin123")

	cases := []struct {
		method, path, token, body, lang, want, code string
	}{
		{"POST", "/api/auth/login", "", `{"username":"admin","password":"falsch"}`, "", "Benutzername oder Passwort falsch", "err.auth.wrong_credentials"},
		{"POST", "/api/auth/login", "", `{"username":"admin","password":"falsch"}`, "en-US,en;q=0.9", "Wrong username or password", "err.auth.wrong_credentials"},
		{"POST", "/api/auth/login", "", `{"username":"admin","password":"falsch"}`, "de-DE", "Benutzername oder Passwort falsch", "err.auth.wrong_credentials"},
		{"GET", "/api/servers/99", tok, "", "en", "Server not found", "err.server.not_found"},
		{"GET", "/api/servers/abc", tok, "", "de", `Ungültiger Parameter "server_id"`, "err.param.invalid"},
		{"GET", "/api/tests?limit=0", tok, "", "en", `Parameter "limit" must be a number between 1 and 1000`, "err.param.range"},
		{"GET", "/api/tests?limit=0", tok, "", "de", `Parameter "limit" muss eine Zahl zwischen 1 und 1000 sein`, "err.param.range"},
		{"GET", "/api/tests?status=x", tok, "", "en", `Parameter "status" must be pending, running, completed or failed`, "err.param.status"},
		{"POST", "/api/servers", tok, `{"name":"x","host":"h","port":0}`, "en", "Port must be between 1 and 65535", "err.input.port"},
		{"POST", "/api/servers", tok, `{"name":"x","host":"h","port":0}`, "de", "Port muss zwischen 1 und 65535 liegen", "err.input.port"},
		{"POST", "/api/servers", tok, `{"name":"x","host":"h","default_udp_bandwidth_mbps":0}`, "de", "UDP-Bandbreite muss größer als 0 und höchstens 100000 Mbit/s sein", "err.input.udp_bandwidth"},
		{"POST", "/api/tests/run", tok, `{"server_id":99}`, "en", "Server 99 not found", "err.server.id_not_found"},
		{"POST", "/api/tests/run", tok, `{"server_id":99}`, "de", "Server 99 nicht gefunden", "err.server.id_not_found"},
		{"POST", "/api/tests/run", tok, `{"server_id":1,"duration":0}`, "de", "Testdauer muss zwischen 1 und 300 Sekunden liegen", "err.input.duration"},
		{"POST", "/api/traces", tok, `{"destination":""}`, "en", "Destination must be 1–255 characters long", "err.trace.destination_length"},
		{"POST", "/api/auth/register", tok, `{"username":"neu","email":"neu@example.net","password":"kurz"}`, "de", "Passwort muss mindestens 8 Zeichen lang sein", "err.auth.password_short"},
		{"DELETE", "/api/admin/cleanup/tests", tok, "", "en", "Please specify days, server_id or all=true", "err.cleanup.tests_scope"},
		{"DELETE", "/api/admin/cleanup/tests?days=-1", tok, "", "de", `Parameter "days" darf nicht negativ sein`, "err.param.negative"},
		{"GET", "/api/public-servers/search", "", "", "en", `Parameter "query" is missing`, "err.param.missing"},
		{"GET", "/api/servers", "", "", "en", "Could not validate credentials", "err.auth.unauthorized"},
	}
	for _, c := range cases {
		var out map[string]any
		_, hdr := request(t, srv.URL, c.method, c.path, c.token, c.body, c.lang, &out)
		if out["detail"] != c.want {
			t.Errorf("%s %s [%s]: detail %q, want %q", c.method, c.path, c.lang, out["detail"], c.want)
		}
		if out["code"] != c.code {
			t.Errorf("%s %s [%s]: code %q, want %q", c.method, c.path, c.lang, out["code"], c.code)
		}
		if want := map[bool]string{true: "en", false: "de"}[strings.HasPrefix(c.lang, "en")]; hdr.Get("Content-Language") != want {
			t.Errorf("%s %s [%s]: Content-Language %q", c.method, c.path, c.lang, hdr.Get("Content-Language"))
		}
	}

	// A body that is no JSON: the key frames the parser's own (English) text.
	var out map[string]any
	request(t, srv.URL, "POST", "/api/servers", tok, `{`, "de", &out)
	if d, _ := out["detail"].(string); !strings.HasPrefix(d, "Ungültiger Request-Body: ") || out["code"] != "err.invalid_body" {
		t.Errorf("invalid body: %v", out)
	}

	// Messages of successful actions.
	out = nil
	request(t, srv.URL, "DELETE", "/api/admin/cleanup/tests?all=true", tok, "", "en", &out)
	if out["message"] != "0 test(s) deleted" {
		t.Errorf("cleanup message: %v", out["message"])
	}
	out = nil
	request(t, srv.URL, "DELETE", "/api/admin/cleanup/traces?all=true", tok, "", "de", &out)
	if out["message"] != "0 Trace(s) und 0 Hop(s) gelöscht" {
		t.Errorf("cleanup message: %v", out["message"])
	}
}

// TestErrorCodesInCatalog: every code an error response names is a catalog key.
func TestErrorCodesInCatalog(t *testing.T) {
	srv := newTestServer(t)
	tok := login(t, srv, "admin", "admin123")
	for _, p := range []string{"/api/servers/1", "/api/tests/1", "/api/traces/1", "/api/tests/1/trace", "/api/stats/servers/1", "/api/tests/x/live"} {
		var out map[string]any
		code, _ := request(t, srv.URL, "GET", p, tok, "", "en", &out)
		key, _ := out["code"].(string)
		if code < 400 || key == "" {
			t.Errorf("GET %s: status %d, code %q", p, code, key)
			continue
		}
		if _, ok := i18n.Lookup(i18n.DE, key); !ok {
			t.Errorf("GET %s: code %q is no catalog key", p, key)
		}
	}
}

// TestStoredErrorsLocalized: the stored error of a test or trace is shown in
// the request language, for new rows (with error_text) and for rows of older
// versions that hold German text only.
func TestStoredErrorsLocalized(t *testing.T) {
	srv, st := newTestServerOpts(t, nil, false)
	tok := login(t, srv, "admin", "admin123")
	ctx := context.Background()

	sv := &model.Server{ServerSettings: model.DefaultServerSettings()}
	sv.Name, sv.Host = "lokal", "127.0.0.1"
	if err := st.CreateServer(ctx, sv); err != nil {
		t.Fatal(err)
	}
	newTest := func(failure error) int64 {
		tt := model.NewTestFromDefaults(sv)
		if err := st.CreateTest(ctx, tt); err != nil {
			t.Fatal(err)
		}
		if err := st.FinishTest(ctx, tt.ID, model.StatusFailed, tt.CreatedAt, model.TestResult{}, failure, nil); err != nil {
			t.Fatal(err)
		}
		return tt.ID
	}
	keyed := newTest(i18n.E("err.iperf.no_results_direction", "upload"))
	// An older version stored the German text without error_text.
	legacy := newTest(i18n.Foreign("Zeitüberschreitung nach 40s"))
	foreign := newTest(i18n.Foreign("unable to connect to server: Connection refused"))

	cases := []struct {
		id     int64
		en, de string
	}{
		{keyed, "iperf3 returned no results for direction upload", "iperf3 lieferte keine Ergebnisse für die Richtung upload"},
		{legacy, "Timed out after 40s", "Zeitüberschreitung nach 40s"},
		{foreign, "unable to connect to server: Connection refused", "unable to connect to server: Connection refused"},
	}
	for _, c := range cases {
		for lang, want := range map[string]string{"en": c.en, "de": c.de} {
			var detail map[string]any
			request(t, srv.URL, "GET", "/api/tests/"+strconv.FormatInt(c.id, 10), tok, "", lang, &detail)
			if detail["error_message"] != want {
				t.Errorf("test %d [%s]: %q, want %q", c.id, lang, detail["error_message"], want)
			}
			var list []map[string]any
			request(t, srv.URL, "GET", "/api/tests?status=failed", tok, "", lang, &list)
			for _, x := range list {
				if int64(x["id"].(float64)) == c.id && x["error_message"] != want {
					t.Errorf("list, test %d [%s]: %q, want %q", c.id, lang, x["error_message"], want)
				}
			}
		}
	}

	tr := &model.Trace{DestinationHost: "x.invalid", Hops: []model.TraceHop{}}
	tr.SetError(i18n.Wrap(i18n.Foreign("lookup x.invalid: no such host"), "err.trace.resolve"))
	if err := st.CreateTrace(ctx, tr); err != nil {
		t.Fatal(err)
	}
	for lang, want := range map[string]string{
		"en": "Could not resolve destination: lookup x.invalid: no such host",
		"de": "Ziel konnte nicht aufgelöst werden: lookup x.invalid: no such host",
	} {
		var out map[string]any
		request(t, srv.URL, "GET", "/api/traces/"+strconv.FormatInt(tr.ID, 10), tok, "", lang, &out)
		if out["error_message"] != want {
			t.Errorf("trace [%s]: %q, want %q", lang, out["error_message"], want)
		}
		var list []map[string]any
		request(t, srv.URL, "GET", "/api/traces", tok, "", lang, &list)
		if len(list) != 1 || list[0]["error_message"] != want {
			t.Errorf("trace list [%s]: %v", lang, list)
		}
	}
}

// TestPublicServersLocalized: location, description and translated names
// follow the request language; search finds both languages.
func TestPublicServersLocalized(t *testing.T) {
	srv := newTestServer(t)
	for lang, want := range map[string][2]string{"en": {"Paris, France", "Public iperf3 server in Paris"}, "de": {"Paris, Frankreich", "Öffentlicher iperf3-Server in Paris"}} {
		var public []map[string]any
		request(t, srv.URL, "GET", "/api/public-servers", "", "", lang, &public)
		if len(public) < 10 || public[0]["location"] != want[0] || public[0]["description"] != want[1] {
			t.Errorf("[%s] first entry: %v", lang, public[0])
		}
		for _, p := range public {
			for _, f := range []string{"name", "location", "description"} {
				if lang == "en" && strings.ContainsAny(p[f].(string), "äöüÖ") || strings.HasPrefix(p[f].(string), "public.") {
					t.Errorf("[%s] %s not translated: %q", lang, f, p[f])
				}
			}
		}
	}
	for _, q := range []string{"schweiz", "switzerland"} {
		var found []map[string]any
		request(t, srv.URL, "GET", "/api/public-servers/search?query="+q, "", "", "en", &found)
		if len(found) != 1 || found[0]["name"] != "Init7 Switzerland" {
			t.Errorf("search %q: %v", q, found)
		}
	}
}
