// errortext_test.go
// Tests and traces store a missing language-neutral error message the same way
// Version: 2026.10.28

package store

import (
	"context"
	"testing"

	"i-tracker/internal/db"
	"i-tracker/internal/i18n"
	"i-tracker/internal/model"
)

// TestErrorTextStorage: error_text is NULL without a catalog message (none or foreign text),
// else its JSON, in tests and traces alike; readers see NULL as "".
func TestErrorTextStorage(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	sv := &model.Server{ServerSettings: model.DefaultServerSettings()}
	sv.Name, sv.Host = "a", "a"
	if err := st.CreateServer(ctx, sv); err != nil {
		t.Fatal(err)
	}

	foreign := i18n.Foreign("unable to connect to server: Connection refused")
	keyed := i18n.E("err.trace.ipv4_only")
	cases := []struct {
		name    string
		failure error
		null    bool
	}{
		{"no failure", nil, true},
		{"foreign text", foreign, true},
		{"keyed error", keyed, false},
	}

	// isNull reports whether error_text of the row is NULL.
	isNull := func(table string, id int64) bool {
		t.Helper()
		var null bool
		if err := st.db.QueryRow(`SELECT error_text IS NULL FROM `+table+` WHERE id = ?`, id).Scan(&null); err != nil {
			t.Fatal(err)
		}
		return null
	}

	for _, c := range cases {
		test := &model.Test{ServerID: sv.ID, Protocol: model.ProtocolTCP, Direction: model.DirectionDownload, Duration: 1, ParallelStreams: 1}
		if err := st.CreateTest(ctx, test); err != nil {
			t.Fatal(err)
		}
		if err := st.FinishTest(ctx, test.ID, model.StatusFailed, db.Now(), model.TestResult{}, c.failure, nil); err != nil {
			t.Fatal(err)
		}
		if got := isNull("tests", test.ID); got != c.null {
			t.Errorf("test, %s: error_text IS NULL = %v, want %v", c.name, got, c.null)
		}

		tr := &model.Trace{DestinationHost: "a"}
		if c.failure != nil {
			tr.SetError(c.failure)
		}
		if err := st.CreateTrace(ctx, tr); err != nil {
			t.Fatal(err)
		}
		if got := isNull("traces", tr.ID); got != c.null {
			t.Errorf("trace, %s: error_text IS NULL = %v, want %v", c.name, got, c.null)
		}

		// Both read the stored message back the same way.
		gotTest, err := st.TestByID(ctx, test.ID)
		if err != nil {
			t.Fatal(err)
		}
		gotTrace, err := st.TraceByID(ctx, tr.ID)
		if err != nil {
			t.Fatal(err)
		}
		want := i18n.MsgOf(c.failure).Encode()
		if gotTest.ErrorText != want || gotTrace.ErrorText != want {
			t.Errorf("%s: ErrorText test %q, trace %q, want %q", c.name, gotTest.ErrorText, gotTrace.ErrorText, want)
		}
	}

	// Tests failed on start take the same path.
	test := &model.Test{ServerID: sv.ID, Protocol: model.ProtocolTCP, Direction: model.DirectionDownload, Duration: 1, ParallelStreams: 1}
	if err := st.CreateTest(ctx, test); err != nil {
		t.Fatal(err)
	}
	if _, err := st.FailUnfinishedTests(ctx, foreign); err != nil {
		t.Fatal(err)
	}
	if !isNull("tests", test.ID) {
		t.Error("FailUnfinishedTests with foreign text: error_text is not NULL")
	}
}
