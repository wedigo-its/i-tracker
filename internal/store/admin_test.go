package store

import (
	"context"
	"testing"
	"time"

	"i-tracker/internal/db"
	"i-tracker/internal/model"
)

func TestCleanupAndDatabaseStats(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	mkServer := func(name string) *model.Server {
		sv := &model.Server{ServerSettings: model.DefaultServerSettings()}
		sv.Name, sv.Host = name, name
		if err := st.CreateServer(ctx, sv); err != nil {
			t.Fatal(err)
		}
		return sv
	}
	// mkTest creates a test with a 2-hop trace, age days old.
	mkTest := func(sv *model.Server, age int) int64 {
		test := &model.Test{ServerID: sv.ID, Protocol: model.ProtocolTCP, Direction: model.DirectionDownload, Duration: 1, ParallelStreams: 1}
		if err := st.CreateTest(ctx, test); err != nil {
			t.Fatal(err)
		}
		tr := &model.Trace{TestID: &test.ID, DestinationHost: sv.Host, Hops: []model.TraceHop{{HopNumber: 1}, {HopNumber: 2}}}
		if err := st.CreateTrace(ctx, tr); err != nil {
			t.Fatal(err)
		}
		old := db.FormatTime(time.Now().AddDate(0, 0, -age))
		st.db.Exec(`UPDATE tests SET created_at = ? WHERE id = ?`, old, test.ID)
		st.db.Exec(`UPDATE traces SET created_at = ? WHERE id = ?`, old, tr.ID)
		return test.ID
	}
	a, b := mkServer("a"), mkServer("b")
	mkTest(a, 40)
	mkTest(a, 1)
	mkTest(b, 40)
	mkTest(b, 1)
	// Live trace without a test, 40 days old.
	live := &model.Trace{DestinationHost: "x", Hops: []model.TraceHop{{HopNumber: 1}}}
	st.CreateTrace(ctx, live)
	st.db.Exec(`UPDATE traces SET created_at = ? WHERE id = ?`, db.FormatTime(time.Now().AddDate(0, 0, -40)), live.ID)

	ds, err := st.DatabaseStats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if ds.TotalTests != 4 || ds.TotalServers != 2 || ds.TotalTraces != 5 || ds.TotalHops != 9 ||
		ds.OldestTest == nil || ds.NewestTest == nil || !ds.OldestTest.Before(*ds.NewestTest) {
		t.Errorf("DatabaseStats: %+v", ds)
	}

	// Old tests of server a: one test; its trace cascades.
	before := time.Now().AddDate(0, 0, -30)
	if n, err := st.DeleteTests(ctx, &before, &a.ID); err != nil || n != 1 {
		t.Fatalf("DeleteTests(old, a) = %d, %v", n, err)
	}
	if ds, _ := st.DatabaseStats(ctx); ds.TotalTests != 3 || ds.TotalTraces != 4 || ds.TotalHops != 7 {
		t.Errorf("after test cleanup: %+v", ds)
	}

	// Old traces: server b's and the live trace (no started_at).
	if tr, hops, err := st.DeleteTraces(ctx, &before); err != nil || tr != 2 || hops != 3 {
		t.Fatalf("DeleteTraces(old) = %d traces, %d hops, %v", tr, hops, err)
	}
	if tr, hops, err := st.DeleteTraces(ctx, nil); err != nil || tr != 2 || hops != 4 {
		t.Fatalf("DeleteTraces(all) = %d traces, %d hops, %v", tr, hops, err)
	}
	if n, err := st.DeleteTests(ctx, nil, nil); err != nil || n != 3 {
		t.Fatalf("DeleteTests(all) = %d, %v", n, err)
	}
	if ds, _ := st.DatabaseStats(ctx); ds.TotalTests != 0 || ds.OldestTest != nil || ds.TotalServers != 2 {
		t.Errorf("empty: %+v", ds)
	}
}
