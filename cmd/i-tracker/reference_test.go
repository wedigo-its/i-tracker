package main

import (
	"bytes"
	"flag"
	"os"
	"regexp"
	"strings"
	"testing"
)

var updateReference = flag.Bool("update", false, "docs/handbook/reference.json neu schreiben")

const (
	referenceFile = "../../docs/handbook/reference.json"
	handbookDir   = "../../docs/handbook/"
)

// TestReferenceFileIsCurrent hält docs/handbook/reference.json auf dem Stand des Codes.
// Neu schreiben: go test ./cmd/i-tracker -run TestReferenceFileIsCurrent -update
func TestReferenceFileIsCurrent(t *testing.T) {
	want, err := marshalReference()
	if err != nil {
		t.Fatal(err)
	}
	if *updateReference {
		if err := os.WriteFile(referenceFile, want, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := os.ReadFile(referenceFile)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(bytes.ReplaceAll(got, []byte("\r\n"), []byte("\n")), want) {
		t.Fatal("docs/handbook/reference.json ist veraltet: go test ./cmd/i-tracker -run TestReferenceFileIsCurrent -update")
	}
}

var (
	defItem = regexp.MustCompile(`(?m)^- (.+?) :: `)
	section = regexp.MustCompile(`(?m)^## ([a-z0-9-]+) \| `)
)

// TestHandbookCoversReference prüft, dass beide Sprachfassungen des Handbuchs jeden
// Schlüssel, jede Option und jede Route erklären und nichts erklären, was es nicht mehr gibt.
func TestHandbookCoversReference(t *testing.T) {
	ref := buildReference()
	required := map[string]bool{}
	for _, k := range ref.Config {
		required[k.Key] = true
	}
	for _, c := range ref.CLI {
		required[c.Name] = true
	}
	for _, r := range ref.API {
		required[r.Pattern] = true
	}

	var sections [2]string
	for i, lang := range []string{"de", "en"} {
		src, err := os.ReadFile(handbookDir + lang + ".md")
		if err != nil {
			t.Fatal(err)
		}
		text := strings.ReplaceAll(string(src), "\r\n", "\n")
		documented := map[string]bool{}
		for _, m := range defItem.FindAllStringSubmatch(text, -1) {
			key := strings.Trim(m[1], "`")
			if documented[key] {
				t.Errorf("%s.md: %q ist doppelt erklärt", lang, key)
			}
			documented[key] = true
			if !required[key] {
				t.Errorf("%s.md erklärt %q, das es im Code nicht (mehr) gibt", lang, key)
			}
		}
		for key := range required {
			if !documented[key] {
				t.Errorf("%s.md: Erklärung für %q fehlt", lang, key)
			}
		}
		var ids []string
		for _, m := range section.FindAllStringSubmatch(text, -1) {
			ids = append(ids, m[1])
		}
		sections[i] = strings.Join(ids, ",")
	}
	if sections[0] != sections[1] {
		t.Errorf("Abschnitte unterscheiden sich:\n de: %s\n en: %s", sections[0], sections[1])
	}
}
