// ui_i18n_test.go
// Guard tests: every text the web UI shows comes from i18n.js in German and English
// Version: 2026.10.28

package web

import (
	"fmt"
	"html"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

// What these tests keep complete, and what to do when one fails:
//
//   - TestI18nEntries: every entry in STRINGS has German and English, no key
//     twice, the same {placeholders} in both, and German = English only when
//     every word is one that reads the same in both languages (sameWords,
//     techWords). Fix: write the missing translation.
//   - TestI18nKeysUsed: every key the page uses (t('…'), data-i18n…) exists,
//     and every key in i18n.js is used somewhere (literally or through a
//     dynamic prefix such as t('dir.' + d)). Fix: add the entry, or remove
//     the key that is no longer used.
//   - TestNoUntranslatedText: no visible text in index.html and app.js
//     bypasses i18n.js: text nodes and title/placeholder/aria-label/alt in
//     markup need data-i18n…, string literals that read like language need
//     t('key'). Fix: add a key and use it. Only values that are never shown
//     as such belong into literalAllowed or techWords, each with a reason.
//     It also fails on broken markup in index.html (a tag without '>', an
//     unclosed <svg>/<script>/<style>/data-i18n element, a stray closing
//     tag), which would hide text from the scanner. Fix: close the element.
//
// Messages from the server (errors, stored test and trace errors) come
// rendered in the UI language (Accept-Language, ?lang=); internal/i18n keeps
// their catalogs complete.

var uiPages = []string{"index.html"}
var uiScripts = []string{"app.js"}

// ---------- Allowlists ----------

// techWords read the same in German and English; text made only of them (plus
// numbers and punctuation) needs no translation.
var techWords = map[string]string{}

func init() {
	for reason, words := range map[string]string{
		"product and brand names":                  "wedigo Tracker iperf iPerf Chart Leaflet DB IP City Lite MaxMind GeoLite GeoIP OpenStreetMap Natural Earth winget Homebrew",
		"protocols and units":                      "TCP UDP IPv4 IPv6 RTT ASN AS ms Mbit Gbit MB GB KB JSON API URL HTTP HTTPS CPU CSRF",
		"technical terms used as such in German":   "ID LIVE Hop Hops Jitter Streams Peering Traceroute Retransmits Build",
		"words that are the same in German":        "Download Upload Test Server Traces Admin",
		"language buttons show the language codes": "DE EN",
	} {
		for _, w := range strings.Fields(words) {
			techWords[w] = reason
		}
	}
}

// sameWords are German words that are also English; an i18n.js entry whose
// words are all here or in techWords may read the same in both languages.
var sameWords = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`Dashboard Tests Test Server Admin Administrator Status OK Details Download
		Upload Trace Traces Name Host Port Info Version Scheduler Live Map Hops Hop Jitter Streams Peering
		Traceroute Retransmits Download Hostname Min Max Start`) {
		sameWords[w] = true
	}
}

// placeholderExtra lists keys whose English uses a value the German does not
// (or the other way round).
var placeholderExtra = map[string]string{}

// literalAllowed are exact texts the scanners find that are not shown as
// language. Keep it short; every entry needs its reason.
var literalAllowed = map[string]string{
	"Deutsch":                       "name of the German language option: each language is named in itself",
	"English":                       "name of the English language option: each language is named in itself",
	"title=Deutsch":                 "title of the DE button: each language is named in itself",
	"title=English":                 "title of the EN button: each language is named in itself",
	"placeholder=iperf.example.org": "example host name in the server form",
	"Accept-Language":               "HTTP header name",
	"Content-Type":                  "HTTP header name",
	"X-CSRF-Token":                  "HTTP header name",
	"Escape":                        "KeyboardEvent.key value",
	"? Unbekannt":                   "city stored by versions before 2026.10.28 for estimated hops, compared and shown as t('unknown') (hopCity)",
	"IP Geolocation by DB-IP":       "attribution required by the DB-IP license (CC BY 4.0), in the licensor's wording",
	"GeoLite2 data by":              "attribution required by the MaxMind GeoLite2 license, in the licensor's wording",
}

// dynamicPrefixes are key families the code builds at run time in a way the
// scanner cannot see as a literal prefix.
var dynamicPrefixes = map[string]string{}

// ---------- i18n.js ----------

