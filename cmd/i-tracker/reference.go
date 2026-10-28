package main

// reference.go – reference for the product docs at www.wedigo.net/docs/i-tracker.
//
// "i-tracker reference" prints config keys, CLI and API routes as JSON; that output is
// docs/handbook/reference.json, from which the website builds its tables (explanations:
// docs/handbook/{de,en}.md). reference_test.go keeps all three in sync.

import (
	"encoding/json"
	"flag"
	"io"

	"i-tracker/internal/config"
	"i-tracker/internal/web"
)

// cliEntry is a CLI flag or subcommand.
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
	ref.CLI = append(ref.CLI, cliEntry{Name: "rollback"})
	return ref
}

// marshalReference returns the reference as indented JSON ending in LF.
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
