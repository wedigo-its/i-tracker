// i18n.go
// Server message catalogs (German and English) and the language of a request
// Version: 2026.10.28

// Package i18n renders the messages the server sends to the web UI in the
// language of the request.
//
// Internally everything is English: errors (Error()), logs and stored texts.
// Messages that reach the UI are built from catalog keys (E, Wrap, M) and
// rendered by key in the request language (ErrorIn, Msg.In). Stored messages
// (the last error of a test or trace) keep a language-neutral form next to the
// English text, see Localize. Texts from other programs (iperf3, traceroute,
// the operating system) have no key and are shown as they are.
package i18n

import (
	"embed"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
)

// Supported languages.
const (
	DE = "de"
	EN = "en"
)

// Default is the language of a request that names none. German, as in all
// earlier versions of the API.
const Default = DE

// Languages returns the supported language codes.
func Languages() []string { return []string{DE, EN} }

// FromRequest picks the language of a request: the query parameter "lang"
// (EventSource cannot set headers), else the first entry of Accept-Language,
// else Default. Anything that is not German counts as English.
func FromRequest(r *http.Request) string {
	if l := r.URL.Query().Get("lang"); l != "" {
		return normalize(l)
	}
	if h := r.Header.Get("Accept-Language"); h != "" {
		tag, _, _ := strings.Cut(h, ",")
		tag, _, _ = strings.Cut(tag, ";")
		return normalize(tag)
	}
	return Default
}

func normalize(tag string) string {
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(tag)), DE) {
		return DE
	}
	return EN
}

//go:embed locales/*.json
var localeFS embed.FS

// catalogs maps language -> key -> text.
var catalogs = mustLoad()

func mustLoad() map[string]map[string]string {
	out := make(map[string]map[string]string, 2)
	for _, lang := range Languages() {
		data, err := localeFS.ReadFile("locales/" + lang + ".json")
		if err != nil {
			panic("i18n: missing catalog " + lang + ": " + err.Error())
		}
		m := map[string]string{}
		if err := json.Unmarshal(data, &m); err != nil {
			panic("i18n: invalid catalog " + lang + ": " + err.Error())
		}
		out[lang] = m
	}
	return out
}

// Lookup returns the text of key in lang, falling back to English. ok is
// false when neither catalog has the key.
func Lookup(lang, key string) (string, bool) {
	if s, ok := catalogs[lang][key]; ok {
		return s, true
	}
	s, ok := catalogs[EN][key]
	return s, ok
}

// T returns the text of key in lang, formatted with args. An unknown key is
// returned as it is.
func T(lang, key string, args ...any) string {
	s, ok := Lookup(lang, key)
	if !ok {
		s = key
	}
	if len(args) > 0 {
		return fmt.Sprintf(s, args...)
	}
	return s
}

// Keys returns the sorted keys of a catalog.
func Keys(lang string) []string {
	keys := make([]string, 0, len(catalogs[lang]))
	for k := range catalogs[lang] {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
