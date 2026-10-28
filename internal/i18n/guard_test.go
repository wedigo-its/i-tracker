// guard_test.go
// Guard tests: complete catalogs, known keys, English-only Go code, keyed errors on the way to the UI
// Version: 2026.10.28

package i18n

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"unicode"
)

// What the guards enforce (fix by translating, adding keys or passing the right arguments):
//   - TestCatalogsComplete: same keys, no empty text, same verbs; de differs from en unless wordless or in sameInBothLanguages.
//   - TestRuntimeKeysUseStringVerbs: arguments are stored as text, so %s only.
//   - TestKeysUsedInCodeExist / TestCatalogKeysUsed: code keys exist in both catalogs; every catalog key is used.
//   - TestNoGermanInGoCode: Go string literals are English; German goes to locales/de.json (exceptions: germanAllowed).
//   - TestCommentsAreEnglish: comments (Go, web UI, SQL, config, Docker, cmd) and test messages are English (exceptions: commentAllowed).
//   - TestUIErrorsAreKeyed: UI-facing errors use E, Wrap or Foreign, never errors.New or fmt.Errorf.
//   - TestKeyCalls: keyFuncs get a literal/constant catalog key and one argument per verb (Wrap: cause last);
//     TestKeyFuncsSignature keeps keyFuncs in line.

// root is the repository root, seen from this package.
var root = filepath.Join("..", "..")

// sameInBothLanguages lists keys whose German text is rightly the English one.
var sameInBothLanguages = map[string]string{}

// germanAllowedFiles hold German on purpose.
var germanAllowedFiles = map[string]string{
	"internal/i18n/legacy.go": "German messages stored by versions before 2026.10.28, recognized in old rows",
}

// germanAllowed are exact literals that look German but are not program texts.
var germanAllowed = map[string]string{
	"Düsseldorf": "city name in the demo route (GeoIP names it the same way)",
}

// uiErrorFiles produce errors that reach the UI (API responses, iperf3 status, stored test/trace errors).
var uiErrorFiles = []string{
	"internal/web/*.go",
	"internal/model/model.go",
	"internal/iperf/runner.go",
	"internal/iperf/setup.go",
	"internal/iperf/install.go",
	"internal/iperf/binary.go",
	"internal/trace/tracer.go",
}

// uiErrorExempt are files matched by uiErrorFiles whose errors never reach the UI.
var uiErrorExempt = map[string]string{
	"internal/web/bootstrap.go": "errors of creating the first admin, returned to main and printed on the console",
}

// germanWords mark a text as German (lowercase); words also English ("die", "an", "so") are left out.
var germanWords = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`nicht und ist wird werden wurde wurden konnte konnten kein keine keinen
		für mit bitte oder zwischen muss müssen darf ungültig ungültige ungültiger ungültiges fehler
		fehlgeschlagen gefunden gestartet beendet gelöscht datei dienst datenbank passwort benutzer
		anmeldung sitzung verbindung ziel abgebrochen angelegt gespeichert geöffnet nur noch auch eine
		einen einem einer der das dem den des sich bei vom zum zur aus wenn sind zeichen sekunden
		erstellt pfad konfiguration verwendung läuft gestoppt unbekannt verfügbar installiert neu alle
		ohne oberfläche beenden starten ausgeben veraltete datensatz wert vergeben zeitstempel nach
		über unter bereits öffentlicher deutschland frankreich schweiz`) {
		germanWords[w] = true
	}
}

// looksGerman reports whether s reads like German text.
func looksGerman(s string) bool {
	if strings.ContainsAny(s, "äöüÄÖÜß„“") {
		return true
	}
	for _, w := range strings.FieldsFunc(s, func(r rune) bool { return !unicode.IsLetter(r) }) {
		if germanWords[strings.ToLower(w)] {
			return true
		}
	}
	return false
}