// i18nEntry is one entry of STRINGS in i18n.js.
type i18nEntry struct {
	Line   int
	DE, EN string
}

// loadI18n parses the STRINGS table of static/i18n.js with the JS lexer. Every
// entry must have the shape 'key': ['de', 'en'].
func loadI18n(t *testing.T) map[string]i18nEntry {
	t.Helper()
	src := readStatic(t, "i18n.js")
	start := strings.Index(src, "const STRINGS = {")
	end := strings.Index(src, "\n};")
	if start < 0 || end < start {
		t.Fatal("i18n.js: STRINGS table not found")
	}
	firstLine := strings.Count(src[:start], "\n")
	lits, err := lexJS(src[start:end])
	if err != nil {
		t.Fatalf("i18n.js: %v", err)
	}
	// after reports whether the code before literal i ends with want (spaces ignored).
	after := func(i int, want string) bool {
		return i < len(lits) && strings.HasSuffix(strings.ReplaceAll(lits[i].Before, " ", ""), want)
	}
	entries := map[string]i18nEntry{}
	for i := 0; i < len(lits); i++ {
		key := lits[i]
		line := key.Line + firstLine
		if !(after(i, "{") || after(i, "],")) || key.Quote != '\'' {
			t.Errorf("i18n.js:%d: expected a key, got %s", line, quoteShort(key.Text))
			continue
		}
		if !after(i+1, `"":[`) || !after(i+2, `"",`) || i+3 < len(lits) && !after(i+3, "],") {
			t.Errorf("i18n.js:%d: %s is not of the form 'key': ['de', 'en']", line, key.Text)
			continue
		}
		de, en := lits[i+1], lits[i+2]
		i += 2
		if _, dup := entries[key.Text]; dup {
			t.Errorf("i18n.js:%d: duplicate key %s", line, key.Text)
			continue
		}
		entries[key.Text] = i18nEntry{Line: line, DE: de.Text, EN: en.Text}
	}
	if len(entries) < 200 {
		t.Fatalf("i18n.js: only %d entries parsed", len(entries))
	}
	return entries
}

