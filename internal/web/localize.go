// localize.go
// Renders the stored error messages of tests and traces in the request language
// Version: 2026.10.28

package web

import (
	"net/http"

	"i-tracker/internal/i18n"
	"i-tracker/internal/model"
)

// localizeTests renders the error message of each test in the request language.
func localizeTests(r *http.Request, tests ...*model.Test) {
	lang := i18n.FromRequest(r)
	for _, t := range tests {
		if t != nil {
			t.ErrorMessage = i18n.LocalizePtr(lang, t.ErrorText, t.ErrorMessage)
		}
	}
}

// localizeTestList is localizeTests for a list.
func localizeTestList(r *http.Request, tests []model.Test) {
	for i := range tests {
		localizeTests(r, &tests[i])
	}
}

// localizeTraces renders the error message of each trace in the request language.
func localizeTraces(r *http.Request, traces ...*model.Trace) {
	lang := i18n.FromRequest(r)
	for _, t := range traces {
		if t != nil {
			t.ErrorMessage = i18n.LocalizePtr(lang, t.ErrorText, t.ErrorMessage)
		}
	}
}

// localizeTraceList is localizeTraces for a list.
func localizeTraceList(r *http.Request, traces []model.Trace) {
	for i := range traces {
		localizeTraces(r, &traces[i])
	}
}