// goFile is a parsed non-test Go file of the repository.
type goFile struct {
	path string // slash-separated, relative to root
	fset *token.FileSet
	file *ast.File
}

func goFiles(t *testing.T) []goFile { return parseGoFiles(t, false) }

// parseGoFiles parses the repository's Go files with comments; tests only if withTests.
func parseGoFiles(t *testing.T, withTests bool) []goFile {
	t.Helper()
	var out []goFile
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "_release", "dist", "bin", "vendor", "node_modules":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || !withTests && strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution|parser.ParseComments)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		out = append(out, goFile{filepath.ToSlash(rel), fset, f})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) < 30 {
		t.Fatalf("only %d Go files found", len(out))
	}
	return out
}

// stringLits calls f for every string literal of the file.
func (g goFile) stringLits(f func(value string, pos token.Position)) {
	ast.Inspect(g.file, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		if v, err := strconv.Unquote(lit.Value); err == nil {
			f(v, g.fset.Position(lit.Pos()))
		}
		return true
	})
}

// ---------- Catalogs ----------

var verbRe = regexp.MustCompile(`%(\[\d+\])?[-+# 0]*\d*(\.\d+)?[a-zA-Z%]`)

// verbs returns a catalog text's verbs in order, excluding "%%".
func verbs(s string) []string {
	var out []string
	for _, v := range verbRe.FindAllString(s, -1) {
		if v != "%%" {
			out = append(out, v)
		}
	}
	return out
}

// sortedVerbs is verbs sorted: a translation may reorder its arguments.
func sortedVerbs(s string) []string {
	v := verbs(s)
	sort.Strings(v)
	return v
}

func hasWord(s string) bool {
	for _, r := range verbRe.ReplaceAllString(s, "") {
		if unicode.IsLetter(r) {
			return true
		}
	}
	return false
}

func TestCatalogsComplete(t *testing.T) {
	if len(Keys(EN)) < 80 {
		t.Fatalf("only %d keys in en.json", len(Keys(EN)))
	}
	for _, lang := range Languages() {
		other := EN
		if lang == EN {
			other = DE
		}
		for _, k := range Keys(lang) {
			if _, ok := catalogs[other][k]; !ok {
				t.Errorf("%s is in %s.json but missing in %s.json", k, lang, other)
			}
			if strings.TrimSpace(catalogs[lang][k]) == "" {
				t.Errorf("%s is empty in %s.json", k, lang)
			}
		}
	}
	for _, k := range Keys(DE) {
		de, en := catalogs[DE][k], catalogs[EN][k]
		if vd, ve := sortedVerbs(de), sortedVerbs(en); strings.Join(vd, " ") != strings.Join(ve, " ") {
			t.Errorf("%s: de has the verbs %v, en has %v", k, vd, ve)
		}
		if de == en && hasWord(de) {
			if _, ok := sameInBothLanguages[k]; !ok {
				t.Errorf("%s has the same text in de and en (%q): translate it, or add it to sameInBothLanguages with a reason", k, de)
			}
		}
		if strings.Contains(de, "Ã") || strings.Contains(de, "â€") {
			t.Errorf("%s: broken encoding in de.json", k)
		}
	}
	for k := range sameInBothLanguages {
		if catalogs[DE][k] != catalogs[EN][k] {
			t.Errorf("sameInBothLanguages lists %s, which differs now: remove it", k)
		}
	}
}

func TestRuntimeKeysUseStringVerbs(t *testing.T) {
	for _, lang := range Languages() {
		for _, k := range Keys(lang) {
			for _, v := range verbRe.FindAllString(catalogs[lang][k], -1) {
				if v != "%s" {
					t.Errorf("%s.json %s uses %s; message arguments are text, use %%s", lang, k, v)
				}
			}
		}
	}
}

// keyLit matches string literals that are catalog keys.
var keyLit = regexp.MustCompile(`^(err|msg|public)\.[a-z0-9_]+(\.[a-z0-9_]+)*$`)