func readStatic(t *testing.T, name string) string {
	t.Helper()
	b, err := static.ReadFile("static/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func quoteShort(s string) string {
	if utf8.RuneCountInString(s) > 70 {
		s = string([]rune(s)[:70]) + "…"
	}
	return strconv.Quote(strings.ReplaceAll(s, "\x00", "${…}"))
}

var placeholderRe = regexp.MustCompile(`\{(\w+)\}`)

func placeholders(s string) string {
	set := map[string]bool{}
	for _, m := range placeholderRe.FindAllStringSubmatch(s, -1) {
		set[m[1]] = true
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}

func TestI18nEntries(t *testing.T) {
	entries := loadI18n(t)
	keys := make([]string, 0, len(entries))
	for k := range entries {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		e := entries[k]
		where := "i18n.js:" + strconv.Itoa(e.Line) + ": " + k
		if strings.TrimSpace(e.DE) == "" || strings.TrimSpace(e.EN) == "" {
			t.Errorf("%s: German and English must not be empty", where)
			continue
		}
		if strings.Contains(e.DE+e.EN, "Ã") || strings.Contains(e.DE+e.EN, "â€") {
			t.Errorf("%s: broken encoding (%s)", where, quoteShort(e.DE))
		}
		if _, ok := placeholderExtra[k]; !ok && placeholders(e.DE) != placeholders(e.EN) {
			t.Errorf("%s: placeholders differ: de {%s}, en {%s}", where, placeholders(e.DE), placeholders(e.EN))
		}
		if e.DE == e.EN {
			for _, w := range wordRe.FindAllString(placeholderRe.ReplaceAllString(e.DE, ""), -1) {
				if _, tech := techWords[w]; !tech && !sameWords[w] {
					t.Errorf("%s: German and English are the same (%s) – translate it, or add %q to sameWords if it really reads the same", where, quoteShort(e.DE), w)
					break
				}
			}
		}
	}
	for k := range placeholderExtra {
		if _, ok := entries[k]; !ok {
			t.Errorf("placeholderExtra: %s is not in i18n.js any more", k)
		}
	}
}

// ---------- Key usage ----------

var (
	// keyLike matches i18n keys such as 'peering.hop'.
	keyLike = regexp.MustCompile(`^[a-z][a-zA-Z0-9]*(\.[a-zA-Z0-9_\p{L}-]+)+$`)
	// keyPrefix matches a dynamic key prefix such as 'dir.' (also `dir.${d}`).
	keyPrefix = regexp.MustCompile(`^([a-z][a-zA-Z0-9]*(?:\.[a-zA-Z0-9_-]+)*\.)(?:\x00|$)`)
	// i18nAttr finds data-i18n, data-i18n-title, … with a literal key.
	i18nAttr = regexp.MustCompile(`data-i18n(?:-[a-z]+)?="([^"\x00]+)"`)
)

func TestI18nKeysUsed(t *testing.T) {
	entries := loadI18n(t)
	used := map[string]bool{}
	prefixes := map[string]bool{}
	for p := range dynamicPrefixes {
		prefixes[p] = true
	}
	namespaces := map[string]bool{}
	for k := range entries {
		namespaces[strings.SplitN(k, ".", 2)[0]] = true
	}
	var missing []string
	for _, f := range append(append([]string{}, uiPages...), uiScripts...) {
		src := readStatic(t, f)
		for _, m := range i18nAttr.FindAllStringSubmatchIndex(src, -1) {
			key := src[m[2]:m[3]]
			used[key] = true
			if _, ok := entries[key]; !ok {
				missing = append(missing, f+":"+strconv.Itoa(strings.Count(src[:m[0]], "\n")+1)+": "+key+" (data-i18n)")
			}
		}
		if !strings.HasSuffix(f, ".js") {
			continue
		}
		lits, err := lexJS(src)
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		for _, l := range lits {
			if _, ok := entries[l.Text]; ok {
				used[l.Text] = true
				continue
			}
			if m := keyPrefix.FindStringSubmatch(l.Text); m != nil {
				prefixes[m[1]] = true
				continue
			}
			if !keyLike.MatchString(l.Text) {
				continue
			}
			// A literal key given straight to t() must exist.
			if strings.HasSuffix(l.Before, " t(") || strings.HasSuffix(l.Before, "(t(") ||
				l.Before == "t(" || strings.HasSuffix(l.Before, "{t(") {
				missing = append(missing, f+":"+strconv.Itoa(l.Line)+": "+l.Text+" (t)")
				continue
			}
			// So must a key-like literal anywhere else whose first segment is
			// a namespace of i18n.js.
			if ns := strings.SplitN(l.Text, ".", 2)[0]; namespaces[ns] {
				missing = append(missing, f+":"+strconv.Itoa(l.Line)+": "+l.Text+" (namespace "+ns+".)")
			}
		}
	}
	for _, m := range missing {
		t.Errorf("key not in i18n.js: %s", m)
	}
	var unused []string
	for k, e := range entries {
		if used[k] {
			continue
		}
		dyn := false
		for p := range prefixes {
			if strings.HasPrefix(k, p) {
				dyn = true
				break
			}
		}
		if !dyn {
			unused = append(unused, "i18n.js:"+strconv.Itoa(e.Line)+": "+k)
		}
	}
	sort.Strings(unused)
	for _, u := range unused {
		t.Errorf("key never used, remove it: %s", u)
	}
}

// ---------- Visible text ----------

// wordRe finds words: two or more letters.
var wordRe = regexp.MustCompile(`\p{L}[\p{L}\p{M}]+`)

// textWords returns the words of s that are not technical tokens.
func textWords(s string) []string {
	var out []string
	for _, w := range wordRe.FindAllString(s, -1) {
		if _, ok := techWords[w]; !ok {
			out = append(out, w)
		}
	}
	return out
}

var (
	// codeToken matches tokens that are code rather than language: identifiers,
	// CSS classes, keys, paths, selectors, URLs, constants, camelCase names.
	codeToken = regexp.MustCompile(`^[a-z0-9_$-]+$|[._/#=\[\]():{}<>&?@\\]|^[A-Z0-9_]+$|^[a-z]+[A-Z]\w*$`)
	// plainWord is a lowercase word without code characters.
	plainWord = regexp.MustCompile(`^\p{Ll}{2,}$`)
	// sinkRe matches code right before a literal that puts it on screen.
	sinkRe = regexp.MustCompile(`(?:\.textContent|\.innerText|\.innerHTML|\.title|\.placeholder|document\.title)\s*\+?=\s*$|\b(?:alert|confirm|prompt|esc|notify)\($|[{,]\s*(?:label|title|text|message|placeholder)\s*:\s*$`)
)

// looksLikeProse reports whether a plain string literal reads like language.
// In a sink (textContent = …, esc(…), label: …) a single lowercase word counts too.
func looksLikeProse(s string, sink bool) bool {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\x00", " "))
	var natural []string
	for _, tok := range strings.Fields(s) {
		core := strings.Trim(tok, ",;!?…–—·\"'„“”%+*|")
		if core == "" {
			continue
		}
		if codeToken.MatchString(core) && !strings.HasSuffix(tok, ".") && !(sink && plainWord.MatchString(core)) {
			continue
		}
		natural = append(natural, tok)
	}
	if len(natural) == 0 || len(textWords(strings.Join(natural, " "))) == 0 {
		return false
	}
	if len(natural) >= 2 || sink {
		return true
	}
	r, _ := utf8.DecodeRuneInString(natural[0])
	if unicode.IsUpper(r) {
		return true
	}
	for _, c := range natural[0] {
		if c > unicode.MaxASCII {
			return true
		}
	}
	return false
}

var (
	// htmlTag matches comments and tags. Quoted attribute values may contain
	// '>' and '<'; outside quotes a '<' ends the match, so a tag that is not
	// closed before the next one starts does not swallow that next tag.
	htmlTag  = regexp.MustCompile(`(?s)<!--.*?-->|<(/?)([a-zA-Z][a-zA-Z0-9-]*)((?:[^<>"']|"[^"]*"|'[^']*')*)>`)
	htmlAttr = regexp.MustCompile(`([a-zA-Z_:@][-a-zA-Z0-9_:.]*)(?:\s*=\s*("[^"]*"|'[^']*'|[^\s"'>]+))?`)
	// htmlStart tells markup from plain text.
	htmlStart = regexp.MustCompile(`<[a-zA-Z/!]`)
	// htmlBrokenTag finds the start of a tag in text between tags: a tag that
	// htmlTag could not match because it is never closed.
	htmlBrokenTag = regexp.MustCompile(`<[a-zA-Z/]`)
)

var voidTags = map[string]bool{"area": true, "base": true, "br": true, "col": true, "embed": true, "hr": true,
	"img": true, "input": true, "link": true, "meta": true, "source": true, "track": true, "wbr": true}

// skipTags hold no visible text of their own.
var skipTags = map[string]bool{"script": true, "style": true, "svg": true}

// visibleAttrs maps attributes the browser shows to the data attribute that
// translates them ("" = none exists, the value must not be language).
var visibleAttrs = map[string]string{"title": "data-i18n-title", "placeholder": "data-i18n-placeholder",
	"aria-label": "data-i18n-aria", "alt": ""}

type htmlElem struct {
	name string
	i18n bool
	line int
}

// covers reports whether the element hides its content from the scanner:
// the text is translated (data-i18n) or not shown (skipTags).
func (e htmlElem) covers() bool { return e.i18n || skipTags[e.name] }

// htmlTexts returns visible text in markup that does not go through data-i18n:
// text nodes outside elements with data-i18n and title/placeholder/aria-label/alt
// values without their data-i18n-* counterpart. "\x00" stands for ${…}.
func htmlTexts(src string) []string {
	texts, _ := htmlScan(src)
	return texts
}

// htmlScan returns the texts of htmlTexts and, as "line N: …", the places
// where the markup is broken: a tag that is never closed ('<path d="…"'
// without '>'), a closing tag without an open element, and an <svg>,
// <script>, <style> or data-i18n element that is never closed. Each of them
// would hide text from the scanner (everything up to the closing parent, or
// the rest of the file), so a complete page must have none (TestNoUntranslatedText).
// The scan goes on after a broken spot: the next tag is read as such, and a
// closing tag ends every element still open inside it. Template fragments
// of app.js are partial markup; only their texts count.
func htmlScan(src string) (texts, problems []string) {
	var stack []htmlElem
	lineAt := func(pos int) int { return strings.Count(src[:pos], "\n") + 1 }
	problem := func(pos int, format string, args ...any) {
		problems = append(problems, fmt.Sprintf("line %d: ", lineAt(pos))+fmt.Sprintf(format, args...))
	}
	covered := func() bool {
		for _, e := range stack {
			if e.covers() {
				return true
			}
		}
		return false
	}
	inCode := func() bool {
		for _, e := range stack {
			if e.name == "script" || e.name == "style" {
				return true
			}
		}
		return false
	}
	text := func(start int, s string) {
		if loc := htmlBrokenTag.FindStringIndex(s); loc != nil && !inCode() {
			problem(start+loc[0], "tag %s is not closed with '>'", quoteShort(strings.SplitN(s[loc[0]:], "\n", 2)[0]))
		}
		// Fragments of a template literal may start inside a tag or end in an
		// unfinished one; only the text between them counts.
		if i := strings.LastIndex(s, ">"); i >= 0 && strings.Contains(s[:i], "=") {
			s = s[i+1:]
		}
		if loc := htmlStart.FindStringIndex(s); loc != nil {
			s = s[:loc[0]]
		}
		s = strings.TrimSpace(html.UnescapeString(s))
		if s != "" && !covered() && len(textWords(s)) > 0 {
			texts = append(texts, s)
		}
	}
	pos := 0
	for _, m := range htmlTag.FindAllStringSubmatchIndex(src, -1) {
		text(pos, src[pos:m[0]])
		pos = m[1]
		if m[4] < 0 {
			continue // comment
		}
		closing := src[m[2]:m[3]] == "/"
		name := strings.ToLower(src[m[4]:m[5]])
		attrText := src[m[6]:m[7]]
		if closing {
			open := -1
			for i := len(stack) - 1; i >= 0; i-- {
				if stack[i].name == name {
					open = i
					break
				}
			}
			if open < 0 {
				problem(m[0], "</%s> closes no open element", name)
				continue
			}
			// Elements left open inside are closed with it. That is fine for
			// <p>, <li> or <option>, not for one that hides its content.
			for _, e := range stack[open+1:] {
				if e.covers() {
					problem(m[0], "<%s> from line %d is not closed before </%s>", e.name, e.line, name)
				}
			}
			stack = stack[:open]
			continue
		}
		attrs := map[string]string{}
		for _, a := range htmlAttr.FindAllStringSubmatch(attrText, -1) {
			v := a[2]
			if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') {
				v = v[1 : len(v)-1]
			}
			attrs[strings.ToLower(a[1])] = v
		}
		for a, i18n := range visibleAttrs {
			v, ok := attrs[a]
			if !ok {
				continue
			}
			if _, translated := attrs[i18n]; i18n != "" && translated {
				continue
			}
			if v = strings.TrimSpace(html.UnescapeString(v)); len(textWords(v)) > 0 && !strings.Contains(v, "\x00") {
				texts = append(texts, a+"="+v)
			}
		}
		if voidTags[name] || strings.HasSuffix(strings.TrimSpace(attrText), "/") {
			continue
		}
		_, i18n := attrs["data-i18n"]
		stack = append(stack, htmlElem{name: name, i18n: i18n, line: lineAt(m[0])})
	}
	text(pos, src[pos:])
	for _, e := range stack {
		if e.covers() {
			problem(len(src), "<%s> from line %d is never closed", e.name, e.line)
		}
	}
	return texts, problems
}

// jsTexts returns visible text in the literals of a JS file, as "line: text".
func jsTexts(lits []jsString) [][2]string {
	var out [][2]string
	for _, l := range lits {
		s := l.Text
		if keyLike.MatchString(s) || keyPrefix.MatchString(s) {
			continue
		}
		if htmlStart.MatchString(s) {
			for _, txt := range htmlTexts(strings.ReplaceAll(s, "\x00", " \x00 ")) {
				out = append(out, [2]string{strconv.Itoa(l.Line), txt})
			}
			continue
		}
		if looksLikeProse(s, sinkRe.MatchString(l.Before)) {
			out = append(out, [2]string{strconv.Itoa(l.Line), s})
		}
	}
	return out
}

func TestNoUntranslatedText(t *testing.T) {
	seen := map[string]bool{}
	report := func(where, text string) {
		clean := strings.TrimSpace(strings.Join(strings.Fields(strings.ReplaceAll(text, "\x00", " ")), " "))
		if _, ok := literalAllowed[clean]; ok {
			seen[clean] = true
			return
		}
		t.Errorf("%s: untranslated text %s – use t('key') / data-i18n, see the comment at the top of ui_i18n_test.go", where, quoteShort(clean))
	}
	for _, f := range uiPages {
		texts, problems := htmlScan(readStatic(t, f))
		for _, p := range problems {
			t.Errorf("%s: broken markup, %s", f, p)
		}
		for _, s := range texts {
			report(f, s)
		}
	}
	for _, f := range uiScripts {
		lits, err := lexJS(readStatic(t, f))
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		for _, x := range jsTexts(lits) {
			report(f+":"+x[0], x[1])
		}
	}
	for k := range literalAllowed {
		if !seen[k] {
			t.Errorf("literalAllowed: %q is not found any more, remove it", k)
		}
	}
}

// TestScannerBrokenMarkup makes sure broken markup on a page fails the test
// and does not hide the text after it from the scanner.
func TestScannerBrokenMarkup(t *testing.T) {
	const after = "\n</div>\n<p>Nur Text</p>\n"
	for _, tc := range []struct{ name, src string }{
		{"tag without '>'", `<div><button><svg viewBox="0 0 24 24"><path d="M18 6 6 18"` + after},
		{"svg not closed", `<div><button><svg><path d="M18 6 6 18"/></button>` + after},
		{"data-i18n element not closed", `<div><span data-i18n="close">Schließen` + after},
		{"svg never closed", `<p>Nur Text</p><svg><path d="M18 6 6 18"/>`},
		{"script never closed", `<p>Nur Text</p><script src="x.js">`},
		{"stray closing tag", `<p>Nur Text</p></svg>`},
	} {
		texts, problems := htmlScan(tc.src)
		if len(problems) == 0 {
			t.Errorf("%s: no problem reported in %q", tc.name, tc.src)
		}
		if strings.Join(texts, "|") != "Nur Text" {
			t.Errorf("%s: texts %q, want the paragraph after the broken markup", tc.name, texts)
		}
	}
	// Well-formed markup, including elements the browser closes implicitly.
	ok := `<!DOCTYPE html><div><button aria-label="x" data-i18n-aria="close"><svg viewBox="0 0 24 24">` +
		`<path d="M18 6 6 18"/><path d="m6 6 12 12"/></svg></button><ul><li>1<li>2</ul>` +
		`<p data-i18n="k">Text</p><span title="a > b" data-i18n-title="k"></span></div>`
	if texts, problems := htmlScan(ok); len(problems) != 0 || len(texts) != 0 {
		t.Errorf("well-formed markup: texts %q, problems %q", texts, problems)
	}
}

// TestScannerFindsUntranslatedText makes sure the scanners still catch the
// usual ways of adding a text without i18n.js.
func TestScannerFindsUntranslatedText(t *testing.T) {
	for _, src := range []string{
		`el.textContent = 'Gespeichert';`,
		`el.textContent = 'gespeichert';`,
		`el.title = 'Save the settings';`,
		"el.innerHTML = `<td>${esc(x)}</td><td>Keine Daten</td>`;",
		"el.innerHTML = `<button title=\"Close\">${icon}</button>`;",
		`const label = cond ? 'Aktiv' : 'Inaktiv';`,
		`if (!ok) notify('Something failed', 'err');`,
		`chart.data.datasets.push({ label: 'used', data });`,
		`const msg = 'Bitte warten …';`,
	} {
		lits, err := lexJS(src)
		if err != nil {
			t.Fatalf("%s: %v", src, err)
		}
		if len(jsTexts(lits)) == 0 {
			t.Errorf("scanner misses the text in %s", src)
		}
	}
	for _, src := range []string{
		`<p>Nur Text</p>`,
		`<input placeholder="Suchen">`,
		`<button aria-label="Close"></button>`,
	} {
		if len(htmlTexts(src)) == 0 {
			t.Errorf("scanner misses the text in %s", src)
		}
	}
	for _, src := range []string{
		`el.textContent = t('peering.hop');`,
		`el.className = 'btn btn-secondary';`,
		"el.innerHTML = `<span class=\"x\" data-i18n=\"nav.tests\">Tests</span>`;",
		`document.getElementById('loginError').hidden = true;`,
		`const v = '100 ms';`,
	} {
		lits, _ := lexJS(src)
		if got := jsTexts(lits); len(got) != 0 {
			t.Errorf("scanner reports %v in %s", got, src)
		}
	}
}
