// legacy.go
// Recognizes the German messages stored by versions before 2026.10.28
// Version: 2026.10.28

package i18n

import (
	"regexp"
	"strings"
)

// Until 2026.10.28 i-Tracker stored the last error of a test or trace as
// German text. legacyMessages maps those texts to today's catalog keys, so
// old rows are shown in the viewer's language too. The German patterns are
// stored data, not texts of this version: the guard test allows this file.
//
// In a pattern, %s is an argument shown as it is (a host, a duration, a
// program name), %v an embedded message that is recognized in turn. The
// first matching pattern wins.
var legacyMessages = []struct{ de, key string }{
	// Tests
	{"Abgebrochen: Dienst wurde neu gestartet", "err.test.aborted_restart"},
	{"Abgebrochen: Dienst wurde beendet", "err.test.aborted_stop"},
	{"Server nicht gefunden", "err.server.not_found"},
	{"iperf3 nicht gefunden – bitte installieren oder iperf.path konfigurieren", "err.iperf.not_found"},
	{"iperf3 lieferte keine Ergebnisse für die Richtung %s", "err.iperf.no_results_direction"},
	{"iperf3 lieferte keine Ergebnisse", "err.iperf.no_results"},
	{"iperf3 konnte nicht gestartet werden: %s", "err.iperf.start"},
	{"iperf3 beendet mit Fehler: %s", "err.iperf.exit"},
	{"Zeitüberschreitung nach %s", "err.timeout"},

	// Traces
	{"Ziel konnte nicht aufgelöst werden: %v", "err.trace.resolve"},
	{"nur IPv4 wird unterstützt", "err.trace.ipv4_only"},
	{"Abgebrochen", "err.trace.aborted"},
	{"%s nicht gefunden – bitte installieren", "err.trace.tool_missing"},
	{"%s konnte nicht gestartet werden: %s", "err.trace.tool_start"},
	{"%s beendet mit Fehler: %s", "err.trace.tool_exit"},
}

type legacyPattern struct {
	re     *regexp.Regexp
	key    string
	nested []bool // per argument: an embedded message (%v)
}

var legacyPatterns = compileLegacy()

func compileLegacy() []legacyPattern {
	verb := regexp.MustCompile(`%[sv]`)
	out := make([]legacyPattern, 0, len(legacyMessages))
	for _, l := range legacyMessages {
		var re strings.Builder
		var nested []bool
		re.WriteString("^")
		rest := l.de
		for {
			loc := verb.FindStringIndex(rest)
			if loc == nil {
				re.WriteString(regexp.QuoteMeta(rest))
				break
			}
			re.WriteString(regexp.QuoteMeta(rest[:loc[0]]))
			re.WriteString(`(.+?)`)
			nested = append(nested, rest[loc[0]+1] == 'v')
			rest = rest[loc[1]:]
		}
		re.WriteString("$")
		out = append(out, legacyPattern{regexp.MustCompile(re.String()), l.key, nested})
	}
	return out
}

// legacyMsg returns the message of a German text stored by an older version.
func legacyMsg(s string) (Msg, bool) {
	if s == "" {
		return Msg{}, false
	}
	for _, p := range legacyPatterns {
		m := p.re.FindStringSubmatch(s)
		if m == nil {
			continue
		}
		msg := Msg{Key: p.key}
		for i, a := range m[1:] {
			arg := Raw(a)
			if p.nested[i] {
				if inner, ok := legacyMsg(a); ok {
					arg = inner
				}
			}
			msg.Args = append(msg.Args, arg)
		}
		return msg, true
	}
	return Msg{}, false
}