// usedKeys returns the catalog keys named in the Go code, with a position each.
func usedKeys(t *testing.T) map[string]string {
	used := map[string]string{}
	for _, g := range goFiles(t) {
		g.stringLits(func(v string, pos token.Position) {
			if keyLit.MatchString(v) {
				used[v] = g.path + ":" + strconv.Itoa(pos.Line)
			}
		})
	}
	return used
}

func TestKeysUsedInCodeExist(t *testing.T) {
	used := usedKeys(t)
	if len(used) < 80 {
		t.Fatalf("only %d key literals found", len(used))
	}
	for k, where := range used {
		for _, lang := range Languages() {
			if _, ok := catalogs[lang][k]; !ok {
				t.Errorf("%s: key %s missing in %s.json", where, k, lang)
			}
		}
	}
}

func TestCatalogKeysUsed(t *testing.T) {
	used := usedKeys(t)
	for _, k := range Keys(EN) {
		if _, ok := used[k]; !ok {
			t.Errorf("%s is never used: remove it from both catalogs", k)
		}
	}
}

// ---------- Go code ----------

func TestNoGermanInGoCode(t *testing.T) {
	seen := map[string]bool{}
	var found []string
	for _, g := range goFiles(t) {
		if _, ok := germanAllowedFiles[g.path]; ok {
			continue
		}
		g.stringLits(func(v string, pos token.Position) {
			if !looksGerman(v) {
				return
			}
			if _, ok := germanAllowed[v]; ok {
				seen[v] = true
				return
			}
			found = append(found, g.path+":"+strconv.Itoa(pos.Line)+": "+strconv.Quote(v))
		})
	}
	sort.Strings(found)
	for _, f := range found {
		t.Errorf("German text in Go code (write it in English; texts for the UI go into the catalogs): %s", f)
	}
	for v := range germanAllowed {
		if !seen[v] {
			t.Errorf("germanAllowed: %q is not found any more, remove it", v)
		}
	}
	for f := range germanAllowedFiles {
		if _, err := os.Stat(filepath.Join(root, f)); err != nil {
			t.Errorf("germanAllowedFiles: %s does not exist any more", f)
		}
	}
}

func TestLooksGerman(t *testing.T) {
	for _, s := range []string{"Datenbank konnte nicht geöffnet werden", "Server nicht gefunden", "Pfad zur Konfigurationsdatei", "Neue Konfiguration erstellt", "Fehler:"} {
		if !looksGerman(s) {
			t.Errorf("looksGerman(%q) = false", s)
		}
	}
	for _, s := range []string{"database could not be opened", "Server not found", "path to the configuration file", "lang", "Content-Language", "iperf3 %s is too old"} {
		if looksGerman(s) {
			t.Errorf("looksGerman(%q) = true", s)
		}
	}
}

// ---------- Comments and test messages ----------

// commentSkipFiles are scanned files whose comments are not maintained here.
var commentSkipFiles = map[string]string{
	"internal/web/static/wedigo-tokens.css": "shared copy of the wedigo design tokens, changed only at its source",
}

// commentAllowed are findings that only look German, as "path: substring of the comment or message".
var commentAllowed = map[string]string{}

// comment is a comment or test message found in a source file.
type comment struct {
	line int
	text string
}

// commentSources are the non-Go files checked, with their comment syntax.
var commentSources = []struct {
	glob    string
	extract func(src string) []comment
}{
	{"internal/web/static/*.js", func(s string) []comment { return cComments(s, true) }},
	{"internal/web/static/*.css", func(s string) []comment { return cComments(s, false) }},
	{"internal/web/static/*.html", htmlComments},
	{"internal/db/schema.sql", sqlComments},
	{"config.example.yaml", func(s string) []comment { return hashComments(s, true) }},
	{"docker-compose.yml", func(s string) []comment { return hashComments(s, true) }},
	{"Dockerfile", func(s string) []comment { return hashComments(s, false) }},
	{"*.cmd", cmdComments},
}

