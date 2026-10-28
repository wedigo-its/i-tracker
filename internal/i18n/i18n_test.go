// i18n_test.go
// Tests for request languages, keyed errors and stored messages
// Version: 2026.10.28

package i18n

import (
	"errors"
	"fmt"
	"net/http/httptest"
	"testing"
)

func TestFromRequest(t *testing.T) {
	cases := []struct{ url, header, want string }{
		{"/", "", DE},
		{"/", "en-US,en;q=0.9", EN},
		{"/", "de-DE,de;q=0.9,en;q=0.8", DE},
		{"/", "fr", EN},
		{"/?lang=en", "de", EN},
		{"/?lang=de", "en", DE},
	}
	for _, c := range cases {
		r := httptest.NewRequest("GET", c.url, nil)
		if c.header != "" {
			r.Header.Set("Accept-Language", c.header)
		}
		if got := FromRequest(r); got != c.want {
			t.Errorf("%s %q: %s, want %s", c.url, c.header, got, c.want)
		}
	}
}

// Error() is English; ErrorIn renders either language, nested included, foreign text as is.
func TestErrorIn(t *testing.T) {
	notInstalled := E("err.iperf.not_installed")
	cases := []struct {
		err    error
		en, de string
	}{
		{E("err.server.not_found"), "Server not found", "Server nicht gefunden"},
		{E("err.server.id_not_found", 7), "Server 7 not found", "Server 7 nicht gefunden"},
		{E("err.param.range", "limit", 1, 1000), `Parameter "limit" must be a number between 1 and 1000`, `Parameter "limit" muss eine Zahl zwischen 1 und 1000 sein`},
		{Wrap(E("err.install.no_winget"), "err.install.failed"),
			"Installation failed: winget is not available – please install iperf3 manually",
			"Installation fehlgeschlagen: winget ist nicht verfügbar – iperf3 bitte manuell installieren"},
		{E("err.with_reason", notInstalled, E("err.install.no_package_manager")),
			"iperf3 not installed – no supported package manager found (apt-get, dnf, yum, zypper, apk, pacman)",
			"iperf3 nicht installiert – kein unterstützter Paketmanager gefunden (apt-get, dnf, yum, zypper, apk, pacman)"},
		{Wrap(errors.New("exec: not found"), "err.trace.tool_start", "tracert"),
			"tracert could not be started: exec: not found", "tracert konnte nicht gestartet werden: exec: not found"},
		{Foreign("unable to connect to server: Connection refused"),
			"unable to connect to server: Connection refused", "unable to connect to server: Connection refused"},
		// Untranslated context around a keyed error: shown as it is.
		{fmt.Errorf("context: %w", notInstalled), "context: iperf3 not installed", "context: iperf3 not installed"},
	}
	for _, c := range cases {
		if got := c.err.Error(); got != c.en {
			t.Errorf("Error() = %q, want %q", got, c.en)
		}
		if got := ErrorIn(EN, c.err); got != c.en {
			t.Errorf("ErrorIn(en) = %q, want %q", got, c.en)
		}
		if got := ErrorIn(DE, c.err); got != c.de {
			t.Errorf("ErrorIn(de) = %q, want %q", got, c.de)
		}
	}
	if !errors.Is(fmt.Errorf("x: %w", notInstalled), notInstalled) {
		t.Error("errors.Is does not see a wrapped *Error")
	}
	if KeyOf(Wrap(errors.New("x"), "err.iperf.start")) != "err.iperf.start" || KeyOf(errors.New("x")) != "" {
		t.Error("KeyOf")
	}
}

// Stored messages render in both languages; old German rows are recognized, stale/unknown fall back.
func TestLocalize(t *testing.T) {
	stored := Wrap(Foreign("lookup x.invalid: no such host"), "err.trace.resolve")
	enc, english := MsgOf(stored).Encode(), stored.Error()
	if got := Localize(DE, enc, english); got != "Ziel konnte nicht aufgelöst werden: lookup x.invalid: no such host" {
		t.Errorf("de: %q", got)
	}
	if got := Localize(EN, enc, english); got != english {
		t.Errorf("en: %q", got)
	}
	// Stale: English column changed by a version unaware of error_text.
	if got := Localize(DE, enc, "something else"); got != "something else" {
		t.Errorf("stale: %q", got)
	}
	// Unknown key (written by a newer version): the English text.
	if got := Localize(DE, `{"k":"err.from_the_future"}`, "Future"); got != "Future" {
		t.Errorf("unknown key: %q", got)
	}
	if MsgOf(Foreign("x")).Encode() != "" {
		t.Error("foreign text needs no stored message")
	}
	// StoredKey names the key of the message Localize renders.
	for _, c := range []struct{ enc, english, key string }{
		{enc, english, "err.trace.resolve"},
		{enc, "something else", ""},
		{`{"k":"err.from_the_future"}`, "Future", ""},
		{"", "Abgebrochen: Dienst wurde beendet", "err.test.aborted_stop"},
		{"", "unable to connect to server: Connection refused", ""},
	} {
		if got := StoredKey(c.enc, c.english); got != c.key {
			t.Errorf("StoredKey(%q, %q) = %q, want %q", c.enc, c.english, got, c.key)
		}
	}

	legacy := []struct{ stored, en, de string }{
		{"Abgebrochen: Dienst wurde neu gestartet", "Aborted: the service was restarted", "Abgebrochen: Dienst wurde neu gestartet"},
		{"Zeitüberschreitung nach 1m30s", "Timed out after 1m30s", "Zeitüberschreitung nach 1m30s"},
		{"iperf3 lieferte keine Ergebnisse für die Richtung upload", "iperf3 returned no results for direction upload", "iperf3 lieferte keine Ergebnisse für die Richtung upload"},
		{"Ziel konnte nicht aufgelöst werden: nur IPv4 wird unterstützt", "Could not resolve destination: only IPv4 is supported", "Ziel konnte nicht aufgelöst werden: nur IPv4 wird unterstützt"},
		{"tracert konnte nicht gestartet werden: exec: not found", "tracert could not be started: exec: not found", "tracert konnte nicht gestartet werden: exec: not found"},
		{"unable to connect to server: Connection refused", "unable to connect to server: Connection refused", "unable to connect to server: Connection refused"},
	}
	for _, l := range legacy {
		if got := Localize(EN, "", l.stored); got != l.en {
			t.Errorf("legacy %q in en: %q, want %q", l.stored, got, l.en)
		}
		if got := Localize(DE, "", l.stored); got != l.de {
			t.Errorf("legacy %q in de: %q, want %q", l.stored, got, l.de)
		}
	}
	if LocalizePtr(DE, "", nil) != nil {
		t.Error("LocalizePtr(nil)")
	}
}

// Every legacy pattern names a key of the catalogs.
func TestLegacyKeysExist(t *testing.T) {
	for _, l := range legacyMessages {
		for _, lang := range Languages() {
			if _, ok := catalogs[lang][l.key]; !ok {
				t.Errorf("legacy %q: key %s missing in %s", l.de, l.key, lang)
			}
		}
	}
}
