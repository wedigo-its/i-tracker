package main

// reference.go – Referenz für die Produktdokumentation auf www.wedigo.net/docs/i-tracker.
//
// "i-tracker reference" gibt Konfigurationsschlüssel, Kommandozeile und API-Routen als
// JSON aus. Die Datei docs/handbook/reference.json ist genau diese Ausgabe; der Generator
// der Website baut daraus die Tabellen, die Erklärungen stehen in docs/handbook/{de,en}.md.
// Die Tests in reference_test.go halten alle drei auf demselben Stand.

import (
	"encoding/json"
	"flag"
	"io"

	"i-tracker/internal/config"
	"i-tracker/internal/web"
)

// cliEntry ist eine Option oder ein Unterbefehl der Kommandozeile.
type cliEntry struct {
	Name    string `json:"name"`
	Default string `json:"default,omitempty"`
}

type reference struct {
	Product string           `json:"product"`
	Config  []config.KeyInfo `json:"config"`
	CLI     []cliEntry       `json:"cli"`
	API     []web.Route      `json:"api"`
}

func buildReference() reference {
	ref := reference{Product: "i-tracker", Config: config.Reference(), API: web.Routes()}
	fs := flag.NewFlagSet("i-tracker", flag.ContinueOnError)
	defineFlags(fs)
	fs.VisitAll(func(f *flag.Flag) {
		ref.CLI = append(ref.CLI, cliEntry{Name: "-" + f.Name, Default: f.DefValue})
	})
	for _, a := range serviceActions {
		ref.CLI = append(ref.CLI, cliEntry{Name: "service " + a})
	}
	return ref
}

// marshalReference liefert die Referenz als eingerücktes JSON mit LF am Ende.
func marshalReference() ([]byte, error) {
	data, err := json.MarshalIndent(buildReference(), "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

func writeReference(w io.Writer) error {
	data, err := marshalReference()
	if err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}