func lineAt(src string, off int) int { return 1 + strings.Count(src[:off], "\n") }

// regexAfter are JavaScript keywords after which '/' starts a regular expression.
var regexAfter = map[string]bool{"return": true, "typeof": true, "case": true, "in": true, "of": true,
	"delete": true, "void": true, "throw": true, "new": true, "else": true, "do": true}

// cComments returns the /* */ comments of src, plus // comments if js; strings,
// template literals and (if js) regular expressions are skipped.
func cComments(src string, js bool) []comment {
	var out []comment
	i := 0
	var last byte           // last code byte that is not white space
	word, open := "", false // identifier ending at last; open while it may go on
	sig := func(c byte) {
		if c == '_' || c == '$' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' {
			if !open {
				word = ""
			}
			word, open = word+string(c), true
		} else {
			word, open = "", false
		}
		last = c
	}
	quoted := func(q byte) {
		for i++; i < len(src) && src[i] != q && src[i] != '\n'; i++ {
			if src[i] == '\\' {
				i++
			}
		}
		i++
		sig('"')
	}
	var scan func(untilBrace bool)
	template := func() {
		for i++; i < len(src); i++ {
			switch {
			case src[i] == '\\':
				i++
			case src[i] == '`':
				i++
				sig('"')
				return
			case src[i] == '$' && i+1 < len(src) && src[i+1] == '{':
				i += 2
				sig('{')
				scan(true)
				i-- // the loop steps past the closing brace again
			}
		}
	}
	scan = func(untilBrace bool) {
		depth := 0
		for i < len(src) {
			c, next := src[i], byte(0)
			if i+1 < len(src) {
				next = src[i+1]
			}
			switch {
			case c == '/' && next == '*':
				end := strings.Index(src[i+2:], "*/")
				if end < 0 {
					end = len(src) - i - 4
				}
				out = append(out, comment{lineAt(src, i), src[i : i+4+end]})
				i += end + 4
			case js && c == '/' && next == '/':
				end := strings.IndexByte(src[i:], '\n')
				if end < 0 {
					end = len(src) - i
				}
				out = append(out, comment{lineAt(src, i), src[i : i+end]})
				i += end
			case js && c == '/' && (last == 0 || strings.IndexByte("(,=:[!&|?{};+-*%<>~^", last) >= 0 || regexAfter[word]):
				class := false
				for i++; i < len(src) && src[i] != '\n' && (src[i] != '/' || class); i++ {
					switch src[i] {
					case '\\':
						i++
					case '[':
						class = true
					case ']':
						class = false
					}
				}
				i++
				sig('"')
			case c == '\'' || c == '"':
				quoted(c)
			case js && c == '`':
				template()
			case c == '}' && untilBrace && depth == 0:
				i++
				return
			default:
				if c == '{' {
					depth++
				} else if c == '}' {
					depth--
				}
				if c != ' ' && c != '\t' && c != '\r' && c != '\n' {
					sig(c)
				} else {
					open = false
				}
				i++
			}
		}
	}
	scan(false)
	return out
}

var htmlCommentRe = regexp.MustCompile(`(?s)<!--.*?-->`)

func htmlComments(src string) []comment {
	var out []comment
	for _, m := range htmlCommentRe.FindAllStringIndex(src, -1) {
		out = append(out, comment{lineAt(src, m[0]), src[m[0]:m[1]]})
	}
	return out
}

// lineComments returns, per line, the text from the first marker found by at to the line's end.
func lineComments(src string, at func(line string) int) []comment {
	var out []comment
	for n, l := range strings.Split(src, "\n") {
		if i := at(l); i >= 0 {
			out = append(out, comment{n + 1, l[i:]})
		}
	}
	return out
}

