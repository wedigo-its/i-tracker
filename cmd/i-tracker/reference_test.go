package main

import (
	"bytes"
	"flag"
	"os"
	"regexp"
	"strings"
	"testing"
)

var updateReference = flag.Bool("update", false, "rewrite docs/handbook/reference.json")

const (
	referenceFile = "../../docs/handbook/reference.json"
	handbookDir   = "../../docs/handbook/"
)

// TestReferenceFileIsCurrent keeps docs/handbook/reference.json in sync with the code.
// Rewrite: go test ./cmd/i-tracker -run TestReferenceFileIsCurrent -update
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
		t.Fatal("docs/handbook/reference.json is outdated: go test ./cmd/i-tracker -run TestReferenceFileIsCurrent -update")
	}
}

var (
	defItem = regexp.MustCompile(`(?m)^- (.+?) :: `)
	section = regexp.MustCompile(`(?m)^## ([a-z0-9-]+) \| `)
)

// TestHandbookCoversReference checks that both handbook languages explain every key,
// flag and route, and nothing that no longer exists.
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
				t.Errorf("%s.md: %q explained twice", lang, key)
			}
			documented[key] = true
			if !required[key] {
				t.Errorf("%s.md explains %q, which is not in the code", lang, key)
			}
		}
		for key := range required {
			if !documented[key] {
				t.Errorf("%s.md: explanation for %q missing", lang, key)
			}
		}
		var ids []string
		for _, m := range section.FindAllStringSubmatch(text, -1) {
			ids = append(ids, m[1])
		}
		sections[i] = strings.Join(ids, ",")
	}
	if sections[0] != sections[1] {
		t.Errorf("sections differ:\n de: %s\n en: %s", sections[0], sections[1])
	}
}
