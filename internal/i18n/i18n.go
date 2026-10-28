// i18n.go
// Server message catalogs (German and English) and the language of a request
// Version: 2026.10.28

// Package i18n renders server messages for the web UI in the request language.
//
// Errors, logs and stored texts are English. UI messages are built from catalog keys
// (E, Wrap, M) and rendered per request (ErrorIn, Msg.In); stored errors keep a
// language-neutral form beside the English text (Localize). Foreign program output has no key.
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

// Default is the language when a request names none; German, as in earlier API versions.
const Default = DE

// Languages returns the supported language codes.
func Languages() []string { return []string{DE, EN} }

// FromRequest picks ?lang= (EventSource cannot set headers), else the first Accept-Language
// entry, else Default; anything not German is English.
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

// Lookup returns key's text in lang, falling back to English; ok is false if neither has it.
func Lookup(lang, key string) (string, bool) {
	if s, ok := catalogs[lang][key]; ok {
		return s, true
	}
	s, ok := catalogs[EN][key]
	return s, ok
}

// T returns key's text in lang formatted with args; an unknown key is returned as is.
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