// sqlComments returns the -- comments outside '…' strings.
func sqlComments(src string) []comment {
	return lineComments(src, func(l string) int {
		in := false
		for i := 0; i+1 < len(l); i++ {
			if l[i] == '\'' {
				in = !in
			} else if !in && l[i] == '-' && l[i+1] == '-' {
				return i
			}
		}
		return -1
	})
}

// hashComments returns # comments: whole lines, and if inline also after white space outside quotes (YAML).
func hashComments(src string, inline bool) []comment {
	return lineComments(src, func(l string) int {
		if !inline {
			if strings.HasPrefix(strings.TrimSpace(l), "#") {
				return strings.IndexByte(l, '#')
			}
			return -1
		}
		var q byte
		for i := 0; i < len(l); i++ {
			switch c := l[i]; {
			case q != 0:
				if c == q {
					q = 0
				}
			case c == '\'' || c == '"':
				q = c
			case c == '#' && (i == 0 || l[i-1] == ' ' || l[i-1] == '\t'):
				return i
			}
		}
		return -1
	})
}

// cmdComments returns rem and :: lines of a batch file.
func cmdComments(src string) []comment {
	return lineComments(src, func(l string) int {
		s := strings.ToLower(strings.TrimLeft(strings.TrimSpace(l), "@"))
		if strings.HasPrefix(s, "::") || s == "rem" || strings.HasPrefix(s, "rem ") || strings.HasPrefix(s, "rem\t") {
			return 0
		}
		return -1
	})
}

// testMessageFuncs are testing.TB methods whose texts are checked; formatted ones only in their format.
var testMessageFuncs = map[string]bool{"Error": false, "Errorf": true, "Fatal": false, "Fatalf": true,
	"Log": false, "Logf": true, "Skip": false, "Skipf": true}

// goComments returns the comments of a Go file and, for tests, the texts of test messages.
func goComments(g goFile) []comment {
	var out []comment
	for _, cg := range g.file.Comments {
		for _, c := range cg.List {
			out = append(out, comment{g.fset.Position(c.Pos()).Line, c.Text})
		}
	}
	if !strings.HasSuffix(g.path, "_test.go") {
		return out
	}
	ast.Inspect(g.file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		formatted, ok := testMessageFuncs[sel.Sel.Name]
		if _, isIdent := sel.X.(*ast.Ident); !ok || !isIdent {
			return true
		}
		for i, arg := range call.Args {
			if formatted && i > 0 {
				break
			}
			if lit, ok := arg.(*ast.BasicLit); ok && lit.Kind == token.STRING {
				if v, err := strconv.Unquote(lit.Value); err == nil {
					out = append(out, comment{g.fset.Position(lit.Pos()).Line, v})
				}
			}
		}
		return true
	})
	return out
}

func TestCommentsAreEnglish(t *testing.T) {
	var found []string
	used := map[string]bool{}
	check := func(path string, cs []comment) {
		for _, c := range cs {
			if !looksGerman(c.text) {
				continue
			}
			allowed := false
			for k := range commentAllowed {
				if p, sub, _ := strings.Cut(k, ": "); p == path && strings.Contains(c.text, sub) {
					used[k], allowed = true, true
				}
			}
			if !allowed {
				found = append(found, fmt.Sprintf("%s:%d: %s", path, c.line, strings.Join(strings.Fields(c.text), " ")))
			}
		}
	}
	for _, g := range parseGoFiles(t, true) {
		check(g.path, goComments(g))
	}
	scanned := 0
	for _, s := range commentSources {
		matches, err := filepath.Glob(filepath.Join(root, filepath.FromSlash(s.glob)))
		if err != nil || len(matches) == 0 {
			t.Errorf("commentSources: %s matches no file", s.glob)
		}
		for _, m := range matches {
			rel, _ := filepath.Rel(root, m)
			rel = filepath.ToSlash(rel)
			if _, skip := commentSkipFiles[rel]; skip {
				used["skip "+rel] = true
				continue
			}
			src, err := os.ReadFile(m)
			if err != nil {
				t.Fatal(err)
			}
			scanned++
			check(rel, s.extract(string(src)))
		}
	}
	if scanned < 10 {
		t.Fatalf("only %d non-Go files scanned", scanned)
	}
	sort.Strings(found)
	for _, f := range found {
		t.Errorf("German comment or test message (write it in English): %s", f)
	}
	for k := range commentAllowed {
		if !used[k] {
			t.Errorf("commentAllowed: %q matches nothing any more, remove it", k)
		}
	}
	for f := range commentSkipFiles {
		if !used["skip "+f] {
			t.Errorf("commentSkipFiles: %s is not scanned any more, remove it", f)
		}
	}
}

// TestCommentExtractors makes sure the extractors find comments, not code or literals.
func TestCommentExtractors(t *testing.T) {
	for _, c := range []struct {
		name string
		got  []comment
		want string
	}{
		{"js", cComments("a = '// x' + \"/* y */\"; // one\nb = /\\/\\/[/]/g.test(s) / 2; /* two */\nc = `${d /* three */} // z`", true), "// one|/* two */|/* three */"},
		{"css", cComments("a { background: url(http://x/y); } /* one */ b::after { content: '/* x */'; }", false), "/* one */"},
		{"html", htmlComments("<p>a</p>\n<!-- one\n two -->"), "<!-- one\n two -->"},
		{"sql", sqlComments("x TEXT DEFAULT '--', -- one\n-- two"), "-- one|-- two"},
		{"yaml", hashComments("a: \"#x\" # one\nb: c#d\n  # two", true), "# one|# two"},
		{"dockerfile", hashComments("# one\nRUN echo a # b", false), "# one"},
		{"cmd", cmdComments("@echo off\r\n:: one\r\nREM two\r\nset rem=x"), ":: one\r|REM two\r"},
	} {
		var texts []string
		for _, cm := range c.got {
			texts = append(texts, cm.text)
		}
		if got := strings.Join(texts, "|"); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestUIErrorsAreKeyed(t *testing.T) {
	matched := 0
	for _, g := range goFiles(t) {
		ui := false
		for _, pattern := range uiErrorFiles {
			if ok, _ := filepath.Match(pattern, g.path); ok {
				ui = true
			}
		}
		if _, exempt := uiErrorExempt[g.path]; !ui || exempt {
			continue
		}
		matched++
		ast.Inspect(g.file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			if name := pkg.Name + "." + sel.Sel.Name; name == "errors.New" || name == "fmt.Errorf" {
				t.Errorf("%s:%d: %s makes an error without a catalog key; use i18n.E or i18n.Wrap (i18n.Foreign for text of other programs)",
					g.path, g.fset.Position(call.Pos()).Line, name)
			}
			return true
		})
	}
	if matched < len(uiErrorFiles)+5 {
		t.Fatalf("uiErrorFiles: only %d files matched", matched)
	}
}

// ---------- Calls with a catalog key ----------

// modulePath is the Go module of the repository (go.mod).
const modulePath = "i-tracker"

// keyFunc takes a catalog key at index key, then the message arguments; extra counts arguments it adds (Wrap: cause).
type keyFunc struct {
	pkg        string // package directory, relative to root
	name       string
	key, extra int
}

// keyFuncs take a catalog key; calls inside package i18n are plumbing and not checked.
var keyFuncs = []keyFunc{
	{"internal/i18n", "E", 0, 0},
	{"internal/i18n", "M", 0, 0},
	{"internal/i18n", "Wrap", 1, 1},
	{"internal/web", "writeError", 3, 0},
	{"internal/web", "message", 1, 0},
}

// importName returns f's import name for repository directory dir, or "" if not imported.
func importName(f *ast.File, dir string) string {
	for _, imp := range f.Imports {
		if p, _ := strconv.Unquote(imp.Path.Value); p == modulePath+"/"+dir {
			if imp.Name != nil {
				return imp.Name.Name
			}
			return path.Base(p)
		}
	}
	return ""
}

// keyFuncOf returns the keyFunc that call calls in file g.
func keyFuncOf(g goFile, call *ast.CallExpr) (keyFunc, bool) {
	for _, kf := range keyFuncs {
		switch fun := call.Fun.(type) {
		case *ast.Ident:
			if fun.Name == kf.name && path.Dir(g.path) == kf.pkg && kf.pkg != "internal/i18n" {
				return kf, true
			}
		case *ast.SelectorExpr:
			if x, ok := fun.X.(*ast.Ident); ok && fun.Sel.Name == kf.name && x.Name == importName(g.file, kf.pkg) {
				return kf, true
			}
		}
	}
	return keyFunc{}, false
}

// paramNames returns the parameter names of a function, in order.
func paramNames(ft *ast.FuncType) []string {
	var out []string
	for _, f := range ft.Params.List {
		if len(f.Names) == 0 {
			out = append(out, "_")
		}
		for _, n := range f.Names {
			out = append(out, n.Name)
		}
	}
	return out
}

// stringConsts returns the string constants of each package directory.
func stringConsts(files []goFile) map[string]map[string]string {
	out := map[string]map[string]string{}
	for _, g := range files {
		dir := path.Dir(g.path)
		ast.Inspect(g.file, func(n ast.Node) bool {
			decl, ok := n.(*ast.GenDecl)
			if !ok || decl.Tok != token.CONST {
				return true
			}
			for _, spec := range decl.Specs {
				vs := spec.(*ast.ValueSpec)
				for i, name := range vs.Names {
					if i >= len(vs.Values) {
						break
					}
					if lit, ok := vs.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
						if v, err := strconv.Unquote(lit.Value); err == nil {
							if out[dir] == nil {
								out[dir] = map[string]string{}
							}
							out[dir][name.Name] = v
						}
					}
				}
			}
			return true
		})
	}
	return out
}

// checkKeyCalls checks each keyFunc call: key is a literal or package constant (or a keyFunc's own
// key parameter), exists in both catalogs, and gets one argument per verb; returns problems and count.
func checkKeyCalls(files []goFile) (problems []string, checked int) {
	consts := stringConsts(files)
	for _, g := range files {
		dir := path.Dir(g.path)
		for _, decl := range g.file.Decls {
			// forward is the key parameter of a keyFunc declared here.
			forward := ""
			if fd, ok := decl.(*ast.FuncDecl); ok && fd.Recv == nil {
				for _, kf := range keyFuncs {
					if kf.pkg == dir && kf.name == fd.Name.Name {
						if names := paramNames(fd.Type); kf.key < len(names) {
							forward = names[kf.key]
						}
					}
				}
			}
			ast.Inspect(decl, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				kf, ok := keyFuncOf(g, call)
				if !ok {
					return true
				}
				checked++
				where := g.path + ":" + strconv.Itoa(g.fset.Position(call.Pos()).Line) + ": " + kf.name
				if len(call.Args) <= kf.key {
					problems = append(problems, where+" has no key argument")
					return true
				}
				var key string
				switch arg := call.Args[kf.key].(type) {
				case *ast.BasicLit:
					if arg.Kind == token.STRING {
						key, _ = strconv.Unquote(arg.Value)
					}
				case *ast.Ident:
					if arg.Name == forward && forward != "" {
						return true // passes on the key its own callers give, which are checked
					}
					key = consts[dir][arg.Name]
				}
				if key == "" {
					problems = append(problems, where+": the key must be a string literal or a string constant naming a catalog key")
					return true
				}
				for _, lang := range Languages() {
					if _, ok := catalogs[lang][key]; !ok {
						problems = append(problems, fmt.Sprintf("%s: %q is not a key of %s.json", where, key, lang))
						return true
					}
				}
				if call.Ellipsis.IsValid() {
					return true // args... : the number is known only at run time
				}
				got := len(call.Args) - kf.key - 1 + kf.extra
				for _, lang := range Languages() {
					if want := len(verbs(catalogs[lang][key])); got != want {
						problems = append(problems, fmt.Sprintf("%s: %s gets %d argument(s), its %s.json text has %d verb(s)", where, key, got, lang, want))
					}
				}
				return true
			})
		}
	}
	return problems, checked
}

func TestKeyCalls(t *testing.T) {
	problems, checked := checkKeyCalls(goFiles(t))
	for _, p := range problems {
		t.Error(p)
	}
	if checked < 100 {
		t.Fatalf("only %d calls with a catalog key found", checked)
	}
}

// TestKeyFuncsSignature checks that each keyFunc has "key" at index key, then "args ...any".
func TestKeyFuncsSignature(t *testing.T) {
	files := goFiles(t)
	for _, kf := range keyFuncs {
		found := false
		for _, g := range files {
			if path.Dir(g.path) != kf.pkg {
				continue
			}
			for _, decl := range g.file.Decls {
				fd, ok := decl.(*ast.FuncDecl)
				if !ok || fd.Recv != nil || fd.Name.Name != kf.name {
					continue
				}
				found = true
				names := paramNames(fd.Type)
				last := fd.Type.Params.List[len(fd.Type.Params.List)-1]
				if _, variadic := last.Type.(*ast.Ellipsis); len(names) != kf.key+2 || names[kf.key] != "key" || !variadic {
					t.Errorf("%s.%s(%s): keyFuncs expects the key at index %d, followed by args ...any",
						kf.pkg, kf.name, strings.Join(names, ", "), kf.key)
				}
			}
		}
		if !found {
			t.Errorf("keyFuncs: %s.%s not found", kf.pkg, kf.name)
		}
	}
}

// TestCheckKeyCalls makes sure checkKeyCalls still catches the usual mistakes.
func TestCheckKeyCalls(t *testing.T) {
	check := func(body string) []string {
		src := `package web
import (
	"fmt"
	"i-tracker/internal/i18n"
)
const okKey = "err.internal"
const textKey = "Something went wrong"
func writeError(w, r any, status int, key string, args ...any) { writeErr(w, r, status, i18n.E(key, args...)) }
func f() {
	` + body + `
}`
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, "fake.go", src, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("%s: %v", body, err)
		}
		problems, _ := checkKeyCalls([]goFile{{"internal/web/fake.go", fset, f}})
		return problems
	}
	for _, body := range []string{
		`writeError(w, r, 400, "Something went wrong")`,
		`writeError(w, r, 400, textKey)`,
		`writeError(w, r, 400, "err.does_not_exist")`,
		`k := "err.internal"; writeError(w, r, 400, k)`,
		`writeError(w, r, 400, fmt.Sprint("err.internal"))`,
		`message(r, "Saved")`,
		`writeError(w, r, 400, "err.internal", 1)`,
		`message(r, "err.param.range", "x")`,
		`i18n.E("err.param.invalid")`,
		`i18n.M("err.param.invalid", "a", "b")`,
		`i18n.Wrap(err, "err.param.invalid", "x")`,
		`i18n.E("Something went wrong")`,
	} {
		if len(check(body)) == 0 {
			t.Errorf("checkKeyCalls misses the mistake in %s", body)
		}
	}
	for _, body := range []string{
		`writeError(w, r, 400, okKey)`,
		`writeError(w, r, 400, "err.param.invalid", "x")`,
		`message(r, "err.param.range", "a", 1, 2)`,
		`i18n.Wrap(err, "err.invalid_body")`,
		`i18n.E("err.param.range", args...)`,
	} {
		if p := check(body); len(p) != 0 {
			t.Errorf("checkKeyCalls reports %v in %s", p, body)
		}
	}
}
